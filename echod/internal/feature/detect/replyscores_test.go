package detect

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/feature/media"
	"github.com/HuskerMinion/techo5/echod/internal/feature/voice"
	"github.com/HuskerMinion/techo5/echod/internal/lib/wake"
)

func replyLines(f *fakePeaks, stops *bool, dropped *uint64) (*replyScores, *[]string) {
	return scoreLines("reply", f, stops, dropped)
}

// announcementLines is replyLines for the scores over an announcement.
func announcementLines(f *fakePeaks, stops *bool, dropped *uint64) (*replyScores, *[]string) {
	return scoreLines("announcement", f, stops, dropped)
}

func scoreLines(over string, f *fakePeaks, stops *bool, dropped *uint64) (*replyScores, *[]string) {
	var lines []string
	r := &replyScores{marks: f, dropped: func() uint64 { return *dropped }, stops: func() bool { return *stops }, over: over,
		say: func(msg string, args ...any) { lines = append(lines, msg+" "+fmt.Sprint(args...)) }}
	return r, &lines
}

// A reply's audio starting starts the scores over, and the turn ending says the stop word's peak in
// one line: when in the reply it came, what it was judged against, whether the canceller ran, whether
// the word could have fired and was heard, how long the scores were kept and how the turn ended. The
// other words follow as a call's line has them.
func TestTheStopWordsPeakIsSaidAsAReplyEnds(t *testing.T) {
	at := time.Date(2026, 11, 20, 20, 0, 0, 0, time.UTC)
	f := &fakePeaks{}
	stops, dropped := true, uint64(4)
	r, lines := replyLines(f, &stops, &dropped)

	r.follow(voice.Reply{Began: at})
	if f.marks != 1 || len(*lines) != 0 {
		t.Fatalf("marks = %d, lines = %d as the reply began, want 1 and none", f.marks, len(*lines))
	}
	dropped = 6
	f.peaks = []Peak{
		{Slot: 0, ID: "okay_nabu", Peak: 0.124, At: at.Add(3400 * time.Millisecond), Cutoff: 0.75, Canceling: true},
		{Slot: StopSlot, ID: "stop", Peak: 0.4149, At: at.Add(5240 * time.Millisecond), Cutoff: 0.5, Canceling: true},
	}
	r.follow(voice.Reply{Began: at, Ended: at.Add(20490 * time.Millisecond), Why: "spoken"})
	if len(*lines) != 1 {
		t.Fatalf("said %d lines as the reply ended, want 1", len(*lines))
	}
	want := "stop word over the reply " + fmt.Sprint("peak", 0.41, "at_s", 5.2, "cutoff", 0.5, "canceling", true,
		"held", false, "heard", false, "window_s", 20.5, "ended", "spoken", "frames_dropped", uint64(2),
		"slot1_okay_nabu", "0.12@3s")
	if (*lines)[0] != want {
		t.Errorf("said %q, want %q", (*lines)[0], want)
	}

	// The turn's end told again, or with no reply begun, says nothing.
	r.follow(voice.Reply{Began: at, Ended: at.Add(21 * time.Second), Why: "spoken"})
	if len(*lines) != 1 {
		t.Error("said the scores again with no reply")
	}
}

