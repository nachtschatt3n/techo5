package mic

import (
	"math"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/lib/aec"
)

// micsApart is one frame per microphone, each at a level of its own, so which of them (or which mix of
// them) a path is can be read off its first sample. At least two, and enough to hold CenterMic.
func micsApart() [][]int16 {
	n := max(2, CenterMic+1)
	mics := make([][]int16, n)
	for i := range mics {
		mics[i] = make([]int16, FrameSamples)
		for j := range mics[i] {
			mics[i][j] = int16((i + 1) * 100)
		}
	}
	return mics
}

// The path is what the switch and the mixing make of it together: the center microphone with the switch
// off, whatever the mix; with it on, the center microphone still under Center mixing, and the average
// under All microphones and under anything that steers. With one microphone there is one path.
func TestThePathIsWhatBothSettingsMakeOfIt(t *testing.T) {
	steered := &reusedBuffer{}
	for _, tc := range []struct {
		name  string
		mixer Mixer
		onMix bool
		want  cancelPath
	}{
		{"center, off", Center{}, false, pathCenter},
		{"average, off", Average{}, false, pathCenter},
		{"steered, off", steered, false, pathCenter},
		{"center, on", Center{}, true, pathCenter},
		{"average, on", Average{}, true, pathAverage},
		{"steered, on", steered, true, pathAverage},
	} {
		want := tc.want
		if Mics == 1 {
			want = pathCenter
		}
		if got := cancelPathOf(tc.mixer, tc.onMix); got != want {
			t.Errorf("%s: %v, want %v", tc.name, got, want)
		}
	}
}

// With the switch off the canceller reads the center microphone, whatever the mix; on, it reads a
// fixed mix as it is, and the plain average in place of a steered one. It says which path that is.
func TestCancelInputFollowsTheSwitch(t *testing.T) {
	mics := micsApart()
	average := Average{}.Mix(mics)
	center := mics[CenterMic]
	if average[0] == center[0] {
		t.Fatalf("the test frames do not tell the average (%d) from the center microphone (%d)", average[0], center[0])
	}

	if got, path := cancelInput(Average{}, mics, average, false); &got[0] != &center[0] || path != pathCenter {
		t.Errorf("switch off, averaging: the canceller reads %d on %v, want the center microphone's %d", got[0], path, center[0])
	}
	if got, path := cancelInput(&reusedBuffer{}, mics, nil, false); &got[0] != &center[0] || path != pathCenter {
		t.Errorf("switch off, steering: the canceller reads %d on %v, want the center microphone's %d", got[0], path, center[0])
	}
	if got, path := cancelInput(Center{}, mics, center, true); &got[0] != &center[0] || path != pathCenter {
		t.Errorf("switch on, center: the canceller reads %d on %v, want the center microphone's %d", got[0], path, center[0])
	}
	if Mics == 1 {
		// One microphone: the average is that microphone, read as it is.
		if got, path := cancelInput(Average{}, mics, average, true); &got[0] != &center[0] || path != pathCenter {
			t.Errorf("one microphone, switch on: the canceller reads %d on %v, want the microphone itself", got[0], path)
		}
		return
	}
	if got, path := cancelInput(Average{}, mics, average, true); &got[0] != &average[0] || path != pathAverage {
		t.Errorf("switch on, averaging: the canceller does not read the mix itself (%v)", path)
	}
	steered := (&reusedBuffer{}).Mix(mics)
	if got, path := cancelInput(&reusedBuffer{}, mics, steered, true); got[0] != average[0] || path != pathAverage {
		t.Errorf("switch on, steering: the canceller reads %d on %v, want the average's %d", got[0], path, average[0])
	}
}

// trained runs the built-in filter over a tone with its echo, half as loud, until it has learned it:
// half a second, after which it takes more than a decibel off.
func trained(t *testing.T, c *canceller) *aec.Canceller {
	t.Helper()
	f := c.filter.Load()
	ref := make([]int16, FrameSamples)
	mic := make([]int16, FrameSamples)
	for nth := range 25 {
		for i := range ref {
			at := float64(nth*FrameSamples+i) / Rate
			v := int16(8000 * math.Sin(2*math.Pi*440*at))
			ref[i], mic[i] = v, v/2
		}
		if _, err := f.Process(mic, ref); err != nil {
			t.Fatal(err)
		}
	}
	if f.ERLE() < 1 {
		t.Fatalf("the filter learned nothing from the tone: %.1f dB", f.ERLE())
	}
	return f
}

