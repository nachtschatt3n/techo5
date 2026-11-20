package mic

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/asp"
)

// The wake words hearing WebRTC's canceller before its suppressor (Wake words hear the canceller before
// suppression). The helper here is a stand-in run from this test binary (TestFakeAECHelper), so what it
// hands back is known: the processed block is the microphone negated and the linear one the microphone
// doubled, which no canceller would do but which makes it plain which of the two a listener was given.

// fakeAECEnv says how the stand-in helper behaves: "linear" understands --linear, "old" is a helper from
// before it, which exits on the argument it does not know, "mute" is one that hangs before its hello
// with --linear, and "dies" one that exits after two blocks with --linear. Without --linear, all of them
// run as a helper does.
const fakeAECEnv = "TECHO5_FAKE_AEC"

// fakeAECStarts, when set, is a file the stand-in adds a line to each time it starts: its arguments.
const fakeAECStarts = "TECHO5_FAKE_AEC_STARTS"

// TestFakeAECHelper is the stand-in helper when this binary is started as one (useFakeAEC), and does
// nothing as a test.
func TestFakeAECHelper(t *testing.T) {
	mode := os.Getenv(fakeAECEnv)
	if mode == "" {
		t.Skip("only runs as the stand-in for techo5-aec")
	}
	var args []string
	if i := slices.Index(os.Args, "--"); i >= 0 {
		args = os.Args[i+1:]
	}
	os.Exit(fakeAEC(mode, args))
}

func fakeAEC(mode string, args []string) int {
	if path := os.Getenv(fakeAECStarts); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return 1
		}
		fmt.Fprintln(f, strings.Join(args, " "))
		f.Close()
	}
	linear := slices.Contains(args, linearArg)
	if linear && mode == "old" {
		fmt.Fprintln(os.Stderr, "techo5-aec: unknown argument", linearArg)
		return 2
	}
	if linear && mode == "mute" {
		time.Sleep(time.Hour)
		return 1
	}
	if linear {
		if _, err := os.Stdout.WriteString(linearHello); err != nil {
			return 1
		}
	}
	n := FrameSamples
	in := make([]byte, 4*n)
	out := make([]byte, 4*n)
	for blocks := 0; ; blocks++ {
		if linear && mode == "dies" && blocks == 2 {
			return 1
		}
		if _, err := io.ReadFull(os.Stdin, in); err != nil {
			return 0
		}
		for i := range n {
			v := int16(binary.LittleEndian.Uint16(in[2*i:]))
			binary.LittleEndian.PutUint16(out[2*i:], uint16(-v))
			binary.LittleEndian.PutUint16(out[2*n+2*i:], uint16(2*v))
		}
		back := out[:2*n]
		if linear {
			back = out
		}
		if _, err := os.Stdout.Write(back); err != nil {
			return 1
		}
	}
}

// useFakeAEC points the helper lookup at a script that starts this test binary as the stand-in, in
// the given mode, for the length of the test.
func useFakeAEC(t *testing.T, mode string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Skip("cannot find this test binary:", err)
	}
	script := filepath.Join(t.TempDir(), WebRTCHelper)
	body := fmt.Sprintf("#!/bin/sh\nexec %q -test.run='^TestFakeAECHelper$' -- \"$@\"\n", self)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeAECEnv, mode)
	// The stand-in is this whole test binary, which can take a while to start on a busy machine: a
	// hello missed for that would be read as a helper without the flag.
	was, wasHello := findHelper, helloStall
	findHelper, helloStall = func() string { return script }, time.Minute
	t.Cleanup(func() { findHelper, helloStall = was, wasHello })
}

// filled is a block of n samples all v.
func filled(n int, v int16) []int16 {
	b := make([]int16, n)
	for i := range b {
		b[i] = v
	}
	return b
}

func TestLinearHelperGivesBothBlocks(t *testing.T) {
	useFakeAEC(t, "linear")
	e, err := startWebRTC(true, "--ns", "low")
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if !e.linear {
		t.Fatal("started with the linear output asked for and understood, but not using it")
	}
	for range 3 {
		out, err := e.Process(filled(FrameSamples, 100), filled(FrameSamples, 7))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(out, filled(FrameSamples, -100)) {
			t.Fatalf("processed block %v..., want the helper's first block", out[:4])
		}
		if lin := e.Linear(); !slices.Equal(lin, filled(FrameSamples, 200)) {
			t.Fatalf("linear block %v..., want the helper's second block", lin[:min(4, len(lin))])
		}
	}
}

