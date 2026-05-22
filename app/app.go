package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	rt "github.com/wailsapp/wails/v2/pkg/runtime"

	"mockagent/internal/asr"
	"mockagent/internal/config"
	"mockagent/internal/conversation"
	"mockagent/internal/docs"
	"mockagent/internal/hotkey"
	"mockagent/internal/llm"
	"mockagent/internal/recorder"
	"mockagent/internal/tray"
)

// 事件名常量。前端订阅与后端发出共同使用。
const (
	EventRecordingStarted    = "recording-started"
	EventRecordingStopped    = "recording-stopped"
	EventASRProgress         = "asr-progress"
	EventASRResult           = "asr-result"
	EventASRError            = "asr-error"
	EventASRNotice           = "asr-notice" // 提示性事件
	EventLLMDelta            = "llm-delta"
	EventLLMDone             = "llm-done"
	EventLLMError            = "llm-error"
	EventConversationCleared = "conversation-cleared"
	EventConfigStatus        = "config-status"
	EventSendHotkeyPressed   = "send-hotkey-pressed" // 发送热键被按下；前端读输入框并调用 SendMessage
	EventHotkeyChanged       = "hotkey-changed"      // 热键修改成功后通知前端刷新展示
	EventDocumentsChanged    = "documents-changed"   // 文档列表/启用状态变更
)

// HotkeyKind 区分录音热键 / 发送热键 / 系统声音热键。
const (
	HotkeyKindRecord = "record"
	HotkeyKindSend   = "send"
	HotkeyKindSystem = "system" // 系统声音环回采集（如腾讯会议里面试官的语音）
)

// allHotkeyKinds 列出所有热键 kind，用于冲突检查时遍历"非自身"的其它热键。
var allHotkeyKinds = []string{HotkeyKindRecord, HotkeyKindSend, HotkeyKindSystem}

// App 是 Wails 后端协调器。
type App struct {
	ctx     context.Context
	cfgDir  string
	cfgMu   sync.RWMutex
	cfg     *config.Config
	loadErr error // 启动时配置加载失败的错误（在前端首次 GetConfig / 发送时返回）

	// 各子模块。
	recordHotkey *hotkey.Manager // 按住录音的全局热键（麦克风）
	sendHotkey   *hotkey.Manager // 一键发送的全局热键
	systemHotkey *hotkey.Manager // 按住录音的全局热键（系统声音环回）
	rec          recorder.Recorder
	sysRec       recorder.Recorder // 系统声音环回 Recorder
	asrCli       asr.Client
	llmCli       *llm.Client
	store        *conversation.Store
	trayMgr      *tray.Manager
	docsMgr      *docs.Manager // 参考文档管理

	// 录音 / ASR 状态。
	asrSession atomic.Int64    // 当前 ASR session id；新一轮 Press 自增以丢弃旧结果
	recording  atomic.Bool     // 当前是否处于录音中（用于发送热键的拒绝逻辑）

	// LLM 流并发控制。
	llmMu     sync.Mutex
	llmCancel context.CancelFunc
}

// NewApp 创建 App，但不会启动任何业务（业务在 startup 中启动以使用 Wails ctx）。
func NewApp() *App {
	return &App{}
}

// startup 由 Wails 在主窗口创建后调用。
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.cfgDir = configDir()

	a.store = conversation.NewStore()
	a.rec = recorder.New(recorder.DefaultConfig())
	a.sysRec = recorder.NewLoopback(recorder.DefaultConfig())

	if err := a.loadConfig(); err != nil {
		a.loadErr = err
		a.emit(EventConfigStatus, map[string]any{"ok": false, "error": a.redactErrText(err)})
		// 即便加载失败，也允许应用继续运行（前端会显示错误页）。
	}

	// 加载文档库（与 config 同目录）。失败不致命，UI 会看到空列表。
	if dm, err := docs.Load(a.cfgDir); err != nil {
		a.emit(EventConfigStatus, map[string]any{
			"ok": false, "error": fmt.Sprintf("加载参考文档失败: %v", err),
		})
		a.docsMgr = docs.New(a.cfgDir)
	} else {
		a.docsMgr = dm
	}

	// Recorder 设备探测（异步，不阻塞启动）。
	go a.probeMicrophone()

	// 注册热键。失败不致命，UI 内的 🎤 按钮仍可用。
	a.recordHotkey = hotkey.NewManager(a.onHotkeyPress, a.onHotkeyRelease)
	a.sendHotkey = hotkey.NewManager(a.onSendHotkey, nil)
	a.systemHotkey = hotkey.NewManager(a.onSystemHotkeyPress, a.onSystemHotkeyRelease)
	a.applyHotkeyConfig()

	// 初始化 ASR/LLM 客户端（基于当前配置）。
	a.rebuildClients()

	// 启动系统托盘。
	a.trayMgr = tray.NewManager(tray.Callbacks{
		OnShowWindow:      func() { rt.WindowShow(a.ctx) },
		OnNewConversation: func() { a.NewConversation() },
		OnOpenConfig:      func() { _ = a.OpenConfigFile() },
		OnQuit:            func() { rt.Quit(a.ctx) },
	})
	a.trayMgr.Start()
}

