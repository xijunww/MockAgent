package hotkey

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	xhotkey "golang.design/x/hotkey"
)

// Spec 是用户配置的快捷键的解析结果。
//
// Mods 中的元素已经去重并按 canonical 顺序排序：Ctrl < Alt < Shift < Win。
type Spec struct {
	Mods []xhotkey.Modifier
	Key  xhotkey.Key

	// 原始小写归一化后的部件，便于 String() 输出与去重比较。
	modNames []string
	keyName  string
}

// ParseSpec 解析快捷键字符串。支持的格式见 design.md 第 6.3 节：
//   - 单键：`F1`–`F12`、`Space`
//   - 修饰键 + 键：`Ctrl+Alt+Space`、`Alt+Q`、`Ctrl+Shift+R` 等
//
// 修饰键之间用 `+` 分隔，大小写不敏感；末尾必须是一个有效的主键。
// 成功时返回的 Spec 形态对相同语义的输入是稳定的（次序、大小写归一化）。
func ParseSpec(s string) (Spec, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Spec{}, errors.New("快捷键不能为空")
	}

	parts := strings.Split(raw, "+")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
		if parts[i] == "" {
			return Spec{}, fmt.Errorf("快捷键 %q 含空段（多余的 +？）", s)
		}
	}

	var (
		modSet  = map[string]xhotkey.Modifier{}
		keyName string
		keyVal  xhotkey.Key
	)

	for i, p := range parts {
		lower := strings.ToLower(p)
		if mod, ok := lookupModifier(lower); ok {
			if i == len(parts)-1 {
				// 修饰键不能是最后一段，那意味着没有主键。
				return Spec{}, fmt.Errorf("快捷键 %q 缺少主键", s)
			}
			if _, dup := modSet[lower]; dup {
				return Spec{}, fmt.Errorf("快捷键 %q 中修饰键 %q 重复", s, p)
			}
			modSet[lower] = mod
			continue
		}

		// 不是修饰键，必须是最后一段且为合法主键。
		if i != len(parts)-1 {
			return Spec{}, fmt.Errorf("快捷键 %q 中段 %q 既不是修饰键也不在末尾", s, p)
		}
		k, name, ok := lookupKey(lower)
		if !ok {
			return Spec{}, fmt.Errorf("快捷键 %q 中未知的主键 %q", s, p)
		}
		keyVal = k
		keyName = name
	}

	if keyName == "" {
		// 这意味着所有段都是修饰键。
		return Spec{}, fmt.Errorf("快捷键 %q 仅包含修饰键，缺少主键", s)
	}

	canonicalMods := canonicalModOrder(modSet)
	modNames := make([]string, 0, len(canonicalMods))
	for _, m := range canonicalMods {
		modNames = append(modNames, modName(m))
	}

	return Spec{
		Mods:     canonicalMods,
		Key:      keyVal,
		modNames: modNames,
		keyName:  keyName,
	}, nil
}

// String 返回 canonical 形式：`Ctrl+Alt+Shift+Win+KEY`，所有部件首字母大写。
func (s Spec) String() string {
	if s.keyName == "" {
		return ""
	}
	if len(s.modNames) == 0 {
		return s.keyName
	}
	return strings.Join(append(append([]string{}, s.modNames...), s.keyName), "+")
}

// Equal 判断两个 Spec 是否表示同一组合键（顺序无关）。
func (s Spec) Equal(o Spec) bool {
	if s.Key != o.Key || len(s.Mods) != len(o.Mods) {
		return false
	}
	for i := range s.Mods {
		if s.Mods[i] != o.Mods[i] {
			return false
		}
	}
	return true
}

// ----- 内部表 -----

var modifierTable = map[string]xhotkey.Modifier{
	"ctrl":    xhotkey.ModCtrl,
	"control": xhotkey.ModCtrl,
	"alt":     xhotkey.ModAlt,
	"shift":   xhotkey.ModShift,
	"win":     xhotkey.ModWin,
	"super":   xhotkey.ModWin,
	"meta":    xhotkey.ModWin,
}

func lookupModifier(lower string) (xhotkey.Modifier, bool) {
	m, ok := modifierTable[lower]
	return m, ok
}

// canonical mod 名（用于 String 输出的展示形式）。
var canonicalModName = map[xhotkey.Modifier]string{
	xhotkey.ModCtrl:  "Ctrl",
	xhotkey.ModAlt:   "Alt",
	xhotkey.ModShift: "Shift",
	xhotkey.ModWin:   "Win",
}

