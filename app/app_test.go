package main

import (
	"strings"
	"testing"

	"mockagent/internal/config"
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
}