// beforeClose 在主窗口关闭前调用。
// 返回 false 让 Wails 正常关闭（触发 OnShutdown，进而注销热键等）。
func (a *App) beforeClose(ctx context.Context) (prevent bool) {
	_ = ctx
	return false
}

// shutdown 在 Wails 退出前调用（OnShutdown 钩子）。
func (a *App) shutdown(ctx context.Context) {
	if a.trayMgr != nil {
		a.trayMgr.Stop()
	}
	if a.recordHotkey != nil {
		_ = a.recordHotkey.Unregister()
	}
	if a.sendHotkey != nil {
		_ = a.sendHotkey.Unregister()
	}
	if a.systemHotkey != nil {
		_ = a.systemHotkey.Unregister()
	}
	if a.rec != nil {
		_ = a.rec.Close()
	}
	if a.sysRec != nil {
		_ = a.sysRec.Close()
	}
	a.cancelLLM()
}

// ----- 配置 -----

func configDir() string {
	exe, err := os.Executable()
	if err == nil {
		return filepath.Dir(exe)
	}
	wd, _ := os.Getwd()
	return wd
}

// loadConfig 加载配置；优先在 exe 同级目录查找 config.json。
// 当 exe 同级 + 工作目录 + 上一级（开发模式）都没有 config.json 但有 config.example.json 时，会自动复制。
func (a *App) loadConfig() error {
	dirs := candidateConfigDirs(a.cfgDir)
	var lastErr error
	for _, d := range dirs {
		if _, err := os.Stat(filepath.Join(d, config.FileName)); err == nil {
			cfg, err := config.Load(d)
			if err != nil {
				lastErr = err
				continue
			}
			a.cfgMu.Lock()
			a.cfg = cfg
			a.cfgDir = d
			a.cfgMu.Unlock()
			return nil
		}
		if _, err := os.Stat(filepath.Join(d, config.ExampleFileName)); err == nil {
			cfg, err := config.Load(d)
			if err != nil {
				lastErr = err
				continue
			}
			a.cfgMu.Lock()
			a.cfg = cfg
			a.cfgDir = d
			a.cfgMu.Unlock()
			return nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("未在以下目录找到 %s 或 %s: %v",
			config.FileName, config.ExampleFileName, dirs)
	}
	return lastErr
}

// candidateConfigDirs 返回可能存放 config.json 的目录顺序。
//
// 顺序：
//  1. 可执行文件所在目录（生产环境）
//  2. 当前工作目录（命令行启动）
//  3. 工作目录的上一级（开发环境：cd app 后跑 wails dev，配置在仓库根目录）
func candidateConfigDirs(primary string) []string {
	out := []string{primary}
	wd, err := os.Getwd()
	if err == nil {
		if wd != primary {
			out = append(out, wd)
		}
		parent := filepath.Dir(wd)
		if parent != "" && parent != wd {
			out = append(out, parent)
		}
	}
	return out
}

func (a *App) currentConfig() *config.Config {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	return a.cfg
}

func (a *App) rebuildClients() {
	cfg := a.currentConfig()
	if cfg == nil {
		return
	}
	a.asrCli = asr.NewTencent(cfg.Tencent.AppID, cfg.Tencent.SecretID, cfg.Tencent.SecretKey)
	a.llmCli = llm.New(llm.Options{
		BaseURL:         cfg.DeepSeek.BaseURL,
		APIKey:          cfg.DeepSeek.APIKey,
		Model:           cfg.DeepSeek.Model,
		Thinking:        cfg.DeepSeek.Thinking,
		ReasoningEffort: cfg.DeepSeek.ReasoningEffort,
	})
	if a.store == nil {
		a.store = conversation.NewStore()
	}
}

// applyHotkeyConfig 把当前 Config 中的两个热键应用到对应 Manager。
//
// 任一热键解析或注册失败都会通过 EventConfigStatus 通知前端，但不会阻止
// 应用继续运行（用户仍可用 UI 按钮录音 / 鼠标点发送按钮）。
func (a *App) applyHotkeyConfig() {
	cfg := a.currentConfig()
	if cfg == nil {
		return
	}
	a.applySingleHotkey(a.recordHotkey, cfg.RecordHotkey, "录音")
	a.applySingleHotkey(a.sendHotkey, cfg.SendHotkey, "发送")
	a.applySingleHotkey(a.systemHotkey, cfg.SystemHotkey, "系统声音")
}

func (a *App) applySingleHotkey(mgr *hotkey.Manager, raw, label string) {
	if mgr == nil {
		return
	}
	if strings.TrimSpace(raw) == "" {
		_ = mgr.Unregister()
		return
	}
	spec, err := hotkey.ParseSpec(raw)
	if err != nil {
		a.emit(EventConfigStatus, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("%s热键 %q 解析失败: %v", label, raw, err),
		})
		return
	}
	if err := mgr.Register(spec); err != nil {
		a.emit(EventConfigStatus, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("注册%s热键 %s 失败: %v", label, spec, err),
		})
	}
}

