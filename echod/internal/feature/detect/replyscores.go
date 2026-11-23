package detect

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/feature/media"
	"github.com/HuskerMinion/techo5/echod/internal/feature/voice"
)

// replyScores puts in the log, as each reply ends, the most the stop word scored while it was said: how
// far into the reply that was, what it was judged against, whether the echo canceller was running, and
// whether the word was heard. A "stop" said over a reply that left no wake or near miss line behind then
// shows how far short it fell. A peak close under the cutoff is a word that nearly made it; one far under
// it is a word the canceller, or the reply itself, took from the microphones. While the speaker plays,
// the stop word's cutoff is often already as low as it goes (thresholdFor), so a lower threshold may not
// bring it down: what the line is for is telling those two apart, and comparing the peaks before and
// after a change to what the stop word hears.
//
// It is one number a reply, the highest. Several tries over one reply show as the best of them, and a
// reply nobody spoke over shows what the reply alone made the stop word score, which is what a try has to
// be told apart from. Measuring it is one "stop" a reply, and some replies with none.
//
// The peak's own canceling and cutoff are those of the frame that scored it, and of equal scores the one
// judged against the lower cutoff counts. canceling_from_s and canceling_to_s say when, in the window,
// the echo canceller ran, which is when the stop word took the cutoff for playing: they normally start a
// moment after at_s 0, the streamed reply's cushion, and end about a second after the reply's last sound.
// "none" is a reply the canceller never ran over: one on another media player, or with cancellation
// switched off. In a call the canceller runs throughout, but the stop word keeps its own cutoff unless
// a ring sounds, so there the span is when the canceller ran, not when the cutoff was lowered.
//
// The window is from the reply's first audio arriving to the turn's end, which is a little longer than
// the reply sounded: a streamed reply waits for its cushion before it plays, and the canceller only
// starts once the speaker does. A peak in that first moment is judged as in a quiet room, and says so
// (canceling=false, the cutoff without the slack for playing). A reply played on another media player
// is told too, though nothing sounded here (canceling=false again), and so is one sent into a call,
// which the room did not hear. Nothing is kept across a restart, so a reply the daemon restarted under
// has no line. A restart of the engine (Start), or of the echo canceller, under a reply leaves its
// scores as they were: the line then has what the stop word scored before it beside what it scored
// after, with the word loaded afresh or the canceller learning again.
//
// The other words' peaks follow as a call's line has them (callscores.go). What a reply by itself scores
// on a wake word is what that word's threshold for playing has to stay over.
//
// An announcement gets the same line from a keeper of its own, so one said over a reply, or a reply over
// one, keeps its own peaks. Its window runs from taking the speaker, before Home Assistant's media is
// fetched and the cushion fills, so a peak with canceling=false there came before its sound, to the end
// of its claim. Its ended is played, stopped, taken or failed (media.AnnouncementEnd).
type replyScores struct {
	marks   peakMarker
	dropped func() uint64
	// stops is whether the stop word is listened for at all: with it off there is nothing to say.
	stops func() bool
	say   func(msg string, args ...any)
	// over is what the scores are over, "reply" or "announcement", as the line says it.
	over string

	// A reply is told from the conversation's goroutine alone, but an announcement starts on its claim's
	// goroutine and ends on the one waiting for the claim, so these are held under a lock.
	mu      sync.Mutex
	began   time.Time // when the reply being said began, zero with none
	missing uint64    // frames missed before it did
}

// follow is a reply's audio starting, or the turn it answers ending (voice.Replying). It runs on the
// conversation's goroutine, which the keeper never holds up (PeakKeeper). A reply whose end is never told
// leaves its keeper marking until the next reply starts it over, a lock and a compare a scored frame
// meanwhile.
func (r *replyScores) follow(v voice.Reply) { r.told(v.Began, v.Ended, v.Why, v.Elsewhere) }

// followAnnouncement is an announcement taking the speaker, or its claim ending (media.Announcing).
func (r *replyScores) followAnnouncement(a media.Announcement) {
	r.told(a.Began, a.Ended, a.Why, false)
}

