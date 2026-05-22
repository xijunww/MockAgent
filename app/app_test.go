package main

import (
	"strings"
	"testing"

	"mockagent/internal/config"
	"mockagent/internal/llm"
)

func TestValidateHotkeyChange(t *testing.T) {
	cfg := &config.Config{
		RecordHotkey: "F2",
		SendHotkey:   "F4",
	}

	// 改 record 与 send 不同的键 → 通过
	if err := validateHotkeyChange(cfg, HotkeyKindRecord, "Ctrl+Alt+R"); err != nil {
		t.Errorf("non-conflicting change: %v", err)
	}
	// 改 send 与 record 不同的键 → 通过
	if err := validateHotkeyChange(cfg, HotkeyKindSend, "Ctrl+Alt+S"); err != nil {
		t.Errorf("non-conflicting change: %v", err)
	}
	// 改 record 为现有 send → 冲突
	if err := validateHotkeyChange(cfg, HotkeyKindRecord, "F4"); err == nil ||
		!strings.Contains(err.Error(), "已被") {
		t.Errorf("expected conflict error, got %v", err)
	}
	// 改 send 为现有 record → 冲突
	if err := validateHotkeyChange(cfg, HotkeyKindSend, "F2"); err == nil ||
		!strings.Contains(err.Error(), "已被") {
		t.Errorf("expected conflict error, got %v", err)
	}
	// 大小写不敏感冲突检测
	if err := validateHotkeyChange(cfg, HotkeyKindRecord, "f4"); err == nil {
		t.Errorf("case-insensitive conflict not detected")
	}
	// 修饰键顺序无关
	cfg2 := &config.Config{RecordHotkey: "Ctrl+Alt+Space", SendHotkey: "F4"}
	if err := validateHotkeyChange(cfg2, HotkeyKindSend, "alt+ctrl+space"); err == nil {
		t.Errorf("modifier-order-independent conflict not detected")
	}
	// 非法键名
	if err := validateHotkeyChange(cfg, HotkeyKindRecord, "Ctrl+Foo"); err == nil {
		t.Errorf("invalid spec should fail")
	}
	// 未知 kind
	if err := validateHotkeyChange(cfg, "weird", "F1"); err == nil {
		t.Errorf("unknown kind should fail")
	}
	// 把当前键改为自身（同值）→ 不视为冲突（应通过，因为另一个热键不变）
	cfg3 := &config.Config{RecordHotkey: "F2", SendHotkey: "F4"}
	if err := validateHotkeyChange(cfg3, HotkeyKindRecord, "F2"); err != nil {
		t.Errorf("setting record to its current value should pass, got %v", err)
	}
	// 另一个热键为空时不冲突
	cfg4 := &config.Config{RecordHotkey: "F2", SendHotkey: ""}
	if err := validateHotkeyChange(cfg4, HotkeyKindRecord, "F2"); err != nil {
		t.Errorf("empty other should pass, got %v", err)
	}

	// 三方冲突覆盖：record / send / system 互相不能撞键。
	cfg5 := &config.Config{RecordHotkey: "F2", SendHotkey: "F4", SystemHotkey: "F3"}

	// system 改为现有 record → 冲突
	if err := validateHotkeyChange(cfg5, HotkeyKindSystem, "F2"); err == nil ||
		!strings.Contains(err.Error(), "已被") {
		t.Errorf("system vs record conflict not detected, got %v", err)
	}
	// system 改为现有 send → 冲突
	if err := validateHotkeyChange(cfg5, HotkeyKindSystem, "F4"); err == nil ||
		!strings.Contains(err.Error(), "已被") {
		t.Errorf("system vs send conflict not detected, got %v", err)
	}
	// record 改为现有 system → 冲突
	if err := validateHotkeyChange(cfg5, HotkeyKindRecord, "F3"); err == nil ||
		!strings.Contains(err.Error(), "已被") {
		t.Errorf("record vs system conflict not detected, got %v", err)
	}
	// send 改为现有 system → 冲突
	if err := validateHotkeyChange(cfg5, HotkeyKindSend, "F3"); err == nil ||
		!strings.Contains(err.Error(), "已被") {
		t.Errorf("send vs system conflict not detected, got %v", err)
	}
	// system 改为不冲突的新键 → 通过
	if err := validateHotkeyChange(cfg5, HotkeyKindSystem, "Ctrl+Alt+J"); err != nil {
		t.Errorf("non-conflicting system change: %v", err)
	}
	// system 改为自身 → 不视为冲突
	if err := validateHotkeyChange(cfg5, HotkeyKindSystem, "F3"); err != nil {
		t.Errorf("setting system to its current value should pass, got %v", err)
	}
}


func TestInjectDocsIntoSystemMessage(t *testing.T) {
	t.Run("empty extra is no-op", func(t *testing.T) {
		msgs := []llm.Message{{Role: "user", Content: "hi"}}
		injectDocsIntoSystemMessage(&msgs, "")
		if len(msgs) != 1 || msgs[0].Role != "user" {
			t.Errorf("expected unchanged, got %+v", msgs)
		}
	})

	t.Run("appends to existing system", func(t *testing.T) {
		msgs := []llm.Message{
			{Role: "system", Content: "be helpful"},
			{Role: "user", Content: "hi"},
		}
		injectDocsIntoSystemMessage(&msgs, "DOCS")
		if msgs[0].Content != "be helpful\n\nDOCS" {
			t.Errorf("got %q", msgs[0].Content)
		}
		if msgs[1].Content != "hi" {
			t.Errorf("user msg should not change")
		}
	})

	t.Run("inserts when no system", func(t *testing.T) {
		msgs := []llm.Message{{Role: "user", Content: "hi"}}
		injectDocsIntoSystemMessage(&msgs, "DOCS")
		if len(msgs) != 2 || msgs[0].Role != "system" || msgs[0].Content != "DOCS" {
			t.Errorf("unexpected: %+v", msgs)
		}
		if msgs[1].Content != "hi" {
			t.Errorf("user msg lost")
		}
	})

	t.Run("system has empty content", func(t *testing.T) {
		msgs := []llm.Message{
			{Role: "system", Content: ""},
			{Role: "user", Content: "hi"},
		}
		injectDocsIntoSystemMessage(&msgs, "DOCS")
		if msgs[0].Content != "DOCS" {
			t.Errorf("got %q", msgs[0].Content)
		}
	})
}
