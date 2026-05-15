package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLoad_CopiesFromExampleWhenMissing(t *testing.T) {
	dir := t.TempDir()
	example := `{"tencent":{"app_id":"a","secret_id":"b","secret_key":"c"},` +
		`"deepseek":{"api_key":"sk-1","base_url":"https://api.deepseek.com","model":"deepseek-v4-pro"},` +
		`"hotkey":"F2","audio":{"sample_rate":16000,"channels":1,"min_duration_ms":300}}`
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
	dir := t.TempDir()
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error when both files missing")
	}
}

func TestEnvOverridesFileValues(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, FileName), `{
		"tencent":{"app_id":"file-app","secret_id":"file-sid","secret_key":"file-skey"},
		"deepseek":{"api_key":"file-key","model":"file-model"},
		"hotkey":"F2"
	}`)

	// Isolate from host env: explicitly unset all known vars, then set the ones we test.
	for _, k := range []string{EnvTencentAppID, EnvTencentSecretID, EnvTencentSecretKey,
		EnvDeepSeekAPIKey, EnvDeepSeekModel, EnvDeepSeekBaseURL, EnvHotkey} {
		old, had := os.LookupEnv(k)
		os.Unsetenv(k)
		if had {
			t.Cleanup(func() { os.Setenv(k, old) })
		}
	}
	t.Setenv(EnvTencentAppID, "env-app")
	t.Setenv(EnvDeepSeekAPIKey, "env-key")
	t.Setenv(EnvHotkey, "Ctrl+Alt+Space")

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
	if cfg.Hotkey != "Ctrl+Alt+Space" {
		t.Errorf("Hotkey env override failed: %q", cfg.Hotkey)
	}
}

func TestStringMasksSecrets(t *testing.T) {
	c := Config{
		Tencent:  Tencent{AppID: "1234567890", SecretID: "AKID-very-secret", SecretKey: "skey-also-secret"},
		DeepSeek: DeepSeek{APIKey: "sk-mysecret-xyz", Model: "deepseek-v4-pro"},
		Hotkey:   "F2",
	}
	s := c.String()
	for _, secret := range []string{"AKID-very-secret", "skey-also-secret", "sk-mysecret-xyz"} {
		if strings.Contains(s, secret) {
			t.Errorf("plaintext secret leaked into String(): %q in %q", secret, s)
		}
	}
	// Default fmt verbs should also use String().
	formatted := fmt.Sprintf("cfg=%v", c)
	for _, secret := range []string{"AKID-very-secret", "sk-mysecret-xyz"} {
		if strings.Contains(formatted, secret) {
			t.Errorf("plaintext secret leaked via %%v: %q in %q", secret, formatted)
		}
	}
}

func TestMaskedView(t *testing.T) {
	c := Config{
		Tencent:  Tencent{AppID: "1234", SecretID: "sid", SecretKey: "skey"},
		DeepSeek: DeepSeek{APIKey: "sk-1"},
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
	// Empty secrets stay empty (so UI knows it's missing).
	empty := Config{}.MaskedView()
	if empty.DeepSeek.APIKey != "" {
		t.Errorf("empty key should remain empty, got %q", empty.DeepSeek.APIKey)
	}

	// Round-trip through JSON should not reintroduce plaintext (we never marshal originals here, only check API).
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "skey") || strings.Contains(string(b), "sk-1") {
		t.Errorf("masked view JSON leaks secrets: %s", b)
	}
}

func TestValidate(t *testing.T) {
	full := Config{
		Tencent:  Tencent{AppID: "a", SecretID: "b", SecretKey: "c"},
		DeepSeek: DeepSeek{APIKey: "sk"},
		Hotkey:   "F2",
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
	noHotkey.Hotkey = ""
	if err := noHotkey.Validate(); !errors.Is(err, ErrHotkeyEmpty) {
		t.Errorf("expected ErrHotkeyEmpty, got %v", err)
	}
}
