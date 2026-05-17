// Package conversation 维护内存中的会话历史并提供 Markdown / JSON 导出。
package conversation

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Role 常量。
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Message 与 design.md 6.4 的结构一致；保留 reasoning_content 字段。
type Message struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// Store 管理一个会话的 messages 列表。线程安全。
type Store struct {
	mu       sync.RWMutex
	messages []Message
}

// NewStore 创建一个空的 Store。
//
// Store 只保存 user / assistant 两类消息；system prompt 由 App_Coordinator
// 在每次 runLLM 时按当前 active 拼接，不存进历史。
func NewStore() *Store {
	return &Store{}
}

// Reset 清空消息历史。
func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = nil
}

// Append 追加一条 Message。
func (s *Store) Append(m Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, m)
}

// Snapshot 返回 messages 的浅拷贝（slice 复制，元素值拷贝）。
func (s *Store) Snapshot() []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Message, len(s.messages))
	copy(out, s.messages)
	return out
}

// Len 返回当前 messages 数量。
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.messages)
}

// 导出格式常量。
const (
	FormatMarkdown = "md"
	FormatJSON     = "json"
)

// ErrUnknownFormat 当 format 不在 {"md","json"} 时返回。
var ErrUnknownFormat = errors.New("conversation: 未知的导出格式（仅支持 md 与 json）")

// GenerateFilename 根据时间和格式生成默认导出文件名，与 design.md 第 9 节一致。
//
// 形如：MockAgent-对话-2026-05-15-1430.md
func GenerateFilename(t time.Time, format string) (string, error) {
	switch format {
	case FormatMarkdown, FormatJSON:
		return fmt.Sprintf("MockAgent-对话-%s.%s", t.Format("2006-01-02-1504"), format), nil
	default:
		return "", ErrUnknownFormat
	}
}

// Export 把 messages 序列化为 format 指定的字节流。
//
// 返回 (filename 建议名, data, err)。filename 由 GenerateFilename 决定。
func Export(t time.Time, format string, messages []Message) (string, []byte, error) {
	switch format {
	case FormatMarkdown:
		fn, _ := GenerateFilename(t, format)
		return fn, []byte(toMarkdown(messages)), nil
	case FormatJSON:
		fn, _ := GenerateFilename(t, format)
		out := messages
		if out == nil {
			out = []Message{}
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return "", nil, err
		}
		return fn, b, nil
	default:
		return "", nil, ErrUnknownFormat
	}
}

func toMarkdown(messages []Message) string {
	var b strings.Builder
	b.WriteString("# MockAgent 对话\n\n")
	for _, m := range messages {
		var heading string
		switch m.Role {
		case RoleUser:
			heading = "## 你"
		case RoleAssistant:
			heading = "## AI"
		case RoleSystem:
			heading = "## 系统提示"
		default:
			heading = "## " + m.Role
		}
		b.WriteString(heading)
		b.WriteString("\n\n")
		if m.ReasoningContent != "" && m.Role == RoleAssistant {
			b.WriteString("> 思考过程：\n>\n")
			for _, line := range strings.Split(m.ReasoningContent, "\n") {
				b.WriteString("> ")
				b.WriteString(line)
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
		b.WriteString(m.Content)
		// 保证消息结尾有空行
		b.WriteString("\n\n")
	}
	return b.String()
}
