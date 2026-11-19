//go:build !dot && !spot

package display

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"log/slog"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/feature/dashboard"
	"github.com/HuskerMinion/techo5/echod/internal/feature/deck"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/touch"
	"github.com/HuskerMinion/techo5/echod/internal/lib/fonts"
)

// TECHO5 Deck's page (render_deck.go): opened by a swipe up from the bottom edge of the clock, once a
// deck is set up, or by Tap on the clock set to Deck. It stays until a swipe down, or a tap on Clock at
// its foot, puts it away.

// bottomEdge is the strip along the bottom of the clock a swipe up has to start in to open the deck,
// in the layout's sizes; above it a swipe up is the volume, as it always was. Thinner than the top
// edge's band, so the volume keeps most of the screen.
const bottomEdge = 90

// A press shows on its button for as long as it is going out, and what came of it after: the pressed
// look until the press is answered, which with a computer that is slow or gone can be seconds, and at
// least deckPressMin so one answered at once is still seen; then, failed, the red look for deckFailedFor
// with the reason at the foot of the page. The look used to last a fixed six tenths of a second
// whatever the press was doing, and a failure was six tenths of a second of red with no word why.
const (
	deckPressMin  = 200 * time.Millisecond
	deckFailedFor = 2 * time.Second
)

// deckPress is a press being shown on its button.
type deckPress struct {
	page, button int
	seq          uint64    // which press it is: the answer to an earlier one is not this one's
	going        bool      // sent, and not answered yet
	shown        time.Time // the pressed look stays until then at least
	until        time.Time // failed: the red look and the reason stay until then
	failed       bool
	reason       string // failed: why, for the foot of the page
}

// pressed and failedAt are the two looks at now.
func (p deckPress) pressed(now time.Time) bool  { return p.going || (!p.failed && now.Before(p.shown)) }
func (p deckPress) failedAt(now time.Time) bool { return !p.going && p.failed && now.Before(p.until) }

// deckDo sends a press; a variable for the tests.
var deckDo = func(page, i int) error { return deck.Get().Press(page, i) }

// deckReason is why a press of b failed, in a line for the foot of the page: the kind of failure and
// whose it was, never the error's own words.
func deckReason(b config.DeckButton, err error) string {
	if b.Action.OnScreen() {
		switch {
		case errors.Is(err, errNothingPlaying):
			return tr("deck_nothing_playing")
		case errors.Is(err, errScreenOff):
			return tr("deck_screen_off")
		}
		return tr("deck_failed")
	}
	switch deck.Why(b, err) {
	case deck.FailedOBSAway:
		return tr("deck_obs_away")
	case deck.FailedOBSRefused:
		return tr("deck_obs_refused")
	case deck.FailedOBSSilent:
		return tr("deck_obs_silent")
	case deck.FailedComputerSilent:
		return fmt.Sprintf(tr("deck_pc_silent"), b.Computer)
	case deck.FailedComputerUnpaired:
		return fmt.Sprintf(tr("deck_pc_unpaired"), b.Computer)
	case deck.FailedComputerKey:
		return fmt.Sprintf(tr("deck_pc_key"), b.Computer)
	}
	return tr("deck_failed")
}

// fromBottomEdge is whether a swipe up starting at y starts in the deck's strip.
func (d *Display) fromBottomEdge(y int) bool {
	return d.r != nil && y >= d.r.h-d.r.s(bottomEdge)
}

// openDeck puts the deck up, if there is one.
func (d *Display) openDeck() bool {
	if !deck.Get().Set() {
		return false
	}
	return d.putDeckUp(false)
}

// openLauncher is Tap on the clock set to Deck: the deck when it has buttons, and on a device with
// none, the deck's built-in page of the screen's own pages (deckLauncherPage), when there is anything
// to put on it.
func (d *Display) openLauncher() bool {
	if d.openDeck() {
		return true
	}
	if !deckLauncherPage().Set() {
		return false
	}
	return d.putDeckUp(true)
}

