package microphone

import (
	"path/filepath"
	"testing"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/HuskerMinion/techo5/echod/internal/component"
	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/mic"
)

// "Echo cancellation on all microphones" is a configuration switch on the microphone's page. Home
// Assistant sorts by name, so it is listed among the E names rather than next to "Microphone echo
// cancellation", the switch it qualifies: the name says what it does where it stands alone.
func TestCancelOnMixIsAMicrophoneSetting(t *testing.T) {
	m := build()
	s := m.onMix
	if s.ObjectID != "microphone_cancel_echo_all_mics" || s.Name != "Echo cancellation on all microphones" {
		t.Errorf("entity is %q %q", s.ObjectID, s.Name)
	}
	if s.Category != esphome.CategoryConfig || s.DeviceID != component.DeviceMicrophone {
		t.Errorf("category %v on device %d, want a configuration entity on the microphone's", s.Category, s.DeviceID)
	}
	listed := false
	for _, e := range m.Entities() {
		if e == esphome.Entity(s) {
			listed = true
		}
	}
	if !listed {
		t.Error("the switch is built but not offered to Home Assistant")
	}
}

// Nobody having chosen is the board's own choice, shown as the switch's state; a choice from Home
// Assistant reaches the capture at once and is saved, and comes back as it was after a restart.
func TestCancelOnMixIsAppliedSavedAndRestored(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	source := mic.Get()
	defer source.SetCancelOnMix(mic.DefaultCancelOnMix)
	m := build()

	m.Restore(config.Get())
	if m.onMix.Get() != mic.DefaultCancelOnMix || source.CancelOnMix() != mic.DefaultCancelOnMix {
		t.Fatalf("unset: switch %v, capture %v, want the board's %v", m.onMix.Get(), source.CancelOnMix(), mic.DefaultCancelOnMix)
	}

	want := !mic.DefaultCancelOnMix
	m.onMix.OnCommand(want)
	if m.onMix.Get() != want || source.CancelOnMix() != want {
		t.Errorf("after a command for %v: switch %v, capture %v", want, m.onMix.Get(), source.CancelOnMix())
	}
	saved := config.Get().Microphone.CancelOnMix
	if saved == nil || *saved != want {
		t.Fatalf("the choice was not saved: %v", saved)
	}

	source.SetCancelOnMix(!want)
	m.Restore(config.Get())
	if m.onMix.Get() != want || source.CancelOnMix() != want {
		t.Errorf("restored: switch %v, capture %v, want %v", m.onMix.Get(), source.CancelOnMix(), want)
	}
}