// told is the start of a reply or an announcement (ended zero) or its end, elsewhere for a reply that
// played on another speaker. It holds the lock throughout: marking and reading the keeper, and saying,
// do not block.
func (r *replyScores) told(began, ended time.Time, why string, elsewhere bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ended.IsZero() {
		// voice tells one start a turn, a reply taken over by its url included. A second start with no
		// end between still starts the scores over, so a lost end never runs one reply into the next.
		r.began, r.missing = began, r.dropped()
		r.marks.MarkPeaks()
		return
	}
	if r.began.IsZero() {
		return
	}
	// An end for an announcement that the next one took over: its start already started the scores over,
	// and reading them now would stop the next one's. A reply always ends with the Began it started with
	// (voice), so its line is never dropped here.
	if !began.Equal(r.began) {
		return
	}
	r.began = time.Time{}
	peaks := r.marks.Peaks()
	if !r.stops() {
		return
	}

	// window_s is the time the scores were kept over, from the first audio arriving to the turn's end:
	// not how long the reply sounded, which "reply played" has as seconds.
	args := []any{"window_s", tenths(ended.Sub(began)), "ended", why, "frames_dropped", r.dropped() - r.missing}
	if elsewhere {
		// The reply played on another speaker: nothing was playing here for the canceller to take out,
		// so these are the stop word's scores over the room, not over a reply.
		args = append(args, "elsewhere", true)
	}
	var others []any
	scored := false
	for _, p := range peaks {
		if p.Slot != StopSlot {
			others = append(others, fmt.Sprintf("slot%d_%s", p.Slot+1, p.ID),
				fmt.Sprintf("%.2f@%ds", hundredths(p.Peak), int(p.At.Sub(began).Seconds())))
			continue
		}
		scored = true
		args = append([]any{"peak", hundredths(p.Peak), "at_s", tenths(p.At.Sub(began)), "cutoff", hundredths(p.Cutoff),
			"canceling", p.Canceling, "held", p.Held, "heard", p.Fired,
			"canceling_from_s", spanAt(p.CancelFrom, began), "canceling_to_s", spanAt(p.CancelTo, began)}, args...)
	}
	if !scored {
		// Listened for and never scored: the microphones were cut, or the loop was not running.
		r.say("stop word not scored over the "+r.over, append(args, others...)...)
		return
	}
	r.say("stop word over the "+r.over, append(args, others...)...)
}

// tenths is d in seconds, to a tenth; hundredths is a score to two places, as the scores view has it.
func tenths(d time.Duration) float64 { return math.Round(d.Seconds()*10) / 10 }
func hundredths(v float64) float64   { return math.Round(v*100) / 100 }

// spanAt is when t was in the window that began at began, in tenths of a second, "none" for no time.
func spanAt(t, began time.Time) any {
	if t.IsZero() {
		return "none"
	}
	return tenths(t.Sub(began))
}

// followReplies keeps the engine's scores over each reply, from a keeper of their own, and says them
// with say as the reply ends. It returns what stops it.
func followReplies(e *Engine, say func(msg string, args ...any)) func() {
	r := &replyScores{
		marks:   e.NewPeaks(),
		dropped: e.source.Dropped,
		stops:   func() bool { return config.Get().Wake.Stop.Listening() },
		say:     say,
		over:    "reply",
	}
	return voice.Replying.Listen(r.follow)
}

// followAnnouncements keeps the engine's scores over each announcement, from a keeper of its own, and
// says them with say as its claim ends. It returns what stops it.
func followAnnouncements(e *Engine, say func(msg string, args ...any)) func() {
	r := &replyScores{
		marks:   e.NewPeaks(),
		dropped: e.source.Dropped,
		stops:   func() bool { return config.Get().Wake.Stop.Listening() },
		say:     say,
		over:    "announcement",
	}
	return media.Announcing.Listen(r.followAnnouncement)
}
