//go:build !dot && !spot

package display

import (
	"image"
	"path/filepath"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/touch"
)

// With a streamed dashboard, the Now Playing page has its Library button, which brings the library up
// on its own: drawn as a page, with the touches its, and not taken away by the dashboard's frame.
// Done leaves it for Now Playing again.
func TestTheLibraryOnItsOwnWithAStreamedDashboard(t *testing.T) {
	noLibrary(t)
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	_ = config.Set().Dashboard().Mode(config.DashboardStreamed)
	t.Cleanup(func() { libLast.Store(nil) })
	libLast.Store(nil)
	if w := nowPlayingLibraryWord(); w != "" {
		t.Errorf("without Music Assistant the button says %q", w)
	}
	libFakeMA(t)
	if w := nowPlayingLibraryWord(); w != libWord(libWordLibrary) {
		t.Fatalf("with a streamed dashboard the button says %q", w)
	}

	r := newRenderer(image.NewRGBA(image.Rect(0, 0, showWide, showHigh)))
	d := &Display{r: r, poke: make(chan struct{}, 1)}
	d.reopenLibrary()
	if libUp(d) == nil || !d.libAlone || d.dash {
		t.Fatalf("library %v, on its own %v, dashboard up %v", libUp(d) != nil, d.libAlone, d.dash)
	}

	s := scene{phase: "idle", now: time.Now()}
	d.libraryScene(&s)
	d.dashScene(&s, false)
	if !s.showLibrary || s.dashLibrary == nil || s.showDash || libUp(d) == nil || !d.libraryAloneUp() {
		t.Fatalf("frame: library %v, dashboard %v; still up %v", s.showLibrary, s.showDash, libUp(d) != nil)
	}
	d.mu.Lock()
	follow, holds := d.dashFollow, d.dashHolds
	d.mu.Unlock()
	if !follow || !holds {
		t.Errorf("the library's fingers are not followed (%v) or held (%v)", follow, holds)
	}

	// Under the settings it waits, and is back when they go.
	under := scene{phase: "idle", now: time.Now(), showSheet: true}
	d.libraryScene(&under)
	if under.showLibrary || libUp(d) == nil {
		t.Error("the library was drawn over the settings, or went")
	}

	r.draw(s)
	r.zmu.Lock()
	zones := r.libZones
	r.zmu.Unlock()
	x, y := libMiddle(libFind(t, zones, libPartDone, -1))
	d.libraryAloneGesture(touch.Gesture{Kind: touch.Tap, X: x, Y: y})
	if libUp(d) != nil || d.libAlone {
		t.Errorf("after Done: library %v, on its own %v", libUp(d) != nil, d.libAlone)
	}
}

// With a drawn dashboard the Now Playing page's Library button is as it was: the library over the
// dashboard, put up for it.
func TestTheLibraryOverADrawnDashboardIsUnchanged(t *testing.T) {
	noLibrary(t)
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	_ = config.Set().Dashboard().Mode(config.DashboardDrawn)
	t.Cleanup(func() { libLast.Store(nil) })
	libLast.Store(nil)
	d := &Display{poke: make(chan struct{}, 1)}
	d.reopenLibrary()
	if libUp(d) == nil || d.libAlone || !d.dash {
		t.Fatalf("library %v, on its own %v, dashboard up %v", libUp(d) != nil, d.libAlone, d.dash)
	}
}

// A frame drawn while the Library button is still putting the library up (on its own, with no library
// in yet) does not take it for one put away: the library comes up when it is in.
func TestAFrameBeforeTheLibraryIsInLeavesItComing(t *testing.T) {
	noLibrary(t)
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	_ = config.Set().Dashboard().Mode(config.DashboardStreamed)
	d := &Display{poke: make(chan struct{}, 1)}
	d.libAlone, d.libTouched = true, time.Now()
	s := scene{phase: "idle", now: time.Now()}
	d.libraryScene(&s)
	if s.showLibrary || !d.libAlone || d.libraryAloneUp() {
		t.Fatalf("before the library is in: drawn %v, on its own %v, fingers its %v", s.showLibrary, d.libAlone, d.libraryAloneUp())
	}
	d.openLibrary("", "", false)
	s = scene{phase: "idle", now: time.Now()}
	d.libraryScene(&s)
	if !s.showLibrary || !d.libraryAloneUp() {
		t.Errorf("once it is in: drawn %v, fingers its %v", s.showLibrary, d.libraryAloneUp())
	}
}

// The deck asked for takes the library up on its own away, as the dashboard does.
func TestTheDeckPutsTheLibraryOnItsOwnAway(t *testing.T) {
	noLibrary(t)
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	_ = config.Set().Dashboard().Mode(config.DashboardStreamed)
	_ = config.Set().Deck().Page(0, []config.DeckButton{{Action: config.DeckOBSStream}})
	t.Cleanup(func() { libLast.Store(nil) })
	libLast.Store(nil)
	libFakeMA(t)
	d := &Display{poke: make(chan struct{}, 1)}
	d.reopenLibrary()
	if !d.libAlone || libUp(d) == nil {
		t.Fatal("the library did not come up on its own")
	}
	if !d.openDeck() || d.libAlone || libUp(d) != nil {
		t.Errorf("deck up, the library on its own %v, up %v", d.libAlone, libUp(d) != nil)
	}
}