// A stop word that was heard says so, one held back says so, and one that was listened for and never
// scored has a line of its own; with the stop word off there is no line.
func TestAReplysLineSaysHeardHeldAndNotScored(t *testing.T) {
	at := time.Date(2026, 11, 20, 20, 0, 0, 0, time.UTC)
	f := &fakePeaks{}
	stops, dropped := true, uint64(0)
	r, lines := replyLines(f, &stops, &dropped)

	r.follow(voice.Reply{Began: at})
	f.peaks = []Peak{{Slot: StopSlot, ID: "stop", Peak: 0.58, At: at.Add(2 * time.Second), Cutoff: 0.5, Canceling: true, Fired: true}}
	r.follow(voice.Reply{Began: at, Ended: at.Add(2100 * time.Millisecond), Why: "canceled"})
	want := "stop word over the reply " + fmt.Sprint("peak", 0.58, "at_s", 2.0, "cutoff", 0.5, "canceling", true,
		"held", false, "heard", true, "window_s", 2.1, "ended", "canceled", "frames_dropped", uint64(0))
	if len(*lines) != 1 || (*lines)[0] != want {
		t.Fatalf("said %q, want %q", *lines, want)
	}

	// Over the cutoff and not heard: held back, after an unmute.
	r.follow(voice.Reply{Began: at})
	f.peaks = []Peak{{Slot: StopSlot, ID: "stop", Peak: 0.8, At: at.Add(time.Second), Cutoff: 0.5, Held: true}}
	r.follow(voice.Reply{Began: at, Ended: at.Add(3 * time.Second), Why: "spoken"})
	want = "stop word over the reply " + fmt.Sprint("peak", 0.8, "at_s", 1.0, "cutoff", 0.5, "canceling", false,
		"held", true, "heard", false, "window_s", 3.0, "ended", "spoken", "frames_dropped", uint64(0))
	if len(*lines) != 2 || (*lines)[1] != want {
		t.Fatalf("said %q, want %q", *lines, want)
	}

	r.follow(voice.Reply{Began: at})
	f.peaks = nil
	r.follow(voice.Reply{Began: at, Ended: at.Add(time.Second), Why: "cut off"})
	want = "stop word not scored over the reply " + fmt.Sprint("window_s", 1.0, "ended", "cut off", "frames_dropped", uint64(0))
	if len(*lines) != 3 || (*lines)[2] != want {
		t.Fatalf("said %q, want %q", *lines, want)
	}

	stops = false
	r.follow(voice.Reply{Began: at})
	r.follow(voice.Reply{Began: at, Ended: at.Add(time.Second), Why: "spoken"})
	if len(*lines) != 3 {
		t.Errorf("said a line with the stop word off: %q", (*lines)[3:])
	}
	if f.marks != 4 {
		t.Errorf("marks = %d, want one for each reply", f.marks)
	}
}

// A second start before an end starts the scores over, and still says one line.
func TestAReplySpokenAgainStartsTheScoresOver(t *testing.T) {
	at := time.Date(2026, 11, 20, 20, 0, 0, 0, time.UTC)
	f := &fakePeaks{}
	stops, dropped := true, uint64(0)
	r, lines := replyLines(f, &stops, &dropped)

	r.follow(voice.Reply{Began: at})
	r.follow(voice.Reply{Began: at.Add(2 * time.Second)})
	if f.marks != 2 {
		t.Fatalf("marks = %d, want the scores started over with the second audio", f.marks)
	}
	f.peaks = []Peak{{Slot: StopSlot, ID: "stop", Peak: 0.2, At: at.Add(3 * time.Second), Cutoff: 0.5}}
	r.follow(voice.Reply{Began: at.Add(2 * time.Second), Ended: at.Add(6 * time.Second), Why: "spoken"})
	want := "stop word over the reply " + fmt.Sprint("peak", 0.2, "at_s", 1.0, "cutoff", 0.5, "canceling", false,
		"held", false, "heard", false, "window_s", 4.0, "ended", "spoken", "frames_dropped", uint64(0))
	if len(*lines) != 1 || (*lines)[0] != want {
		t.Fatalf("said %q, want %q", *lines, want)
	}
}

// What the device runs: voice tells of a reply, the engine's own keeper keeps the stop word's scores
// over it, and the line is said as the turn ends. Scores before the reply, and after it, are not its.
func TestARepliesStopWordScoresReachTheLog(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	made := fakes(t)
	e := New(StopSlot+1, nil)
	e.Threshold = func(int) float64 { return 0.5 }
	if err := e.Use(StopSlot, model("stop", wake.KindMicroWakeWord)); err != nil {
		t.Fatalf("Use(StopSlot): %v", err)
	}
	scores := made[wake.KindMicroWakeWord].loaded

	var lines []string
	t.Cleanup(followReplies(e, func(msg string, args ...any) { lines = append(lines, msg+" "+fmt.Sprint(args...)) }))

	scores["stop"] = 0.45
	e.score([]int16{0}, nil)

	began := time.Now()
	voice.Replying.Emit(voice.Reply{Began: began})
	scores["stop"] = 0.31
	e.score([]int16{0}, nil)
	scores["stop"] = 0.12
	e.score([]int16{0}, nil)
	voice.Replying.Emit(voice.Reply{Began: began, Ended: began.Add(4 * time.Second), Why: "spoken"})

	scores["stop"] = 0.4
	e.score([]int16{0}, nil)

	if len(lines) != 1 {
		t.Fatalf("said %q, want one line", lines)
	}
	if want := "stop word over the reply " + fmt.Sprint("peak", 0.31); !strings.HasPrefix(lines[0], want) {
		t.Errorf("said %q, want the stop word's 0.31 from the reply alone", lines[0])
	}
}

