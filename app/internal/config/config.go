// Package config 负责加载 config.json 与环境变量、合并产生最终 Config。
// 详见 docs/specs/2026-05-15-mockagent-design.md 第 6.1 / 6.2 节。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// 文件名常量。
const (
	FileName        = "config.json"
	ExampleFileName = "config.example.json"
)

// 环境变量键名。
const (
	EnvTencentAppID     = "TENCENT_APP_ID"
	EnvTencentSecretID  = "TENCENT_SECRET_ID"
	EnvTencentSecretKey = "TENCENT_SECRET_KEY"
	EnvDeepSeekAPIKey   = "DEEPSEEK_API_KEY"
	EnvDeepSeekModel    = "DEEPSEEK_MODEL"
	EnvDeepSeekBaseURL  = "DEEPSEEK_BASE_URL"
	// EnvHotkey 旧名（仅录音热键）；新代码用 EnvRecordHotkey。
	EnvHotkey       = "MOCK_AGENT_HOTKEY"
	EnvRecordHotkey = "MOCK_AGENT_RECORD_HOTKEY"
	EnvSendHotkey   = "MOCK_AGENT_SEND_HOTKEY"
	EnvSystemHotkey = "MOCK_AGENT_SYSTEM_HOTKEY"
)

// Tencent 腾讯云语音 SDK 凭证。
type Tencent struct {
	AppID     string `json:"app_id"`
	SecretID  string `json:"secret_id"`
	SecretKey string `json:"secret_key"`
}

// DeepSeek DeepSeek 大模型相关配置。
//
// 系统提示词从单一字符串改为"历史列表 + 当前活跃"：
//   - SystemPromptHistory：用户用过的所有 prompt（最近用的在前，无重复）
//   - ActiveSystemPrompt：当前生效的 prompt 内容（用内容本身做 key，
//     既然 history 内部唯一，直接用字符串就够了）
//   - LegacySystemPrompt：仅用于读取旧版 `system_prompt` 字段做迁移；
//     写入时清空。
type DeepSeek struct {
	APIKey              string   `json:"api_key"`
	BaseURL             string   `json:"base_url"`
	Model               string   `json:"model"`
	Thinking            string   `json:"thinking"`
	ReasoningEffort     string   `json:"reasoning_effort"`
	SystemPromptHistory []string `json:"system_prompt_history"`
	ActiveSystemPrompt  string   `json:"active_system_prompt"`

	// LegacySystemPrompt 仅做迁移用；写入时清空。
	LegacySystemPrompt string `json:"system_prompt,omitempty"`
}

// Audio 录音参数。
type Audio struct {
	SampleRate    int `json:"sample_rate"`
	Channels      int `json:"channels"`
	MinDurationMs int `json:"min_duration_ms"`
}

// Config 是合并文件与环境变量后的最终配置。
//
// 旧版本只有一个 `hotkey` 字段（仅录音热键）。新版本拆成
// `record_hotkey` 与 `send_hotkey` 两个字段。Load 阶段如果只看到旧
// `hotkey` 字段，会自动迁移到 RecordHotkey；Save 阶段写入新字段格式。
type Config struct {
	Tencent       Tencent  `json:"tencent"`
	DeepSeek      DeepSeek `json:"deepseek"`
	RecordHotkey  string   `json:"record_hotkey"`
	SendHotkey    string   `json:"send_hotkey"`
	SystemHotkey  string   `json:"system_hotkey"`
	Audio         Audio    `json:"audio"`

	// LegacyHotkey 仅用于读取旧字段做向后兼容；写入时不再使用。
	// 通过 json:"hotkey,omitempty" 让旧文件能被反序列化进来。
	LegacyHotkey string `json:"hotkey,omitempty"`

	// 加载时记录的源信息（不参与序列化），用于诊断与 OpenConfigFile。
	sourcePath string
}

// MaskedString 在错误信息或序列化中替代敏感字段的明文值。
const MaskedString = "***"