// putDeckUp puts the deck up, on its built-in page when launcher is set.
func (d *Display) putDeckUp(launcher bool) bool {
	d.mu.Lock()
	d.deckLauncher = launcher
	// On the screen from now, not from the next frame: the swipe that opened it is still sending its
	// steps up, and they are the deck's to ignore, not the volume's.
	// A press still going out from before stays what it is, its page and button with it: its answer is
	// that button's (a failure lights it, not the first one on the page the deck opens on), and the same
	// button takes no second press behind it. Otherwise the look of the last one is over.
	d.deckUp, d.deckOnScreen = true, true
	if !d.deckPress.going {
		d.deckPress = deckPress{seq: d.deckPress.seq + 1}
	}
	d.closeAlert()
	d.calUntil, d.weatherUntil = time.Time{}, time.Time{}
	d.sheet, d.dash, d.drawer, d.appsPop = false, false, false, false
	if d.libAlone {
		d.libGoneLocked() // the deck asked for comes first, as it does over the dashboard
	}
	pages := len(d.deckNowLocked().Pages)
	if d.deckPage >= pages {
		d.deckPage = 0
	}
	d.mu.Unlock()
	slog.Info("deck up", "launcher", launcher)
	d.wake()
	return true
}

func (d *Display) closeDeck() {
	d.mu.Lock()
	d.deckUp = false
	d.mu.Unlock()
	d.wake()
}

// deckShowing is whether the deck page is on the screen now: up, and drawn in the last frame rather
// than under something else.
func (d *Display) deckShowing() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.deckUp && d.deckOnScreen && pageIdle(d.view.Phase)
}

// onClock is whether the clock itself is the page: not now playing, not the weather.
func (d *Display) onClock() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.showingPlaying && !time.Now().Before(d.weatherUntil)
}

// deckFrameKey is what a frame of the deck shows, or "" when the frame isn't just the deck: two frames
// with the same key look the same.
func deckFrameKey(s scene, ringing, calling bool) string {
	// The update dialog is drawn over the deck, and its progress changes every frame, so a frame with
	// it up is not the unchanged deck: otherwise the dialog is skipped while the deck is still there
	// to take the taps that dismiss it, and the screen looks normal while ignoring every touch.
	if !s.showDeck || s.showVideo || s.showVideoAsk || s.showVolume || ringing || calling || s.setupAsking || s.showAnnouncement ||
		s.showReminder || s.showNotification || s.popup != nil || s.bt.Pairing || s.pin.open || s.redClock || s.announceRecording || s.updateActive ||
		s.cue != "" {
		return ""
	}
	// The colors too: the deck is drawn in the theme's, and a change from the setup page (a custom
	// palette's included) should show. So should a change of font, of the owner's own fonts, or of
	// the text size.
	return fmt.Sprintf("%+v|%v|%s|%v|%s|%d|%d", s.deck, s.micOffSaid(), s.linkDown, [5]color.RGBA{walnut, amber, cream, dim, ember},
		drawnFont(), fonts.Generation(), textSize())
}

// deckScene fills in what the deck page shows, when it is up.
func (d *Display) deckScene(s *scene, now time.Time) {
	// The pages asked for while the deck is up are drawn over it, and have the touches.
	over := s.showSheet || s.showDrawer || s.showCamera || s.showWifi || s.showAlert || s.showCalendar ||
		s.showWeather || s.showRadar || s.bt.Pairing || s.showCast || s.addonPage != nil
	d.mu.Lock()
	up := d.deckUp && s.phase == "idle" && !over
	d.deckOnScreen = up
	press := d.deckPress
	d.mu.Unlock()
	if !up {
		return
	}
	d.mu.Lock()
	cfg := d.deckNowLocked()
	// Pages taken away on the setup page while the deck was up on one of them.
	pages := max(len(cfg.Pages), 1)
	if d.deckPage >= pages {
		d.deckPage = pages - 1
	}
	page := d.deckPage
	d.mu.Unlock()
	if !cfg.Set() {
		// Every button taken off it from the setup page while it was up.
		d.closeDeck()
		return
	}
	cols, rows := cfg.Grid()
	st := deck.Get().OBS()
	pcs := deck.Get().Computers()
	v := deckView{cols: cols, rows: rows, page: page, pages: pages, obs: deckPageOBS(cfg, page),
		connected: st.Connected, problem: st.Problem, obsSet: cfg.OBS.Addr != "", away: deck.Away(cfg, page, pcs)}
	if press.failedAt(now) && press.page == page {
		v.reason = press.reason
	}
	for i := range cols * rows {
		b := cfg.Button(page, i)
		lit, known := deck.Lit(b, st, pcs)
		bv := deckButtonView{empty: b.Action == config.DeckNone, label: deck.Label(b), icon: deck.Icon(b, lit),
			color: b.Color, lit: lit, known: known}
		if press.page == page && press.button == i {
			bv.pressed, bv.failed = press.pressed(now), press.failedAt(now)
		}
		v.buttons = append(v.buttons, bv)
	}
	s.showDeck, s.deck = true, v
}

