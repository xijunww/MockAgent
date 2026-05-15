package recorder

import (
	"bytes"
	"errors"
	"testing"
)

func TestFakeRecorder_StartStopRoundTrip(t *testing.T) {
	r := NewFake(true)
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	want := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	r.Push(want[:3])
	r.Push(want[3:])
	got, err := r.Stop()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("buffer mismatch:\n got: %v\nwant: %v", got, want)
	}
}

func TestFakeRecorder_DoubleStartFails(t *testing.T) {
	r := NewFake(true)
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	err := r.Start()
	if !errors.Is(err, ErrAlreadyRecording) {
		t.Errorf("second Start should fail with ErrAlreadyRecording, got %v", err)
	}
}

func TestFakeRecorder_StopWithoutStart(t *testing.T) {
	r := NewFake(true)
	_, err := r.Stop()
	if !errors.Is(err, ErrNotRecording) {
		t.Errorf("Stop without Start should fail with ErrNotRecording, got %v", err)
	}
}

func TestFakeRecorder_PushIgnoredWhenIdle(t *testing.T) {
	r := NewFake(true)
	r.Push([]byte{1, 2, 3}) // ignored
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	r.Push([]byte{9, 9, 9})
	got, _ := r.Stop()
	if !bytes.Equal(got, []byte{9, 9, 9}) {
		t.Errorf("idle Push should be ignored, got %v", got)
	}
}

func TestShouldRecognize(t *testing.T) {
	// 16kHz mono s16: 1 second = 32000 bytes, 300 ms = 9600 bytes (rounding fine).
	cases := []struct {
		byteLen int
		want    bool
	}{
		{0, false},
		{9_599, false},
		{9_600, true},
		{32_000, true},
	}
	for _, tc := range cases {
		got := ShouldRecognize(tc.byteLen, 16000, 1, 2, 300)
		if got != tc.want {
			t.Errorf("ShouldRecognize(%d) = %v, want %v", tc.byteLen, got, tc.want)
		}
	}

	// Defensive: invalid params return false.
	if ShouldRecognize(100, 0, 1, 2, 300) {
		t.Error("zero sample rate must return false")
	}
	if ShouldRecognize(100, 16000, 0, 2, 300) {
		t.Error("zero channels must return false")
	}
	if ShouldRecognize(100, 16000, 1, 0, 300) {
		t.Error("zero bytes per sample must return false")
	}
	if ShouldRecognize(100, 16000, 1, 2, 0) {
		t.Error("zero min duration must return false")
	}
}
