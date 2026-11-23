//go:build !dot && !spot

package display

import (
	"context"
	"image"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/feature/dashboard"
	"github.com/HuskerMinion/techo5/echod/internal/feature/voice"
	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

// Each Tap on the clock choice is kept and read back; nothing chosen, or a value no choice has, is
// Assist, as a tap always was. Meals is the last choice, and the Spot's three are still the first three.
func TestClockTapSetting(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	if clockTapIndex() != 0 {
		t.Fatalf("default is %s", clockTaps[clockTapIndex()].label)
	}
	s := clockTapSelect()
	for _, c := range []struct{ label, kept string }{{"Dashboard", "dashboard"}, {"Nothing", "nothing"}, {"Deck", "deck"}, {"Meals", "meals"}, {"Assist", ""}} {
		s.OnCommand(c.label)
		if got := config.Get().Screen.ClockTap; got != c.kept {
			t.Errorf("%s: kept %q, want %q", c.label, got, c.kept)
		}
		if got := clockTaps[clockTapIndex()].label; got != c.label {
			t.Errorf("%s: read back as %s", c.label, got)
		}
	}
	if err := config.Set().Screen().ClockTap("sideways"); err != nil {
		t.Fatal(err)
	}
	if clockTapIndex() != 0 {
		t.Error("an unknown value is not Assist")
	}
	s.OnCommand("Assist")
	if last := clockTaps[len(clockTaps)-1]; last.label != "Meals" || last.value != "meals" {
		t.Errorf("the last choice is %v, want Meals", last)
	}
	if got := clockTaps[:3]; got[0].label != "Assist" || got[1].label != "Dashboard" || got[2].label != "Nothing" {
		t.Errorf("the Spot's choices are %v, want Assist, Dashboard and Nothing", got)
	}
}

// A tap on the idle clock set to Meals puts the Meals page up, with Mealie set up.
func TestClockTapMeals(t *testing.T) {
	mealsSetUp(t)
	clockTapSelect().OnCommand("Meals")
	if got := config.Get().Screen.ClockTap; got != "meals" {
		t.Fatalf("kept %q, want meals", got)
	}
	if got := clockTapOptions()[clockTapIndex()]; got != "Meals" {
		t.Fatalf("read back as %s", got)
	}
	d := &Display{on: true, poke: make(chan struct{}, 1)}
	d.view = voice.State{Phase: "idle"}
	tapAt(d, image.Pt(480, 240))
	if d.customs.up != mealsID {
		t.Fatalf("a tap on the clock set to Meals put up %q, want %q", d.customs.up, mealsID)
	}
}

// With Mealie not set up, a tap meant for Meals does nothing of Meals' own and asks no server: it
// falls through, as Assist does with no microphone, to the dashboard.
func TestClockTapMealsNotSetUpFallsBack(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	if err := config.Set().Screen().ClockTap("meals"); err != nil {
		t.Fatal(err)
	}
	if err := config.Set().Dashboard().Mode(config.DashboardDrawn); err != nil {
		t.Fatal(err)
	}
	var asked atomic.Bool
	wasHub, wasViews, wasMic := meals, mealsViews, hasMicrophone
	meals = &mealsHub{}
	mealsViews = []mealsView{mealsTestView("shopping", 1, func(context.Context, mealie.Client) (any, error) {
		asked.Store(true)
		return nil, nil
	}, nil)}
	hasMicrophone = func() bool { return false } // no voice turn here, so the fall-through is the dashboard
	t.Cleanup(func() {
		meals.wg.Wait()
		meals, mealsViews, hasMicrophone = wasHub, wasViews, wasMic
	})

	d := &Display{on: true, poke: make(chan struct{}, 1)}
	d.view = voice.State{Phase: "idle"}
	tapAt(d, image.Pt(480, 240))
	if d.customs.up != "" {
		t.Errorf("a tap with Mealie not set up put up the custom screen %q", d.customs.up)
	}
	if !d.dash {
		t.Error("a tap with Mealie not set up did not fall through to the dashboard")
	}
	meals.wg.Wait()
	if asked.Load() {
		t.Error("a tap with Mealie not set up asked the server")
	}
}

// A dashboard opened by hand comes back to the clock after the Dashboard returns setting, and stays
// with Never.
func TestDashboardReturnsAfterTheSetting(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	f := dashboard.Get()
	t.Cleanup(func() { f.SetBack("10 minutes") }) // the tests after this one expect the default
	check := func(choice string, untouched time.Duration, want bool) {
		t.Helper()
		f.SetBack(choice)
		d := &Display{}
		d.dash, d.dashTouched = true, time.Now().Add(-untouched)
		d.dashScene(&scene{phase: "idle"}, false)
		if d.dash == want {
			t.Errorf("%s, untouched %v: still up %v", choice, untouched, d.dash)
		}
	}
	check("1 minute", 90*time.Second, true)
	check("1 minute", 30*time.Second, false)
	check("10 minutes", 9*time.Minute, false)
	check("10 minutes", 11*time.Minute, true)
	check("Never", 24*time.Hour, false)
}
