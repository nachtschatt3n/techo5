package detect

import (
	"slices"
	"testing"
	"time"
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
// still hears every one of them, the command words' included. It is told as said NearMissSettle ago,
// when it peaked.
func TestANearMissIsToldAfterWhatListenedBefore(t *testing.T) {
	e := &Engine{}
	var got []string
	var said []time.Time
	e.OnNearMiss = func(slot int, _ float64) { got = append(got, "before") }
	watchTalkingOver(e, func(at time.Time) {
		got = append(got, "told")
		said = append(said, at)
	})

	start := time.Now()
	e.OnNearMiss(StopSlot, 0.52)
	e.OnNearMiss(0, 0.6)
	e.OnNearMiss(CommandSlot, 0.7)

	want := []string{"before", "told", "before", "told", "before"}
	if !slices.Equal(got, want) {
		t.Errorf("near misses of the stop word, a wake word and a command word: %v, want %v", got, want)
	}
	for _, at := range said {
		if at.After(start.Add(-NearMissSettle + 100*time.Millisecond)) {
			t.Errorf("a near miss told as said %v before it was told, want about %v", time.Since(at), NearMissSettle)
		}
	}
}

// With nothing listening before, the near miss is still told.
func TestANearMissIsToldWithNothingBefore(t *testing.T) {
	e := &Engine{}
	told := 0
	watchTalkingOver(e, func(time.Time) { told++ })
	e.OnNearMiss(StopSlot, 0.52)
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
