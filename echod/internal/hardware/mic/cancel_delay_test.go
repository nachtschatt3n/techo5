package mic

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// At no delay the helper is started exactly as it always was, so a helper older than the setting
// still starts; a delay adds the flag.
func TestHelperArgs(t *testing.T) {
	if got, want := helperArgs(0), []string{"--ns", "low"}; !slices.Equal(got, want) {
		t.Errorf("no delay: %q, want %q", got, want)
	}
	if got, want := helperArgs(8), []string{"--ns", "low", "--delay-ms", "8"}; !slices.Equal(got, want) {
		t.Errorf("8 ms: %q, want %q", got, want)
	}
	if got, want := helperArgs(-5), []string{"--ns", "low"}; !slices.Equal(got, want) {
		t.Errorf("a negative delay: %q, want %q", got, want)
	}
}

// fakeHelper puts a techo5-aec on PATH that notes the arguments of every start, one line each, and
// then takes blocks and never answers. It returns where the notes go.
func fakeHelper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	notes := filepath.Join(dir, "starts")
	script := "#!/bin/sh\necho \"$*\" >> \"$TECHO5_AEC_TEST_STARTS\"\nexec cat > /dev/null\n"
	if err := os.WriteFile(filepath.Join(dir, WebRTCHelper), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TECHO5_AEC_TEST_STARTS", notes)
	return notes
}

// helperWait is how long the test waits on the fake helper, a shell starting and writing a line, or
// on a killed one being reaped. Waits end as soon as what they wait for happens, so it only matters on
// a machine too busy to start a shell for seconds, and the test should not fail there.
const helperWait = 20 * time.Second

// starts waits until the fake helper has noted n starts and returns them.
func starts(t *testing.T, notes string, n int) []string {
	t.Helper()
	deadline := time.Now().Add(helperWait)
	for {
		b, _ := os.ReadFile(notes)
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(b) == 0 {
			lines = nil
		}
		if len(lines) >= n || time.Now().After(deadline) {
			return lines
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A new delay starts a running helper again with it, the same delay does not, and the built-in filter
// never starts one for it.
func TestCancelDelayRestartsTheHelper(t *testing.T) {
	notes := fakeHelper(t)
	c := newCanceller()
	t.Cleanup(func() {
		if c.ext != nil {
			c.ext.Close()
		}
	})

	c.setDelay(5)
	if c.ext != nil || c.engine != "builtin" {
		t.Fatal("a delay started the helper while the built-in filter was chosen")
	}

	if got := c.setEngine("webrtc"); got != "webrtc" {
		t.Fatalf("engine %q with a helper on PATH", got)
	}
	if got := starts(t, notes, 1); len(got) != 1 || got[0] != "--ns low --delay-ms 5" {
		t.Fatalf("helper started with %q, want the delay set before it", got)
	}

	first := c.ext
	c.setDelay(5)
	if c.ext != first {
		t.Error("the same delay started the helper again")
	}

	c.setDelay(12)
	if c.ext == first || c.engine != "webrtc" {
		t.Fatalf("a new delay left the helper as it was (engine %q)", c.engine)
	}
	// Killed by Close; the process is reaped, and the helper marked gone, a moment after.
	for deadline := time.Now().Add(helperWait); first.Healthy() && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if first.Healthy() {
		t.Error("the helper started with the old delay is still running")
	}
	if got := starts(t, notes, 2); len(got) != 2 || got[1] != "--ns low --delay-ms 12" {
		t.Fatalf("helper restarted with %q, want the new delay", got)
	}

	c.setDelay(0)
	if got := starts(t, notes, 3); len(got) != 3 || got[2] != "--ns low" {
		t.Fatalf("helper restarted with %q, want no delay flag at 0", got)
	}
}

// A helper too old for the flag exits with the code it gives an unknown argument; that is told apart
// from any other end, and only when a delay was passed.
func TestRefusedDelay(t *testing.T) {
	exit := func(code string) error { return exec.Command("sh", "-c", "exit "+code).Run() }
	if !refusedDelay(helperArgs(8), exit("2")) {
		t.Error("an exit 2 after --delay-ms was not taken for a refused delay")
	}
	if refusedDelay(helperArgs(0), exit("2")) {
		t.Error("an exit 2 without a delay was taken for a refused delay")
	}
	if refusedDelay(helperArgs(8), exit("1")) {
		t.Error("an exit 1 was taken for a refused delay")
	}
	if refusedDelay(helperArgs(8), nil) {
		t.Error("a clean end was taken for a refused delay")
	}
	// With --linear too, the exit may be the linear output refused by a helper that has the delay;
	// startWebRTC tries again without it, and that start says whether the delay was refused.
	if refusedDelay(append(helperArgs(8), linearArg), exit("2")) {
		t.Error("an exit 2 with --linear among the arguments was taken for a refused delay")
	}
}