// 默认快捷键。
const (
	DefaultRecordHotkey = "F2"
	DefaultSendHotkey   = "F4"
	DefaultSystemHotkey = "F3"
)

// Default 提供录音参数等字段的默认值。
func Default() Config {
	return Config{
		DeepSeek: DeepSeek{
			BaseURL:             "https://api.deepseek.com",
			Model:               "deepseek-v4-pro",
			Thinking:            "enabled",
			ReasoningEffort:     "medium",
			SystemPromptHistory: []string{"You are a helpful assistant."},
			ActiveSystemPrompt:  "You are a helpful assistant.",
		},
		RecordHotkey: DefaultRecordHotkey,
		SendHotkey:   DefaultSendHotkey,
		SystemHotkey: DefaultSystemHotkey,
		Audio: Audio{
			SampleRate:    16000,
			Channels:      1,
			MinDurationMs: 300,
		},
	}
}

// SourcePath 返回本次加载实际使用的 config.json 绝对路径。
func (c *Config) SourcePath() string { return c.sourcePath }

// SetSourcePath 在测试或运行时手动设置源路径（Load 之外的场景）。
func (c *Config) SetSourcePath(p string) { c.sourcePath = p }

// String 实现 fmt.Stringer，对密钥字段做掩码处理。
func (c Config) String() string {
	masked := c
	masked.Tencent.SecretID = maskNonEmpty(c.Tencent.SecretID)
	masked.Tencent.SecretKey = maskNonEmpty(c.Tencent.SecretKey)
	masked.DeepSeek.APIKey = maskNonEmpty(c.DeepSeek.APIKey)
	return fmt.Sprintf("Config{Tencent:{AppID:%q SecretID:%s SecretKey:%s} "+
		"DeepSeek:{APIKey:%s BaseURL:%q Model:%q Thinking:%q ReasoningEffort:%q "+
		"PromptHistory:%d ActivePromptLen:%d} "+
		"RecordHotkey:%q SendHotkey:%q SystemHotkey:%q Audio:%+v Source:%q}",
		masked.Tencent.AppID, masked.Tencent.SecretID, masked.Tencent.SecretKey,
		masked.DeepSeek.APIKey, masked.DeepSeek.BaseURL, masked.DeepSeek.Model,
		masked.DeepSeek.Thinking, masked.DeepSeek.ReasoningEffort,
		len(masked.DeepSeek.SystemPromptHistory), len(masked.DeepSeek.ActiveSystemPrompt),
		masked.RecordHotkey, masked.SendHotkey, masked.SystemHotkey, masked.Audio, masked.sourcePath,
	)
}

// MaskedView 返回一份所有敏感字段已掩码的副本，可安全用于 GetConfig() 返回前端。
func (c Config) MaskedView() Config {
	v := c
	v.Tencent.SecretID = maskNonEmpty(c.Tencent.SecretID)
	v.Tencent.SecretKey = maskNonEmpty(c.Tencent.SecretKey)
	v.DeepSeek.APIKey = maskNonEmpty(c.DeepSeek.APIKey)
	v.LegacyHotkey = ""             // 不暴露给前端
	v.DeepSeek.LegacySystemPrompt = "" // 同上
	return v
}

func maskNonEmpty(s string) string {
	if s == "" {
		return ""
	}
	return MaskedString
}

// 验证错误的具体类型。
var (
	ErrTencentMissing     = errors.New("腾讯云凭证未配置（app_id / secret_id / secret_key）")
	ErrDeepSeekKeyMissing = errors.New("DeepSeek API Key 未配置")
	ErrHotkeyEmpty        = errors.New("hotkey 不能为空")
)

