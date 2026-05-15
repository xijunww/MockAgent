// Package llm 封装 DeepSeek OpenAI 兼容 /chat/completions 流式调用。
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// 流式响应中 SSE 终止标记。
const sseDoneMarker = "[DONE]"

// Message 与 design.md 6.4 的结构一致。
type Message struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// ChatRequest 是 OpenAI 兼容 /chat/completions 的请求体子集。
type ChatRequest struct {
	Model           string         `json:"model"`
	Messages        []Message      `json:"messages"`
	Stream          bool           `json:"stream"`
	Thinking        *ThinkingField `json:"thinking,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
}

// ThinkingField 与 DeepSeek 文档对齐：{"type":"enabled"}。
type ThinkingField struct {
	Type string `json:"type"`
}

// ChatChunk 是一段 SSE 数据块（OpenAI 兼容）。
type ChatChunk struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Choices []Choice `json:"choices"`
}

// Choice 一个候选项。
type Choice struct {
	Index        int    `json:"index"`
	Delta        Delta  `json:"delta"`
	FinishReason string `json:"finish_reason,omitempty"`
}

// Delta 增量文本，对应 SSE 中本帧新增的内容。
type Delta struct {
	Role             string `json:"role,omitempty"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// StreamEvent 是从 LLM client 推到上层的事件。
type StreamEvent struct {
	// Content 是本次 chunk 的 content 增量；为空表示无该字段。
	Content string
	// Reasoning 是本次 chunk 的 reasoning_content 增量；为空表示无该字段。
	Reasoning string
	// Done 表示流结束（收到 [DONE]）。本字段为 true 时 Content/Reasoning 为空。
	Done bool
}

// HasContent 当 chunk 至少携带一种增量内容时返回 true。
func (e StreamEvent) HasContent() bool {
	return e.Content != "" || e.Reasoning != ""
}

// Options 控制 LLM 客户端行为。
type Options struct {
	BaseURL         string
	APIKey          string
	Model           string
	Thinking        string // "enabled" 或空
	ReasoningEffort string // "low" / "medium" / "high"
	HTTPClient      *http.Client
}

// Client 调用 DeepSeek 流式 /chat/completions。
type Client struct {
	opts Options
}

// New 构造一个 Client。HTTPClient 为 nil 时使用默认客户端（无超时，由 ctx 控制）。
func New(opts Options) *Client {
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{}
	}
	if opts.BaseURL == "" {
		opts.BaseURL = "https://api.deepseek.com"
	}
	if opts.Model == "" {
		opts.Model = "deepseek-v4-pro"
	}
	return &Client{opts: opts}
}

// HTTPError 表示服务端返回非 2xx 状态。
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("llm http %d: %s", e.StatusCode, truncate(e.Body, 500))
}

// Stream 发起一次流式调用，返回 chunk channel 与错误。
//
// channel 在以下情况关闭：
//   - 收到 [DONE]（最后一个事件 Done=true 入队后关闭）
//   - ctx.Done()
//   - 网络错误或 4xx
//
// 调用方应在另一 goroutine 中持续 range 返回的 channel 直至关闭，
// 错误通过 channel 关闭后的 Err() 获取（见 StreamResult 包装）或 Stream 直接返回。
func (c *Client) Stream(ctx context.Context, messages []Message) (<-chan StreamEvent, error) {
	if c.opts.APIKey == "" {
		return nil, errors.New("DeepSeek API Key 未配置")
	}

	body := ChatRequest{
		Model:    c.opts.Model,
		Messages: messages,
		Stream:   true,
	}
	if c.opts.Thinking == "enabled" {
		body.Thinking = &ThinkingField{Type: "enabled"}
		if c.opts.ReasoningEffort != "" {
			body.ReasoningEffort = c.opts.ReasoningEffort
		}
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := strings.TrimRight(c.opts.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+c.opts.APIKey)

	resp, err := c.opts.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, &HTTPError{StatusCode: resp.StatusCode, Body: string(bodyBytes)}
	}

	out := make(chan StreamEvent, 16)
	go parseSSE(ctx, resp.Body, out)
	return out, nil
}

// parseSSE 读取 SSE 流并把事件投递到 out，结束时关闭 out 与底层 body。
func parseSSE(ctx context.Context, body io.ReadCloser, out chan<- StreamEvent) {
	defer close(out)
	defer body.Close()

	reader := bufio.NewReader(body)
	for {
		// 提前响应 ctx 取消。
		if err := ctx.Err(); err != nil {
			return
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF || errors.Is(err, context.Canceled) {
				return
			}
			// 网络断开等：直接结束流。上层通过 ctx 或 channel 关闭感知。
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue // SSE 事件分隔
		}
		if !strings.HasPrefix(line, "data:") {
			continue // 注释或心跳
		}
		payload := strings.TrimSpace(line[len("data:"):])
		if payload == sseDoneMarker {
			select {
			case out <- StreamEvent{Done: true}:
			case <-ctx.Done():
			}
			return
		}
		var chunk ChatChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // 不合法 chunk 直接跳过
		}
		for _, ch := range chunk.Choices {
			ev := StreamEvent{
				Content:   ch.Delta.Content,
				Reasoning: ch.Delta.ReasoningContent,
			}
			if !ev.HasContent() {
				continue
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
