package hotkey

import "testing"

func TestParseSpec_Valid(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"F2", "F2"},
		{"f2", "F2"},
		{"Space", "Space"},
		{"Ctrl+Alt+Space", "Ctrl+Alt+Space"},
		{"alt+ctrl+space", "Ctrl+Alt+Space"},     // 顺序归一化
		{"Ctrl+Shift+R", "Ctrl+Shift+R"},
		{"Alt+Q", "Alt+Q"},
		{"Win+1", "Win+1"},
		{"  Ctrl + Alt + S  ", "Ctrl+Alt+S"},     // 段内空白容忍
		{"Control+Alt+Shift+F12", "Ctrl+Alt+Shift+F12"}, // Control 同义于 Ctrl
		{"super+meta+Q", "Win+Q"},                // super/meta 同义于 Win，并去重
	}
	for _, tc := range cases {
		got, err := ParseSpec(tc.in)
		if err != nil {
			t.Errorf("ParseSpec(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got.String() != tc.want {
			t.Errorf("ParseSpec(%q).String() = %q, want %q", tc.in, got.String(), tc.want)
		}
	}
}

func TestParseSpec_Invalid(t *testing.T) {
	cases := []string{
		"",
		"   ",
		"+",
		"Ctrl",          // 仅修饰键
		"Ctrl+",         // 末尾空段
		"+Ctrl+A",       // 起始空段
		"Ctrl+A+B",      // 主键不在末尾
		"Ctrl+Foo",      // 未知键
		"Ctrl+Ctrl+A",   // 重复修饰键
	}
	for _, in := range cases {
		_, err := ParseSpec(in)
		if err == nil {
			t.Errorf("ParseSpec(%q): expected error, got nil", in)
		}
	}
}

func TestSpecEqual(t *testing.T) {
	a, _ := ParseSpec("Ctrl+Alt+Space")
	b, _ := ParseSpec("alt+ctrl+space")
	c, _ := ParseSpec("Ctrl+Shift+Space")
	if !a.Equal(b) {
		t.Error("Ctrl+Alt+Space and alt+ctrl+space should be equal")
	}
	if a.Equal(c) {
		t.Error("Ctrl+Alt+Space and Ctrl+Shift+Space should not be equal")
	}
}
