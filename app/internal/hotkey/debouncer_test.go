package hotkey

import "testing"

func TestDebouncer_BasicCycle(t *testing.T) {
	var presses, releases int
	d := NewDebouncer(func() { presses++ }, func() { releases++ })

	if !d.Press() {
		t.Fatal("first Press should fire")
	}
	if d.Press() {
		t.Fatal("second Press during pressed state should be swallowed")
	}
	if d.Press() {
		t.Fatal("third Press during pressed state should be swallowed")
	}

	if !d.Release() {
		t.Fatal("first Release should fire")
	}
	if d.Release() {
		t.Fatal("Release while idle should be swallowed")
	}
	if presses != 1 || releases != 1 {
		t.Fatalf("expected 1/1, got presses=%d releases=%d", presses, releases)
	}

	// Next cycle works again.
	d.Press()
	d.Release()
	if presses != 2 || releases != 2 {
		t.Fatalf("expected 2/2, got presses=%d releases=%d", presses, releases)
	}
}

func TestDebouncer_FinalReleaseStops(t *testing.T) {
	// Sequence with multiple presses and finally one release: should be 1 press + 1 release.
	var presses, releases int
	d := NewDebouncer(func() { presses++ }, func() { releases++ })

	for i := 0; i < 5; i++ {
		d.Press()
	}
	for i := 0; i < 3; i++ {
		d.Release()
	}
	if presses != 1 {
		t.Errorf("presses = %d, want 1", presses)
	}
	if releases != 1 {
		t.Errorf("releases = %d, want 1", releases)
	}
}

func TestDebouncer_Reset(t *testing.T) {
	var presses, releases int
	d := NewDebouncer(func() { presses++ }, func() { releases++ })
	d.Press()
	d.Reset() // Reset should not call onRelease.
	if releases != 0 {
		t.Errorf("Reset must not call onRelease, got releases=%d", releases)
	}
	// After reset, a Release is a no-op.
	if d.Release() {
		t.Error("Release after Reset should be no-op")
	}
	// Press cycle works again.
	d.Press()
	if presses != 2 {
		t.Errorf("presses = %d, want 2", presses)
	}
}
