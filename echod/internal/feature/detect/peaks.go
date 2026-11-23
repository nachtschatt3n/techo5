package detect

import (
	"sync"
	"sync/atomic"
	"time"
)

// PeakKeeper keeps each slot's highest score from a MarkPeaks to the Peaks after it. Each keeper is its
// own (Engine.NewPeaks), one for calls, one for replies and one for announcements, so a reply said
// during a call has its peaks and the call has its own. The engine's one set, which reading cleared, had the calls as its only
// user: a second user of it would start the call's peaks over with each reply, and take them as the
// reply ended.
//
// It is fed with the engine's lock held and only copies, as the board is. Its own lock is taken after
// the engine's, never before, so marking and reading never wait on a frame's inference, and with
// nothing marked a score costs it one atomic load.
type PeakKeeper struct {
	slots int
	on    atomic.Bool

	// canceling is whether the echo canceller is running, asked on each score while something is marked
	// (one atomic load on the board). It is the microphones' own (NewPeaks), and a test's stand-in.
	canceling func() bool

	mu    sync.Mutex
	peaks []Peak // nil when nothing asked
}

// Peak is a slot's highest score since MarkPeaks, with the word that scored it.
type Peak struct {
	Slot int
	ID   string
	Peak float64
	At   time.Time // when it scored that

	// Cutoff is what that score was judged against, and Canceling whether the echo canceller was
	// running as it came: what the microphones gave the word then was what the canceller left of it.
	Cutoff    float64
	Canceling bool

	// CancelFrom and CancelTo are the first and last scores of the slot, since MarkPeaks, that came with
	// the echo canceller running, zero when none did: the span the canceller ran over, which is where the
	// stop word took the cutoff for playing. In a call the stop word keeps its own cutoff unless a ring
	// sounds. It runs a second past the last sound the speaker made, which the canceller holds on for. A
	// word swapped into the slot since the mark starts the span over with its peak.
	CancelFrom, CancelTo time.Time

	// Held is that score having come while the word could not fire whatever it reached: settling
	// (Settle, after an unmute), or in the hold and the pause after a detection, which may have been
	// before MarkPeaks. A peak at or over its cutoff that did not fire is one of these.
	Held bool

	// Fired is the word having been detected since MarkPeaks. The peak of a word that fired is then
	// only what it had reached when Peaks was asked, which may be before its hold was over.
	Fired bool
}

// NewPeaks is a keeper of its own of each slot's highest score, for as long as the engine lasts.
func (e *Engine) NewPeaks() *PeakKeeper {
	e.mu.Lock()
	defer e.mu.Unlock()
	source := e.source
	k := &PeakKeeper{slots: len(e.slots), canceling: func() bool { return source != nil && source.Canceling() }}
	e.keepers = append(e.keepers, k)
	return k
}

// MarkPeaks starts each slot's highest score over again, the held back ones included, for Peaks.
func (k *PeakKeeper) MarkPeaks() {
	fresh := make([]Peak, k.slots)
	// on is set with the peaks, under the lock: a mark and a read on two goroutines (a call's state
	// changes are followed on goroutines of their own) then never leave it on with nothing to keep.
	k.mu.Lock()
	k.peaks = fresh
	k.on.Store(true)
	k.mu.Unlock()
}

// Peaks is the highest score of each slot that scored anything since MarkPeaks, a word taken out
// since included, and stops keeping them. Nothing when MarkPeaks was not called.
func (k *PeakKeeper) Peaks() []Peak {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.on.Store(false)
	var out []Peak
	for _, p := range k.peaks {
		if p.ID != "" {
			out = append(out, p)
		}
	}
	k.peaks = nil
	return out
}

// note is a fresh score of the word id in slot n, judged against cutoff; held is the word unable to fire
// as it came (Peak.Held). Held with the engine's lock.
func (k *PeakKeeper) note(n int, id string, score, cutoff float64, held bool, now time.Time) {
	if !k.on.Load() {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if n >= len(k.peaks) {
		return
	}
	canceling := k.canceling()
	p := &k.peaks[n]
	if p.ID != id {
		// A word swapped in since the mark starts its own peak.
		*p = Peak{Slot: n, ID: id}
	}
	if canceling {
		if p.CancelFrom.IsZero() {
			p.CancelFrom = now
		}
		p.CancelTo = now
	}
	// A score that only ties the peak, judged against a lower cutoff, takes its place. That way a word
	// that never rose over its first frame says the cutoff it was judged by while the reply played, not
	// the one from before the speaker started. Scores are steps of 1/255, so a reply the word heard
	// nothing in ties at 0 all the way.
	if score > p.Peak || p.At.IsZero() || (score == p.Peak && cutoff < p.Cutoff) {
		p.Peak, p.At, p.Cutoff, p.Held, p.Canceling = score, now, cutoff, held, canceling
	}
}

// fired is the word in slot n detected. Held with the engine's lock, after the score that did it was
// noted.
func (k *PeakKeeper) fired(n int) {
	if !k.on.Load() {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if n < len(k.peaks) && k.peaks[n].ID != "" {
		k.peaks[n].Fired = true
	}
}
