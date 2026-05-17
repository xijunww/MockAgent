//go:build !windows

package hotkey

// IsKeyPressed 在非 Windows 平台不可用；保留接口以便代码能跨平台编译。
func IsKeyPressed(vkCode uint16) bool { return false }