// ----- 麦克风探测 -----

func (a *App) probeMicrophone() {
	defer a.recoverPanic("microphone-probe")
	ok, err := a.rec.Probe()
	if err != nil {
		a.emit(EventASRError, map[string]any{"error": fmt.Sprintf("音频设备探测失败: %v", err)})
		return
	}
	if !ok {
		a.emit(EventConfigStatus, map[string]any{
			"ok":    false,
			"error": "未找到可用音频输入设备",
		})
	}
}

// ----- 热键 → 录音 → ASR 流水线 -----

func (a *App) onHotkeyPress() {
	defer a.recoverPanic("hotkey-press")
	a.startRecordingWith(a.rec, "麦克风")
}

func (a *App) onHotkeyRelease() {
	defer a.recoverPanic("hotkey-release")
	a.stopRecordingWith(a.rec)
}

func (a *App) onSystemHotkeyPress() {
	defer a.recoverPanic("hotkey-system-press")
	a.startRecordingWith(a.sysRec, "系统声音")
}

func (a *App) onSystemHotkeyRelease() {
	defer a.recoverPanic("hotkey-system-release")
	a.stopRecordingWith(a.sysRec)
}

// onSendHotkey 由发送热键的 Press 触发；只通知前端，前端读输入框内容并调用 SendMessage。
func (a *App) onSendHotkey() {
	defer a.recoverPanic("hotkey-send")
	if a.recording.Load() {
		// 录音进行中拒绝发送，避免抢占
		return
	}
	a.emit(EventSendHotkeyPressed, nil)
}

// SimulatePress / SimulateRelease 由前端 🎤 按钮调用，行为与录音热键完全一致。
func (a *App) SimulatePress()   { a.onHotkeyPress() }
func (a *App) SimulateRelease() { a.onHotkeyRelease() }

// startRecordingWith 启动指定 recorder 的一轮录音。label 用于错误信息（"麦克风"/"系统声音"）。
//
// 不同源的两个 recorder 共用 a.recording 与 a.asrSession 状态，因此一次只能
// 录一路；如果另一路已经在录，本次请求会被忽略。
func (a *App) startRecordingWith(rec recorder.Recorder, label string) {
	if rec == nil {
		return
	}
	cfg := a.currentConfig()
	if cfg == nil {
		a.emit(EventASRNotice, map[string]any{"message": "配置未加载，无法录音"})
		return
	}
	if cfg.Tencent.AppID == "" || cfg.Tencent.SecretID == "" || cfg.Tencent.SecretKey == "" {
		a.emit(EventASRNotice, map[string]any{"message": "腾讯云凭证未配置，无法录音"})
		return
	}

	// 已经在录另一路时拒绝；避免两路 PCM 互相覆盖 ASR session。
	if !a.recording.CompareAndSwap(false, true) {
		return
	}

	if err := rec.Start(); err != nil {
		a.recording.Store(false)
		a.emit(EventASRError, map[string]any{"error": fmt.Sprintf("%s启动失败: %v", label, err)})
		return
	}
	a.emit(EventRecordingStarted, nil)
}

