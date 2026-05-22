// Package recorder 基于 malgo 提供 16kHz 单声道 16-bit PCM 麦克风采集。
package recorder

import (
	"errors"
	"fmt"
	"sync"

	"github.com/gen2brain/malgo"
)

// 默认音频参数（与 design.md 6.1 audio 字段对齐）。
const (
	DefaultSampleRate    = 16000
	DefaultChannels      = 1
	DefaultBytesPerSample = 2 // s16
)

// Mode 选择采集来源：麦克风 or 系统声音环回。
type Mode int

const (
	// ModeMicrophone 通过 malgo.Capture 采集默认输入设备（麦克风）。
	ModeMicrophone Mode = iota
	// ModeSystem 通过 malgo.Loopback 采集默认输出设备正在播放的声音
	// （Windows WASAPI 环回；其它平台 malgo 自身不支持时会返回错误）。
	ModeSystem
)

// String 仅用于错误信息与日志。
func (m Mode) String() string {
	switch m {
	case ModeMicrophone:
		return "microphone"
	case ModeSystem:
		return "system"
	default:
		return "unknown"
	}
}

// ErrAlreadyRecording 当一个录音 Session 进行时，再次调用 Start 返回此错误。
var ErrAlreadyRecording = errors.New("recorder: 已经在录音")

// ErrNotRecording 在没有进行中 Session 时调用 Stop 返回此错误。
var ErrNotRecording = errors.New("recorder: 当前未录音")

// Recorder 是录音器抽象，便于在测试中注入 fake 实现。
type Recorder interface {
	// Start 开始一次新的录音 Session。
	Start() error
	// Stop 结束当前 Session，返回累积 PCM 数据（小端 int16）。
	Stop() ([]byte, error)
	// Probe 探测系统是否存在可用音频输入设备。
	Probe() (bool, error)
	// Close 释放底层资源（malgo context 等）。可重复调用。
	Close() error
}

// Config 录音参数。
type Config struct {
	SampleRate     uint32
	Channels       uint32
	BytesPerSample int // 仅用于 ShouldRecognize 等纯逻辑判定；malgo 固定使用 s16
	Mode           Mode
}

// DefaultConfig 返回与 design.md 一致的默认参数（麦克风模式）。
func DefaultConfig() Config {
	return Config{
		SampleRate:     DefaultSampleRate,
		Channels:       DefaultChannels,
		BytesPerSample: DefaultBytesPerSample,
		Mode:           ModeMicrophone,
	}
}

// MalgoRecorder 是 Recorder 的 malgo 实现。
type MalgoRecorder struct {
	cfg Config

	mu        sync.Mutex
	ctx       *malgo.AllocatedContext
	device    *malgo.Device
	buffer    []byte
	recording bool
}

// New 创建一个 MalgoRecorder，但不会立即初始化设备。
// 第一次 Start 时再延迟初始化 malgo context。
func New(cfg Config) *MalgoRecorder {
	if cfg.SampleRate == 0 {
		cfg.SampleRate = DefaultSampleRate
	}
	if cfg.Channels == 0 {
		cfg.Channels = DefaultChannels
	}
	if cfg.BytesPerSample == 0 {
		cfg.BytesPerSample = DefaultBytesPerSample
	}
	return &MalgoRecorder{cfg: cfg}
}

// NewLoopback 是 New 的便捷构造，把 Mode 强制设为 ModeSystem，
// 让调用点意图明确（"采集系统正在播放的声音"）。
func NewLoopback(cfg Config) *MalgoRecorder {
	cfg.Mode = ModeSystem
	return New(cfg)
}

// initContext 创建 malgo context（懒加载），返回的 context 由 r.ctx 持有。
// 必须在持有 r.mu 的状态下调用，或在调用方保证串行的位置调用。
func (r *MalgoRecorder) initContext() error {
	if r.ctx != nil {
		return nil
	}
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return fmt.Errorf("初始化音频上下文失败: %w", err)
	}
	r.ctx = ctx
	return nil
}

// Probe 探测系统是否存在可用音频设备。
//   - ModeMicrophone：枚举 Capture（输入）设备。
//   - ModeSystem：枚举 Playback（输出）设备，环回挂在默认播放设备上。
func (r *MalgoRecorder) Probe() (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.initContext(); err != nil {
		return false, err
	}
	devType := malgo.Capture
	label := "输入"
	if r.cfg.Mode == ModeSystem {
		devType = malgo.Playback
		label = "输出"
	}
	infos, err := r.ctx.Devices(devType)
	if err != nil {
		return false, fmt.Errorf("枚举音频%s设备失败: %w", label, err)
	}
	return len(infos) > 0, nil
}

