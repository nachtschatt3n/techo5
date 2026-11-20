package mic

import (
	"log/slog"
	"math"
	"strconv"
	"sync/atomic"

	"github.com/HuskerMinion/techo5/echod/internal/lib/aec"
	"github.com/HuskerMinion/techo5/echod/internal/lib/audio"
)

// cancelTaps is how far the filter reaches, in samples, so 64 ms of tail. Not a setting: measured on
// this enclosure, cancellation rises with every tap available — 16.5 dB at 256, 18.6 at 512, 23.0 at
// 1024, 27.2 at 2048 — so there is no room-dependent best value to look for, only what the device can
// afford. This costs 6.4% of a core, and only while something is playing.
const cancelTaps = 1024

// cancelTapsJack is the filter's reach while the sound leaves by the headphone jack, 256 ms. A
// powered speaker on the jack sits across the room, not a few centimeters from the microphones, and
// its echo comes back late and spread out: measured on a Dot driving a portable speaker over AUX
// (2026-11-15, white noise, the seven microphones averaged), the path peaks at 22-33 ms and holds 90%
// of its energy by 88 ms and 99% by 185 ms. An ideal filter takes 7 dB off it at 1024 taps, 13 at
// 2048 and 19 at 4096, while a fixed delay in front of 1024 taps buys only 1.5 dB: the filter was too
// short, not too late. The capture replayed through this filter, six seconds of noise, gave 8.6, 14.5
// and 15.4 dB, the last still rising. On the device, replies and sounds had 6-8 dB taken off before,
// and a song 20.8 after. The filter's cost grows with its length, so this is about four times
// cancelTaps' 6.4% of a core, and only while the jack plays, on the builds where longJack is set.
const cancelTapsJack = 4096

// LongEchoPath reports whether playback currently leaves by a path whose echo needs cancelTapsJack:
// the speaker package sets it to "the output is the headphone jack". Nil, as in tests and tools that
// never start a speaker, keeps cancelTaps. Read when a run starts, never during one.
var LongEchoPath func() bool

// tapsWanted is the filter length for the run about to start.
func tapsWanted() int {
	if longJack && LongEchoPath != nil && LongEchoPath() {
		return cancelTapsJack
	}
	return cancelTaps
}

// cancelMu is how fast the filter adapts. It still takes 10 dB off the music within half a second of a
// reply, and keeps what it learned between replies, so the first second is not where it matters.
//
// Faster leaves less music between words but costs the voice over it. Measured on 127 wake word takes
// laid over a Dot's recorded music at 10 dB under its echo: 36 caught at 0.5, 72 at 0.1, 74 at 0.05,
// 66 at 0.02, with 2 dB more music left at 0.1 than at 0.5. Pausing the learning while somebody talks
// did not help (35 caught), which points at the fast learning itself bending the voice rather than at
// what it learns from it.
const cancelMu = 0.1

// refQuiet is the mean square per sample, at int16 scale, below which the loopback counts as silence.
// About -76 dBFS. Below it there is no echo to remove, so the filter is skipped entirely and the frame
// costs nothing — which is what keeps this free on an idle device. It has to sit well under a
// radio at a quiet volume: measured at -50 dBFS on the loopback, which a -60 dBFS gate flapped on
// every second, resetting the canceller each time.
const refQuiet = 1e-7 * 32768 * 32768 / 4

// refHold is how long the filter keeps running after the loopback goes quiet, in samples.
//
// Speech is full of gaps, and without this the gate flaps between every word. That is not only untidy:
// the room is still ringing with the echo of the word that just played, and the measured tail here runs
// past 100 ms, so disengaging on the gap stops canceling exactly as that tail arrives. A second
// covers it with room to spare, and rides across the pauses in a radio talk show.
const refHold = Rate