// stopRecordingWith 结束指定 recorder 的当前录音并送 ASR。与 startRecordingWith 配对。
func (a *App) stopRecordingWith(rec recorder.Recorder) {
	if rec == nil {
		return
	}
	pcm, err := rec.Stop()
	a.recording.Store(false)
	a.emit(EventRecordingStopped, nil)
	if err != nil {
		// Stop 失败但前端已收到 stopped；不再继续 ASR。
		if !errors.Is(err, recorder.ErrNotRecording) {
			a.emit(EventASRError, map[string]any{"error": fmt.Sprintf("停止录音失败: %v", err)})
		}
		return
	}

	cfg := a.currentConfig()
	if cfg == nil {
		return
	}
	if !recorder.ShouldRecognize(len(pcm), cfg.Audio.SampleRate, cfg.Audio.Channels,
		recorder.DefaultBytesPerSample, cfg.Audio.MinDurationMs) {
		a.emit(EventASRNotice, map[string]any{"message": "录音过短"})
		return
	}

	// 只在通过最小时长校验后才推进 sessionID。
	sessionID := a.asrSession.Add(1)
	go a.runASR(sessionID, pcm)
}

func (a *App) runASR(sessionID int64, pcm []byte) {
	defer a.recoverPanic("asr")
	a.emit(EventASRProgress, map[string]any{"stage": "recognizing"})

	// 调试模式：把 PCM 写到 MOCK_AGENT_DEBUG_DIR 指定目录便于离线诊断。
	if logDir := os.Getenv("MOCK_AGENT_DEBUG_DIR"); logDir != "" {
		_ = os.MkdirAll(logDir, 0o755)
		fname := filepath.Join(logDir, fmt.Sprintf("asr-%d.pcm", sessionID))
		_ = os.WriteFile(fname, pcm, 0o644)
	}

	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()

	text, err := a.asrCli.Recognize(ctx, pcm)

	// 若已过期（用户开了新一轮录音），直接丢弃。
	if a.asrSession.Load() != sessionID {
		return
	}
	if err != nil {
		var aerr *asr.Error
		switch {
		case errors.As(err, &aerr) && aerr.Kind == "empty":
			a.emit(EventASRNotice, map[string]any{"message": "未识别到内容"})
			return
		default:
			a.emit(EventASRError, map[string]any{"error": a.redactErrText(err)})
			return
		}
	}
	a.emit(EventASRResult, map[string]any{"text": text})
}

// ----- LLM 流式对话 -----

