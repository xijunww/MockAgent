package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// 在每个测试开头隔离环境变量，避免主机环境污染。
func isolateEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		EnvTencentAppID, EnvTencentSecretID, EnvTencentSecretKey,
		EnvDeepSeekAPIKey, EnvDeepSeekModel, EnvDeepSeekBaseURL,
		EnvHotkey, EnvRecordHotkey, EnvSendHotkey,
	} {
		old, had := os.LookupEnv(k)
		os.Unsetenv(k)
		if had {
			k := k
			old := old
			t.Cleanup(func() { os.Setenv(k, old) })
		}
	}
}

func TestLoad_CopiesFromExampleWhenMissing(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	example := `{"tencent":{"app_id":"a","secret_id":"b","secret_key":"c"},` +
		`"deepseek":{"api_key":"sk-1","base_url":"https://api.deepseek.com","model":"deepseek-v4-pro"},` +
		`"record_hotkey":"F2","send_hotkey":"F4","audio":{"sample_rate":16000,"channels":1,"min_duration_ms":300}}`
	writeFile(t, filepath.Join(dir, ExampleFileName), example)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tencent.AppID != "a" || cfg.DeepSeek.Model != "deepseek-v4-pro" {
		t.Fatalf("unexpected cfg: %#v", cfg)
	}

	got, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("config.json should exist: %v", err)
	}
	if string(got) != example {
		t.Fatalf("config.json bytes do not match example.\nwant: %s\ngot: %s", example, string(got))
	}
}

func TestLoad_ParseError(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, FileName), "{not json")
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error on bad json")
	}
	if !strings.Contains(err.Error(), "解析") {
		t.Fatalf("error should mention parse failure, got: %v", err)
	}
}

func TestLoad_MissingFileAndExample(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error when both files missing")
	}
}

func TestLoad_BackwardsCompatibleHotkey(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	// 旧文件只有 hotkey，没有 record_hotkey / send_hotkey
	writeFile(t, filepath.Join(dir, FileName), `{
		"tencent":{"app_id":"a","secret_id":"b","secret_key":"c"},
		"deepseek":{"api_key":"sk-1"},
		"hotkey":"Ctrl+Alt+R"
	}`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RecordHotkey != "Ctrl+Alt+R" {
		t.Errorf("legacy hotkey should migrate to RecordHotkey, got %q", cfg.RecordHotkey)
	}
	if cfg.SendHotkey != DefaultSendHotkey {
		t.Errorf("SendHotkey should default to %q, got %q", DefaultSendHotkey, cfg.SendHotkey)
	}
	if cfg.LegacyHotkey != "" {
		t.Errorf("LegacyHotkey should be cleared after migration, got %q", cfg.LegacyHotkey)
	}
}

func TestLoad_NewFieldsTakePrecedence(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	// 同时出现新旧字段时优先用新字段
	writeFile(t, filepath.Join(dir, FileName), `{
		"tencent":{"app_id":"a","secret_id":"b","secret_key":"c"},
		"deepseek":{"api_key":"sk-1"},
		"hotkey":"F1",
		"record_hotkey":"F2",
		"send_hotkey":"F4"
	}`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RecordHotkey != "F2" {
		t.Errorf("new field should win, got %q", cfg.RecordHotkey)
	}
	if cfg.SendHotkey != "F4" {
		t.Errorf("SendHotkey: got %q want F4", cfg.SendHotkey)
	}
}

func TestEnvOverridesFileValues(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, FileName), `{
		"tencent":{"app_id":"file-app","secret_id":"file-sid","secret_key":"file-skey"},
		"deepseek":{"api_key":"file-key","model":"file-model"},
		"record_hotkey":"F2"
	}`)

	t.Setenv(EnvTencentAppID, "env-app")
	t.Setenv(EnvDeepSeekAPIKey, "env-key")
	t.Setenv(EnvRecordHotkey, "Ctrl+Alt+Space")
	t.Setenv(EnvSendHotkey, "Ctrl+Enter")

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tencent.AppID != "env-app" {
		t.Errorf("Tencent.AppID env override failed: %q", cfg.Tencent.AppID)
	}
	if cfg.Tencent.SecretID != "file-sid" {
		t.Errorf("Tencent.SecretID should keep file value, got %q", cfg.Tencent.SecretID)
	}
	if cfg.DeepSeek.APIKey != "env-key" {
		t.Errorf("DeepSeek.APIKey env override failed: %q", cfg.DeepSeek.APIKey)
	}
	if cfg.DeepSeek.Model != "file-model" {
		t.Errorf("DeepSeek.Model should keep file value, got %q", cfg.DeepSeek.Model)
	}
	if cfg.RecordHotkey != "Ctrl+Alt+Space" {
		t.Errorf("RecordHotkey env override failed: %q", cfg.RecordHotkey)
	}
	if cfg.SendHotkey != "Ctrl+Enter" {
		t.Errorf("SendHotkey env override failed: %q", cfg.SendHotkey)
	}
}

