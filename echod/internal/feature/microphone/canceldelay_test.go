package microphone

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/mic"
)

// The delay is a setting beside the canceller it is for, 0 to 20 ms, off by default, kept as it is
// set, handed to the capture source, and brought into range when Home Assistant or a file asks for
// more.
func TestEchoCancellerDelay(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	m := build()
	d := m.delay
	source := mic.Get()
	if d.ObjectID != "microphone_echo_canceller_delay" || d.Category != esphome.CategoryConfig ||
		d.Min != 0 || d.Max != config.MaxCancelDelay || d.Step != 1 || d.Unit != "ms" {
		t.Fatalf("entity: id=%q category=%v range=%v-%v step=%v unit=%q",
			d.ObjectID, d.Category, d.Min, d.Max, d.Step, d.Unit)
	}
	// The source is shared by every test in the package: leave it without a delay.
	t.Cleanup(func() {
		c := config.Get()
		c.Microphone.CancelDelay = 0
		m.Restore(c)
	})

	m.Restore(config.Get())
	if got := d.Get(); got != 0 {
		t.Errorf("a new device shows %v ms", got)
	}

	d.OnCommand(8)
	if got := config.Get().Microphone.CancelDelay; got != 8 {
		t.Errorf("saved %d ms, want 8", got)
	}
	if got := d.Get(); got != 8 {
		t.Errorf("shows %v ms, want 8", got)
	}
	if got := source.CancelDelay(); got != 8 {
		t.Errorf("the source holds back %d ms, want 8", got)
	}

	d.OnCommand(45)
	if got := config.Get().Microphone.CancelDelay; got != config.MaxCancelDelay {
		t.Errorf("saved %d ms past the range, want %d", got, config.MaxCancelDelay)
	}
	if got := source.CancelDelay(); got != config.MaxCancelDelay {
		t.Errorf("the source holds back %d ms past the range, want %d", got, config.MaxCancelDelay)
	}

	c := config.Get()
	c.Microphone.CancelDelay = 6
	m.Restore(c)
	if got := d.Get(); got != 6 {
		t.Errorf("restored %v ms, want 6", got)
	}
	if got := source.CancelDelay(); got != 6 {
		t.Errorf("the source holds back %d ms after a restore, want 6", got)
	}
}

// As the device comes up with WebRTC's canceller and a delay, the helper starts once, already with
// the delay: the delay is restored before the engine. The starts are counted from the daemon's own
// log line, which is written as each one happens; a helper killed at once may never run far enough
// to say so itself.
func TestRestoreStartsTheHelperOnceWithItsDelay(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, mic.WebRTCHelper), []byte("#!/bin/sh\nexec cat > /dev/null\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	config.Use(filepath.Join(t.TempDir(), "state.json"))
	m := build()
	t.Cleanup(func() {
		c := config.Get()
		c.Microphone.CancelEngine = config.CancelBuiltin
		c.Microphone.CancelDelay = 0
		m.Restore(c)
	})

	c := config.Get()
	c.Microphone.CancelEngine = config.CancelWebRTC
	c.Microphone.CancelDelay = 7
	m.Restore(c)
	if got := mic.Get().CancelEngine(); got != "webrtc" {
		t.Fatalf("engine %q with a helper on PATH", got)
	}

	var started []string
	for _, line := range strings.Split(logged.String(), "\n") {
		if strings.Contains(line, "echo cancellation helper started") {
			started = append(started, line[strings.Index(line, "args="):])
		}
	}
	if len(started) != 1 || started[0] != `args="[--ns low --delay-ms 7]"` {
		t.Fatalf("helper started %d times, with %q; want once, with the delay", len(started), started)
	}
}