// canceller subtracts the playback loopback from one fixed acoustic path, and replaces the mix while it
// runs.
//
// The path it reads cannot move: the filter learns one path from the speaker, and the beamformer steers
// at the loudest sound, which during playback is our own speaker. Steering into the echo is the opposite
// of useful when the echo is what is being removed. A fixed combination of microphones is as fixed as one
// microphone, though, because the microphones do not move: on the Dot, the average of seven canceled
// once measured exactly as well as seven cancellers averaged, and 5 dB better than the center microphone
// alone. Which path it reads is cancelInput's choice.
type canceller struct {
	// filter is swapped by fitTaps from the capture loop while SetAdapting reaches it from elsewhere.
	filter atomic.Pointer[aec.Canceller]

	// adapting is what SetAdapting last asked for, so a filter built by fitTaps starts the same way.
	adapting atomic.Bool

	// ext is WebRTC's canceller in a helper process (webrtc.go), used instead of filter while it is
	// set and healthy. engine names which one the user asked for.
	ext    *external
	engine string

	// delayMs is how far the helper holds the microphone back behind the loopback (helperArgs). Set,
	// like engine, under Source.mu, which the capture loop holds too.
	delayMs int

	// linear is whether the helper is asked for its linear output as well, for the wake words
	// (SetWakeBeforeSuppression). Read when the helper starts.
	linear bool

	// ref and mono are reused every frame; decoding into them keeps the audio path free of allocation.
	// lin is the same for the linear output, and holds it only when the last apply had one: see
	// linearBlock.
	ref  []int16
	mono []int16
	lin  []int16

	// erle is published for diagnostics, as thousandths of a dB so it fits an integer. best is the most
	// it reached during the current run: ERLE is averaged over the last half second, so reading it as
	// playback ends catches the reference fading out and says the filter did worse than it did.
	erle atomic.Int64
	best atomic.Int64

	// linBest is best for the linear output, while there is one. Reader-only.
	linBest float64

	// active says whether the last frame had playback in it, which is when any of this happened.
	active atomic.Bool

	// hold counts down the samples still to filter after the loopback went quiet. Reader-only.
	hold int

	// learned counts the samples of the current run that the built-in filter learned from: playback in
	// the loopback while it was adapting. It goes into the idle line, to tell how much the filter learned
	// over a run. A run is everything played until a second of quiet, so music before the wake word is
	// in the same run as the reply after it, and was learned from outside the turn as it always is: only
	// with nothing playing before the turn does the count say the reply alone was learned over or not
	// (the turn logs that itself, feature/voice learnOverReply). Reader-only, like hold.
	learned int

	// path is the one the filter last read (cancelInput), so that follow can tell when it moved.
	// Reader-only.
	path cancelPath
}

// follow is told, before every frame it cancels, which path that frame comes from. When it is not the
// one the filter learned, the built-in filter forgets what it knew and learns again: the path from the
// speaker to one microphone is not the path to the average of them, and taps fitted to one are wrong
// for the other (aec.Canceller.Reset says the same). It is the path that counts, not either setting that
// chooses it: the switch moves it only where the mixing is not Center, and with the switch on a change of
// mixing moves it (Center to All microphones) or does not (All microphones to Beamformer, which is
// canceled on the average too). A filter starting from nothing cancels nothing, which is the device
// without a canceller, until it has learned the new path: about half a second of playback (cancelMu), or
// the end of the turn if the path moves while somebody is being listened to and the learning is held
// (SetAdapting). WebRTC's canceller is left as it is: it adapts to a changed echo path on its own, and
// restarting the helper would cost more than it relearns. The built-in filter starts over even while
// WebRTC's runs, so that it does not take up the old path if the helper fails.
func (c *canceller) follow(path cancelPath) {
	if c.path == path {
		return
	}
	was := c.path
	c.path = path
	c.filter.Load().Reset()
	slog.Info("echo cancellation input changed, the built-in filter starts over",
		"from", was.String(), "to", path.String(), "engine", c.engine)
}

func newCanceller() *canceller {
	f, err := aec.New(aec.Config{Taps: cancelTaps, Mu: cancelMu})
	if err != nil {
		// Only a bad Taps or Mu reaches this, and both are constants above.
		slog.Error("echo cancellation unavailable", "err", err)
		return nil
	}
	c := &canceller{engine: "builtin"}
	c.filter.Store(f)
	c.adapting.Store(f.Adapting())
	return c
}

