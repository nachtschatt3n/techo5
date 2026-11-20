package mic

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// WebRTCHelper is the helper that runs WebRTC's echo canceller (tools/aec/techo5-aec.cpp); the
// daemon feeds it blocks over pipes. Looked up on PATH and next to the daemon.
const WebRTCHelper = "techo5-aec"

// helperStall is how long a block may take before the helper is taken for hung. A block is 20 ms of
// audio and normally comes back in a few; a second is far past anything a busy CPU explains.
const helperStall = time.Second

// linearArg asks the helper for the linear canceller's output beside the processed one, and
// linearHello is what a helper that understood it writes before anything else (techo5-aec.cpp).
const (
	linearArg   = "--linear"
	linearHello = "T5LN"
)

// external is WebRTC's canceller in a helper process. One block of microphone and one of loopback
// go in; one block comes out, in order, so Process blocks for as long as the helper takes. Started
// with linearArg, two come out: the processed block, then the same 20 ms from the linear filter alone.
//
// A helper that dies or misbehaves marks itself broken and the canceller falls back to the
// built-in filter; the next SetEngine starts it again.
type external struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	buf    []byte
	broken atomic.Bool

	// linear says the helper writes the linear output too, and lin is where the last block of it is,
	// overwritten by the next Process.
	linear bool
	lin    []int16

	// erle is estimated here from what went in and what came out, averaged over about half a second.
	// linErle is the same for the linear output: how much the filter alone takes off, which is the
	// part that says whether it has learned the path, where erle has the suppressor's share in it too.
	erle    atomic.Int64 // thousandths of a dB
	linErle atomic.Int64
	inE     float64
	outE    float64
	linE    float64
}

