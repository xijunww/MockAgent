package asr

import (
	"context"
	"errors"
	"testing"
)

func TestDispatchResult(t *testing.T) {
	cases := []struct {
		in       string
		wantText string
		wantOK   bool
	}{
		{"", "", false},
		{"   ", "", false},
		{"\t\n  ", "", false},
		{"hello", "hello", true},
		{"  你好  ", "你好", true},
	}
	for _, c := range cases {
		got, ok := DispatchResult(c.in)
		if got != c.wantText || ok != c.wantOK {
			t.Errorf("DispatchResult(%q) = (%q,%v), want (%q,%v)",
				c.in, got, ok, c.wantText, c.wantOK)
		}
	}
}

func TestClassifyError(t *testing.T) {
	cases := []struct {
		errMsg   string
		wantKind string
	}{
		{"AuthFailure: signature mismatch", "auth"},
		{"http code not 200, respData: {\"code\":401}", "auth"},
		{"quota exceeded for today", "quota"},
		{"dial tcp: i/o timeout", "network"},
		{"connection reset by peer", "network"},
		{"some unrelated message", "unknown"},
	}
	for _, c := range cases {
		got := classifyError(errors.New(c.errMsg))
		var e *Error
		if !errors.As(got, &e) {
			t.Errorf("expected *Error for %q, got %T", c.errMsg, got)
			continue
		}
		if e.Kind != c.wantKind {
			t.Errorf("classifyError(%q).Kind = %q, want %q", c.errMsg, e.Kind, c.wantKind)
		}
	}
}

func TestNewTencent_RecognizeRejectsMissingCredentials(t *testing.T) {
	c := NewTencent("", "", "")
	_, err := c.Recognize(context.Background(), []byte{1, 2})
	var e *Error
	if !errors.As(err, &e) || e.Kind != "auth" {
		t.Errorf("missing credentials must yield auth error, got %v", err)
	}
}

func TestNewTencent_RecognizeRejectsEmptyPCM(t *testing.T) {
	c := NewTencent("a", "b", "c")
	_, err := c.Recognize(context.Background(), nil)
	var e *Error
	if !errors.As(err, &e) || e.Kind != "empty" {
		t.Errorf("empty pcm must yield empty error, got %v", err)
	}
}

func TestFakeClient_Sequence(t *testing.T) {
	fc := &FakeClient{Results: []FakeResult{
		{Text: "你好"},
		{Text: "  "},                                            // empty after trim
		{Err: &Error{Kind: "network", Message: "boom"}},
	}}
	if got, err := fc.Recognize(context.Background(), nil); err != nil || got != "你好" {
		t.Errorf("call 1: got (%q,%v)", got, err)
	}
	if _, err := fc.Recognize(context.Background(), nil); err == nil {
		t.Error("call 2 should be empty error")
	} else {
		var e *Error
		if !errors.As(err, &e) || e.Kind != "empty" {
			t.Errorf("call 2 expected empty kind, got %v", err)
		}
	}
	if _, err := fc.Recognize(context.Background(), nil); err == nil {
		t.Error("call 3 should be network error")
	}
}

func TestFakeClient_ContextCancel(t *testing.T) {
	fc := &FakeClient{Results: []FakeResult{{Text: "你好", Delay: 100000}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	_, err := fc.Recognize(ctx, nil)
	var e *Error
	if !errors.As(err, &e) || e.Kind != "canceled" {
		t.Errorf("expected canceled error, got %v", err)
	}
}