// A helper from before the flag exits on it. The canceller keeps WebRTC, started again without it,
// rather than falling back to the built-in filter over an extra.
func TestHelperWithoutLinearIsStartedAgainWithout(t *testing.T) {
	useFakeAEC(t, "old")
	e, err := startWebRTC(true, "--ns", "low")
	if err != nil {
		t.Fatalf("no helper at all: %v", err)
	}
	defer e.Close()
	if e.linear {
		t.Fatal("takes a helper that refused the linear output for one that gives it")
	}
	out, err := e.Process(filled(FrameSamples, 100), filled(FrameSamples, 7))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out, filled(FrameSamples, -100)) {
		t.Fatalf("processed block %v...", out[:4])
	}
	if e.Linear() != nil {
		t.Error("a linear block from a helper that gives none")
	}
}

// webrtcSource is testSource with the stand-in helper as its canceller, asked for the linear output or
// not, and canceling on.
func webrtcSource(t *testing.T, linear bool) *Source {
	t.Helper()
	return webrtcSourceAs(t, "linear", linear)
}

// webrtcSourceAs is webrtcSource with the stand-in in another mode.
func webrtcSourceAs(t *testing.T, mode string, linear bool) *Source {
	t.Helper()
	useFakeAEC(t, mode)
	s := testSource()
	if s.cancel == nil {
		t.Skip("no canceller in this build")
	}
	s.canceling = true
	if got := s.SetWakeBeforeSuppression(linear); got {
		t.Fatal("the wake words hear the linear output with the built-in filter, which has none")
	}
	if got := s.SetCancelEngine("webrtc"); got != "webrtc" {
		t.Fatalf("engine %q, want webrtc", got)
	}
	t.Cleanup(func() { s.SetCancelEngine("builtin") })
	return s
}

// all reports whether every sample of a frame is v.
func all(frame []int16, v int16) bool {
	if len(frame) == 0 {
		return false
	}
	for _, s := range frame {
		if s != v {
			return false
		}
	}
	return true
}

// With the switch on, the wake engine is handed the linear output while the canceller runs, and every
// other listener, and the history, keeps the one channel.
func TestOnlyTheWakeListenerHearsTheLinearOutput(t *testing.T) {
	s := webrtcSource(t, true)
	if !s.SetWakeBeforeSuppression(true) {
		t.Fatal("the helper gives the linear output, but the wake words do not hear it")
	}
	wake, stopWake := s.ListenWake("wake")
	defer stopWake()
	turn, stopTurn := s.Listen("turn")
	defer stopTurn()

	for i := range behindFrames {
		s.broadcast(rawFrame(i, true))
	}
	if !s.Canceling() {
		t.Fatal("the canceller did not engage")
	}
	for i := range behindFrames {
		mic := int16(i + 1) // rawFrame's microphones carry the frame's number
		if f := <-wake; !all(f, 2*mic) {
			t.Fatalf("frame %d: the wake listener got %v..., want the linear block (%d)", i, f[:4], 2*mic)
		}
		if f := <-turn; !all(f, -mic) {
			t.Fatalf("frame %d: another listener got %v..., want the processed block (%d)", i, f[:4], -mic)
		}
	}
	if h := s.Recent(20 * time.Millisecond); !all(h, -int16(behindFrames)) {
		t.Errorf("the history holds %v..., want the processed block", h[:4])
	}

	// Nothing playing: the canceller stands aside after its hold, and the wake words hear the mix,
	// as everything else does.
	for i := range Rate/FrameSamples + 2 {
		s.broadcast(rawFrame(i, false))
		<-turn
		<-wake
	}
	s.broadcast(rawFrame(0, false))
	if a, b := <-wake, <-turn; !slices.Equal(a, b) {
		t.Errorf("with nothing playing the wake listener got %v..., the others %v...", a[:4], b[:4])
	}
}

// With the switch off, the helper is not asked for the linear output and the wake words hear the
// processed block like everything else: the behavior from before the setting.
func TestSwitchOffKeepsTheOneChannel(t *testing.T) {
	s := webrtcSource(t, false)
	wake, stopWake := s.ListenWake("wake")
	defer stopWake()
	turn, stopTurn := s.Listen("turn")
	defer stopTurn()

	s.broadcast(rawFrame(0, true))
	if a, b := <-wake, <-turn; !all(a, -1) || !slices.Equal(a, b) {
		t.Errorf("the wake listener got %v..., the others %v...: want the same processed block", a[:4], b[:4])
	}
}