// fitTaps rebuilds the filter at taps long when it is another length, keeping whether it learns. What
// it had learned goes: a new length comes with a new output, and the path it knew is not this one.
// Called only as a run starts, so no block is ever canceled by half a filter.
func (c *canceller) fitTaps(taps int) {
	old := c.filter.Load()
	if old.Taps() == taps {
		return
	}
	f, err := aec.New(aec.Config{Taps: taps, Mu: cancelMu})
	if err != nil {
		slog.Error("echo cancellation: keeping the filter as it is", "taps", taps, "err", err)
		return
	}
	f.SetAdapting(c.adapting.Load())
	c.filter.Store(f)
	f.SetAdapting(c.adapting.Load()) // a SetAdapting between the two lines above reached the old filter
	slog.Info("echo cancellation filter length", "from", old.Taps(), "to", taps)
}

// setEngine picks the canceller: "builtin" is the LMS filter above, "webrtc" the helper process.
// It reports what it settled on: webrtc falls back to builtin when the helper cannot start.
func (c *canceller) setEngine(name string) string {
	if c.ext != nil {
		c.ext.Close()
		c.ext = nil
	}
	c.engine = "builtin"
	if name != "webrtc" {
		c.logLinear()
		return c.engine
	}
	e, err := startWebRTC(c.linear, helperArgs(c.delayMs)...)
	if err != nil {
		slog.Warn("webrtc echo cancellation unavailable, using the built-in filter", "err", err)
		c.logLinear()
		return c.engine
	}
	c.ext, c.engine = e, "webrtc"
	c.logLinear()
	return c.engine
}

// helperArgs is how the helper is started: low noise suppression, and the microphone held back by
// delayMs behind the loopback. linearArg goes beside them when the wake words want the linear output
// (startWebRTC), which the delay holds back the same as the processed output.
//
// The loopback is sample aligned with the microphones, but the echo of what it carries reaches them
// only after the amplifier, the speaker and a few centimeters of air, which may be very little later.
// Offline, WebRTC's linear filter did not converge with the echo less than about 2 ms behind the
// reference, leaving the reply to the suppressor, which takes a voice talking over it along with the
// echo. Holding the microphone back gives the reference that lead; how short the lag really is on a
// device has not been measured, so 0 stays the default and the delay is a setting (Microphone echo
// canceller delay).
//
// The helper still answers each block with one block, so nothing here or downstream waits any
// longer; what it answers is only delayMs older. Everything that listens hears a voice over playback
// that much later, and the edges of a run, where the frame switches between the mix and the canceled
// path, show it: as a run starts, the first delayMs out of the helper is silence (it is only fed while
// something plays, and empties its line after a pause rather than hand over the end of the run
// before), and as a run ends, the last delayMs it held is never heard. At most 20 ms either way, a
// fraction of a syllable, and the half second the ERLE estimate in external.Process averages over
// does not notice the offset between what went in and what came out.
//
// At 0 the flag is left off, so the arguments are the ones the helper has always had, and a helper
// older than the setting still starts. An older helper given a delay refuses the flag and exits; the
// canceller then falls back to the built-in filter, and the log says the helper refused its delay
// (startExternal).
func helperArgs(delayMs int) []string {
	args := []string{"--ns", "low"}
	if delayMs > 0 {
		args = append(args, "--delay-ms", strconv.Itoa(delayMs))
	}
	return args
}

// setDelay changes how far the helper holds the microphone back. The helper takes it as it starts,
// so a running one is started again with it, and comes back having forgotten the room, to learn it
// again from the next thing that plays. The built-in filter has no use for it (see helperArgs).
func (c *canceller) setDelay(ms int) {
	ms = max(ms, 0)
	if ms == c.delayMs {
		return
	}
	c.delayMs = ms
	if c.ext != nil {
		slog.Info("echo cancellation helper restarting for its delay", "delay_ms", ms)
		c.setEngine("webrtc")
	}
}

// logLinear says what the wake words hear, when the switch asks for the linear output. It is said
// wherever a helper is started or given up, because that is where it is settled: the switch is put
// back before the canceller as the device starts, and calls change the canceller on their own, so the
// line from the switch alone would describe a canceller that is about to change.
func (c *canceller) logLinear() {
	if !c.linear {
		return
	}
	slog.Info("wake words before the suppressor", "on", true, "hearing", c.linearRunning(), "engine", c.engine)
}

