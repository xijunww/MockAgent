//go:build windows

package hotkey

import "syscall"

var (
	user32           = syscall.MustLoadDLL("user32.dll")
	procGetAsyncKey  = user32.MustFindProc("GetAsyncKeyState")
)

// IsKeyPressed 通过 Win32 GetAsyncKeyState 判断给定 vkCode 当前是否处于按下状态。
// 返回的高位（0x8000）置位表示按下。
func IsKeyPressed(vkCode uint16) bool {
	ret, _, _ := procGetAsyncKey.Call(uintptr(vkCode))
	return (ret & 0x8000) != 0
}