// An announcement is told as a reply is, and its line says what it was over. A stop said over it that
// the device took for its own voice shows as heard, with the announcement ended stopped.
func TestTheStopWordsPeakIsSaidAsAnAnnouncementEnds(t *testing.T) {
	at := time.Date(2026, 11, 20, 20, 0, 0, 0, time.UTC)
	f := &fakePeaks{}
	stops, dropped := true, uint64(0)
	r, lines := announcementLines(f, &stops, &dropped)

	r.followAnnouncement(media.Announcement{Began: at})
	if f.marks != 1 || len(*lines) != 0 {
		t.Fatalf("marks = %d, lines = %d as the announcement began, want 1 and none", f.marks, len(*lines))
	}
	f.peaks = []Peak{
		{Slot: StopSlot, ID: "stop", Peak: 0.712, At: at.Add(3400 * time.Millisecond), Cutoff: 0.7, Canceling: true, Fired: true},
	}
	r.followAnnouncement(media.Announcement{Began: at, Ended: at.Add(3500 * time.Millisecond), Why: "stopped"})
	want := "stop word over the announcement " + fmt.Sprint("peak", 0.71, "at_s", 3.4, "cutoff", 0.7, "canceling", true,
		"held", false, "heard", true, "window_s", 3.5, "ended", "stopped", "frames_dropped", uint64(0))
	if len(*lines) != 1 || (*lines)[0] != want {
		t.Fatalf("said %q, want %q", *lines, want)
	}

	r.followAnnouncement(media.Announcement{Began: at, Ended: at.Add(4 * time.Second), Why: "stopped"})
	if len(*lines) != 1 {
		t.Error("said the scores again with no announcement")
	}
}

// An announcement the next one took over has its end told after the next one's start. That end must not
// read, and so stop, the peaks the next one is keeping.
func TestAnAnnouncementTakenOverByTheNextKeepsTheNextsScores(t *testing.T) {
	at := time.Date(2026, 11, 20, 20, 0, 0, 0, time.UTC)
	f := &fakePeaks{}
	stops, dropped := true, uint64(0)
	r, lines := announcementLines(f, &stops, &dropped)

	a, b := at, at.Add(2*time.Second)
	r.followAnnouncement(media.Announcement{Began: a})
	r.followAnnouncement(media.Announcement{Began: b})
	r.followAnnouncement(media.Announcement{Began: a, Ended: at.Add(2100 * time.Millisecond), Why: "taken"})
	if len(*lines) != 0 {
		t.Fatalf("said %q for an announcement taken over, want nothing", *lines)
	}
	f.peaks = []Peak{{Slot: StopSlot, ID: "stop", Peak: 0.3, At: b.Add(time.Second), Cutoff: 0.5}}
	r.followAnnouncement(media.Announcement{Began: b, Ended: b.Add(4 * time.Second), Why: "played"})
	if len(*lines) != 1 || !strings.HasPrefix((*lines)[0], "stop word over the announcement "+fmt.Sprint("peak", 0.3)) {
		t.Fatalf("said %q, want one line with the second announcement's peak", *lines)
	}
}

// One listened for and never scored has its own line, and with the stop word off there is none.
func TestAnAnnouncementNotScoredHasItsOwnLine(t *testing.T) {
	at := time.Date(2026, 11, 20, 20, 0, 0, 0, time.UTC)
	f := &fakePeaks{}
	stops, dropped := true, uint64(0)
	r, lines := announcementLines(f, &stops, &dropped)

	r.followAnnouncement(media.Announcement{Began: at})
	r.followAnnouncement(media.Announcement{Began: at, Ended: at.Add(time.Second), Why: "played"})
	want := "stop word not scored over the announcement " + fmt.Sprint("window_s", 1.0, "ended", "played", "frames_dropped", uint64(0))
	if len(*lines) != 1 || (*lines)[0] != want {
		t.Fatalf("said %q, want %q", *lines, want)
	}

	stops = false
	r.followAnnouncement(media.Announcement{Began: at})
	r.followAnnouncement(media.Announcement{Began: at, Ended: at.Add(time.Second), Why: "played"})
	if len(*lines) != 1 {
		t.Errorf("said a line with the stop word off: %q", (*lines)[1:])
	}
}