// setLinear asks for the helper's linear output, or stops asking, and restarts a running helper so the
// change takes: the flag is one of its arguments. It reports whether the helper now gives it, which
// needs the WebRTC canceller and a helper that has it.
func (c *canceller) setLinear(on bool) bool {
	if c.linear == on {
		return c.linearRunning()
	}
	c.linear = on
	if c.engine == "webrtc" {
		c.setEngine("webrtc")
	}
	return c.linearRunning()
}

// linearRunning is whether the running helper gives the linear output.
func (c *canceller) linearRunning() bool { return c.ext != nil && c.ext.linear }

// process runs one block through whichever engine is on. A helper that has died is dropped for
// the built-in filter, once, with a log line.
//
// One that died giving the linear output is first started once more without it. The linear output
// comes from building AEC3 from a header the library does not install (techo5-aec.cpp), so a helper
// that runs well without it and falls over with it is the failure this setting brings, and it should
// cost the wake words their extra rather than the device its canceller. The switch stays on, so the
// next time the canceller is chosen or the switch turned, the linear output is tried again.
func (c *canceller) process(mic, ref []int16) ([]int16, float64, error) {
	if c.ext != nil {
		if c.ext.Healthy() {
			out, err := c.ext.Process(mic, ref)
			if err == nil {
				return out, c.ext.ERLE(), nil
			}
			slog.Warn("webrtc echo cancellation failed", "err", err, "linear", c.ext.linear)
		}
		wasLinear := c.ext.linear
		c.ext.Close()
		c.ext = nil
		c.engine = "builtin"
		if wasLinear {
			if out, erle, ok := c.restartPlain(mic, ref); ok {
				return out, erle, nil
			}
		}
		slog.Warn("webrtc echo cancellation dropped, using the built-in filter")
		c.logLinear()
	}
	f := c.filter.Load()
	out, err := f.Process(mic, ref)
	if err != nil {
		return nil, 0, err
	}
	return out, f.ERLE(), nil
}

// restartPlain starts the helper without the linear output, after one with it failed, and runs the block
// through it. ok is false when that fails too; nothing is left running then.
func (c *canceller) restartPlain(mic, ref []int16) (out []int16, erle float64, ok bool) {
	e, err := startExternal(helperArgs(c.delayMs)...)
	if err != nil {
		slog.Warn("webrtc echo cancellation could not be started again without the linear output", "err", err)
		return nil, 0, false
	}
	out, err = e.Process(mic, ref)
	if err != nil {
		e.Close()
		slog.Warn("webrtc echo cancellation failed without the linear output too", "err", err)
		return nil, 0, false
	}
	c.ext, c.engine = e, "webrtc"
	slog.Warn("webrtc echo cancellation started again without the linear output; the wake words hear what everything else hears")
	c.logLinear()
	return out, e.ERLE(), true
}