// Turning the switch on and off restarts a running helper with and without the flag, so it takes at
// once; with the built-in canceller it only remembers what was asked for the next helper.
func TestTheSwitchRestartsTheHelper(t *testing.T) {
	s := webrtcSource(t, false)
	if s.cancel.linearRunning() {
		t.Fatal("asked for the linear output with the switch off")
	}
	if !s.SetWakeBeforeSuppression(true) || !s.cancel.linearRunning() {
		t.Fatal("turned on, the helper was not restarted with the linear output")
	}
	if s.SetWakeBeforeSuppression(false) || s.cancel.linearRunning() {
		t.Fatal("turned off, the helper still gives the linear output")
	}

	s.SetCancelEngine("builtin")
	if s.SetWakeBeforeSuppression(true) {
		t.Fatal("the built-in filter reported as giving the linear output")
	}
	if s.cancel.ext != nil {
		t.Fatal("a helper started for the switch while the built-in filter is chosen")
	}
	if s.SetCancelEngine("webrtc"); !s.cancel.linearRunning() {
		t.Error("WebRTC chosen with the switch on, but its helper was started without the linear output")
	}
}

// An external processor's output is what every listener gets, the wake words included: it replaces
// the channel the linear output is an exception to.
func TestExternalProcessorOutputGoesToTheWakeWordsToo(t *testing.T) {
	useFakeAEC(t, "linear")
	p := aspTestFake(1642)
	s := aspTestSource(t, func(context.Context, asp.Descriptor) (aspProcess, error) { return p, nil })
	if s.cancel == nil {
		t.Skip("no canceller in this build")
	}
	s.canceling = true
	s.SetWakeBeforeSuppression(true)
	s.SetCancelEngine("webrtc")
	t.Cleanup(func() { s.SetCancelEngine("builtin") })

	wake, stop := s.ListenWake("wake")
	defer stop()
	s.captureNative(aspTestBlock())
	aspTestAwait(t, func() bool { return aspTestReady(s) })
	r := s.captureNative(aspTestBlock())
	s.broadcastSelected(rawFrame(0, true), false, r)
	if f := <-wake; !all(f, 1642) {
		t.Errorf("the wake listener got %v..., want the external processor's output", f[:4])
	}
}

// The linear output gets the gain the leveler just gave the frame, capped only by its own loudest
// sample, and the leveler learns nothing from it.
func TestApplyLastUsesTheGainAndLearnsNothing(t *testing.T) {
	l := newLeveler()
	speech := make([]int16, FrameSamples)
	for i := range speech {
		speech[i] = int16(600 * math.Sin(2*math.Pi*300*float64(i)/Rate))
	}
	for range 50 {
		l.apply(slices.Clone(speech))
	}
	gain, floor, published, clipped := l.gain, l.floor, l.published.Load(), l.clipped.Load()

	quiet := filled(FrameSamples, 10)
	l.applyLast(quiet)
	if want := int16(float32(10) * gain); quiet[0] != want {
		t.Errorf("scaled to %d, want %d at the leveler's gain of %.1f", quiet[0], want, gain)
	}
	loud := filled(FrameSamples, 20000)
	l.applyLast(loud)
	if limit := int16(l.headroom); loud[0] > limit {
		t.Errorf("a loud block went to %d, over the headroom %d", loud[0], limit)
	}
	if l.gain != gain || l.floor != floor || l.published.Load() != published || l.clipped.Load() != clipped {
		t.Error("the leveler learned from the linear output")
	}
}

// A helper started with --linear that never says it understood (one that is wedged, or far too slow) is
// killed after helloStall and started again without the flag, so WebRTC stays on.
func TestHelperSilentOnTheLinearOutputIsStartedAgainWithout(t *testing.T) {
	useFakeAEC(t, "mute")
	// Long enough for the stand-in, this whole test binary, to start on a busy machine before its first
	// block is asked for, short enough not to hold the test up.
	helloStall = 2 * time.Second
	began := time.Now()
	e, err := startWebRTC(true, "--ns", "low")
	if err != nil {
		t.Fatalf("no helper at all: %v", err)
	}
	defer e.Close()
	if took := time.Since(began); took < helloStall {
		t.Errorf("gave up on the hello after %v, before helloStall (%v)", took, helloStall)
	}
	if e.linear {
		t.Fatal("takes a helper that never sent its hello for one that gives the linear output")
	}
	out, err := e.Process(filled(FrameSamples, 100), filled(FrameSamples, 7))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out, filled(FrameSamples, -100)) {
		t.Fatalf("processed block %v...", out[:4])
	}
}