// What the device runs: media tells of an announcement and the engine's own keeper keeps the scores over
// it. Scores before it, and after it, are not its.
func TestAnAnnouncementsStopWordScoresReachTheLog(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	made := fakes(t)
	e := New(StopSlot+1, nil)
	e.Threshold = func(int) float64 { return 0.5 }
	if err := e.Use(StopSlot, model("stop", wake.KindMicroWakeWord)); err != nil {
		t.Fatalf("Use(StopSlot): %v", err)
	}
	scores := made[wake.KindMicroWakeWord].loaded

	var lines []string
	t.Cleanup(followAnnouncements(e, func(msg string, args ...any) { lines = append(lines, msg+" "+fmt.Sprint(args...)) }))

	scores["stop"] = 0.45
	e.score([]int16{0}, nil)

	began := time.Now()
	media.Announcing.Emit(media.Announcement{Began: began})
	scores["stop"] = 0.31
	e.score([]int16{0}, nil)
	scores["stop"] = 0.12
	e.score([]int16{0}, nil)
	media.Announcing.Emit(media.Announcement{Began: began, Ended: began.Add(4 * time.Second), Why: "played"})

	scores["stop"] = 0.4
	e.score([]int16{0}, nil)

	if len(lines) != 1 {
		t.Fatalf("said %q, want one line", lines)
	}
	if want := "stop word over the announcement " + fmt.Sprint("peak", 0.31); !strings.HasPrefix(lines[0], want) {
		t.Errorf("said %q, want the stop word's 0.31 from the announcement alone", lines[0])
	}
}

// A reply said over an announcement, and an announcement over a reply, each keep their own peaks.
func TestAReplyAndAnAnnouncementKeepTheirOwnPeaks(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	made := fakes(t)
	e := New(StopSlot+1, nil)
	e.Threshold = func(int) float64 { return 0.5 }
	if err := e.Use(StopSlot, model("stop", wake.KindMicroWakeWord)); err != nil {
		t.Fatalf("Use(StopSlot): %v", err)
	}
	scores := made[wake.KindMicroWakeWord].loaded

	var lines []string
	say := func(msg string, args ...any) { lines = append(lines, msg+" "+fmt.Sprint(args...)) }
	t.Cleanup(followReplies(e, say))
	t.Cleanup(followAnnouncements(e, say))

	at := time.Now()
	media.Announcing.Emit(media.Announcement{Began: at})
	scores["stop"] = 0.6
	e.score([]int16{0}, nil)
	voice.Replying.Emit(voice.Reply{Began: at.Add(time.Second)})
	scores["stop"] = 0.3
	e.score([]int16{0}, nil)
	voice.Replying.Emit(voice.Reply{Began: at.Add(time.Second), Ended: at.Add(2 * time.Second), Why: "spoken"})
	scores["stop"] = 0.2
	e.score([]int16{0}, nil)
	media.Announcing.Emit(media.Announcement{Began: at, Ended: at.Add(3 * time.Second), Why: "played"})

	if len(lines) != 2 {
		t.Fatalf("said %q, want two lines", lines)
	}
	if want := "stop word over the reply " + fmt.Sprint("peak", 0.3); !strings.HasPrefix(lines[0], want) {
		t.Errorf("said %q, want the reply's 0.3", lines[0])
	}
	if want := "stop word over the announcement " + fmt.Sprint("peak", 0.6); !strings.HasPrefix(lines[1], want) {
		t.Errorf("said %q, want the announcement's 0.6", lines[1])
	}
}

// An announcement starts on one goroutine and ends on another, so the scores are held under a lock. Run
// with -race this is the check.
func TestAnnouncementScoresTakeALock(t *testing.T) {
	at := time.Date(2026, 11, 20, 20, 0, 0, 0, time.UTC)
	f := &fakePeaks{}
	stops, dropped := true, uint64(0)
	r, _ := announcementLines(f, &stops, &dropped)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		began := at.Add(time.Duration(i) * time.Second)
		go func() { defer wg.Done(); r.followAnnouncement(media.Announcement{Began: began}) }()
		go func() {
			defer wg.Done()
			r.followAnnouncement(media.Announcement{Began: began, Ended: began.Add(time.Second), Why: "played"})
		}()
	}
	wg.Wait()
}
