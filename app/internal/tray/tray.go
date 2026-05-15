// Package tray 提供基于 getlantern/systray 的系统托盘图标与菜单。
//
// 集成约束（见 design.md 第 8 节）：
//   - getlantern/systray 的 Run 会阻塞当前 goroutine，必须在独立 goroutine 中启动。
//   - 菜单项点击事件通过 systray 内部 channel 派发，外部回调通过我们的 Callbacks 字段转发。
package tray

import (
	"runtime"
	"sync"

	_ "embed"

	"github.com/getlantern/systray"
)

//go:embed icon.ico
var iconBytes []byte

// Callbacks 描述托盘菜单的外部回调集合。
// 任一回调可为 nil，对应菜单项点击时无动作（但菜单项仍可见）。
type Callbacks struct {
	OnShowWindow      func()
	OnNewConversation func()
	OnOpenConfig      func()
	OnQuit            func()
}

// Manager 启动 / 停止 systray 主循环。
//
// 用法：
//
//	mgr := tray.NewManager(callbacks)
//	mgr.Start() // 不阻塞，内部启动 goroutine
//	...
//	mgr.Stop()  // 退出 systray 主循环
type Manager struct {
	cb Callbacks

	mu       sync.Mutex
	started  bool
	stopOnce sync.Once
}

// NewManager 构造一个 Manager。
func NewManager(cb Callbacks) *Manager { return &Manager{cb: cb} }

// Start 在独立 goroutine 启动 systray。
// 在 Windows / Linux 下可重入，幂等。
func (m *Manager) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return
	}
	m.started = true
	go func() {
		// systray.Run 会持有 OS 主线程；通过 LockOSThread 与该 goroutine 绑定线程。
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		systray.Run(m.onReady, m.onExit)
	}()
}

// Stop 退出 systray 主循环。线程安全且幂等。
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		systray.Quit()
	})
}

func (m *Manager) onReady() {
	systray.SetIcon(iconBytes)
	systray.SetTitle("MockAgent")
	systray.SetTooltip("MockAgent · 语音对话助手")

	mShow := systray.AddMenuItem("显示主窗口", "把主窗口呼出到前台")
	mNew := systray.AddMenuItem("新建对话", "清空当前会话")
	mConfig := systray.AddMenuItem("打开配置文件", "用系统默认编辑器打开 config.json")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出", "退出 MockAgent")

	go func() {
		for {
			select {
			case <-mShow.ClickedCh:
				if m.cb.OnShowWindow != nil {
					m.cb.OnShowWindow()
				}
			case <-mNew.ClickedCh:
				if m.cb.OnNewConversation != nil {
					m.cb.OnNewConversation()
				}
			case <-mConfig.ClickedCh:
				if m.cb.OnOpenConfig != nil {
					m.cb.OnOpenConfig()
				}
			case <-mQuit.ClickedCh:
				if m.cb.OnQuit != nil {
					m.cb.OnQuit()
				}
				systray.Quit()
				return
			}
		}
	}()
}

func (m *Manager) onExit() {
	// systray 主循环退出后被调用。当前不需要做特殊清理；
	// 真正的应用退出由 OnQuit 回调里的 Wails runtime.Quit 完成。
}