// deckGesture is a finger on the deck page: a tap presses a button, a swipe left or right turns the
// page, a swipe down puts the deck away. Swipes up do nothing: the swipe that opened it keeps
// reporting notches until the finger lifts.
func (d *Display) deckGesture(g touch.Gesture) {
	switch g.Kind {
	case touch.Tap:
		if d.r == nil {
			return
		}
		if d.r.deckCloseHit(image.Pt(g.X, g.Y)) {
			d.closeDeck()
			return
		}
		i, ok := d.r.deckHit(image.Pt(g.X, g.Y))
		if !ok {
			return
		}
		d.pressDeck(i)
	case touch.SwipeLeft, touch.SwipeRight:
		d.mu.Lock()
		pages := max(len(d.deckNowLocked().Pages), 1)
		if g.Kind == touch.SwipeLeft {
			d.deckPage = min(d.deckPage+1, pages-1)
		} else {
			d.deckPage = max(d.deckPage-1, 0)
		}
		d.mu.Unlock()
	case touch.SwipeDown:
		d.closeDeck()
	}
}

// pressDeck sends a press of button i on the page that is up, and shows it on the button until it is
// answered. A button whose press is still going out takes no second one: it would be sent behind the
// first, and a toggle pressed twice by an impatient finger ends where it began.
func (d *Display) pressDeck(i int) {
	now := time.Now()
	d.mu.Lock()
	page := d.deckPage
	if p := d.deckPress; p.going && p.page == page && p.button == i {
		d.mu.Unlock()
		return
	}
	seq := d.deckPress.seq + 1
	d.deckPress = deckPress{page: page, button: i, seq: seq, going: true, shown: now.Add(deckPressMin)}
	b := d.deckNowLocked().Button(page, i)
	d.mu.Unlock()
	d.wake()
	go func() {
		var err error
		if b.Action.OnScreen() {
			// The screen's own pages are opened here, the built-in page's buttons included, which
			// are not in the settings for the deck to look up.
			err = deckScreenDo(d, b)
		} else {
			err = deckDo(page, i)
		}
		failed := err != nil && !errors.Is(err, deck.ErrEmpty)
		var reason string
		if failed {
			reason = deckReason(b, err)
		}
		answered := time.Now()
		d.mu.Lock()
		// Only the press the button is showing: one tapped since has the button's look now.
		ours := d.deckPress.seq == seq
		if ours {
			d.deckPress.going = false
			if failed {
				d.deckPress.failed, d.deckPress.until, d.deckPress.reason = true, answered.Add(deckFailedFor), reason
			}
		}
		left := d.deckPress.shown.Sub(answered)
		d.mu.Unlock()
		if !ours {
			return
		}
		d.wake()
		// And once more when the look is over, so the button is drawn plain again.
		switch {
		case failed:
			time.AfterFunc(deckFailedFor+50*time.Millisecond, d.wake)
		case left > 0:
			time.AfterFunc(left+50*time.Millisecond, d.wake)
		}
	}()
}

// deckChanged redraws the page when OBS's state changes under it.
func (d *Display) deckChanged(struct{}) {
	if d.deckShowing() {
		d.wake()
	}
}