// Validate 检查必填字段。
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Tencent.AppID) == "" ||
		strings.TrimSpace(c.Tencent.SecretID) == "" ||
		strings.TrimSpace(c.Tencent.SecretKey) == "" {
		return ErrTencentMissing
	}
	if strings.TrimSpace(c.DeepSeek.APIKey) == "" {
		return ErrDeepSeekKeyMissing
	}
	if strings.TrimSpace(c.RecordHotkey) == "" {
		return ErrHotkeyEmpty
	}
	return nil
}

var loadMu sync.Mutex

// Load 从 dir 中读取 config.json；当 config.json 不存在但 config.example.json 存在时
// 按字节复制示例文件作为初始配置文件，再继续解析。环境变量覆盖文件值。
func Load(dir string) (*Config, error) {
	loadMu.Lock()
	defer loadMu.Unlock()

	configPath := filepath.Join(dir, FileName)
	examplePath := filepath.Join(dir, ExampleFileName)

	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		if _, ex := os.Stat(examplePath); ex == nil {
			if cerr := copyFile(examplePath, configPath); cerr != nil {
				return nil, fmt.Errorf("从示例复制 %s 失败: %w", FileName, cerr)
			}
		} else {
			return nil, fmt.Errorf("配置文件 %s 不存在且未找到 %s", FileName, ExampleFileName)
		}
	} else if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", FileName, err)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", FileName, err)
	}

	cfg := Default()
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", FileName, err)
	}
	cfg.sourcePath, _ = filepath.Abs(configPath)

	// 向后兼容：如果文件里出现了旧字段 hotkey，且文件里**没有显式给出**新字段，
	// 把旧值迁移到 RecordHotkey。需要二次解析判断字段是否在文件里出现，否则
	// Default() 已经填了 RecordHotkey 而无从分辨。
	if cfg.LegacyHotkey != "" {
		var probe struct {
			RecordHotkey *string `json:"record_hotkey"`
		}
		_ = json.Unmarshal(raw, &probe)
		if probe.RecordHotkey == nil {
			cfg.RecordHotkey = cfg.LegacyHotkey
		}
	}
	cfg.LegacyHotkey = ""

	// 仍允许 SendHotkey / SystemHotkey 缺省。
	if cfg.SendHotkey == "" {
		cfg.SendHotkey = DefaultSendHotkey
	}
	if cfg.RecordHotkey == "" {
		cfg.RecordHotkey = DefaultRecordHotkey
	}
	if cfg.SystemHotkey == "" {
		cfg.SystemHotkey = DefaultSystemHotkey
	}

	// 系统提示词迁移：如果文件里只有旧 system_prompt 字段，没有 history/active，
	// 把旧值作为唯一一条历史并设为 active。
	if cfg.DeepSeek.LegacySystemPrompt != "" {
		var probe struct {
			Hist   *[]string `json:"system_prompt_history"`
			Active *string   `json:"active_system_prompt"`
		}
		var ds struct {
			DeepSeek struct {
				Hist   *[]string `json:"system_prompt_history"`
				Active *string   `json:"active_system_prompt"`
			} `json:"deepseek"`
		}
		_ = json.Unmarshal(raw, &ds)
		probe.Hist = ds.DeepSeek.Hist
		probe.Active = ds.DeepSeek.Active
		if probe.Hist == nil && probe.Active == nil {
			cfg.DeepSeek.SystemPromptHistory = []string{cfg.DeepSeek.LegacySystemPrompt}
			cfg.DeepSeek.ActiveSystemPrompt = cfg.DeepSeek.LegacySystemPrompt
		}
	}
	cfg.DeepSeek.LegacySystemPrompt = ""

	applyEnvOverrides(&cfg)

	return &cfg, nil
}

