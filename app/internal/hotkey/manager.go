package hotkey

import (
	"errors"
	"fmt"
	"sync"
	"time"

	xhotkey "golang.design/x/hotkey"
)

const (
	releasePollInterval = 20 * time.Millisecond
	// 触发 onPress 后，至少持续轮询这么久后再判定松开。这是为了消化
	// hotkey 库 / Windows 消息队列里在我们松开瞬间已积压的 WM_HOTKEY 消息，
	// 避免"按一次出现多次按下/松开"的风暴。
	minPressDuration = 80 * time.Millisecond
	// 判定松开需要连续 N 次轮询都报告未按下，避免单次抖动误判。
	releaseConfirmCount = 3
	// 一次按下回合结束后，吞掉这段时间内仍在到来的 keydown（消息队列回放）。
	postReleaseQuietPeriod = 200 * time.Millisecond
)

// Manager 负责注册/注销系统级全局热键并把事件喂给 Debouncer。
//
// 设计要点：
//   - 仅消费底层库的 Keydown 通道，松开判定改用 Win32 GetAsyncKeyState 自己轮询；
//   - 收到 Keydown 后强制至少持续 minPressDuration 才检查松开；
//   - 一次按下回合结束后进入静默期，丢弃残留的 Keydown 事件；
//   - 这是为了规避 golang.design/x/hotkey 在 Windows 下连续按住/AutoRepeat 时
//     消息堆积造成的风暴（v0.4.1 的内部 ticker 实现存在该问题）。
type Manager struct {
	mu        sync.Mutex
	hk        *xhotkey.Hotkey
	spec      Spec
	debouncer *Debouncer
	cancel    chan struct{}
	done      chan struct{}
}

// NewManager 构造 Manager。
func NewManager(onPress, onRelease func()) *Manager {
	return &Manager{
		debouncer: NewDebouncer(onPress, onRelease),
	}
}

// SetCallbacks 修改按下/松开回调。
func (m *Manager) SetCallbacks(onPress, onRelease func()) {
	m.debouncer.SetCallbacks(onPress, onRelease)
}

// CurrentSpec 返回当前已注册的 Spec。
func (m *Manager) CurrentSpec() Spec {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spec
}

// Register 注册新热键，先注销旧的。
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

	go m.listen(hk, spec, cancel, done)
	return nil
}

// Unregister 注销当前热键。
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
	if err := hk.Unregister(); err != nil {
		return err
	}
	if done != nil {
		<-done
	}
	m.debouncer.Reset()
	return nil
}

// listen 在 goroutine 中读取 Keydown 通道；按下后由 handlePress 自己阻塞到松开。
func (m *Manager) listen(hk *xhotkey.Hotkey, spec Spec, cancel <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	keydown := hk.Keydown()
	keyup := hk.Keyup() // 同步 drain，避免库内 channel 阻塞内部 goroutine

	for {
		select {
		case _, ok := <-keydown:
			if !ok {
				return
			}
			m.handlePress(spec, keydown, keyup, cancel)
			// 一次按下回合结束后进入静默期，吃掉消息队列里残留的 keydown。
			drainUntilQuiet(keydown, keyup, postReleaseQuietPeriod)
		case <-keyup:
			// drain
		case <-cancel:
			return
		}
	}
}

// handlePress 处理一次按下。先调用 onPress，然后阻塞直到键被松开。
func (m *Manager) handlePress(spec Spec, keydown, keyup <-chan xhotkey.Event, cancel <-chan struct{}) {
	m.debouncer.Press()
	defer m.debouncer.Release()

	pressedAt := time.Now()
	ticker := time.NewTicker(releasePollInterval)
	defer ticker.Stop()

	missCount := 0
	for {
		select {
		case <-ticker.C:
			pressed := IsKeyPressed(uint16(spec.Key))
			elapsed := time.Since(pressedAt)
			if pressed {
				missCount = 0
				continue
			}
			// 强制最短按下时长，避免按下后立刻被误判松开
			if elapsed < minPressDuration {
				continue
			}
			missCount++
			if missCount >= releaseConfirmCount {
				return
			}
		case <-keydown:
			// 持续按住时 AutoRepeat 会触发；忽略
		case <-keyup:
			// 同上
		case <-cancel:
			return
		}
	}
}

// drainUntilQuiet 在 d 时间窗口内吞掉 keydown/keyup 通道残留事件。
//
// 用于消化一次按下回合结束后消息队列里残留的 WM_HOTKEY，避免它们
// 被解释成新的按下回合。
func drainUntilQuiet(keydown, keyup <-chan xhotkey.Event, d time.Duration) {
	deadline := time.NewTimer(d)
	defer deadline.Stop()

	for {
		select {
		case <-keydown:
		case <-keyup:
		case <-deadline.C:
			return
		}
	}
}
