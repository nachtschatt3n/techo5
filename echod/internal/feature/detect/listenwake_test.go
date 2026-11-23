package detect

import (
	"context"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/mic"
)

// The engine subscribes to the microphones as the wake listener, the one mic.Source hands the
// canceller's linear output to with Wake words hear the canceller before suppression on. Subscribed with
// plain Listen, the switch would do nothing at all, and every test of the linear output in the mic
// package would still pass, since they subscribe as the wake listener themselves.
func TestEngineListensAsTheWakeListener(t *testing.T) {
	source := mic.New()
	e := New(1, source)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for !source.ListensWake("wake") {
		if time.Now().After(deadline) {
			t.Fatal("the wake word engine is not subscribed as the wake listener (mic.Source.ListenWake)")
		}
		time.Sleep(time.Millisecond)
	}
}
