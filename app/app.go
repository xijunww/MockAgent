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
)

// HotkeyKind 区分录音热键与发送热键。
const (
	HotkeyKindRecord = "record"
	HotkeyKindSend   = "send"
)

// App 是 Wails 后端协调器。
type App struct {
	ctx     context.Context
	cfgDir  string
	cfgMu   sync.RWMutex
	cfg     *config.Config
	loadErr error // 启动时配置加载失败的错误（在前端首次 GetConfig / 发送时返回）

	// 各子模块。
	recordHotkey *hotkey.Manager // 按住录音的全局热键
	sendHotkey   *hotkey.Manager // 一键发送的全局热键
	rec          recorder.Recorder
	asrCli       asr.Client
	llmCli       *llm.Client
	store        *conversation.Store
	trayMgr      *tray.Manager

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

	a.store = conversation.NewStore("")
	a.rec = recorder.New(recorder.DefaultConfig())

	if err := a.loadConfig(); err != nil {
		a.loadErr = err
		a.emit(EventConfigStatus, map[string]any{"ok": false, "error": a.redactErrText(err)})
		// 即便加载失败，也允许应用继续运行（前端会显示错误页）。
	}

	// Recorder 设备探测（异步，不阻塞启动）。
	go a.probeMicrophone()

	// 注册热键。失败不致命，UI 内的 🎤 按钮仍可用。
	a.recordHotkey = hotkey.NewManager(a.onHotkeyPress, a.onHotkeyRelease)
	a.sendHotkey = hotkey.NewManager(a.onSendHotkey, nil)
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

// beforeClose 在主窗口关闭前调用：返回 true 阻止关闭并隐藏到托盘。
func (a *App) beforeClose(ctx context.Context) (prevent bool) {
	rt.WindowHide(ctx)
	return true
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
	if a.rec != nil {
		_ = a.rec.Close()
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
	// 重置会话 system prompt（仅当当前会话为空或仅含 system 时才重置；非空会话保留）。
	if a.store == nil {
		a.store = conversation.NewStore(cfg.DeepSeek.SystemPrompt)
	} else if a.store.Len() <= 1 {
		a.store.Reset(cfg.DeepSeek.SystemPrompt)
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
	a.startRecording()
}

func (a *App) onHotkeyRelease() {
	defer a.recoverPanic("hotkey-release")
	a.stopRecording()
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

// SimulatePress / SimulateRelease 由前端 🎤 按钮调用，行为与全局热键完全一致。
func (a *App) SimulatePress()   { a.onHotkeyPress() }
func (a *App) SimulateRelease() { a.onHotkeyRelease() }

func (a *App) startRecording() {
	cfg := a.currentConfig()
	if cfg == nil {
		a.emit(EventASRNotice, map[string]any{"message": "配置未加载，无法录音"})
		return
	}
	if cfg.Tencent.AppID == "" || cfg.Tencent.SecretID == "" || cfg.Tencent.SecretKey == "" {
		a.emit(EventASRNotice, map[string]any{"message": "腾讯云凭证未配置，无法录音"})
		return
	}

	if err := a.rec.Start(); err != nil {
		a.emit(EventASRError, map[string]any{"error": fmt.Sprintf("麦克风启动失败: %v", err)})
		return
	}
	a.recording.Store(true)
	a.emit(EventRecordingStarted, nil)
}

func (a *App) stopRecording() {
	pcm, err := a.rec.Stop()
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
	msgs := make([]llm.Message, 0, len(snapshot))
	for _, m := range snapshot {
		msgs = append(msgs, llm.Message{
			Role:             m.Role,
			Content:          m.Content,
			ReasoningContent: m.ReasoningContent,
		})
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

// NewConversation 取消进行中的 LLM，重置 messages。
func (a *App) NewConversation() {
	a.cancelLLM()
	cfg := a.currentConfig()
	prompt := ""
	if cfg != nil {
		prompt = cfg.DeepSeek.SystemPrompt
	}
	a.store.Reset(prompt)
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
	out["model"] = v.DeepSeek.Model
	out["base_url"] = v.DeepSeek.BaseURL
	out["thinking"] = v.DeepSeek.Thinking
	out["reasoning_effort"] = v.DeepSeek.ReasoningEffort
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
	case HotkeyKindRecord, HotkeyKindSend:
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
	})
	return nil
}

// validateHotkeyChange 校验新热键格式合法且不与另一个热键冲突。
//
// 抽成纯函数便于测试：传入 cfg 表示当前配置（不会修改），kind/raw 是请求。
func validateHotkeyChange(cfg *config.Config, kind, raw string) error {
	newSpec, err := hotkey.ParseSpec(raw)
	if err != nil {
		return fmt.Errorf("快捷键 %q 解析失败: %v", raw, err)
	}
	var otherRaw string
	switch kind {
	case HotkeyKindRecord:
		otherRaw = cfg.SendHotkey
	case HotkeyKindSend:
		otherRaw = cfg.RecordHotkey
	default:
		return fmt.Errorf("未知热键种类 %q", kind)
	}
	if strings.TrimSpace(otherRaw) == "" {
		return nil
	}
	otherSpec, err := hotkey.ParseSpec(otherRaw)
	if err != nil {
		// 现存的另一个热键解析不出来——不视为冲突，让用户至少能改这个
		return nil
	}
	if newSpec.Equal(otherSpec) {
		return fmt.Errorf("快捷键 %s 已被另一个热键使用", newSpec)
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
