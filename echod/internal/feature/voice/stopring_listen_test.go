package voice

import (
	"testing"
	"time"
)

// ringRoom is stopRoom with the "a ring sounded a moment ago" answer set by the test, and the real
// one put back after: a ring ended in one test must not leak into the next one's listening turn.
func ringRoom(t *testing.T, near, music bool) (*Voice, *int) {
	t.Helper()
	v, _, paused := stopRoom(t, music)
	was := ringNear
	t.Cleanup(func() { ringNear = was })
	ringNear = func() bool { return near }
	return v, paused
}

// "<wake word>, stop" at a ringing alarm: the wake word silenced it, and the stop word then hears
// "stop" while the turn listens. Both end, so "stop" is not sent on as a request.
func TestStopAfterTheWakeWordAtARingEndsTheRingAndTheTurn(t *testing.T) {
	c := listeningTurn(t)
	v, _ := ringRoom(t, true, false)
	v.turn = c
	sound(t)

	v.Interrupt()

	if ringing.Load() {
		t.Error("the ring was left sounding")
	}
	c.handle(next(t, c, evCancel))
	if c.phase != phaseIdle {
		t.Errorf("the turn is %v, want idle", c.phase)
	}
}

// The ring is already over, ended by a tap a moment ago, and the wake word and "stop" came after.
func TestStopAfterTheWakeWordJustAfterARingEndsTheTurn(t *testing.T) {
	c := listeningTurn(t)
	v, paused := ringRoom(t, true, false)
	v.turn = c

	v.Interrupt()

	c.handle(next(t, c, evCancel))
	if *paused != 0 {
		t.Errorf("music paused %d times with none playing", *paused)
	}
}

// With music playing and no ring sounding, "stop the music" after the wake word still pauses it.
func TestStopAfterTheWakeWordJustAfterARingWithMusicPausesIt(t *testing.T) {
	c := listeningTurn(t)
	v, paused := ringRoom(t, true, true)
	v.turn = c

	v.Interrupt()

	c.handle(next(t, c, evCancel))
	if *paused != 1 {
		t.Errorf("music paused %d times, want 1", *paused)
	}
}

// "Stop the timer" said to Home Assistant, with no ring about, is a request and is left alone.
func TestStopWhileListeningWithNoRingLeavesTheTurn(t *testing.T) {
	c := listeningTurn(t)
	v, _ := ringRoom(t, false, false)
	v.turn = c

	v.Interrupt()

	select {
	case e := <-c.events:
		t.Fatalf("the turn was posted %v, want nothing", e.kind)
	case <-time.After(100 * time.Millisecond):
	}
	if c.phase != phaseListening {
		t.Errorf("the turn is %v, want listening", c.phase)
	}
}

// A turn that is already waiting on its answer, over a ring, keeps going: the ring is what ends.
func TestStopWhileThinkingOverARingKeepsTheTurn(t *testing.T) {
	c := thinkingTurn(t)
	v, _ := ringRoom(t, true, false)
	v.turn = c
	sound(t)

	v.Interrupt()

	if ringing.Load() {
		t.Error("the ring was left sounding")
	}
	select {
	case e := <-c.events:
		t.Fatalf("the turn was posted %v, want nothing", e.kind)
	case <-time.After(100 * time.Millisecond):
	}
}