// SendMessage 由前端调用，触发一次完整的 LLM 流式响应。
//
// 拒绝条件：配置未加载 / 文本为空 / DeepSeek 未配置 / 已有进行中的回答 / 正在录音。
func (a *App) SendMessage(text string) error {
	defer a.recoverPanic("send-message")
	cfg := a.currentConfig()
	if cfg == nil {
		return errors.New("配置未加载")
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("消息不能为空")
	}
	if strings.TrimSpace(cfg.DeepSeek.APIKey) == "" {
		return errors.New("DeepSeek API Key 未配置")
	}
	if a.recording.Load() {
		return errors.New("正在录音中，请先松开录音键")
	}

	a.llmMu.Lock()
	if a.llmCancel != nil {
		a.llmMu.Unlock()
		return errors.New("当前已有正在进行的回答，请先停止再发送")
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.llmCancel = cancel
	a.llmMu.Unlock()

	a.store.Append(conversation.Message{Role: conversation.RoleUser, Content: text})

	go a.runLLM(ctx)
	return nil
}

// StopGeneration 取消当前进行中的 LLM 流（如果有）。
func (a *App) StopGeneration() {
	a.cancelLLM()
}

func (a *App) cancelLLM() {
	a.llmMu.Lock()
	cancel := a.llmCancel
	a.llmCancel = nil
	a.llmMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) runLLM(ctx context.Context) {
	defer a.recoverPanic("llm")
	defer func() {
		a.llmMu.Lock()
		a.llmCancel = nil
		a.llmMu.Unlock()
	}()

	snapshot := a.store.Snapshot()
	msgs := make([]llm.Message, 0, len(snapshot)+1)

	// 拼当前 active system prompt 作为第一条消息（空则不发）。
	cfg := a.currentConfig()
	if cfg != nil && strings.TrimSpace(cfg.DeepSeek.ActiveSystemPrompt) != "" {
		msgs = append(msgs, llm.Message{
			Role:    conversation.RoleSystem,
			Content: cfg.DeepSeek.ActiveSystemPrompt,
		})
	}
	for _, m := range snapshot {
		msgs = append(msgs, llm.Message{
			Role:             m.Role,
			Content:          m.Content,
			ReasoningContent: m.ReasoningContent,
		})
	}
	// 注入参考文档：把所有启用的文档拼到第一条 system 消息后面（没有 system 则会插一条）。
	// 注意只影响发送给 LLM 的拷贝，不修改 store 里的历史，保证导出时不泄露文档。
	if a.docsMgr != nil {
		if extra := a.docsMgr.BuildContext(); extra != "" {
			injectDocsIntoSystemMessage(&msgs, extra)
		}
	}
	ch, err := a.llmCli.Stream(ctx, msgs)
	if err != nil {
		var herr *llm.HTTPError
		if errors.As(err, &herr) {
			a.emit(EventLLMError, map[string]any{
				"error":  fmt.Sprintf("HTTP %d: %s", herr.StatusCode, herr.Body),
				"status": herr.StatusCode,
			})
			return
		}
		a.emit(EventLLMError, map[string]any{"error": a.redactErrText(err)})
		return
	}

	var content, reasoning strings.Builder
	for ev := range ch {
		if ev.Done {
			break
		}
		if ev.Content != "" {
			content.WriteString(ev.Content)
		}
		if ev.Reasoning != "" {
			reasoning.WriteString(ev.Reasoning)
		}
		a.emit(EventLLMDelta, map[string]any{
			"content":   ev.Content,
			"reasoning": ev.Reasoning,
		})
	}

	full := content.String()
	rsn := reasoning.String()

	// ctx 是否被取消？取消时也保存已累积内容并发 llm-error。
	if err := ctx.Err(); err != nil {
		if full != "" || rsn != "" {
			a.store.Append(conversation.Message{
				Role:             conversation.RoleAssistant,
				Content:          full,
				ReasoningContent: rsn,
			})
		}
		a.emit(EventLLMError, map[string]any{
			"error":   "已停止生成",
			"partial": full,
		})
		return
	}

	a.store.Append(conversation.Message{
		Role:             conversation.RoleAssistant,
		Content:          full,
		ReasoningContent: rsn,
	})
	a.emit(EventLLMDone, map[string]any{"full_content": full, "reasoning": rsn})
}

// ----- Wails 绑定方法 -----

// NewConversation 取消进行中的 LLM，重置消息历史。
//
// 不再涉及 system prompt：每次发消息时按当前 active prompt 即时拼接。
func (a *App) NewConversation() {
	a.cancelLLM()
	a.store.Reset()
	a.emit(EventConversationCleared, nil)
}

// GetConfig 返回掩码后的配置视图供 UI 显示。
func (a *App) GetConfig() map[string]any {
	cfg := a.currentConfig()
	out := map[string]any{
		"loaded": cfg != nil,
	}
	if a.loadErr != nil {
		out["error"] = a.redactErrText(a.loadErr)
	}
	if cfg == nil {
		return out
	}
	v := cfg.MaskedView()
	out["record_hotkey"] = v.RecordHotkey
	out["send_hotkey"] = v.SendHotkey
	out["system_hotkey"] = v.SystemHotkey
	out["model"] = v.DeepSeek.Model
	out["base_url"] = v.DeepSeek.BaseURL
	out["thinking"] = v.DeepSeek.Thinking
	out["reasoning_effort"] = v.DeepSeek.ReasoningEffort
	out["active_system_prompt"] = v.DeepSeek.ActiveSystemPrompt
	out["system_prompt_history"] = v.DeepSeek.SystemPromptHistory
	out["tencent"] = map[string]any{
		"app_id":     v.Tencent.AppID,
		"secret_id":  v.Tencent.SecretID,
		"secret_key": v.Tencent.SecretKey,
	}
	out["api_key_set"] = v.DeepSeek.APIKey != ""
	out["audio"] = v.Audio
	out["source"] = cfg.SourcePath()
	return out
}

// UpdateHotkey 修改并持久化指定 kind 的热键。
//
// kind: HotkeyKindRecord 或 HotkeyKindSend；
// raw:  快捷键字符串（如 "F2" / "Ctrl+Alt+Space"）。
//
// 流程：解析 raw → 检查是否与另一个热键冲突 → 写回 config.json →
// 重新注册对应 hotkey 的 Manager → 通过 EventHotkeyChanged 通知前端。
func (a *App) UpdateHotkey(kind, raw string) error {
	defer a.recoverPanic("update-hotkey")

	switch kind {
	case HotkeyKindRecord, HotkeyKindSend, HotkeyKindSystem:
	default:
		return fmt.Errorf("未知的热键种类 %q", kind)
	}

	if strings.TrimSpace(raw) == "" {
		return errors.New("快捷键不能为空")
	}

	a.cfgMu.Lock()
	if a.cfg == nil {
		a.cfgMu.Unlock()
		return errors.New("配置未加载，无法保存")
	}
	cfg := *a.cfg
	a.cfgMu.Unlock()

	if err := validateHotkeyChange(&cfg, kind, raw); err != nil {
		return err
	}

	switch kind {
	case HotkeyKindRecord:
		cfg.RecordHotkey = raw
	case HotkeyKindSend:
		cfg.SendHotkey = raw
	case HotkeyKindSystem:
		cfg.SystemHotkey = raw
	}

	if err := cfg.Save(); err != nil {
		return err
	}

	a.cfgMu.Lock()
	a.cfg = &cfg
	a.cfgMu.Unlock()

	a.applyHotkeyConfig()
	a.emit(EventHotkeyChanged, map[string]any{
		"record_hotkey": cfg.RecordHotkey,
		"send_hotkey":   cfg.SendHotkey,
		"system_hotkey": cfg.SystemHotkey,
	})
	return nil
}

// hotkeyByKind 返回 cfg 中指定 kind 的原始快捷键字符串。
// 未知 kind 返回 ""，由调用方按 "空即跳过冲突检查" 处理。
func hotkeyByKind(cfg *config.Config, kind string) string {
	switch kind {
	case HotkeyKindRecord:
		return cfg.RecordHotkey
	case HotkeyKindSend:
		return cfg.SendHotkey
	case HotkeyKindSystem:
		return cfg.SystemHotkey
	default:
		return ""
	}
}

// validateHotkeyChange 校验新热键格式合法且不与任何其它已配置的热键冲突。
//
// 抽成纯函数便于测试：传入 cfg 表示当前配置（不会修改），kind/raw 是请求。
// 遍历所有 kind 而非两两 if，新增热键种类时无需再扩 switch。
func validateHotkeyChange(cfg *config.Config, kind, raw string) error {
	newSpec, err := hotkey.ParseSpec(raw)
	if err != nil {
		return fmt.Errorf("快捷键 %q 解析失败: %v", raw, err)
	}

	knownKind := false
	for _, k := range allHotkeyKinds {
		if k == kind {
			knownKind = true
			break
		}
	}
	if !knownKind {
		return fmt.Errorf("未知热键种类 %q", kind)
	}

	for _, other := range allHotkeyKinds {
		if other == kind {
			continue
		}
		otherRaw := hotkeyByKind(cfg, other)
		if strings.TrimSpace(otherRaw) == "" {
			continue
		}
		otherSpec, err := hotkey.ParseSpec(otherRaw)
		if err != nil {
			// 已有热键解析不出来——不视为冲突，让用户至少能改其它的
			continue
		}
		if newSpec.Equal(otherSpec) {
			return fmt.Errorf("快捷键 %s 已被另一个热键使用", newSpec)
		}
	}
	return nil
}

// OpenConfigFile 打开 config.json 让用户编辑。
func (a *App) OpenConfigFile() error {
	cfg := a.currentConfig()
	path := filepath.Join(a.cfgDir, config.FileName)
	if cfg != nil && cfg.SourcePath() != "" {
		path = cfg.SourcePath()
	}
	return config.OpenInEditor(path)
}

// ReloadConfig 重读 config.json 与环境变量；如热键变更则重新注册。
// 不会中断进行中的录音或 LLM 流。
func (a *App) ReloadConfig() error {
	if err := a.loadConfig(); err != nil {
		a.loadErr = err
		a.emit(EventConfigStatus, map[string]any{"ok": false, "error": a.redactErrText(err)})
		return err
	}
	a.loadErr = nil
	a.rebuildClients()
	a.applyHotkeyConfig()
	a.emit(EventConfigStatus, map[string]any{"ok": true})
	return nil
}

// ----- 系统提示词 -----

// SystemPromptState 是返回给前端的提示词状态。
type SystemPromptState struct {
	Active  string   `json:"active"`
	History []string `json:"history"`
}

// GetSystemPromptState 返回当前的 active prompt 与历史列表。
func (a *App) GetSystemPromptState() SystemPromptState {
	cfg := a.currentConfig()
	if cfg == nil {
		return SystemPromptState{}
	}
	return SystemPromptState{
		Active:  cfg.DeepSeek.ActiveSystemPrompt,
		History: append([]string(nil), cfg.DeepSeek.SystemPromptHistory...),
	}
}

// SaveSystemPrompt 把 content 设为新 active；如果在历史里就置顶，否则追加到首位。
// 立即生效，下一次 SendMessage 用新值。
func (a *App) SaveSystemPrompt(content string) error {
	defer a.recoverPanic("save-system-prompt")
	if strings.TrimSpace(content) == "" {
		return errors.New("系统提示词不能为空")
	}
	a.cfgMu.Lock()
	if a.cfg == nil {
		a.cfgMu.Unlock()
		return errors.New("配置未加载")
	}
	cfg := *a.cfg
	a.cfgMu.Unlock()

	if err := cfg.DeepSeek.SaveSystemPrompt(content); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	a.cfgMu.Lock()
	a.cfg = &cfg
	a.cfgMu.Unlock()
	return nil
}

// DeleteSystemPromptHistory 从历史中删除 content；删的是 active 时自动接 history 下一条。
func (a *App) DeleteSystemPromptHistory(content string) error {
	defer a.recoverPanic("delete-system-prompt")
	a.cfgMu.Lock()
	if a.cfg == nil {
		a.cfgMu.Unlock()
		return errors.New("配置未加载")
	}
	cfg := *a.cfg
	a.cfgMu.Unlock()

	if err := cfg.DeepSeek.DeleteSystemPromptHistory(content); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	a.cfgMu.Lock()
	a.cfg = &cfg
	a.cfgMu.Unlock()
	return nil
}

// ----- 参考文档 -----

// ListDocuments 返回当前文档元数据列表。
func (a *App) ListDocuments() []docs.Document {
	if a.docsMgr == nil {
		return nil
	}
	return a.docsMgr.List()
}

// AddDocument 弹出文件选择框，让用户选一份文档加入参考文档库。
//
// 用户取消选择时返回 nil, nil（不视为错误）。
func (a *App) AddDocument() (*docs.Document, error) {
	defer a.recoverPanic("add-document")
	if a.docsMgr == nil {
		return nil, errors.New("文档管理器未初始化")
	}
	path, err := rt.OpenFileDialog(a.ctx, rt.OpenDialogOptions{
		Title: "选择参考文档",
		Filters: []rt.FileFilter{
			{DisplayName: "受支持的文档", Pattern: "*.txt;*.md;*.markdown;*.docx;*.pdf;*.csv;*.json;*.yaml;*.yml;*.xml;*.html;*.htm;*.log;*.go;*.py;*.js;*.ts"},
			{DisplayName: "所有文件", Pattern: "*.*"},
		},
	})
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	doc, err := a.docsMgr.Add(path)
	if err != nil {
		return nil, err
	}
	a.emit(EventDocumentsChanged, nil)
	return &doc, nil
}

// RemoveDocument 删除指定 id 的文档。
func (a *App) RemoveDocument(id string) error {
	defer a.recoverPanic("remove-document")
	if a.docsMgr == nil {
		return errors.New("文档管理器未初始化")
	}
	if err := a.docsMgr.Remove(id); err != nil {
		return err
	}
	a.emit(EventDocumentsChanged, nil)
	return nil
}

// SetDocumentEnabled 切换文档的启用状态。
func (a *App) SetDocumentEnabled(id string, enabled bool) error {
	defer a.recoverPanic("set-document-enabled")
	if a.docsMgr == nil {
		return errors.New("文档管理器未初始化")
	}
	if err := a.docsMgr.SetEnabled(id, enabled); err != nil {
		return err
	}
	a.emit(EventDocumentsChanged, nil)
	return nil
}

// GetDocumentPreview 返回文档预览（截断到 docs.PreviewMaxRunes）。
func (a *App) GetDocumentPreview(id string) (map[string]any, error) {
	defer a.recoverPanic("preview-document")
	if a.docsMgr == nil {
		return nil, errors.New("文档管理器未初始化")
	}
	text, truncated, err := a.docsMgr.Preview(id)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"text":      text,
		"truncated": truncated,
	}, nil
}

