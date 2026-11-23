package detect

import (
	"slices"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/lib/wake"
)

// The words that say somebody may be talking over a reply are the stop word and the wake words. The
// command words' near misses are mostly music, and the hang-up word listens only during a call.
func TestTheWordsSaidOverAReplyAreTheStopWordAndTheWakeWords(t *testing.T) {
	for _, c := range []struct {
		name string
		slot int
		want bool
	}{
		{"first wake word", 0, true},
		{"second wake word", 1, true},
		{"stop word", StopSlot, true},
		{"hang-up word", HangupSlot, false},
		{"volume down word", CommandSlot, false},
		{"next song word", NextSongSlot, false},
		{"no slot", -1, false},
	} {
		if got := talkingOver(c.slot); got != c.want {
			t.Errorf("%s (slot %d): talking over %v, want %v", c.name, c.slot, got, c.want)
		}
	}
}

// A near miss is told after whatever listened for near misses before (the ducker, the clips), which
// still hears every one of them, the command words' included. It is told as said when it peaked, the
// time the engine reports it with, and they hear that time too.
func TestANearMissIsToldAfterWhatListenedBefore(t *testing.T) {
	e := &Engine{}
	var got []string
	var heard, said []time.Time
	e.OnNearMiss = func(slot int, _ float64, at time.Time) {
		got = append(got, "before")
		heard = append(heard, at)
	}
	watchTalkingOver(e, func(at time.Time) {
		got = append(got, "told")
		said = append(said, at)
	})

	peaked := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	e.OnNearMiss(StopSlot, 0.52, peaked)
	e.OnNearMiss(0, 0.6, peaked.Add(time.Second))
	e.OnNearMiss(CommandSlot, 0.7, peaked.Add(2*time.Second))

	want := []string{"before", "told", "before", "told", "before"}
	if !slices.Equal(got, want) {
		t.Errorf("near misses of the stop word, a wake word and a command word: %v, want %v", got, want)
	}
	if w := []time.Time{peaked, peaked.Add(time.Second)}; !slices.Equal(said, w) {
		t.Errorf("near misses told as said at %v, want when they peaked, %v", said, w)
	}
	if w := []time.Time{peaked, peaked.Add(time.Second), peaked.Add(2 * time.Second)}; !slices.Equal(heard, w) {
		t.Errorf("what listened before heard them at %v, want when they peaked, %v", heard, w)
	}
}

// The engine reports a near miss on the first score more than NearMissSettle after its peak, and the
// scores come every 80 ms with openWakeWord, so it reports late by up to a score, and later still by
// however long its goroutine waits. It says when the word peaked all the same: a peak just before the
// turn stopped listening, reported once it has, is the request and not a word over the reply.
func TestANearMissIsReportedWithWhenItPeaked(t *testing.T) {
	fakes(t)
	e := New(2, nil)
	e.Threshold = func(int) float64 { return 0.9 }
	if err := e.Use(0, model("hey_jarvis", wake.KindMicroWakeWord)); err != nil {
		t.Fatalf("Use(0): %v", err)
	}
	type report struct {
		peak float64
		at   time.Time
	}
	got := make(chan report, 2)
	e.OnNearMiss = func(_ int, peak float64, at time.Time) { got <- report{peak, at} }

	peaked := time.Now()
	s := &e.slots[0]
	e.judge(0, s, 0.3, peaked.Add(-80*time.Millisecond), nil)
	e.judge(0, s, 0.6, peaked, nil)
	every := 80 * time.Millisecond
	last := peaked
	for at := peaked.Add(every); at.Sub(peaked) <= NearMissSettle; at = at.Add(every) {
		e.judge(0, s, 0.1, at, nil)
		last = at
	}
	select {
	case r := <-got:
		t.Fatalf("reported %+v before NearMissSettle had passed", r)
	case <-time.After(50 * time.Millisecond):
	}

	reported := last.Add(every)
	e.judge(0, s, 0.1, reported, nil)
	select {
	case r := <-got:
		if r.peak != 0.6 || !r.at.Equal(peaked) {
			t.Errorf("reported a peak of %v at %v, %v after the report, want 0.6 at the peak, %v before it",
				r.peak, r.at, reported.Sub(r.at), reported.Sub(peaked))
		}
	case <-time.After(time.Second):
		t.Fatal("the near miss was not reported")
	}
}

// With nothing listening before, the near miss is still told.
func TestANearMissIsToldWithNothingBefore(t *testing.T) {
	e := &Engine{}
	told := 0
	watchTalkingOver(e, func(time.Time) { told++ })
	e.OnNearMiss(StopSlot, 0.52, time.Now())
	if told != 1 {
		t.Errorf("told %d times, want 1", told)
	}
}

// A detection and a candidate to ask Home Assistant about are told before they are routed: a wake word
// can wait on the other devices before its turn opens, and whoever said it goes on talking. A word that
// does not count is routed all the same, untold.
func TestADetectionAndACandidateAreToldBeforeTheyAreRouted(t *testing.T) {
	e := &Engine{}
	var got []string
	e.OnDetect = func(slot int) { got = append(got, "routed") }
	e.OnCandidate = func(slot int, _ float64) { got = append(got, "asked") }
	watchTalkingOver(e, func(time.Time) { got = append(got, "told") })

	e.OnDetect(StopSlot)
	e.OnDetect(CommandSlot)
	e.OnCandidate(0, 0.4)

	want := []string{"told", "routed", "routed", "told", "asked"}
	if !slices.Equal(got, want) {
		t.Errorf("the stop word, a command word and a wake candidate: %v, want %v", got, want)
	}
}

// Hooks that were never set are no crash: the word is still told.
func TestTalkingOverWithNoHooksSet(t *testing.T) {
	e := &Engine{}
	told := 0
	watchTalkingOver(e, func(time.Time) { told++ })
	e.OnDetect(0)
	e.OnCandidate(0, 0.4)
	if told != 2 {
		t.Errorf("told %d times, want 2", told)
	}
}
