package voice

import (
	"context"
	"sync"
	"testing"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/HuskerMinion/techo5/echod/internal/feature/media"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/speaker"
)

// toldAnnouncements collects what media.Announcing tells, from whichever goroutine tells it.
type toldAnnouncements struct {
	mu  sync.Mutex
	got []media.Announcement
}

func (a *toldAnnouncements) list() []media.Announcement {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]media.Announcement(nil), a.got...)
}

func toldAnnounced(t *testing.T) *toldAnnouncements {
	t.Helper()
	a := &toldAnnouncements{}
	t.Cleanup(media.Announcing.Listen(func(n media.Announcement) {
		a.mu.Lock()
		a.got = append(a.got, n)
		a.mu.Unlock()
	}))
	return a
}

// waitFor polls until cond holds, for up to five seconds.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(end) {
			t.Fatalf("gave up waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// An announcement is told once as it takes the speaker and once as it ends, how it ended with it.
func TestAnAnnouncementIsToldAsItStartsAndAsItEnds(t *testing.T) {
	got := toldAnnounced(t)
	c := thinkingTurn(t)
	url, release := heldSpeech(t)
	close(release)

	c.announce(esphome.Announce{MediaID: url})
	waitUntil(t, "the announcement's end", func() bool { return len(got.list()) == 2 })

	l := got.list()
	if l[0].Began.IsZero() || !l[0].Ended.IsZero() {
		t.Errorf("the first telling is %+v, want a start", l[0])
	}
	if !l[1].Began.Equal(l[0].Began) || l[1].Ended.Before(l[1].Began) || l[1].Why != "played" {
		t.Errorf("the second telling is %+v, want the end of the one begun at %v, played", l[1], l[0].Began)
	}
}

// One silenced while it plays, as the stop word does, is told as stopped.
func TestASilencedAnnouncementIsToldAsStopped(t *testing.T) {
	got := toldAnnounced(t)
	c := thinkingTurn(t)
	url, _ := heldSpeech(t) // never released: still being fetched, so the claim is held
	c.announce(esphome.Announce{MediaID: url})
	waitUntil(t, "the announcement's start", func() bool { return len(got.list()) == 1 })

	c.sound.Silence()
	waitUntil(t, "the announcement's end", func() bool { return len(got.list()) == 2 })
	if why := got.list()[1].Why; why != "stopped" {
		t.Errorf("ended %q, want stopped", why)
	}
}

// One another claim takes the speaker from is told as taken.
func TestAnAnnouncementTakenByAnotherSoundIsToldAsTaken(t *testing.T) {
	got := toldAnnounced(t)
	c := thinkingTurn(t)
	url, _ := heldSpeech(t)
	c.announce(esphome.Announce{MediaID: url})
	waitUntil(t, "the announcement's start", func() bool { return len(got.list()) == 1 })

	c.sound.Claim("other", func(ctx context.Context, _ *speaker.Player) error { <-ctx.Done(); return nil })
	t.Cleanup(c.sound.Silence)
	waitUntil(t, "the announcement's end", func() bool { return len(got.list()) == 2 })
	if why := got.list()[1].Why; why != "taken" {
		t.Errorf("ended %q, want taken", why)
	}
}

// An announcement is not a reply: voice.Replying hears nothing of it, which is what keeps the turn's
// deadline and the reply's scores from being its.
func TestAnAnnouncementPostsNoReplying(t *testing.T) {
	got := told(t)
	ann := toldAnnounced(t)
	c := thinkingTurn(t)
	url, release := heldSpeech(t)
	close(release)

	c.announce(esphome.Announce{MediaID: url})
	waitUntil(t, "the announcement's end", func() bool { return len(ann.list()) == 2 })
	settle(c, 100*time.Millisecond)
	if len(*got) != 0 {
		t.Errorf("Replying told %+v for an announcement", *got)
	}
}
