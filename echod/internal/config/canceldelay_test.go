package config

import (
	"path/filepath"
	"testing"
)

// None is the default, and a value from an older or edited file is brought into the range.
func TestCancelDelayMs(t *testing.T) {
	for set, want := range map[int]int{0: 0, -3: 0, 1: 1, 8: 8, 20: 20, 45: MaxCancelDelay} {
		if got := (Microphone{CancelDelay: set}).CancelDelayMs(); got != want {
			t.Errorf("CancelDelay %d: %d, want %d", set, got, want)
		}
	}
	if DefaultCancelDelay != 0 {
		t.Errorf("the default delay is %d ms; 0 is how the canceller ran before it was a setting", DefaultCancelDelay)
	}
}

// A new device and a file saved before the setting existed both come up without a delay, and a delay
// that was set comes back after a restart.
func TestCancelDelayIsKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Get().Microphone.CancelDelayMs(); got != 0 {
		t.Fatalf("a new device delays the microphones by %d ms", got)
	}
	if err := st.Set().Microphone().CancelDelay(8); err != nil {
		t.Fatal(err)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Get().Microphone.CancelDelayMs(); got != 8 {
		t.Errorf("after a restart the delay is %d ms, want 8", got)
	}
}
