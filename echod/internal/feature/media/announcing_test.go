package media

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/speaker"
)

// An announcement's end is named by how its claim ended: played out, silenced, taken by another sound,
// or failed. A claim canceled is a stop and not a failure.
func TestAnAnnouncementsEndIsNamed(t *testing.T) {
	d := speaker.Sound()
	t.Cleanup(d.Silence)
	done := func(c *speaker.Claim) {
		t.Helper()
		select {
		case <-c.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the claim did not end")
		}
	}
	hold := func(ctx context.Context, _ *speaker.Player) error { <-ctx.Done(); return ctx.Err() }

	played := d.ClaimSpeech("announce", func(context.Context, *speaker.Player) error { return nil })
	done(played)
	if got := AnnouncementEnd(played); got != "played" {
		t.Errorf("a claim that played out is %q, want played", got)
	}

	failed := d.ClaimSpeech("announce", func(context.Context, *speaker.Player) error { return errors.New("no route") })
	done(failed)
	if got := AnnouncementEnd(failed); got != "failed" {
		t.Errorf("a claim whose play failed is %q, want failed", got)
	}

	silenced := d.ClaimSpeech("announce", hold)
	d.Silence()
	done(silenced)
	if got := AnnouncementEnd(silenced); got != "stopped" {
		t.Errorf("a claim silenced is %q, want stopped, not failed", got)
	}

	first := d.ClaimSpeech("announce", hold)
	d.Claim("other", hold)
	done(first)
	if got := AnnouncementEnd(first); got != "taken" {
		t.Errorf("a claim another sound took the speaker from is %q, want taken", got)
	}
}
