// Package asr 封装腾讯云语音 SDK 的 FlashRecognizer，把 PCM 转成中文文本。
package asr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	tasr "github.com/tencentcloud/tencentcloud-speech-sdk-go/asr"
	"github.com/tencentcloud/tencentcloud-speech-sdk-go/common"
)

// Engine 默认引擎类型；16k 中文。
const Engine = "16k_zh"

// ErrEmptyResult 当腾讯云返回空结果或仅空白时返回此错误。
var ErrEmptyResult = errors.New("asr: 识别结果为空")

// Error 是 ASR 调用错误的统一类型，便于上层根据 Kind 区分。
type Error struct {
	Kind    string // "auth" / "network" / "quota" / "empty" / "unknown"
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("asr[%s]: %s: %v", e.Kind, e.Message, e.Cause)
	}
	return fmt.Sprintf("asr[%s]: %s", e.Kind, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

// Client 是 ASR 抽象接口，便于测试时注入 fake。
type Client interface {
	Recognize(ctx context.Context, pcm []byte) (string, error)
}

// TencentClient 是 Client 的腾讯云实现。
type TencentClient struct {
	appID     string
	secretID  string
	secretKey string
}

// NewTencent 构造一个腾讯云 ASR 客户端。
// 凭证为空时返回的 client 仍可创建，但 Recognize 会立即报错（避免在错误时机泄露状态）。
func NewTencent(appID, secretID, secretKey string) *TencentClient {
	return &TencentClient{appID: appID, secretID: secretID, secretKey: secretKey}
}

// Recognize 调用腾讯云 FlashRecognizer 把 PCM 数据转写为文本。
//
// ctx 用于取消（此 SDK 内部不直接支持 ctx，因此我们用 ctx 在 goroutine 周围构造取消语义）。
func (c *TencentClient) Recognize(ctx context.Context, pcm []byte) (string, error) {
	if c.appID == "" || c.secretID == "" || c.secretKey == "" {
		return "", &Error{Kind: "auth", Message: "腾讯云凭证未配置"}
	}
	if len(pcm) == 0 {
		return "", &Error{Kind: "empty", Message: "PCM 数据为空", Cause: ErrEmptyResult}
	}

	credential := common.NewCredential(c.secretID, c.secretKey)
	recognizer := tasr.NewFlashRecognizer(c.appID, credential)

	req := &tasr.FlashRecognitionRequest{
		EngineType:       Engine,
		VoiceFormat:      "pcm",
		FilterPunc:       0,
		FilterDirty:      0,
		FilterModal:      0,
		ConvertNumMode:   1,
		WordInfo:         0,
		FirstChannelOnly: 1,
	}

	type result struct {
		text string
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		resp, err := recognizer.Recognize(req, pcm)
		if err != nil {
			resCh <- result{err: classifyError(err)}
			return
		}
		var b strings.Builder
		for _, r := range resp.FlashResult {
			b.WriteString(r.Text)
		}
		resCh <- result{text: b.String()}
	}()

	select {
	case <-ctx.Done():
		// 上层取消（例如用户按下新一轮 F2）。腾讯云 SDK 不可中断，让其后台跑完就好；
		// 这里立刻返回 ctx.Err()，丢弃后续到达的结果。
		return "", &Error{Kind: "canceled", Message: "识别已取消", Cause: ctx.Err()}
	case r := <-resCh:
		if r.err != nil {
			return "", r.err
		}
		text, ok := DispatchResult(r.text)
		if !ok {
			return "", &Error{Kind: "empty", Message: "识别结果为空", Cause: ErrEmptyResult}
		}
		return text, nil
	}
}

// DispatchResult 判断是否应该把识别文本回填到输入框。
// 返回 (规范化后的文本, 是否回填)：仅当文本含至少一个非空白字符时为 true。
func DispatchResult(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false
	}
	return trimmed, true
}

// classifyError 把腾讯云返回的错误归类，便于前端展示更具体的提示。
func classifyError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	lower := strings.ToLower(msg)

	switch {
	case strings.Contains(lower, "authorization") || strings.Contains(lower, "signature") ||
		strings.Contains(lower, "secret") || strings.Contains(lower, "appid") ||
		strings.Contains(lower, "401") || strings.Contains(lower, "403"):
		return &Error{Kind: "auth", Message: "鉴权失败，请检查 AppID/SecretID/SecretKey", Cause: err}
	case strings.Contains(lower, "quota") || strings.Contains(lower, "limit"):
		return &Error{Kind: "quota", Message: "调用配额已用尽或被限流", Cause: err}
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline") ||
		strings.Contains(lower, "connection") || strings.Contains(lower, "network") ||
		strings.Contains(lower, "dial") || strings.Contains(lower, "tls"):
		return &Error{Kind: "network", Message: "网络连接异常", Cause: err}
	default:
		return &Error{Kind: "unknown", Message: "识别失败", Cause: err}
	}
}

// FakeClient 是测试用 Client：按 ID 序列返回预设结果，可注入完成时延以测试取消。
type FakeClient struct {
	id      atomic.Int32
	Results []FakeResult
}

// FakeResult 是 FakeClient 的一次响应。
type FakeResult struct {
	Text  string
	Err   error
	Delay int // 调用前模拟等待的次数（每次循环 1ms），用于让其它操作抢先
}

// Recognize 实现 Client 接口。
func (f *FakeClient) Recognize(ctx context.Context, _ []byte) (string, error) {
	idx := int(f.id.Add(1)) - 1
	if idx >= len(f.Results) {
		return "", &Error{Kind: "unknown", Message: "FakeClient: 没有更多预设结果"}
	}
	r := f.Results[idx]
	for i := 0; i < r.Delay; i++ {
		select {
		case <-ctx.Done():
			return "", &Error{Kind: "canceled", Message: "fake canceled", Cause: ctx.Err()}
		default:
		}
	}
	if r.Err != nil {
		return "", r.Err
	}
	if t, ok := DispatchResult(r.Text); ok {
		return t, nil
	}
	return "", &Error{Kind: "empty", Message: "识别结果为空", Cause: ErrEmptyResult}
}

// 编译期断言：TencentClient 与 FakeClient 都实现了 Client。
var (
	_ Client = (*TencentClient)(nil)
	_ Client = (*FakeClient)(nil)
)