// Start 开始一次新的录音 Session。
func (r *MalgoRecorder) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.recording {
		return ErrAlreadyRecording
	}
	if err := r.initContext(); err != nil {
		return err
	}

	devType := malgo.Capture
	if r.cfg.Mode == ModeSystem {
		devType = malgo.Loopback
	}
	deviceCfg := malgo.DefaultDeviceConfig(devType)
	// Loopback 与 Capture 在 malgo 里共用 Capture 字段（环回是"输出方向的回采"，
	// 数据仍走 capture callback），所以下面这几行无需根据 mode 分叉。
	deviceCfg.Capture.Format = malgo.FormatS16
	deviceCfg.Capture.Channels = r.cfg.Channels
	deviceCfg.SampleRate = r.cfg.SampleRate
	deviceCfg.Alsa.NoMMap = 1

	r.buffer = r.buffer[:0]

	onRecv := func(_, pSample []byte, _ uint32) {
		// 直接 append；malgo 的回调串行调用，无需额外加锁。
		r.buffer = append(r.buffer, pSample...)
	}

	device, err := malgo.InitDevice(r.ctx.Context, deviceCfg, malgo.DeviceCallbacks{
		Data: onRecv,
	})
	if err != nil {
		return fmt.Errorf("初始化音频输入设备失败: %w", err)
	}
	if err := device.Start(); err != nil {
		device.Uninit()
		return fmt.Errorf("启动音频输入设备失败: %w", err)
	}
	r.device = device
	r.recording = true
	return nil
}

// Stop 结束当前 Session，返回累积 PCM 数据（小端 int16）。
func (r *MalgoRecorder) Stop() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.recording {
		return nil, ErrNotRecording
	}
	if r.device != nil {
		r.device.Uninit()
		r.device = nil
	}
	r.recording = false

	out := make([]byte, len(r.buffer))
	copy(out, r.buffer)
	r.buffer = r.buffer[:0]
	return out, nil
}

// IsRecording 返回当前是否处于录音状态。
func (r *MalgoRecorder) IsRecording() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recording
}

// Close 释放 malgo context（应用退出时调用）。
func (r *MalgoRecorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recording && r.device != nil {
		r.device.Uninit()
		r.device = nil
		r.recording = false
	}
	if r.ctx != nil {
		_ = r.ctx.Uninit()
		r.ctx.Free()
		r.ctx = nil
	}
	return nil
}

// ShouldRecognize 根据已采集字节数与配置判断本次录音是否达到最小时长，
// 用于 App_Coordinator 在 ASR 之前过滤掉误触。
func ShouldRecognize(byteLen, sampleRate, channels, bytesPerSample, minDurationMs int) bool {
	if sampleRate <= 0 || channels <= 0 || bytesPerSample <= 0 || minDurationMs <= 0 {
		return false
	}
	frames := byteLen / (channels * bytesPerSample)
	durationMs := frames * 1000 / sampleRate
	return durationMs >= minDurationMs
}

// FakeRecorder 是用于测试的伪实现：通过 Push 向缓冲区注入 PCM 字节，
// Start/Stop 行为与 MalgoRecorder 一致。
type FakeRecorder struct {
	mu        sync.Mutex
	buffer    []byte
	recording bool
	probeOK   bool
}

// NewFake 创建一个 FakeRecorder。
func NewFake(probeOK bool) *FakeRecorder {
	return &FakeRecorder{probeOK: probeOK}
}

// Probe 实现 Recorder 接口。
func (f *FakeRecorder) Probe() (bool, error) { return f.probeOK, nil }

// Start 实现 Recorder 接口。
func (f *FakeRecorder) Start() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recording {
		return ErrAlreadyRecording
	}
	f.buffer = f.buffer[:0]
	f.recording = true
	return nil
}

// Stop 实现 Recorder 接口。
func (f *FakeRecorder) Stop() ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.recording {
		return nil, ErrNotRecording
	}
	f.recording = false
	out := make([]byte, len(f.buffer))
	copy(out, f.buffer)
	f.buffer = f.buffer[:0]
	return out, nil
}

// Push 在测试中向当前 Session 注入一段 PCM 字节。仅当 recording 时生效。
func (f *FakeRecorder) Push(b []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recording {
		f.buffer = append(f.buffer, b...)
	}
}

// Close 实现 Recorder 接口。
func (f *FakeRecorder) Close() error { return nil }

// 编译期断言：MalgoRecorder 与 FakeRecorder 都实现了 Recorder 接口。
var (
	_ Recorder = (*MalgoRecorder)(nil)
	_ Recorder = (*FakeRecorder)(nil)
)
