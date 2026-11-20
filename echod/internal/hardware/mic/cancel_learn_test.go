package mic

import "testing"

// The idle line says how much of the run the built-in filter learned from: the blocks with playback in
// them while it was adapting, none while it was frozen, none of the tail after the playback, and the
// count starts again with the next run.
func TestCancellerCountsWhatItLearnedFrom(t *testing.T) {
	c := newCanceller()
	if c == nil {
		t.Fatal("no canceller")
	}
	n := 0
	feed := func(playing bool, frames int) {
		for range frames {
			raw := rawFrame(n, playing)
			c.apply(raw, Average{}.Mix(Decode(raw)))
			n++
		}
	}
	freeze := func(on bool) {
		c.adapting.Store(!on)
		c.filter.Load().SetAdapting(!on)
	}

	feed(true, 10)
	if want := 10 * FrameSamples; c.learned != want {
		t.Fatalf("learning over 10 frames of playback counted %d samples, want %d", c.learned, want)
	}

	freeze(true)
	feed(true, 5)
	if want := 10 * FrameSamples; c.learned != want {
		t.Fatalf("frozen, the count moved to %d, want it to stay at %d", c.learned, want)
	}

	freeze(false)
	feed(false, 3) // the tail: still running, nothing to learn from
	if want := 10 * FrameSamples; c.learned != want {
		t.Fatalf("the tail after the playback counted: %d samples, want %d", c.learned, want)
	}

	feed(false, refHold/FrameSamples+2)
	if c.active.Load() {
		t.Fatal("the run never ended")
	}
	if c.learned != 0 {
		t.Errorf("the count carried over into the next run: %d samples", c.learned)
	}
}
