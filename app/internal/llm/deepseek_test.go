package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// helper：构造一个 SSE 服务器，按顺序写出每一行（每行是完整 SSE 帧），结尾自动追加 [DONE]。
// 调用方传入的字符串应为 chunk 的 JSON 序列化。
func newSSEServer(t *testing.T, captured *http.Request, chunks []string, includeDone bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if captured != nil {
			*captured = *r.Clone(r.Context())
			b, _ := io.ReadAll(r.Body)
			captured.Body = io.NopCloser(strings.NewReader(string(b)))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			if flusher != nil {
				flusher.Flush()
			}
		}
		if includeDone {
			fmt.Fprintf(w, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
}

func TestStream_BasicConcatenation(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":" World"}}]}`,
		`{"choices":[{"index":0,"delta":{"reasoning_content":"thinking..."}}]}`,
	}
	srv := newSSEServer(t, nil, chunks, true)
	defer srv.Close()

	c := New(Options{
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "test-model",
	})
	ch, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var content, reasoning strings.Builder
	var doneSeen bool
	for ev := range ch {
		if ev.Done {
			doneSeen = true
			continue
		}
		content.WriteString(ev.Content)
		reasoning.WriteString(ev.Reasoning)
	}
	if !doneSeen {
		t.Error("Done event not seen")
	}
	if got := content.String(); got != "Hello World" {
		t.Errorf("content = %q, want %q", got, "Hello World")
	}
	if got := reasoning.String(); got != "thinking..." {
		t.Errorf("reasoning = %q, want %q", got, "thinking...")
	}
}

func TestStream_RequestShape(t *testing.T) {
	var captured http.Request
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"content":"ok"}}]}`,
	}
	srv := newSSEServer(t, &captured, chunks, true)
	defer srv.Close()

	c := New(Options{
		BaseURL:         srv.URL,
		APIKey:          "test-key",
		Model:           "test-model",
		Thinking:        "enabled",
		ReasoningEffort: "high",
	})
	msgs := []Message{
		{Role: "system", Content: "be helpful"},
		{Role: "user", Content: "你好"},
	}
	ch, err := c.Stream(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
		// drain
	}

	// 检查 Authorization 头
	if got := captured.Header.Get("Authorization"); got != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", got)
	}
	if got := captured.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	// 路径
	if got := captured.URL.Path; got != "/chat/completions" {
		t.Errorf("path = %q", got)
	}
	// 解析 body
	body, _ := io.ReadAll(captured.Body)
	var req ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal request: %v\nbody: %s", err, body)
	}
	if req.Model != "test-model" {
		t.Errorf("Model = %q", req.Model)
	}
	if !req.Stream {
		t.Error("Stream should be true")
	}
	if req.Thinking == nil || req.Thinking.Type != "enabled" {
		t.Errorf("Thinking = %+v", req.Thinking)
	}
	if req.ReasoningEffort != "high" {
		t.Errorf("ReasoningEffort = %q", req.ReasoningEffort)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Content != "你好" {
		t.Errorf("Messages = %+v", req.Messages)
	}
}

func TestStream_4xxReturnsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL, APIKey: "bad"})
	ch, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "x"}})
	if ch != nil {
		t.Error("expected nil channel on error")
	}
	var herr *HTTPError
	if !errors.As(err, &herr) {
		t.Fatalf("expected HTTPError, got %v", err)
	}
	if herr.StatusCode != 401 {
		t.Errorf("StatusCode = %d", herr.StatusCode)
	}
	if !strings.Contains(herr.Body, "invalid api key") {
		t.Errorf("body should contain message, got %q", herr.Body)
	}
}

func TestStream_MissingAPIKey(t *testing.T) {
	c := New(Options{APIKey: ""})
	_, err := c.Stream(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "API Key") {
		t.Errorf("expected api key error, got %v", err)
	}
}

func TestStream_ContextCancel(t *testing.T) {
	// 服务器会持续推送 chunk；客户端取消 ctx 后应迅速结束。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 1000; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL, APIKey: "k"})
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := c.Stream(ctx, []Message{{Role: "user", Content: "x"}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	count := 0
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				if count == 0 {
					t.Error("expected at least one chunk before cancel")
				}
				return
			}
			if !ev.Done {
				count++
			}
		case <-deadline:
			t.Fatal("did not stop after cancel")
		}
	}
}

func TestParseSSE_IgnoresMalformed(t *testing.T) {
	// 发送一段含坏 JSON 的流；客户端应跳过坏 chunk 并继续处理后面的好 chunk。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		fmt.Fprintf(w, ": ping\n\n")
		fl.Flush()
		fmt.Fprintf(w, "data: {malformed\n\n")
		fl.Flush()
		fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"good\"}}]}\n\n")
		fl.Flush()
		fmt.Fprintf(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL, APIKey: "k"})
	ch, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "x"}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var sb strings.Builder
	for ev := range ch {
		if ev.Done {
			continue
		}
		sb.WriteString(ev.Content)
	}
	if got := sb.String(); got != "good" {
		t.Errorf("content = %q, want %q", got, "good")
	}
}