func TestEnvLegacyHotkeyOverride(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, FileName), `{
		"tencent":{"app_id":"a","secret_id":"b","secret_key":"c"},
		"deepseek":{"api_key":"sk"},
		"record_hotkey":"F2"
	}`)
	// 仅设置旧名 EnvHotkey，应当生效
	t.Setenv(EnvHotkey, "Ctrl+Alt+Q")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RecordHotkey != "Ctrl+Alt+Q" {
		t.Errorf("legacy env should override, got %q", cfg.RecordHotkey)
	}
}

func TestStringMasksSecrets(t *testing.T) {
	c := Config{
		Tencent:      Tencent{AppID: "1234567890", SecretID: "AKID-very-secret", SecretKey: "skey-also-secret"},
		DeepSeek:     DeepSeek{APIKey: "sk-mysecret-xyz", Model: "deepseek-v4-pro"},
		RecordHotkey: "F2",
		SendHotkey:   "F4",
	}
	s := c.String()
	for _, secret := range []string{"AKID-very-secret", "skey-also-secret", "sk-mysecret-xyz"} {
		if strings.Contains(s, secret) {
			t.Errorf("plaintext secret leaked into String(): %q in %q", secret, s)
		}
	}
	formatted := fmt.Sprintf("cfg=%v", c)
	for _, secret := range []string{"AKID-very-secret", "sk-mysecret-xyz"} {
		if strings.Contains(formatted, secret) {
			t.Errorf("plaintext secret leaked via %%v: %q in %q", secret, formatted)
		}
	}
}

func TestMaskedView(t *testing.T) {
	c := Config{
		Tencent:      Tencent{AppID: "1234", SecretID: "sid", SecretKey: "skey"},
		DeepSeek:     DeepSeek{APIKey: "sk-1"},
		LegacyHotkey: "OLD",
	}
	v := c.MaskedView()
	if v.Tencent.SecretID != MaskedString || v.Tencent.SecretKey != MaskedString {
		t.Errorf("Tencent secrets not masked: %+v", v.Tencent)
	}
	if v.DeepSeek.APIKey != MaskedString {
		t.Errorf("DeepSeek key not masked: %q", v.DeepSeek.APIKey)
	}
	if v.Tencent.AppID != "1234" {
		t.Errorf("AppID should NOT be masked: %q", v.Tencent.AppID)
	}
	if v.LegacyHotkey != "" {
		t.Errorf("MaskedView should hide LegacyHotkey, got %q", v.LegacyHotkey)
	}
	empty := Config{}.MaskedView()
	if empty.DeepSeek.APIKey != "" {
		t.Errorf("empty key should remain empty, got %q", empty.DeepSeek.APIKey)
	}

	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "skey") || strings.Contains(string(b), "sk-1") {
		t.Errorf("masked view JSON leaks secrets: %s", b)
	}
}

func TestValidate(t *testing.T) {
	full := Config{
		Tencent:      Tencent{AppID: "a", SecretID: "b", SecretKey: "c"},
		DeepSeek:     DeepSeek{APIKey: "sk"},
		RecordHotkey: "F2",
	}
	if err := full.Validate(); err != nil {
		t.Errorf("full config should validate, got %v", err)
	}

	noTencent := full
	noTencent.Tencent.SecretKey = ""
	if err := noTencent.Validate(); !errors.Is(err, ErrTencentMissing) {
		t.Errorf("expected ErrTencentMissing, got %v", err)
	}

	noKey := full
	noKey.DeepSeek.APIKey = "  "
	if err := noKey.Validate(); !errors.Is(err, ErrDeepSeekKeyMissing) {
		t.Errorf("expected ErrDeepSeekKeyMissing, got %v", err)
	}

	noHotkey := full
	noHotkey.RecordHotkey = ""
	if err := noHotkey.Validate(); !errors.Is(err, ErrHotkeyEmpty) {
		t.Errorf("expected ErrHotkeyEmpty, got %v", err)
	}
}

func TestSave_RoundTrip(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	original := Default()
	original.Tencent = Tencent{AppID: "id", SecretID: "sid", SecretKey: "skey"}
	original.DeepSeek.APIKey = "sk-x"
	original.RecordHotkey = "Ctrl+Alt+R"
	original.SendHotkey = "Ctrl+Enter"
	original.SetSourcePath(path)

	if err := original.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// sourcePath 由 Load 重新填，比较时忽略
	original.sourcePath = ""
	loaded.sourcePath = ""
	if !reflect.DeepEqual(*loaded, original) {
		t.Errorf("round trip mismatch.\noriginal: %#v\nloaded:   %#v", original, *loaded)
	}
}

func TestSave_DropsLegacyHotkey(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	cfg := Default()
	cfg.Tencent = Tencent{AppID: "x", SecretID: "y", SecretKey: "z"}
	cfg.DeepSeek.APIKey = "sk"
	cfg.LegacyHotkey = "F1" // 不应该写入
	cfg.SetSourcePath(path)
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `"hotkey"`) {
		t.Errorf("Save should drop legacy hotkey, got: %s", raw)
	}
	if !strings.Contains(string(raw), `"record_hotkey"`) {
		t.Errorf("Save should write record_hotkey, got: %s", raw)
	}
}

func TestSave_NoSourcePath(t *testing.T) {
	cfg := Default()
	if err := cfg.Save(); err == nil {
		t.Error("Save without sourcePath should fail")
	}
}