// deckNowLocked is the deck as it is up: the settings', or the built-in page while that is what is up.
// With mu held.
func (d *Display) deckNowLocked() config.Deck {
	cfg := config.Get().Deck
	if d.deckLauncher && !cfg.Set() {
		return deckLauncherPage()
	}
	return cfg
}

// deckPageOBS is whether page p has an OBS button: only then does its foot say how OBS is.
func deckPageOBS(cfg config.Deck, p int) bool {
	cols, rows := cfg.Grid()
	for i := range cols * rows {
		if cfg.Button(p, i).Action.OnOBS() {
			return true
		}
	}
	return false
}

// deckLauncherPage is the deck's built-in page, for a device whose deck has no buttons of its own: one
// large button for each of the screen's pages that is there to open, three by two. A deck with
// buttons of its own is never this page; one made on the setup page can have these buttons and more.
// Its labels are in the screen's language, as nobody wrote them.
func deckLauncherPage() config.Deck {
	var bs []config.DeckButton
	add := func(a config.DeckAction, label string, on bool) {
		if on {
			bs = append(bs, config.DeckButton{Action: a, Label: label})
		}
	}
	_, music := libClient()
	add(config.DeckScreenLibrary, tr("music"), music)
	add(config.DeckScreenDashboard, tr("dashboard"), dashboard.Get().Mode() != config.DashboardOff)
	add(config.DeckScreenCalendar, tr("ui_calendar"), calendarsShown())
	add(config.DeckScreenWeather, tr("weather"), config.Get().Home.WeatherEntity() != "")
	add(config.DeckScreenRadio, tr("drawer_radio"), true)
	add(config.DeckScreenCameras, tr("cameras"), true)
	return config.Deck{Cols: 3, Rows: 2, Pages: []config.DeckPage{{Buttons: bs}}}
}

// What a press of a button for one of the screen's pages can fail with, for the foot of the deck.
var (
	errScreenOff      = errors.New("deck: that page is not set up on this screen")
	errNothingPlaying = errors.New("deck: nothing is playing")
)

// deckScreenDo opens the page a button for one of the screen's pages is for; a variable for the tests.
var deckScreenDo = func(d *Display, b config.DeckButton) error { return d.screenPress(b) }

// screenPress opens the page b is for, from the deck on the screen or from Home Assistant's
// deck_press. The calendar, the forecast and the drawer come up over the deck, which is there again
// when they go, as it is under a camera; the music library up on its own goes back to it with Done.
// The dashboard and Now Playing are pages the deck gives way to.
func (d *Display) screenPress(b config.DeckButton) error {
	d.mu.Lock()
	fromDeck := d.deckUp && d.deckOnScreen
	d.mu.Unlock()
	slog.Info("deck: screen page", "action", b.Action, "from deck", fromDeck)
	switch b.Action {
	case config.DeckScreenLibrary:
		// A fresh library on this device's player, up on its own whatever the dashboard is
		// (library_alone.go), and Done goes back to the deck it was opened from.
		back := libBackNone
		if fromDeck {
			back = libBackDeck
		}
		d.openLibraryAlone(back)
		return nil
	case config.DeckScreenDashboard:
		if dashboard.Get().Mode() == config.DashboardOff {
			return errScreenOff
		}
		if b.Path != "" {
			if err := dashboard.Get().GoTo(b.Path); err != nil {
				return err
			}
		}
		if !d.openDashboard() {
			return errScreenOff
		}
		return nil
	case config.DeckScreenCalendar:
		if !d.OpenCalendar() {
			return errScreenOff
		}
		return nil
	case config.DeckScreenWeather:
		if config.Get().Home.WeatherEntity() == "" {
			return errScreenOff
		}
		d.ShowWeather(false)
		return nil
	case config.DeckScreenRadio:
		d.openDrawer(drawerRadio)
		return nil
	case config.DeckScreenCameras:
		d.openDrawer(drawerCameras)
		return nil
	case config.DeckScreenNowPlaying:
		if !d.nowPlaying() {
			return errNothingPlaying
		}
		d.mu.Lock()
		d.deckUp = false
		d.mu.Unlock()
		d.showMusicPage()
		return nil
	}
	return deck.ErrEmpty
}