// Save 把当前 Config 序列化回 sourcePath 指定的 config.json。
//
// 仅持久化 Config 的字段（不会保存 LegacyHotkey）。环境变量覆盖的值
// 也会被写入磁盘——这是用户的预期：UI 改了的内容下次启动还在。
func (c *Config) Save() error {
	if c.sourcePath == "" {
		return errors.New("config: 未知保存路径（sourcePath 为空）")
	}
	// 写出前先清空 Legacy 字段，避免新旧 key 同时出现。
	out := *c
	out.LegacyHotkey = ""
	out.DeepSeek.LegacySystemPrompt = ""
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	// 加末尾换行，更友好。
	data = append(data, '\n')

	tmp := c.sourcePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, c.sourcePath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换配置文件失败: %w", err)
	}
	return nil
}

// applyEnvOverrides 用环境变量覆盖 cfg 中已设置的字段。
func applyEnvOverrides(cfg *Config) {
	if v, ok := os.LookupEnv(EnvTencentAppID); ok {
		cfg.Tencent.AppID = v
	}
	if v, ok := os.LookupEnv(EnvTencentSecretID); ok {
		cfg.Tencent.SecretID = v
	}
	if v, ok := os.LookupEnv(EnvTencentSecretKey); ok {
		cfg.Tencent.SecretKey = v
	}
	if v, ok := os.LookupEnv(EnvDeepSeekAPIKey); ok {
		cfg.DeepSeek.APIKey = v
	}
	if v, ok := os.LookupEnv(EnvDeepSeekModel); ok {
		cfg.DeepSeek.Model = v
	}
	if v, ok := os.LookupEnv(EnvDeepSeekBaseURL); ok {
		cfg.DeepSeek.BaseURL = v
	}
	// 优先 EnvRecordHotkey；若未设置回落到旧的 EnvHotkey。
	if v, ok := os.LookupEnv(EnvRecordHotkey); ok {
		cfg.RecordHotkey = v
	} else if v, ok := os.LookupEnv(EnvHotkey); ok {
		cfg.RecordHotkey = v
	}
	if v, ok := os.LookupEnv(EnvSendHotkey); ok {
		cfg.SendHotkey = v
	}
	if v, ok := os.LookupEnv(EnvSystemHotkey); ok {
		cfg.SystemHotkey = v
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// OpenInEditor 用系统默认编辑器打开 path 所指文件。
func OpenInEditor(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("文件不存在: %s", abs)
	}
	switch runtime.GOOS {
	case "windows":
		return exec.Command("cmd", "/c", "start", "", abs).Run()
	case "darwin":
		return exec.Command("open", abs).Run()
	default:
		return exec.Command("xdg-open", abs).Run()
	}
}


// SaveSystemPrompt 把 content 设为新的 active prompt，并整理历史：
//   - content 为空时不做任何事，返回错误
//   - content 已存在于历史中：将其移到第一位
//   - 否则：追加到第一位
//
// 调用方应在之后调用 Config.Save() 落盘。
func (d *DeepSeek) SaveSystemPrompt(content string) error {
	if content == "" {
		return errors.New("system prompt 不能为空")
	}
	d.SystemPromptHistory = removeString(d.SystemPromptHistory, content)
	d.SystemPromptHistory = append([]string{content}, d.SystemPromptHistory...)
	d.ActiveSystemPrompt = content
	return nil
}

// DeleteSystemPromptHistory 从历史中删除 content。
//   - 不存在则返回错误
//   - 删除的是 active：active 自动改为新的第一项；历史空时 active 改为 ""
func (d *DeepSeek) DeleteSystemPromptHistory(content string) error {
	idx := indexOfString(d.SystemPromptHistory, content)
	if idx < 0 {
		return errors.New("该历史项不存在")
	}
	d.SystemPromptHistory = append(d.SystemPromptHistory[:idx], d.SystemPromptHistory[idx+1:]...)
	if d.ActiveSystemPrompt == content {
		if len(d.SystemPromptHistory) > 0 {
			d.ActiveSystemPrompt = d.SystemPromptHistory[0]
		} else {
			d.ActiveSystemPrompt = ""
		}
	}
	return nil
}

func removeString(s []string, v string) []string {
	out := make([]string, 0, len(s))
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

func indexOfString(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
