package hotkey

import "sync"

// Debouncer 把热键的"按下/松开"原始事件流转换为有效的"开始/停止"动作。
//
// 行为：
//   - 第一次 Press 触发 onPress；
//   - 若已处于按下状态，重复 Press 被吞掉；
//   - 处于按下状态时收到 Release 触发 onRelease，回到空闲；
//   - 空闲状态收到 Release 被吞掉（理论上不会发生，但要稳健）。
//
// 这是一个纯逻辑结构，可独立测试。
type Debouncer struct {
	mu      sync.Mutex
	pressed bool

	onPress   func()
	onRelease func()
}

// NewDebouncer 创建一个 Debouncer。两个回调可为 nil。
func NewDebouncer(onPress, onRelease func()) *Debouncer {
	return &Debouncer{onPress: onPress, onRelease: onRelease}
}

// SetCallbacks 替换回调（线程安全）。
func (d *Debouncer) SetCallbacks(onPress, onRelease func()) {
	d.mu.Lock()
	d.onPress = onPress
	d.onRelease = onRelease
	d.mu.Unlock()
}

// Press 投递一个原始按下事件。返回值仅用于测试观察：true 表示触发了 onPress。
func (d *Debouncer) Press() bool {
	d.mu.Lock()
	if d.pressed {
		d.mu.Unlock()
		return false
	}
	d.pressed = true
	cb := d.onPress
	d.mu.Unlock()
	if cb != nil {
		cb()
	}
	return true
}

// Release 投递一个原始松开事件。返回值仅用于测试观察：true 表示触发了 onRelease。
func (d *Debouncer) Release() bool {
	d.mu.Lock()
	if !d.pressed {
		d.mu.Unlock()
		return false
	}
	d.pressed = false
	cb := d.onRelease
	d.mu.Unlock()
	if cb != nil {
		cb()
	}
	return true
}

// IsPressed 仅供测试使用。
func (d *Debouncer) IsPressed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pressed
}

// Reset 强制把状态置为空闲且不触发回调（用于注销/重启时清理）。
func (d *Debouncer) Reset() {
	d.mu.Lock()
	d.pressed = false
	d.mu.Unlock()
}
