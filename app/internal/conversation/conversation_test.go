package conversation

import (
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestStore_NewIsEmpty(t *testing.T) {
	s := NewStore()
	if s.Len() != 0 {
		t.Errorf("expected empty store, got len=%d", s.Len())
	}
	if got := s.Snapshot(); len(got) != 0 {
		t.Errorf("snapshot should be empty, got %+v", got)
	}
}

func TestStore_AppendOrderPreserved(t *testing.T) {
	s := NewStore()
	in := []Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "hello"},
		{Role: RoleUser, Content: "how are you"},
	}
	for _, m := range in {
		s.Append(m)
	}
	got := s.Snapshot()
	if !reflect.DeepEqual(got, in) {
		t.Errorf("append order not preserved\nwant: %+v\ngot: %+v", in, got)
	}
}

func TestStore_ResetClears(t *testing.T) {
	s := NewStore()
	s.Append(Message{Role: RoleUser, Content: "1"})
	s.Append(Message{Role: RoleAssistant, Content: "2"})
	s.Reset()
	if s.Len() != 0 {
		t.Errorf("after Reset expected empty, got %d", s.Len())
	}
}

func TestGenerateFilenameFormat(t *testing.T) {
	re := regexp.MustCompile(`^MockAgent-对话-\d{4}-\d{2}-\d{2}-\d{4}\.(md|json)$`)
	t0 := time.Date(2026, 5, 15, 14, 30, 0, 0, time.UTC)
	for _, f := range []string{FormatMarkdown, FormatJSON} {
		fn, err := GenerateFilename(t0, f)
		if err != nil {
			t.Errorf("GenerateFilename(%q): %v", f, err)
			continue
		}
		if !re.MatchString(fn) {
			t.Errorf("filename %q does not match pattern", fn)
		}
		if !strings.HasSuffix(fn, "."+f) {
			t.Errorf("suffix wrong for %q: got %q", f, fn)
		}
	}
	if _, err := GenerateFilename(t0, "txt"); !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("unknown format should return ErrUnknownFormat, got %v", err)
	}
}

func TestExportJSONRoundTrip(t *testing.T) {
	in := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "你好"},
		{Role: RoleAssistant, Content: "嗨", ReasoningContent: "thinking"},
	}
	t0 := time.Date(2026, 5, 15, 14, 30, 0, 0, time.UTC)
	_, data, err := Export(t0, FormatJSON, in)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	var out []Message
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v\ndata=%s", err, data)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip mismatch\nin:  %+v\nout: %+v", in, out)
	}
}

func TestExportMarkdownStructure(t *testing.T) {
	in := []Message{
		{Role: RoleUser, Content: "show me code"},
		{Role: RoleAssistant, Content: "```go\npackage main\n```"},
		{Role: RoleUser, Content: "thanks"},
	}
	t0 := time.Date(2026, 5, 15, 14, 30, 0, 0, time.UTC)
	_, data, err := Export(t0, FormatMarkdown, in)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	s := string(data)
	if got := strings.Count(s, "## 你"); got != 2 {
		t.Errorf("'## 你' count = %d, want 2", got)
	}
	if got := strings.Count(s, "## AI"); got != 1 {
		t.Errorf("'## AI' count = %d, want 1", got)
	}
	// 代码块原样保留
	if !strings.Contains(s, "```go\npackage main\n```") {
		t.Errorf("code block not preserved verbatim, got:\n%s", s)
	}
}

func TestExportRejectsUnknownFormat(t *testing.T) {
	_, data, err := Export(time.Now(), "txt", nil)
	if data != nil {
		t.Error("data should be nil for unknown format")
	}
	if !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("expected ErrUnknownFormat, got %v", err)
	}
}

func TestExportJSONEmptyMessages(t *testing.T) {
	t0 := time.Date(2026, 5, 15, 14, 30, 0, 0, time.UTC)
	_, data, err := Export(t0, FormatJSON, nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	var out []Message
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("nil messages should encode as []: %v\ndata=%s", err, data)
	}
	if out == nil || len(out) != 0 {
		t.Errorf("expected empty slice, got %+v", out)
	}
}
