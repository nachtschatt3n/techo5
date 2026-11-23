//go:build !dot && !spot

package display

import (
	"path/filepath"
	"slices"
	"testing"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

func mealsEntitiesFor(t *testing.T) *Display {
	t.Helper()
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	d := &Display{}
	d.buildMealsEntities()
	return d
}

// The switch is the setup page's checkbox: on by default, and turning it off is what the config keeps as
// the screen being allowed to sleep.
func TestEntityMealsKeepAwake(t *testing.T) {
	d := mealsEntitiesFor(t)
	if !d.meals.awake.Get() {
		t.Error("the screen stays on while cooking by default")
	}
	d.meals.awake.OnCommand(false)
	if !config.Get().Mealie.Sleeps {
		t.Error("off did not let the screen sleep")
	}
	if d.meals.awake.Get() {
		t.Error("the switch does not show off")
	}
	d.meals.awake.OnCommand(true)
	if config.Get().Mealie.Sleeps || !d.meals.awake.Get() {
		t.Error("on did not keep the screen on")
	}
	// A save on the setup page shows in the entity.
	if err := config.Set().Mealie().Sleeps(true); err != nil {
		t.Fatal(err)
	}
	d.mealsChanged()
	if d.meals.awake.Get() {
		t.Error("a setup page save is not shown")
	}
}

// The options are the names Mealie gave, behind "The first list", and choosing one saves its id.
func TestEntityMealsShoppingListOptions(t *testing.T) {
	d := mealsEntitiesFor(t)
	if got := d.meals.list.Options; !slices.Equal(got, []string{mealsFirstList}) {
		t.Fatalf("before any answer: %q", got)
	}
	d.mealsNoteLists([]mealie.ListSummary{{ID: "a1", Name: "Weekly"}, {ID: "b2"}, {ID: "c3", Name: "Weekly"}})
	want := []string{mealsFirstList, "Weekly", mealsListFallback, "Weekly (2)"}
	if got := d.meals.list.Options; !slices.Equal(got, want) {
		t.Fatalf("options %q, want %q", got, want)
	}
	d.meals.list.OnCommand("Weekly (2)")
	if got := config.Get().Mealie.List; got != "c3" {
		t.Errorf("saved %q, want c3", got)
	}
	if got := d.meals.list.Get(); got != "Weekly (2)" {
		t.Errorf("shows %q", got)
	}
	d.meals.list.OnCommand(mealsFirstList)
	if got := config.Get().Mealie.List; got != "" {
		t.Errorf("the first list is kept as %q", got)
	}
	d.meals.list.OnCommand("A list nobody offered")
	if got := d.meals.list.Get(); got != mealsFirstList {
		t.Errorf("an unknown choice changed the entity to %q", got)
	}
}

// A saved list the server does not name, because it is unreachable or the list is gone, stays an option,
// and an answer with no lists changes nothing saved.
func TestEntityMealsListKeepsSaved(t *testing.T) {
	d := mealsEntitiesFor(t)
	if err := config.Set().Mealie().List("gone9"); err != nil {
		t.Fatal(err)
	}
	d.mealsChanged()
	if got := d.meals.list.Get(); got != mealsSavedList {
		t.Fatalf("unasked, shows %q", got)
	}
	d.mealsNoteLists([]mealie.ListSummary{{ID: "a1", Name: "Weekly"}})
	if got, want := d.meals.list.Options, []string{mealsFirstList, "Weekly", mealsSavedList}; !slices.Equal(got, want) {
		t.Fatalf("options %q, want %q", got, want)
	}
	d.mealsNoteLists([]mealie.ListSummary{{ID: "gone9", Name: "Back"}})
	if got := d.meals.list.Get(); got != "Back" {
		t.Errorf("once named it shows %q", got)
	}
	d.meals.list.OnCommand(mealsSavedList)
	if got := config.Get().Mealie.List; got != "gone9" {
		t.Errorf("choosing the saved option changed the id to %q", got)
	}
}

// mealsStarted is the device as a daemon started with Mealie set up or not finds it, for one test: what
// Home Assistant was told (mealsAtStart), with Meals the only custom screen there is, as on a Show.
func mealsStarted(t *testing.T, setUp bool) *Display {
	t.Helper()
	d := Get()
	wasListed, wasScreens := mealsAtStart.Load(), customScreens
	t.Cleanup(func() {
		mealsAtStart.Store(wasListed)
		customScreens = wasScreens
		d.mealsChanged()
	})
	customScreens = slices.DeleteFunc(slices.Clone(customScreens), func(c customScreen) bool { return c.id != mealsID })
	mealsAtStart.Store(setUp)
	return d
}

func mealsSetUpTo(t *testing.T, on bool) {
	t.Helper()
	tok := ""
	if on {
		tok = "token-for-a-test"
	}
	if err := config.Set().Mealie().Server("https://mealie.example.test", &tok); err != nil {
		t.Fatal(err)
	}
}

// What Home Assistant is told of Meals: nothing on a device without Mealie set up, neither entity and no
// Idle screen select, which Meals alone would have been the reason for, as on a device without Meals; all
// of it once a daemon starts with Mealie set up; and nothing again after the restart that follows taking
// it away. Between a save and its restart a Mealie taken away is no longer a choice of Idle screen.
func TestMealsInHomeAssistantOnlyWhenSetUp(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	listed := func(d *Display) (list, awake, idle bool) {
		ents := d.Entities()
		return slices.Contains(ents, esphome.Entity(d.meals.list)), slices.Contains(ents, esphome.Entity(d.meals.awake)),
			slices.Contains(ents, esphome.Entity(d.idleScreenSel))
	}

	d := mealsStarted(t, false)
	if list, awake, idle := listed(d); list || awake || idle {
		t.Fatalf("not set up: shopping list %v, keep awake %v, idle screen %v", list, awake, idle)
	}
	// Set up on the setup page: nothing shows before the restart the save asks for.
	mealsSetUpTo(t, true)
	d.mealsChanged()
	if list, awake, idle := listed(d); list || awake || idle {
		t.Fatalf("set up, before the restart: shopping list %v, keep awake %v, idle screen %v", list, awake, idle)
	}
	if _, ok := customByID(mealsID); ok {
		t.Fatal("Meals is offered before the restart")
	}

	// The restart: a daemon that finds Mealie set up.
	mealsAtStart.Store(true)
	d.mealsChanged()
	if list, awake, idle := listed(d); !list || !awake || !idle {
		t.Fatalf("set up: shopping list %v, keep awake %v, idle screen %v", list, awake, idle)
	}
	if got := idleScreenOptions(); !slices.Equal(got, []string{"Clock", "Meals"}) {
		t.Fatalf("set up: idle screen options %v", got)
	}
	d.idleScreenSel.OnCommand("Meals")
	if got := d.idleScreenSel.Get(); got != "Meals" {
		t.Fatalf("choosing Meals shows %q", got)
	}

	// Taken away: Meals is no choice from the save on, and the restart takes the rest.
	mealsSetUpTo(t, false)
	d.mealsChanged()
	if got := d.idleScreenSel.Options; !slices.Equal(got, []string{"Clock"}) {
		t.Fatalf("removed, before the restart: idle screen options %v", got)
	}
	if got := d.idleScreenSel.Get(); got != "Clock" {
		t.Fatalf("removed, before the restart: the idle screen shows %q", got)
	}
	mealsAtStart.Store(false)
	if list, awake, idle := listed(d); list || awake || idle {
		t.Fatalf("removed: shopping list %v, keep awake %v, idle screen %v", list, awake, idle)
	}
	_ = config.Set().Screen().IdleScreen("")
}
