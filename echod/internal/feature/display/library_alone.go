//go:build !dot && !spot

package display

import (
	"log/slog"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/feature/dashboard"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/touch"
)

// The music library as a page of its own. Over the drawn dashboard (Drawn and Tessera) the library is
// one of the page's sheets: it is drawn over the page's tiles, and the page's fingers reach it. A
// streamed dashboard is a picture from the dashcast server, with nothing of the device's own drawn
// into it and every finger sent to the browser, and with no dashboard at all there is no page to be
// over. Yet the library never needed the page under it: it is drawn over the whole panel, in its own
// card, from Music Assistant, and plays through Home Assistant. So there it comes up on its own, over
// whatever was on the screen (the clock, Now Playing, the deck), and Done goes back to that.
//
// The library itself is the same one: the same librarySheet in dashLibrary, drawn by librarySheet and
// driven by the same taps, holds and drags as over the drawn dashboard (drawnHold, drawnMove,
// drawnRelease). libAlone says it is up on its own: the dashboard's frame does not then take it away
// with the dashboard, and the dashboard is not drawn while it is up.

// libraryOverDashboard is whether the library goes over the dashboard, as it always has, rather than
// up on its own: when the dashboard is drawn here, and so has a page to put it over.
func libraryOverDashboard() bool {
	m := dashboard.Get().Mode()
	return m == config.DashboardDrawn || m == config.DashboardTessera
}

// Where Done takes a library up on its own back to.
const (
	libBackNone = iota // what was under it: the clock, or Now Playing
	libBackDeck        // the deck, which opened it
)

// openLibraryAlone puts the library up on its own, a fresh one on this device's player, and back
// says where Done returns to. Without Music Assistant set up the library says how to set it up, as
// it does over the dashboard.
func (d *Display) openLibraryAlone(back int) {
	d.mu.Lock()
	d.libAlone, d.libBack, d.libTouched = true, back, time.Now()
	d.dash, d.deckUp = false, false
	d.closeAlert()
	d.calUntil, d.weatherUntil = time.Time{}, time.Time{}
	d.sheet, d.drawer = false, false
	d.mu.Unlock()
	slog.Info("music library on its own", "back", back)
	d.openLibrary("", "", true)
}

// libGoneLocked takes the library away, on its own or over the dashboard. With mu held.
func (d *Display) libGoneLocked() {
	d.dashLibrary = nil
	d.libAlone, d.libBack = false, libBackNone
}

// libDone is the library's Done: away it goes, and one up on its own goes back to the deck it was
// opened from, on the page it was left on.
func (d *Display) libDone() {
	d.mu.Lock()
	back := d.libAlone && d.libBack == libBackDeck
	d.libGoneLocked()
	if back {
		d.deckUp, d.deckOnScreen = true, true
	}
	d.mu.Unlock()
	d.wake()
}

// libraryAloneUp is whether the library is up on its own and drawn in the last frame, and so has the
// fingers.
func (d *Display) libraryAloneUp() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.libAlone && d.dashLibrary != nil && d.libOnScreen
}

// libraryScene fills in the library on its own, when it is up and nothing asked for since is over it.
// Like the deck, it stays up under what comes over it (a camera, the settings, the calendar or the
// forecast) and is back when that goes; and like a dashboard opened by hand, it goes once it has been
// left alone for the Dashboard returns time.
func (d *Display) libraryScene(s *scene) {
	over := s.showSheet || s.showDrawer || s.showCamera || s.showWifi || s.showAlert || s.showCalendar ||
		s.showWeather || s.showRadar || s.bt.Pairing || s.showDeck
	d.mu.Lock()
	if d.libAlone && dashForgotten(d.libTouched) {
		slog.Info("music library on its own closed: not touched for a while")
		d.libGoneLocked()
	}
	// Everything that puts the library away does it through libGoneLocked, which ends libAlone too. A
	// libAlone with no library yet is reopenLibrary between the two: it is not taken for one put away.
	up := d.libAlone && d.dashLibrary != nil && s.phase == "idle" && !over
	d.libOnScreen = up
	d.mu.Unlock()
	if !up {
		return
	}
	s.showLibrary = true
	s.dashLibrary = d.libraryViewNow()
	if s.dashLibrary == nil {
		s.showLibrary = false
	}
}

// libraryAloneGesture is a finger on the library up on its own: the same taps, holds and drags it
// takes over the drawn dashboard. The screen's edges are its too, as they are under a sheet there.
func (d *Display) libraryAloneGesture(g touch.Gesture) {
	d.mu.Lock()
	d.libTouched = time.Now()
	d.mu.Unlock()
	switch g.Kind {
	case touch.Tap:
		d.libraryTap(g.X, g.Y)
	case touch.Hold:
		d.drawnHold(g.X, g.Y)
	case touch.Drag:
		d.drawnMove(g.X, g.Y)
	case touch.Release:
		d.drawnRelease(g.X, g.Y)
	}
}

// libraryPage draws the library on its own: the panel's own background, and the library over it in
// the theme of a page with none of its own.
func (r *renderer) libraryPage(s scene) {
	fillCanvas(r.dst, walnut)
	r.librarySheet(s.dashLibrary, dashboard.Theme{})
}
