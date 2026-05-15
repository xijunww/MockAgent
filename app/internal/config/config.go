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
	EnvHotkey           = "MOCK_AGENT_HOTKEY"
)

// Tencent 腾讯云语音 SDK 凭证。
type Tencent struct {
	AppID     string `json:"app_id"`
	SecretID  string `json:"secret_id"`
	SecretKey string `json:"secret_key"`
}

// DeepSeek DeepSeek 大模型相关配置。
type DeepSeek struct {
	APIKey          string `json:"api_key"`
	BaseURL         string `json:"base_url"`
	Model           string `json:"model"`
	Thinking        string `json:"thinking"`
	ReasoningEffort string `json:"reasoning_effort"`
	SystemPrompt    string `json:"system_prompt"`
}

// Audio 录音参数。
type Audio struct {
	SampleRate    int `json:"sample_rate"`
	Channels      int `json:"channels"`
	MinDurationMs int `json:"min_duration_ms"`
}

// Config 是合并文件与环境变量后的最终配置。
type Config struct {
	Tencent  Tencent  `json:"tencent"`
	DeepSeek DeepSeek `json:"deepseek"`
	Hotkey   string   `json:"hotkey"`
	Audio    Audio    `json:"audio"`

	// 加载时记录的源信息（不参与序列化），用于诊断与 OpenConfigFile。
	sourcePath string `json:"-"`
}

// MaskedString 在错误信息或序列化中替代敏感字段的明文值。
const MaskedString = "***"

// Default 提供录音参数等字段的默认值。
func Default() Config {
	return Config{
		DeepSeek: DeepSeek{
			BaseURL:         "https://api.deepseek.com",
			Model:           "deepseek-v4-pro",
			Thinking:        "enabled",
			ReasoningEffort: "medium",
			SystemPrompt:    "You are a helpful assistant.",
		},
		Hotkey: "F2",
		Audio: Audio{
			SampleRate:    16000,
			Channels:      1,
			MinDurationMs: 300,
		},
	}
}

// SourcePath 返回本次加载实际使用的 config.json 绝对路径。
func (c *Config) SourcePath() string { return c.sourcePath }

// String 实现 fmt.Stringer，对密钥字段做掩码处理。
// 任何 %v / %+v / 默认 fmt 输出都会走到这里。
func (c Config) String() string {
	masked := c
	masked.Tencent.SecretID = maskNonEmpty(c.Tencent.SecretID)
	masked.Tencent.SecretKey = maskNonEmpty(c.Tencent.SecretKey)
	masked.DeepSeek.APIKey = maskNonEmpty(c.DeepSeek.APIKey)
	return fmt.Sprintf("Config{Tencent:{AppID:%q SecretID:%s SecretKey:%s} "+
		"DeepSeek:{APIKey:%s BaseURL:%q Model:%q Thinking:%q ReasoningEffort:%q SystemPromptLen:%d} "+
		"Hotkey:%q Audio:%+v Source:%q}",
		masked.Tencent.AppID, masked.Tencent.SecretID, masked.Tencent.SecretKey,
		masked.DeepSeek.APIKey, masked.DeepSeek.BaseURL, masked.DeepSeek.Model,
		masked.DeepSeek.Thinking, masked.DeepSeek.ReasoningEffort, len(masked.DeepSeek.SystemPrompt),
		masked.Hotkey, masked.Audio, masked.sourcePath,
	)
}

// MaskedView 返回一份所有敏感字段已掩码的副本，可安全用于 GetConfig() 返回前端。
func (c Config) MaskedView() Config {
	v := c
	v.Tencent.SecretID = maskNonEmpty(c.Tencent.SecretID)
	v.Tencent.SecretKey = maskNonEmpty(c.Tencent.SecretKey)
	v.DeepSeek.APIKey = maskNonEmpty(c.DeepSeek.APIKey)
	return v
}

func maskNonEmpty(s string) string {
	if s == "" {
		return ""
	}
	return MaskedString
}

// 验证错误的具体类型，便于上层判断。
var (
	// ErrTencentMissing 任一腾讯云凭证缺失。
	ErrTencentMissing = errors.New("腾讯云凭证未配置（app_id / secret_id / secret_key）")
	// ErrDeepSeekKeyMissing DeepSeek API Key 缺失。
	ErrDeepSeekKeyMissing = errors.New("DeepSeek API Key 未配置")
	// ErrHotkeyEmpty 热键为空。
	ErrHotkeyEmpty = errors.New("hotkey 不能为空")
)

// Validate 检查必填字段。返回 nil 表示完整可用；
// 返回的错误可与 ErrTencentMissing / ErrDeepSeekKeyMissing / ErrHotkeyEmpty 比较。
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Tencent.AppID) == "" ||
		strings.TrimSpace(c.Tencent.SecretID) == "" ||
		strings.TrimSpace(c.Tencent.SecretKey) == "" {
		return ErrTencentMissing
	}
	if strings.TrimSpace(c.DeepSeek.APIKey) == "" {
		return ErrDeepSeekKeyMissing
	}
	if strings.TrimSpace(c.Hotkey) == "" {
		return ErrHotkeyEmpty
	}
	return nil
}

var loadMu sync.Mutex

// Load 从 dir 中读取 config.json；当 config.json 不存在但 config.example.json 存在时
// 按字节复制示例文件作为初始配置文件，再继续解析。环境变量覆盖文件值。
//
// 返回的错误已剥离敏感信息（不会包含密钥明文）。
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

	applyEnvOverrides(&cfg)

	return &cfg, nil
}

// applyEnvOverrides 用环境变量覆盖 cfg 中已设置的字段。
// 仅当环境变量被显式设置（os.LookupEnv ok=true）时才覆盖。
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
	if v, ok := os.LookupEnv(EnvHotkey); ok {
		cfg.Hotkey = v
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
// Windows 用 cmd /c start ""，macOS 用 open，Linux 用 xdg-open。
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
		// "" 是 start 的窗口标题占位符，避免 abs 被当成标题。
		return exec.Command("cmd", "/c", "start", "", abs).Run()
	case "darwin":
		return exec.Command("open", abs).Run()
	default:
		return exec.Command("xdg-open", abs).Run()
	}
}
