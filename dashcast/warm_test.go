package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// A tab is shown, and sends frames, while another screen's tab is open and after it has been parked
// and picked up again. A parked tab came back hidden, and a hidden page sends one frame and then
// none: a screen that came back to its dashboard froze on it, taps and all (#112).
//
// Skipped without a browser, and run inside the image by the image build, as
// TestOpenATabInTheRunningBrowser is.
func TestAParkedTabShowsAgain(t *testing.T) {
	required := os.Getenv("DASHCAST_REQUIRE_BROWSER") != ""
	if chromePath("") == "" {
		if required {
			t.Fatal("no Chrome or headless-shell to run")
		}
		t.Skip("no Chrome or headless-shell to run")
	}
	ha := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><title>dashboard</title><p id=t></p>
<script>setInterval(() => { t.textContent = Date.now() }, 100)</script>`))
	}))
	defer ha.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	b, err := newBrowser(ctx, config{ha: ha.URL})
	if err != nil {
		if required {
			t.Fatalf("the browser did not start: %v", err)
		}
		t.Skipf("the browser did not start: %v", err)
	}
	defer b.close()

	open := func(key string) *warmTab {
		tab, closeTab, err := b.open(ctx, "/lovelace/0", 480, 240, map[string]bool{"lovelace": true}, false)
		if err != nil {
			t.Fatal(err)
		}
		return newWarmTab(tab, closeTab, key)
	}
	// showing is the tab's visibility and whether frames keep coming: more than the first one in
	// two seconds of a page that changes ten times a second.
	showing := func(name string, w *warmTab) {
		t.Helper()
		var frames atomic.Int32
		w.watch(func(f *page.EventScreencastFrame) {
			frames.Add(1)
			go func() { _ = chromedp.Run(w.ctx, page.ScreencastFrameAck(f.SessionID)) }()
		})
		defer w.watch(nil)
		var shown string
		if err := chromedp.Run(w.ctx, chromedp.Evaluate(`document.visibilityState`, &shown),
			page.StartScreencast().WithFormat(page.ScreencastFormatPng)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		time.Sleep(2 * time.Second)
		_ = chromedp.Run(w.ctx, page.StopScreencast())
		if n := frames.Load(); shown != "visible" || n < 3 {
			t.Errorf("%s: %s, %d frames in 2 s", name, shown, n)
		}
	}

	a := open("a")
	showing("a new tab", a)
	other := open("b")
	defer other.close()
	showing("another screen's new tab", other)
	showing("the first tab, with another open", a)
	warm.park(a)
	again := warm.take("a")
	if again == nil {
		t.Fatal("the parked tab was not picked up")
	}
	defer again.close()
	showing("the tab parked and picked up again", again)
	showing("the other screen's tab, after", other)
}

// A parked tab comes back wherever the page was taken while it was up, unless the screen asks for its
// view (hello.Go): then it goes there, in the page, as the frontend's own links go. A deck button that
// names a view showed the view the page had been tapped to instead, after the dashboard was put away
// and opened again within warmFor.
func TestAParkedTabGoesToTheViewAskedFor(t *testing.T) {
	required := os.Getenv("DASHCAST_REQUIRE_BROWSER") != ""
	if chromePath("") == "" {
		if required {
			t.Fatal("no Chrome or headless-shell to run")
		}
		t.Skip("no Chrome or headless-shell to run")
	}
	ha := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><title>dashboard</title>
<script>addEventListener("location-changed", () => { window.routed = location.pathname })</script>`))
	}))
	defer ha.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	b, err := newBrowser(ctx, config{ha: ha.URL})
	if err != nil {
		if required {
			t.Fatalf("the browser did not start: %v", err)
		}
		t.Skipf("the browser did not start: %v", err)
	}
	defer b.close()

	tab, closeTab, err := b.open(ctx, "/dash/a", 480, 240, map[string]bool{"dash": true}, false)
	if err != nil {
		t.Fatal(err)
	}
	const key = "go|480x240|/dash/a|false|"
	w := newWarmTab(tab, closeTab, key)
	where := func(name string, w *warmTab) (path, routed string) {
		t.Helper()
		var at struct {
			Path   string `json:"path"`
			Routed string `json:"routed"`
		}
		if err := chromedp.Run(w.ctx, chromedp.Evaluate(`({path: location.pathname, routed: window.routed || ""})`, &at)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return at.Path, at.Routed
	}
	// Tapped to another view while it was up.
	if err := chromedp.Run(w.ctx, chromedp.Evaluate(`history.pushState(null, "", "/dash/b")`, nil)); err != nil {
		t.Fatal(err)
	}

	warm.park(w)
	w = warm.take(key)
	if w == nil {
		t.Fatal("the parked tab was not picked up")
	}
	defer w.close()
	if path, _ := where("picked up", w); path != "/dash/b" {
		t.Errorf("picked up without asking for the view: at %s, not where it was left", path)
	}

	if err := showPath(w.ctx, "/dash/a"); err != nil {
		t.Fatal(err)
	}
	if path, routed := where("asked for its view", w); path != "/dash/a" || routed != "/dash/a" {
		t.Errorf("asked for /dash/a: at %s, the router told of %q", path, routed)
	}
	// Already there: nothing to tell the router.
	if err := chromedp.Run(w.ctx, chromedp.Evaluate(`window.routed = ""`, nil)); err != nil {
		t.Fatal(err)
	}
	if err := showPath(w.ctx, "/dash/a"); err != nil {
		t.Fatal(err)
	}
	if _, routed := where("asked again", w); routed != "" {
		t.Errorf("a tab already at its view was moved again: %q", routed)
	}
	// Somewhere the page's guard keeps it from is not gone to, and said so.
	if err := showPath(w.ctx, "/config/users"); err == nil {
		t.Error("a path the guard refuses was taken for gone to")
	}
	if path, _ := where("refused", w); path != "/dash/a" {
		t.Errorf("a path the guard refuses moved the page to %s", path)
	}
}