// helperPath is where the helper is: on PATH, else next to the daemon; empty when the image has none.
func helperPath() string {
	if path, err := exec.LookPath(WebRTCHelper); err == nil {
		return path
	}
	if exe, err := os.Executable(); err == nil {
		path := filepath.Join(filepath.Dir(exe), WebRTCHelper)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// HaveWebRTC is whether this image carries the helper, so WebRTC's canceller is offered only where it
// can run (the Dot's image has none).
func HaveWebRTC() bool { return helperPath() != "" }

// findHelper is where startExternal looks for the helper: helperPath, except in tests.
var findHelper = helperPath

// startExternal starts the helper with args. With linearArg among them it waits for the helper to say
// it understood, and fails when it does not: a helper from before the flag, or one built without what
// it needs, exits on it instead (startWebRTC copes with that).
func startExternal(args ...string) (*external, error) {
	path := findHelper()
	if path == "" {
		return nil, fmt.Errorf("%s: not found", WebRTCHelper)
	}
	cmd := exec.Command(path, args...)
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	e := &external{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 4*FrameSamples*2),
		linear: slices.Contains(args, linearArg)}
	go func() {
		err := cmd.Wait()
		e.broken.Store(true)
		slog.Warn("echo cancellation helper exited", "err", err)
	}()
	if e.linear {
		if err := e.hello(); err != nil {
			e.Close()
			return nil, err
		}
	}
	slog.Info("echo cancellation helper started", "path", path, "args", args)
	return e, nil
}

// helloStall is how long a helper started with linearArg has to say it understood. It says so as soon
// as it has built its canceller, which takes milliseconds; without the hello the first block would have
// waited for the same thing, so this wait is not a new one, only an earlier one. A helper that has not
// said it by then is taken for one without the flag, and started again without it. Longer than
// helperStall because it includes starting the process, on a device that may be busy booting.
var helloStall = 3 * time.Second

// hello reads what a helper started with linearArg writes first.
func (e *external) hello() error {
	stall := time.AfterFunc(helloStall, func() { _ = e.cmd.Process.Kill() })
	defer stall.Stop()
	got := make([]byte, len(linearHello))
	if _, err := io.ReadFull(e.out, got); err != nil {
		return fmt.Errorf("%s gave no linear output: %w", WebRTCHelper, err)
	}
	if string(got) != linearHello {
		return fmt.Errorf("%s gave no linear output: it began with %q", WebRTCHelper, got)
	}
	return nil
}

// startWebRTC starts the helper with args, and with linearArg as well when linear is set. A helper that
// will not give the linear output is started again without it, so the wake words go on hearing what
// everything else hears rather than the device losing WebRTC's canceller over an extra.
func startWebRTC(linear bool, args ...string) (*external, error) {
	if !linear {
		return startExternal(args...)
	}
	e, err := startExternal(append(slices.Clone(args), linearArg)...)
	if err == nil {
		return e, nil
	}
	slog.Warn("echo cancellation helper has no linear output; the wake words hear what everything else hears",
		"err", err)
	return startExternal(args...)
}

// Process runs one block through the helper. len(mic) must equal len(ref).
func (e *external) Process(mic, ref []int16) ([]int16, error) {
	if e.broken.Load() {
		return nil, errors.New("helper is gone")
	}
	if len(mic) != len(ref) {
		return nil, fmt.Errorf("mic %d and reference %d differ", len(mic), len(ref))
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	n := len(mic)
	if cap(e.buf) < 4*n {
		e.buf = make([]byte, 4*n)
	}
	b := e.buf[:4*n]
	// A helper that is alive but no longer answering would hold this goroutine, and capture with it,
	// in the Write or the Read below for good. Killing it closes its pipes, which ends either one.
	stall := time.AfterFunc(helperStall, func() {
		e.broken.Store(true)
		slog.Warn("echo cancellation helper stopped answering; killing it", "after", helperStall)
		_ = e.cmd.Process.Kill()
	})
	defer stall.Stop()
	for i, v := range mic {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	for i, v := range ref {
		binary.LittleEndian.PutUint16(b[2*n+2*i:], uint16(v))
	}
	if _, err := e.in.Write(b); err != nil {
		e.broken.Store(true)
		return nil, err
	}
	// What comes back is one block, or with the linear output two, the same size as what went in: the
	// buffer holds both.
	back := 2 * n
	if e.linear {
		back = 4 * n
	}
	if _, err := io.ReadFull(e.out, b[:back]); err != nil {
		e.broken.Store(true)
		return nil, err
	}
	out := make([]int16, n)
	var inE, outE float64
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
		inE += float64(mic[i]) * float64(mic[i])
		outE += float64(out[i]) * float64(out[i])
	}
	// About half a second at 20 ms blocks.
	const k = 1.0 / 25
	e.inE += (inE - e.inE) * k
	e.outE += (outE - e.outE) * k
	if e.outE > 0 && e.inE > 0 {
		e.erle.Store(int64(10 * math.Log10(e.inE/e.outE) * 1000))
	}
	if e.linear {
		if cap(e.lin) < n {
			e.lin = make([]int16, n)
		}
		e.lin = e.lin[:n]
		var linE float64
		for i := range e.lin {
			e.lin[i] = int16(binary.LittleEndian.Uint16(b[2*n+2*i:]))
			linE += float64(e.lin[i]) * float64(e.lin[i])
		}
		e.linE += (linE - e.linE) * k
		if e.linE > 0 && e.inE > 0 {
			e.linErle.Store(int64(10 * math.Log10(e.inE/e.linE) * 1000))
		}
	}
	return out, nil
}

// Linear is the linear output of the block Process last returned, or nil from a helper started
// without it. It is good until the next Process.
func (e *external) Linear() []int16 {
	if !e.linear {
		return nil
	}
	return e.lin
}

// LinearERLE is how much echo the linear filter alone removes, in dB, from a helper started with the
// linear output.
func (e *external) LinearERLE() float64 { return float64(e.linErle.Load()) / 1000 }

// ERLE is the estimated echo removal in dB.
func (e *external) ERLE() float64 { return float64(e.erle.Load()) / 1000 }

// Healthy reports whether the helper is still there.
func (e *external) Healthy() bool { return !e.broken.Load() }

// Close ends the helper.
func (e *external) Close() {
	// Kill first: a Process stuck on the helper holds the lock, and this is what lets it go.
	if e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_ = e.in.Close()
}
