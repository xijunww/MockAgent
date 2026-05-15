package hotkey

import (
	"errors"
	"fmt"
	"sync"

	xhotkey "golang.design/x/hotkey"
)

// Manager 负责注册/注销系统级全局热键，并把按下/松开事件喂给 Debouncer。
//
// 同一时刻最多注册一个 Spec；调用 Register 时若已有热键，会先注销旧的。
type Manager struct {
	mu        sync.Mutex
	hk        *xhotkey.Hotkey
	spec      Spec
	debouncer *Debouncer
	cancel    chan struct{} // 关闭通知监听 goroutine 退出
	done      chan struct{} // 监听 goroutine 退出后关闭
}

// NewManager 构造 Manager。回调可为 nil，可后续通过 SetCallbacks 设置。
func NewManager(onPress, onRelease func()) *Manager {
	return &Manager{
		debouncer: NewDebouncer(onPress, onRelease),
	}
}

// SetCallbacks 修改按下/松开回调。可在 Register 之后随时调用。
func (m *Manager) SetCallbacks(onPress, onRelease func()) {
	m.debouncer.SetCallbacks(onPress, onRelease)
}

// CurrentSpec 返回当前已注册的 Spec（未注册时为零值）。
func (m *Manager) CurrentSpec() Spec {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spec
}

// Register 注册一个新热键。若已有注册的热键，会先注销。
//
// 当 spec 与当前已注册的 spec 完全相同时，是 no-op，避免无谓重启监听。
func (m *Manager) Register(spec Spec) error {
	if spec.keyName == "" {
		return errors.New("hotkey: empty Spec, did you call ParseSpec?")
	}

	m.mu.Lock()
	if m.hk != nil && m.spec.Equal(spec) {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	// 先注销旧的（如果有）。
	if err := m.Unregister(); err != nil {
		return fmt.Errorf("注销旧热键失败: %w", err)
	}

	hk := xhotkey.New(spec.Mods, spec.Key)
	if err := hk.Register(); err != nil {
		return fmt.Errorf("注册全局热键 %s 失败: %w", spec, err)
	}

	cancel := make(chan struct{})
	done := make(chan struct{})

	m.mu.Lock()
	m.hk = hk
	m.spec = spec
	m.cancel = cancel
	m.done = done
	m.mu.Unlock()

	go m.listen(hk, cancel, done)
	return nil
}

// Unregister 注销当前热键。无注册时返回 nil。
func (m *Manager) Unregister() error {
	m.mu.Lock()
	hk := m.hk
	cancel := m.cancel
	done := m.done
	m.hk = nil
	m.spec = Spec{}
	m.cancel = nil
	m.done = nil
	m.mu.Unlock()

	if hk == nil {
		return nil
	}

	if cancel != nil {
		close(cancel)
	}
	// 注销底层热键；这会让 Keydown / Keyup channel 关闭，从而让 listen 协程退出。
	if err := hk.Unregister(); err != nil {
		return err
	}
	if done != nil {
		<-done
	}
	m.debouncer.Reset()
	return nil
}

// listen 在独立 goroutine 中读取按下/松开事件，喂给 Debouncer。
func (m *Manager) listen(hk *xhotkey.Hotkey, cancel <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	keydown := hk.Keydown()
	keyup := hk.Keyup()

	for {
		select {
		case _, ok := <-keydown:
			if !ok {
				return
			}
			m.debouncer.Press()
		case _, ok := <-keyup:
			if !ok {
				return
			}
			m.debouncer.Release()
		case <-cancel:
			return
		}
	}
}
