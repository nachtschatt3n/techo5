package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Wake words hear the canceller before suppression is off on a new device, comes back on after a
// restart once turned on, and once turned off again leaves nothing in the file, as a device that never
// touched it.
func TestWakeBeforeSuppressionIsOffUntilTurnedOnAndKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if st.Get().Microphone.WakeBeforeSuppression {
		t.Fatal("on for a new device: the wake words would hear the device's own voice without anyone asking")
	}

	if err := st.Set().Microphone().WakeBeforeSuppression(true); err != nil {
		t.Fatalf("turning it on: %v", err)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	if !again.Get().Microphone.WakeBeforeSuppression {
		t.Error("off again after a restart")
	}

	if err := again.Set().Microphone().WakeBeforeSuppression(false); err != nil {
		t.Fatalf("turning it off: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "wake_before_suppression") {
		t.Errorf("turned off, it is still in the file:\n%s", raw)
	}
}