// injectDocsIntoSystemMessage 把 extra 拼接到 msgs 第一条 system 消息后面；
// 若没有 system 消息则在前部插入一条。修改是就地的。
func injectDocsIntoSystemMessage(msgs *[]llm.Message, extra string) {
	if extra == "" {
		return
	}
	for i, m := range *msgs {
		if m.Role == "system" {
			if m.Content == "" {
				(*msgs)[i].Content = extra
			} else {
				(*msgs)[i].Content = m.Content + "\n\n" + extra
			}
			return
		}
	}
	// 没有 system 消息：在最前插入一条
	*msgs = append([]llm.Message{{Role: "system", Content: extra}}, (*msgs)...)
}

// ExportConversation 把当前会话导出到用户选择的路径。format 仅支持 "md" / "json"。
func (a *App) ExportConversation(format string) error {
	now := time.Now()
	suggest, _, err := conversation.Export(now, format, a.store.Snapshot())
	if err != nil {
		return err
	}

	path, err := rt.SaveFileDialog(a.ctx, rt.SaveDialogOptions{
		DefaultFilename: suggest,
		Title:           "导出对话",
		Filters: []rt.FileFilter{
			filterFor(format),
		},
	})
	if err != nil {
		return err
	}
	if path == "" {
		return nil // 用户取消
	}
	_, data, err := conversation.Export(now, format, a.store.Snapshot())
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func filterFor(format string) rt.FileFilter {
	switch format {
	case conversation.FormatJSON:
		return rt.FileFilter{DisplayName: "JSON 文件 (*.json)", Pattern: "*.json"}
	default:
		return rt.FileFilter{DisplayName: "Markdown 文件 (*.md)", Pattern: "*.md"}
	}
}

// ----- 工具 -----

// emit 把事件发送给前端。在 ctx 未就绪时安静丢弃（不应发生于正常运行流）。
func (a *App) emit(name string, payload any) {
	if a.ctx == nil {
		return
	}
	if payload == nil {
		rt.EventsEmit(a.ctx, name)
		return
	}
	rt.EventsEmit(a.ctx, name, payload)
}

// recoverPanic 在 goroutine 边界上恢复 panic，转化为 *-error 事件而非崩溃主进程。
// stage 标识 panic 发生的阶段，用于事件名映射。
func (a *App) recoverPanic(stage string) {
	if r := recover(); r != nil {
		stack := string(debug.Stack())
		msg := fmt.Sprintf("内部错误（%s）: %v", stage, r)
		// 仅向前端发简短信息，详细堆栈写到 stderr 便于调试。
		fmt.Fprintln(os.Stderr, msg+"\n"+stack)
		switch stage {
		case "llm", "send-message":
			a.emit(EventLLMError, map[string]any{"error": msg})
		case "asr", "hotkey-press", "hotkey-release":
			a.emit(EventASRError, map[string]any{"error": msg})
		default:
			a.emit(EventConfigStatus, map[string]any{"ok": false, "error": msg})
		}
	}
}

// redactErr 在错误信息中剔除敏感片段，避免密钥泄露。
// Config / asr / llm 包内部已经各自避免回传密钥；这里再做一道兜底。
func (a *App) redactErrText(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	cfg := a.currentConfig()
	if cfg != nil {
		for _, secret := range []string{
			cfg.DeepSeek.APIKey, cfg.Tencent.SecretID, cfg.Tencent.SecretKey,
		} {
			if secret != "" {
				s = strings.ReplaceAll(s, secret, config.MaskedString)
			}
		}
	}
	return s
}