// The canceller starts over when the path it reads moves, and only then: a frame from the path it
// already knows keeps what it learned.
func TestTheCancellerStartsOverWhenThePathMoves(t *testing.T) {
	c := newCanceller()
	if c == nil {
		t.Skip("no canceller in this build")
	}
	c.path = pathCenter

	f := trained(t, c)
	c.follow(pathCenter)
	if f.ERLE() < 1 {
		t.Error("the same path again threw away what the filter had learned")
	}

	c.follow(pathAverage)
	if c.path != pathAverage {
		t.Error("the canceller did not note the new path")
	}
	if f.ERLE() != 0 {
		t.Error("the path moved to the average and the filter kept what it learned on one microphone")
	}

	trained(t, c)
	c.follow(pathCenter)
	if f.ERLE() != 0 {
		t.Error("the path moved back and the filter kept what it learned on the average")
	}
}

// frameWithin is the next frame a listener is handed, or a failure if none comes: a broadcast that
// does not reach the listener fails the test rather than leaving it waiting.
func frameWithin(t *testing.T, frames <-chan []int16) {
	t.Helper()
	select {
	case <-frames:
	case <-time.After(time.Second):
		t.Fatal("no frame reached the listener")
	}
}

// The switch reaches the next frame with nothing restarted, and the canceller starts out on the path
// the settings name, so turning the device on is not taken for a change.
func TestTheSwitchNeedsNoRestart(t *testing.T) {
	s := testSource()
	if s.cancel == nil {
		t.Skip("no canceller in this build")
	}
	if want := cancelPathOf(s.mixer, s.cancelOnMix); s.cancel.path != want {
		t.Fatalf("the canceller starts on %v while the settings make it %v", s.cancel.path, want)
	}
	if Mics == 1 {
		t.Skip("one microphone: the switch never moves the path")
	}
	// All microphones, where the switch moves the path (under Center it does not).
	s.mixer = Average{}
	s.canceling = true

	frames, stop := s.Listen("switch")
	defer stop()

	want := !s.CancelOnMix()
	s.SetCancelOnMix(want)
	if s.CancelOnMix() != want {
		t.Fatalf("CancelOnMix = %v after setting %v", s.CancelOnMix(), want)
	}
	s.broadcast(rawFrame(0, true))
	frameWithin(t, frames)
	if got := cancelPathOf(Average{}, want); s.cancel.path != got {
		t.Errorf("after a frame the canceller reads %v, want %v", s.cancel.path, got)
	}

	// With canceling off the frame passes the canceller by, and the change waits for it.
	s.canceling = false
	s.SetCancelOnMix(!want)
	s.broadcast(rawFrame(1, true))
	frameWithin(t, frames)
	if got := cancelPathOf(Average{}, want); s.cancel.path != got {
		t.Error("the canceller moved its path while it was switched off")
	}
	s.canceling = true
	s.broadcast(rawFrame(2, true))
	frameWithin(t, frames)
	if got := cancelPathOf(Average{}, !want); s.cancel.path != got {
		t.Error("the canceller did not take up the path once it was switched back on")
	}
}

// A change of mixing moves the path as much as the switch does, and the canceller follows it on the
// next frame: with the switch on, Center to All microphones goes from one microphone to the average.
func TestAChangeOfMixingMovesThePathToo(t *testing.T) {
	s := testSource()
	if s.cancel == nil {
		t.Skip("no canceller in this build")
	}
	if Mics == 1 {
		t.Skip("one microphone: nothing moves the path")
	}
	s.canceling = true
	s.SetCancelOnMix(true)
	s.SetMixing(config.MixCenter)

	frames, stop := s.Listen("mixing")
	defer stop()

	s.broadcast(rawFrame(0, true))
	frameWithin(t, frames)
	if s.cancel.path != pathCenter {
		t.Fatalf("switch on under Center mixing: the canceller reads %v, want %v", s.cancel.path, pathCenter)
	}

	if got := s.SetMixing(config.MixAll); got != config.MixAll {
		t.Fatalf("All microphones settled on %v", got)
	}
	s.broadcast(rawFrame(1, true))
	frameWithin(t, frames)
	if s.cancel.path != pathAverage {
		t.Errorf("after the mixing moved to All microphones the canceller reads %v, want %v", s.cancel.path, pathAverage)
	}

	// The switch turned off from there goes back to the center microphone.
	s.SetCancelOnMix(false)
	s.broadcast(rawFrame(2, true))
	frameWithin(t, frames)
	if s.cancel.path != pathCenter {
		t.Errorf("switch off: the canceller reads %v, want %v", s.cancel.path, pathCenter)
	}
}
