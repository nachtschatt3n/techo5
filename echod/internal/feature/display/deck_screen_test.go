//go:build !dot && !spot

package display

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/lib/musicassistant"
)

// deckUpOn is a display with the deck up and drawn, as a finger finds it.
func deckUpOn() *Display {
	d := &Display{poke: make(chan struct{}, 1)}
	d.deckUp, d.deckOnScreen = true, true
	return d
}

// The deck's Music library button opens a fresh library over the drawn dashboard, as a card navigating
// to #techo5-library does, and the deck gives way. Without a drawn dashboard to open it over, it says
// so at the foot of the deck. From Home Assistant's deck_press it is the same.
func TestTheDecksLibraryButton(t *testing.T) {
	noLibrary(t)
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	b := config.DeckButton{Action: config.DeckScreenLibrary}
	for _, mode := range []config.DashboardMode{config.DashboardStreamed, config.DashboardOff} {
		_ = config.Set().Dashboard().Mode(mode)
		d := deckUpOn()
		err := d.screenPress(b)
		if !errors.Is(err, errScreenOff) || libUp(d) != nil || !d.deckUp {
			t.Errorf("%s: %v, library %v, deck up %v", mode, err, libUp(d) != nil, d.deckUp)
		}
		if r := deckReason(b, err); r != tr("deck_screen_off") {
			t.Errorf("%s: the foot says %q", mode, r)
		}
	}
	_ = config.Set().Dashboard().Mode(config.DashboardDrawn)
	for _, d := range []*Display{deckUpOn(), {poke: make(chan struct{}, 1)}} {
		if err := d.screenPress(b); err != nil {
			t.Fatal(err)
		}
		if libUp(d) == nil || !d.dash || d.deckUp {
			t.Errorf("library %v, dashboard up %v, deck up %v", libUp(d) != nil, d.dash, d.deckUp)
		}
	}
}

// The dashboard's button opens the dashboard, at its path when it has one, and the deck gives way.
// With the dashboard off it fails, and says so at the foot of the deck.
func TestTheDecksDashboardButton(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	_ = config.Set().Dashboard().Mode(config.DashboardOff)
	d := deckUpOn()
	b := config.DeckButton{Action: config.DeckScreenDashboard, Path: "/dashboard-energy/today"}
	err := d.screenPress(b)
	if !errors.Is(err, errScreenOff) || d.dash {
		t.Fatalf("with the dashboard off: %v, dashboard up %v", err, d.dash)
	}
	if r := deckReason(b, err); r != tr("deck_screen_off") {
		t.Errorf("the foot says %q", r)
	}

	_ = config.Set().Dashboard().Mode(config.DashboardStreamed)
	if err := d.screenPress(b); err != nil {
		t.Fatal(err)
	}
	if !d.dash || d.deckUp || config.Get().Dashboard.Path != "dashboard-energy/today" {
		t.Errorf("dashboard up %v, deck up %v, path %q", d.dash, d.deckUp, config.Get().Dashboard.Path)
	}
	// Without a path it opens as it was left.
	if err := d.screenPress(config.DeckButton{Action: config.DeckScreenDashboard}); err != nil || config.Get().Dashboard.Path != "dashboard-energy/today" {
		t.Errorf("err %v, path %q", err, config.Get().Dashboard.Path)
	}
}

// Now Playing needs something playing, and the forecast a weather entity; each says so when it has
// not. The forecast comes up over the deck, which stays up under it.
func TestTheDecksOtherScreenButtons(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	d := deckUpOn()
	np := config.DeckButton{Action: config.DeckScreenNowPlaying}
	if err := d.screenPress(np); !errors.Is(err, errNothingPlaying) || deckReason(np, err) != tr("deck_nothing_playing") {
		t.Errorf("with nothing playing: %v", err)
	}
	_ = config.Set().Home().Weather(config.WeatherOff)
	if err := d.screenPress(config.DeckButton{Action: config.DeckScreenWeather}); !errors.Is(err, errScreenOff) {
		t.Errorf("with no weather: %v", err)
	}
	_ = config.Set().Home().Weather("")
	if err := d.screenPress(config.DeckButton{Action: config.DeckScreenWeather}); err != nil {
		t.Fatal(err)
	}
	if !time.Now().Before(d.weatherUntil) || !d.deckUp {
		t.Errorf("forecast until %v, deck up %v", d.weatherUntil, d.deckUp)
	}
	if err := d.screenPress(config.DeckButton{Action: config.DeckScreenCalendar}); !errors.Is(err, errScreenOff) {
		t.Errorf("with no calendar: %v", err)
	}
}