// A helper that fails while it gives the linear output is started once more without it: the switch
// costs the wake words their extra, not the device its WebRTC canceller.
func TestHelperFailingWithTheLinearOutputKeepsWebRTC(t *testing.T) {
	s := webrtcSourceAs(t, "dies", true)
	if !s.SetWakeBeforeSuppression(true) {
		t.Fatal("the helper gives the linear output, but the wake words do not hear it")
	}
	wake, stopWake := s.ListenWake("wake")
	defer stopWake()
	turn, stopTurn := s.Listen("turn")
	defer stopTurn()

	for i := range 2 {
		s.broadcast(rawFrame(i, true))
		if f := <-wake; !all(f, 2*int16(i+1)) {
			t.Fatalf("frame %d: the wake listener got %v..., want the linear block", i, f[:4])
		}
		<-turn
	}
	// The stand-in has exited: this block finds it gone.
	s.broadcast(rawFrame(2, true))
	if got := s.CancelEngine(); got != "webrtc" {
		t.Fatalf("engine %q after the helper with the linear output failed, want webrtc", got)
	}
	if s.cancel.linearRunning() {
		t.Fatal("started again with the linear output that failed")
	}
	if a, b := <-wake, <-turn; !all(b, -3) || !slices.Equal(a, b) {
		t.Errorf("the wake listener got %v..., the others %v...: want the same processed block", a[:4], b[:4])
	}
}

// A call holds the wake words on the one channel (HoldWakeLinear): the hang-up word is judged against
// how much the whole canceller takes off, so it has to hear what that describes. Let go, they hear the
// linear output again.
func TestHoldWakeLinearKeepsTheOneChannel(t *testing.T) {
	s := webrtcSource(t, true)
	if !s.SetWakeBeforeSuppression(true) {
		t.Fatal("the helper gives the linear output, but the wake words do not hear it")
	}
	wake, stopWake := s.ListenWake("wake")
	defer stopWake()
	turn, stopTurn := s.Listen("turn")
	defer stopTurn()

	release := s.HoldWakeLinear()
	other := s.HoldWakeLinear()
	s.broadcast(rawFrame(0, true))
	if a, b := <-wake, <-turn; !all(a, -1) || !slices.Equal(a, b) {
		t.Errorf("held, the wake listener got %v..., the others %v...: want the same processed block", a[:4], b[:4])
	}
	release()
	release() // a second call does not let go of the other hold
	s.broadcast(rawFrame(1, true))
	if a := <-wake; !all(a, -2) {
		t.Errorf("one hold left, the wake listener got %v..., want the processed block", a[:4])
	}
	<-turn
	other()
	s.broadcast(rawFrame(2, true))
	if a := <-wake; !all(a, 6) {
		t.Errorf("let go, the wake listener got %v..., want the linear block", a[:4])
	}
	<-turn
}

// The broadcast tests above run with leveling and noise reduction off. With both on, as a device has
// them, the wake listener gets the linear block at the gain the leveler just gave the frame and not
// through the noise reduction, and the others and the history get the frame through both.
func TestLinearOutputIsLeveledWithTheFrameAndNotDenoised(t *testing.T) {
	s := webrtcSource(t, true)
	if !s.SetWakeBeforeSuppression(true) {
		t.Fatal("the helper gives the linear output, but the wake words do not hear it")
	}
	s.leveling.Store(true)
	s.denoising.Store(true)
	wake, stopWake := s.ListenWake("wake")
	defer stopWake()
	turn, stopTurn := s.Listen("turn")
	defer stopTurn()

	for i := range behindFrames {
		s.broadcast(rawFrame(i, true))
		got, other := <-wake, <-turn
		if s.leveler.gain == 1 {
			t.Fatal("the leveler's gain is 1, which cannot show whether the linear block got it")
		}
		// applyLast learns nothing, so asking it here is what the broadcast did.
		want := filled(FrameSamples, 2*int16(i+1))
		s.leveler.applyLast(want)
		if !slices.Equal(got, want) {
			t.Fatalf("frame %d: the wake listener got %v..., want the linear block at the frame's gain %v...",
				i, got[:4], want[:4])
		}
		if h := s.Recent(20 * time.Millisecond); !slices.Equal(h, other) {
			t.Fatalf("frame %d: the history holds %v..., the other listener got %v...", i, h[:4], other[:4])
		}
		if slices.Equal(got, other) {
			t.Fatalf("frame %d: the wake listener got the other listeners' frame", i)
		}
	}
}

// The switch is put back before the canceller as the device starts (feature/microphone Restore), so a
// WebRTC helper restored with it is started once, already with the flag, rather than started plain and
// then again.
func TestSwitchBeforeTheEngineStartsTheHelperOnce(t *testing.T) {
	useFakeAEC(t, "linear")
	starts := filepath.Join(t.TempDir(), "starts")
	t.Setenv(fakeAECStarts, starts)
	s := testSource()
	if s.cancel == nil {
		t.Skip("no canceller in this build")
	}
	s.SetWakeBeforeSuppression(true)
	if got := s.SetCancelEngine("webrtc"); got != "webrtc" {
		t.Fatalf("engine %q, want webrtc", got)
	}
	t.Cleanup(func() { s.SetCancelEngine("builtin") })

	b, err := os.ReadFile(starts)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], linearArg) {
		t.Errorf("the helper was started %d times, with %q; want once, with %s", len(lines), lines, linearArg)
	}
}