// apply returns mic, one fixed path (see cancelInput), with the echo removed, or nil when there is
// nothing playing and the caller should use the mix it already has.
func (c *canceller) apply(raw []byte, mic []int16) []int16 {
	// No linear output from this block until the helper has given one for it.
	c.lin = c.lin[:0]
	n := len(mic)
	if cap(c.ref) < n {
		c.ref = make([]int16, n)
	}
	c.ref = c.ref[:n]
	referenceInto(raw, c.ref)

	live := playing(c.ref)
	switch {
	case live:
		c.hold = refHold
	case c.hold > 0:
		// Still inside the tail of what just played.
		c.hold -= n
	default:
		// Nothing to cancel. The filter keeps what it learned: the room has not changed just because
		// the reply ended, so the next one starts converged rather than from nothing.
		if c.active.Swap(false) {
			attrs := []any{"best_db", float64(c.best.Load()) / 1000, "last_db", float64(c.erle.Load()) / 1000}
			if c.engine == "builtin" {
				// WebRTC's canceller decides for itself when to learn, whatever SetAdapting says.
				attrs = append(attrs, "learned_s", math.Round(float64(c.learned)/Rate*10)/10)
			}
			if c.linearRunning() {
				// What the linear filter alone took off: low here, with best_db high, is the suppressor
				// doing the work, which is the case the linear output for the wake words is about.
				attrs = append(attrs, "linear_best_db", c.linBest, "linear_last_db", c.ext.LinearERLE())
			}
			slog.Info("echo cancellation idle", attrs...)
			c.erle.Store(0)
			c.best.Store(0)
			c.learned = 0
			c.linBest = 0
		}
		return nil
	}

	if !c.active.Swap(true) {
		c.fitTaps(tapsWanted())
		attrs := []any{"engine", c.engine, "ref_dbfs", blockDBFS(c.ref), "mic_dbfs", blockDBFS(mic)}
		if c.engine == "builtin" {
			attrs = append(attrs, "taps", c.filter.Load().Taps()) // WebRTC's canceller has its own
		} else if c.delayMs > 0 {
			attrs = append(attrs, "delay_ms", c.delayMs)
		}
		slog.Info("echo cancellation running", attrs...)
	}

	out, erleDB, err := c.process(mic, c.ref)
	if err != nil {
		slog.Error("echo cancellation failed", "err", err)
		return nil
	}
	erle := int64(erleDB * 1000)
	c.erle.Store(erle)
	if erle > c.best.Load() {
		c.best.Store(erle)
	}
	// Only the blocks with playback in them: in the tail after it the reference is silent, and the filter
	// learns nothing there whatever it was told. Counted by the block against refQuiet, which sits under
	// the filter's own idea of silence, so very quiet playback counts a little the filter skipped: close
	// enough to tell a run learned over from one sat through frozen.
	if live && c.engine == "builtin" && c.adapting.Load() {
		c.learned += n
	}

	// The engines hand back a buffer of their own that the next block overwrites, and the caller runs
	// the denoiser and the leveler over what this returns, so the block is moved somewhere they may
	// write on. This buffer is reused as well: what apply returns is only good until the next call,
	// and broadcast copies it before any listener keeps it.
	if cap(c.mono) < len(out) {
		c.mono = make([]int16, len(out))
	}
	c.mono = c.mono[:len(out)]
	copy(c.mono, out)

	// The helper's linear output for the same block, when it gives one and this block came from it
	// rather than from the built-in filter it falls back to. Copied for the same reason.
	if c.ext != nil && c.engine == "webrtc" {
		if lin := c.ext.Linear(); len(lin) == len(out) {
			c.lin = append(c.lin[:0], lin...)
			c.linBest = max(c.linBest, c.ext.LinearERLE())
		}
	}
	return c.mono
}

// linearBlock is the linear canceller's output for the block apply last returned, or nil when it had
// none: nothing playing, the built-in filter, or a helper not asked for it. Good until the next apply.
func (c *canceller) linearBlock() []int16 {
	if len(c.lin) == 0 {
		return nil
	}
	return c.lin
}

// blockDBFS is the RMS of a block in dBFS, rounded, for the log.
func blockDBFS(s []int16) float64 {
	if len(s) == 0 {
		return -120
	}
	var sum float64
	for _, v := range s {
		sum += float64(v) * float64(v)
	}
	rms := math.Sqrt(sum / float64(len(s)))
	if rms < 1 {
		return -120
	}
	return math.Round(20*math.Log10(rms/32768)*10) / 10
}

// playing reports whether the loopback carries anything.
func playing(ref []int16) bool {
	var sum float64
	for _, v := range ref {
		sum += float64(v) * float64(v)
	}
	return len(ref) > 0 && sum/float64(len(ref)) > refQuiet
}

// referenceInto decodes ch7, the left half of the playback loopback, into dst. ch8 is left alone: on
// music the two measure within 12-17 dB of each other, which caps cancellation far above anything the
// filter reaches, so a stereo reference would buy nothing.
func referenceInto(raw []byte, dst []int16) {
	frameBytes := Channels * Bits / 8

	frames := min(len(raw)/frameBytes, len(dst))
	for f := range frames {
		o := f*frameBytes + RefFirst*3
		dst[f] = int16(audio.DecodeS24LE3(raw[o:o+3]) >> 8)
	}
	for f := frames; f < len(dst); f++ {
		dst[f] = 0
	}
}