// A press of a screen button on the deck goes to the screen, not to the deck's OBS and computers, and
// a failure shows on the button with the screen's reason.
func TestAScreenButtonIsPressedOnTheScreen(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	_ = config.Set().Deck().Page(0, []config.DeckButton{{Action: config.DeckScreenNowPlaying}})
	_, sent := slowDeck(t)
	var screened []config.DeckButton
	was := deckScreenDo
	deckScreenDo = func(_ *Display, b config.DeckButton) error { screened = append(screened, b); return errNothingPlaying }
	t.Cleanup(func() { deckScreenDo = was })

	d := deckUpOn()
	d.pressDeck(0)
	p := shownPress(t, d, "the screen button's failure", func(p deckPress) bool { return !p.going && p.failed })
	if p.reason != tr("deck_nothing_playing") || sent.Load() != 0 || len(screened) != 1 {
		t.Errorf("reason %q, sent to the deck %d, to the screen %d", p.reason, sent.Load(), len(screened))
	}
}

// The foot of a page of the screen's own buttons says nothing of OBS, set up or not: those buttons
// do not need it. A page with an OBS button still does.
func TestTheFootSaysNothingOfOBSWithoutOBSButtons(t *testing.T) {
	for _, v := range []deckView{{}, {obsSet: true}, {obsSet: true, problem: "refused"}} {
		if line, _ := v.footLine(); line != "" {
			t.Errorf("%+v: %q", v, line)
		}
	}
	if line, _ := (deckView{obs: true}).footLine(); line != tr("obs_is_not_set_up") {
		t.Errorf("a page with an OBS button: %q", line)
	}
	cfg := config.Deck{Pages: []config.DeckPage{
		{Buttons: []config.DeckButton{{Action: config.DeckScreenLibrary}, {Action: config.DeckPCKeys}}},
		{Buttons: []config.DeckButton{{Action: config.DeckScreenRadio}, {Action: config.DeckOBSStream}}},
	}}
	if deckPageOBS(cfg, 0) || !deckPageOBS(cfg, 1) {
		t.Error("which pages have OBS buttons")
	}
}

// With no buttons of its own, Tap on the clock set to Deck opens the built-in page of the screen's
// pages: the library first when Music Assistant is set up, and only the pages there are to open. The
// swipe up from the bottom edge does not: it stays the volume until the deck has buttons.
func TestTheBuiltInLauncherPage(t *testing.T) {
	noLibrary(t)
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	_ = config.Set().Dashboard().Mode(config.DashboardOff)
	d := &Display{poke: make(chan struct{}, 1)}
	if d.openDeck() {
		t.Fatal("the deck opened with no buttons")
	}
	if !d.openLauncher() || !d.deckUp || !d.deckLauncher {
		t.Fatalf("the launcher did not open: up %v, launcher %v", d.deckUp, d.deckLauncher)
	}
	actions := func() []config.DeckAction {
		d.mu.Lock()
		defer d.mu.Unlock()
		var out []config.DeckAction
		for _, b := range d.deckNowLocked().Pages[0].Buttons {
			out = append(out, b.Action)
		}
		return out
	}
	want := []config.DeckAction{config.DeckScreenWeather, config.DeckScreenRadio, config.DeckScreenCameras}
	if got := actions(); !slices.Equal(got, want) {
		t.Errorf("with no library, dashboard or calendar: %v", got)
	}
	libClient = func() (musicassistant.Client, bool) { return musicassistant.Client{URL: "http://ma.invalid"}, true }
	_ = config.Set().Dashboard().Mode(config.DashboardStreamed)
	if got := actions(); len(got) != 4 || got[0] != config.DeckScreenDashboard {
		t.Errorf("with a streamed dashboard, which the library can't open over: %v", got)
	}
	_ = config.Set().Dashboard().Mode(config.DashboardDrawn)
	if got := actions(); len(got) != 5 || got[0] != config.DeckScreenLibrary || got[1] != config.DeckScreenDashboard {
		t.Errorf("with the library and a drawn dashboard: %v", got)
	}
	cols, rows := deckLauncherPage().Grid()
	if cols != 3 || rows != 2 {
		t.Errorf("the launcher's grid is %dx%d", cols, rows)
	}
	// Nobody wrote its labels, so they are the screen's language's words, not the action's English.
	_ = config.Set().Screen().Language("de")
	if b := deckLauncherPage().Pages[0].Buttons[0]; b.Label != tr("music") || b.Label == "Music" {
		t.Errorf("the library button's label on a German screen: %q", b.Label)
	}
	_ = config.Set().Screen().Language("")

	// A deck of its own is what opens once it has buttons, never the built-in page.
	_ = config.Set().Deck().Page(0, []config.DeckButton{{Action: config.DeckOBSStream}})
	if !d.openLauncher() || d.deckLauncher {
		t.Error("the deck's own buttons did not replace the built-in page")
	}
}
