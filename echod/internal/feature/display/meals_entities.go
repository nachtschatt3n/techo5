//go:build !dot && !spot

package display

import (
	"fmt"
	"log/slog"
	"sync"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/feature/setup"
	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

// The Meals page's two choices in Home Assistant, the same as on the setup page: which shopping list it
// shows and whether the screen stays on while a recipe is open. The server and its token are the setup
// page's alone, as Music Assistant's are, since a token has no place in an entity.
//
// The list is a select of names, and a name is only known once Mealie has been asked. It is never asked
// for here, and never on a timer: the options change when the setup card has just asked (setup.MealieNoted)
// or when the Meals page has loaded the lists to show them (mealsNoteLists). Until then, and whenever
// Mealie cannot be reached, the saved choice is the one option besides "The first list", so that Home
// Assistant never shows a choice blank and a save from it never loses one.

const (
	// mealsFirstList is the choice that is no list in particular: Mealie's first one, whatever it is called.
	mealsFirstList = "The first list"
	// mealsSavedList stands for a saved list the server does not name now (gone, or not asked yet).
	mealsSavedList = "The saved list"
	// mealsListFallback is what a list with no name is called, as on the setup page.
	mealsListFallback = "Shopping list"
)

type mealsEntities struct {
	list  *esphome.Select
	awake *esphome.Switch

	mu sync.Mutex
	// lists are the shopping lists the server last named; empty until it has.
	lists []mealie.ListSummary
	// ids is the id behind each option label, as offered now.
	ids map[string]string
}

func (d *Display) buildMealsEntities() {
	m := &d.meals
	m.list = &esphome.Select{
		Base: esphome.Base{
			ObjectID: "meals_shopping_list",
			Name:     "Meals shopping list",
			Icon:     "mdi:cart-outline",
			Category: esphome.CategoryConfig,
		},
	}
	m.list.OnCommand = func(label string) {
		m.mu.Lock()
		id, ok := m.ids[label]
		m.mu.Unlock()
		if !ok {
			slog.Warn("meals: a shopping list that is not offered was chosen")
			return
		}
		if err := config.Set().Mealie().List(id); err != nil {
			slog.Error("saving the shopping list failed", "err", err)
			return
		}
		d.mealsChanged()
	}
	m.awake = &esphome.Switch{
		Base: esphome.Base{
			ObjectID: "meals_keep_awake",
			Name:     "Meals keep the screen on",
			Icon:     "mdi:chef-hat",
			Category: esphome.CategoryConfig,
		},
	}
	m.awake.OnCommand = func(on bool) {
		if err := config.Set().Mealie().Sleeps(!on); err != nil {
			slog.Error("saving the keep-awake setting failed", "err", err)
		}
		d.mealsChanged()
	}
	d.mealsChanged()
}

// listenMeals follows the setup page: a save changes what the entities say, and an answer from Mealie
// changes the names the select offers.
func (d *Display) listenMeals() {
	setup.MealieNoted.Listen(func(n setup.MealieNote) {
		if n.Listed {
			d.mealsNoteLists(n.Lists)
			return
		}
		d.mealsChanged()
	})
}

// mealsNoteLists is Mealie's answer to which shopping lists it has, from the setup card or the Meals page.
func (d *Display) mealsNoteLists(lists []mealie.ListSummary) {
	d.meals.mu.Lock()
	d.meals.lists = append([]mealie.ListSummary(nil), lists...)
	d.meals.mu.Unlock()
	d.mealsChanged()
}

// mealsOptions are the labels the select offers for the lists known and the id saved, and the id behind
// each. Two lists with one name get a number, so that each can be chosen.
func mealsOptions(lists []mealie.ListSummary, saved string) (labels []string, ids map[string]string) {
	ids = map[string]string{}
	add := func(name, id string) {
		label := name
		for n := 2; ; n++ {
			if _, taken := ids[label]; !taken {
				break
			}
			label = fmt.Sprintf("%s (%d)", name, n)
		}
		labels = append(labels, label)
		ids[label] = id
	}
	add(mealsFirstList, "")
	found := saved == ""
	for _, l := range lists {
		name := l.Name
		if name == "" {
			name = mealsListFallback
		}
		add(name, l.ID)
		found = found || l.ID == saved
	}
	if !found {
		add(mealsSavedList, saved)
	}
	return labels, ids
}

// mealsChanged shows the saved choices in Home Assistant: after a command, a save on the setup page, an
// answer from Mealie, and at the start.
func (d *Display) mealsChanged() {
	m := &d.meals
	if m.list == nil {
		return
	}
	c := config.Get().Mealie
	m.mu.Lock()
	labels, ids := mealsOptions(m.lists, c.List)
	m.ids = ids
	m.mu.Unlock()
	m.list.SetOptions(labels)
	for _, l := range labels {
		if ids[l] == c.List {
			m.list.Set(l)
			break
		}
	}
	m.awake.Set(!c.Sleeps)
}