// Canceling reports whether the canceller is currently running, which it does only while the loopback
// is carrying audio.
func (s *Source) Canceling() bool {
	return s.cancel != nil && s.cancel.active.Load()
}

// ERLE is how much echo the canceller is removing, in dB, or zero when it is not running.
func (s *Source) ERLE() float64 {
	if s.cancel == nil {
		return 0
	}
	return float64(s.cancel.erle.Load()) / 1000
}

// SetCanceling turns echo cancellation on or off. Off is the same signal path the device had before it
// existed, which is what makes it worth having as a switch: it is the comparison.
func (s *Source) SetCanceling(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.canceling = on
}

// SetCancelOnMix chooses which microphones the echo canceller reads while something plays: the mix
// (with a steered mix, the plain average) or the center microphone alone. It takes effect on the next
// frame, with no restart: the canceller notices there when its path moved and starts over (follow).
// Which one is better is the board's to measure (DefaultCancelOnMix), and this is how to measure it.
func (s *Source) SetCancelOnMix(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelOnMix = on
}

// CancelOnMix reports whether the echo canceller reads the mix rather than the center microphone.
func (s *Source) CancelOnMix() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelOnMix
}

// SetCancelEngine chooses which canceller runs — "builtin" or "webrtc" — and reports what it
// settled on. Serialized with the capture loop, since the engine is used from there.
func (s *Source) SetCancelEngine(name string) string {
	if s.cancel == nil {
		return "builtin"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancel.setEngine(name)
}

// SetCancelDelay sets how far WebRTC's canceller holds the microphone back behind the loopback, in
// milliseconds, starting the helper again when it is running. Serialized with the capture loop, as
// SetCancelEngine is. The built-in filter does not read it.
func (s *Source) SetCancelDelay(ms int) {
	if s.cancel == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel.setDelay(ms)
}

// CancelDelay is how far WebRTC's canceller holds the microphone back, in milliseconds, as last set.
func (s *Source) CancelDelay() int {
	if s.cancel == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancel.delayMs
}

// SetWakeBeforeSuppression has the wake words hear WebRTC's canceller before its suppressor, or not,
// and reports whether they now do: only with the WebRTC canceller chosen and a helper that gives its
// linear output (broadcastSelected says what they hear and why). A running helper is restarted for it,
// which costs the canceller what it had learned of the room; the built-in filter is not touched.
func (s *Source) SetWakeBeforeSuppression(on bool) bool {
	if s.cancel == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	was := s.cancel.engine
	got := s.cancel.setLinear(on)
	switch {
	case !on:
		slog.Info("wake words before the suppressor", "on", false, "hearing", got, "engine", s.cancel.engine)
	case was != "webrtc":
		// Nothing started, so setEngine said nothing: this is the switch put back as the device starts,
		// before the canceller is, or turned on with the built-in one chosen. The line saying what the
		// wake words hear comes when a WebRTC helper starts.
		slog.Info("wake words before the suppressor, once WebRTC's canceller runs", "on", true, "engine", s.cancel.engine)
	}
	return got
}

// CancelEngine is which canceller is running.
func (s *Source) CancelEngine() string {
	if s.cancel == nil {
		return "builtin"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancel.engine
}

// SetAdapting stops or resumes the canceller learning, while it goes on canceling with what it has.
//
// Turn it off while somebody is being listened to. The filter cannot tell a voice it was never given a
// reference for from an echo it predicted badly, so it treats the voice as its own error and fits itself
// to it — at exactly the moment canceling matters. What it already learned stays correct: the room did
// not change because somebody spoke.
//
// Only the built-in filter is told. WebRTC's canceller decides for itself when to learn, and its helper
// is never asked to stop.
func (s *Source) SetAdapting(on bool) {
	if s.cancel != nil {
		s.cancel.adapting.Store(on)
		s.cancel.filter.Load().SetAdapting(on)
	}
}
