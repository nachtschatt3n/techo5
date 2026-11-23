package media

import (
	"context"
	"errors"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/speaker"
	"github.com/HuskerMinion/techo5/echod/internal/lib/hook"
)

// Announcement is an announcement being played (assist_satellite.announce, or a media player's announce):
// told once as it takes the speaker and once more as it ends, however that was. It is for what measures
// something over one, as voice.Replying is for a reply (detect's scores of the stop word).
type Announcement struct {
	// Began is when it took the speaker, which tells the two tellings of one announcement apart from
	// another's. It is before the sound: what Home Assistant serves is fetched and the cushion filled
	// first.
	Began time.Time

	// Ended is when its claim ended, zero while it plays, and Why how: "played", "stopped", "taken" or
	// "failed" (AnnouncementEnd).
	Ended time.Time
	Why   string
}

// Announcing is told of every announcement that took the speaker, as it does and as its claim ends. It
// runs on the claim's goroutine for the start and on the one waiting for the claim for the end, so
// listeners must not block and must not assume they are one goroutine.
var Announcing hook.Hook[Announcement]

// AnnouncementEnd is how an announcement's claim ended, as Announcement.Why says it. It is taken to be
// over only once the claim is done.
func AnnouncementEnd(c *speaker.Claim) string {
	switch {
	case c.Preempted():
		// Another sound, a reply or an alarm, took the speaker. Checked before Stopped, because being
		// taken over stops the claim as well.
		return "taken"
	case c.Stopped():
		// Silenced: the stop word, a button, or Home Assistant.
		return "stopped"
	case c.Err() != nil && !errors.Is(c.Err(), context.Canceled):
		return "failed"
	}
	return "played"
}