// canonical mod 顺序：Ctrl、Alt、Shift、Win。
var canonicalModSequence = []xhotkey.Modifier{xhotkey.ModCtrl, xhotkey.ModAlt, xhotkey.ModShift, xhotkey.ModWin}

func canonicalModOrder(set map[string]xhotkey.Modifier) []xhotkey.Modifier {
	have := map[xhotkey.Modifier]bool{}
	for _, m := range set {
		have[m] = true
	}
	out := make([]xhotkey.Modifier, 0, len(have))
	for _, m := range canonicalModSequence {
		if have[m] {
			out = append(out, m)
		}
	}
	return out
}

func modName(m xhotkey.Modifier) string {
	if name, ok := canonicalModName[m]; ok {
		return name
	}
	return fmt.Sprintf("Mod(%d)", uint8(m))
}

// 主键查表：lower -> (Key 值, canonical 名)
var keyTable = func() map[string]struct {
	k    xhotkey.Key
	name string
} {
	tbl := map[string]struct {
		k    xhotkey.Key
		name string
	}{
		"space": {xhotkey.KeySpace, "Space"},
	}
	// Letters
	letters := []struct {
		k xhotkey.Key
		c rune
	}{
		{xhotkey.KeyA, 'A'}, {xhotkey.KeyB, 'B'}, {xhotkey.KeyC, 'C'}, {xhotkey.KeyD, 'D'},
		{xhotkey.KeyE, 'E'}, {xhotkey.KeyF, 'F'}, {xhotkey.KeyG, 'G'}, {xhotkey.KeyH, 'H'},
		{xhotkey.KeyI, 'I'}, {xhotkey.KeyJ, 'J'}, {xhotkey.KeyK, 'K'}, {xhotkey.KeyL, 'L'},
		{xhotkey.KeyM, 'M'}, {xhotkey.KeyN, 'N'}, {xhotkey.KeyO, 'O'}, {xhotkey.KeyP, 'P'},
		{xhotkey.KeyQ, 'Q'}, {xhotkey.KeyR, 'R'}, {xhotkey.KeyS, 'S'}, {xhotkey.KeyT, 'T'},
		{xhotkey.KeyU, 'U'}, {xhotkey.KeyV, 'V'}, {xhotkey.KeyW, 'W'}, {xhotkey.KeyX, 'X'},
		{xhotkey.KeyY, 'Y'}, {xhotkey.KeyZ, 'Z'},
	}
	for _, l := range letters {
		tbl[strings.ToLower(string(l.c))] = struct {
			k    xhotkey.Key
			name string
		}{l.k, string(l.c)}
	}
	// F1-F12
	fkeys := []xhotkey.Key{
		xhotkey.KeyF1, xhotkey.KeyF2, xhotkey.KeyF3, xhotkey.KeyF4,
		xhotkey.KeyF5, xhotkey.KeyF6, xhotkey.KeyF7, xhotkey.KeyF8,
		xhotkey.KeyF9, xhotkey.KeyF10, xhotkey.KeyF11, xhotkey.KeyF12,
	}
	for i, fk := range fkeys {
		name := fmt.Sprintf("F%d", i+1)
		tbl[strings.ToLower(name)] = struct {
			k    xhotkey.Key
			name string
		}{fk, name}
	}
	// Digits 0-9
	digits := []xhotkey.Key{
		xhotkey.Key0, xhotkey.Key1, xhotkey.Key2, xhotkey.Key3, xhotkey.Key4,
		xhotkey.Key5, xhotkey.Key6, xhotkey.Key7, xhotkey.Key8, xhotkey.Key9,
	}
	for i, dk := range digits {
		name := fmt.Sprintf("%d", i)
		tbl[name] = struct {
			k    xhotkey.Key
			name string
		}{dk, name}
	}
	return tbl
}()

func lookupKey(lower string) (xhotkey.Key, string, bool) {
	v, ok := keyTable[lower]
	if !ok {
		return 0, "", false
	}
	return v.k, v.name, true
}

// SupportedKeyNames 返回所有合法主键名（用于错误提示或 UI 展示）；按字母序排列。
func SupportedKeyNames() []string {
	names := make([]string, 0, len(keyTable))
	for _, v := range keyTable {
		names = append(names, v.name)
	}
	sort.Strings(names)
	return names
}
