package microphone

import (
	"path/filepath"
	"testing"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/HuskerMinion/techo5/echod/internal/component"
	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/mic"
)

// Wake words hear the canceller before suppression is a configuration switch on the microphone's
// device, off until somebody turns it on, saved when they do and put back by Restore.
func TestWakeBeforeSuppressionSwitch(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	m := build()
	if m.linear.ObjectID != "microphone_wake_before_suppression" ||
		m.linear.Name != "Wake words hear the canceller before suppression" ||
		m.linear.Category != esphome.CategoryConfig || m.linear.DeviceID != component.DeviceMicrophone {
		t.Fatalf("entity: id=%q name=%q category=%v device=%v",
			m.linear.ObjectID, m.linear.Name, m.linear.Category, m.linear.DeviceID)
	}

	m.Restore(config.Get())
	if m.linear.Get() {
		t.Fatal("on for a new device")
	}

	m.linear.OnCommand(true)
	if !m.linear.Get() || !config.Get().Microphone.WakeBeforeSuppression {
		t.Fatal("turned on, but not shown or not saved")
	}

	c := config.Get()
	c.Microphone.WakeBeforeSuppression = false
	m.Restore(c)
	if m.linear.Get() {
		t.Error("Restore did not put it back off")
	}
	m.linear.OnCommand(false)
	if config.Get().Microphone.WakeBeforeSuppression {
		t.Error("turned off, but still saved on")
	}
}

// The switch is offered only where WebRTC's canceller can run: on an image without its helper (the
// Dot's) it could never do anything, and the canceller select there offers only the built-in one.
func TestWakeBeforeSuppressionOnlyWithWebRTC(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	m := build()
	shown := false
	for _, e := range m.Entities() {
		if e == esphome.Entity(m.linear) {
			shown = true
		}
	}
	if shown != mic.HaveWebRTC() {
		t.Errorf("switch shown: %v, WebRTC helper on this image: %v", shown, mic.HaveWebRTC())
	}
}
