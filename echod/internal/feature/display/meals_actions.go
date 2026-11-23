//go:build !dot && !spot

package display

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

// The Meals page's two actions for Home Assistant: meals_show puts the page up at a view, or at a recipe
// ready to cook, and meals_step moves the recipe being cooked, so that a voice assistant or an
// automation can say "next step" to a cook whose hands are full. Both answer with what is now on the
// screen. They are registered with the Show's other actions (Display.Actions in night_show.go).

const (
	// mealsActionMost is how long an action waits for the server to tell it a name or the day's plan:
	// somebody is waiting for the answer, so less than a page's own fetch is given (mealsLoadMost).
	mealsActionMost = 10 * time.Second

	// mealsActionOff is what an action says on a device the setup page has not given a Mealie address and
	// token. It is said before anything is asked of the network.
	mealsActionOff = "meals: Mealie is not set up on this device"
)

// mealsAnswer is what meals_show says: the view it put up, and the name of the recipe or the list on it.
type mealsAnswer struct {
	Page  string `json:"page"`
	Title string `json:"title"`
}

// mealsStepAnswer is what meals_step says: the page of the recipe now on the screen, how many steps it
// has, and the step's words without the markup. Step 0 is the overview.
type mealsStepAnswer struct {
	Step int    `json:"step"`
	Of   int    `json:"of"`
	Text string `json:"text"`
}

// mealsActions are the actions of the Meals page.
func (d *Display) mealsActions() []*esphome.Action {
	return []*esphome.Action{{
		Name: "meals_show",
		Args: []esphome.Arg{
			{Name: "page", Type: esphome.ArgString, Example: "today",
				Description: "shopping, plan, today, recipe or cooking"},
			{Name: "recipe", Type: esphome.ArgString, Example: "tomato-soup",
				Description: "the recipe's slug or id; only read when page is recipe, else leave empty"},
		},
		Answers: true,
		Run:     func(c esphome.Call) (any, error) { return d.mealsShow(c.String("page"), c.String("recipe")) },
	}, {
		Name: "meals_step",
		Args: []esphome.Arg{
			{Name: "step", Type: esphome.ArgString, Example: "next",
				Description: "next, back, first, overview, a step's number, or timer to start the step's timer"},
		},
		Answers: true,
		Run:     func(c esphome.Call) (any, error) { return d.mealsStep(c.String("step")) },
	}}
}

// mealsShow is the meals_show action. The recipe is read only for page recipe, so that an automation can
// pass the same fields every time.
//
//   - shopping and plan put those views up.
//   - recipe cooks the recipe named, at the scale it is written for.
//   - today cooks today's dinner, else the first recipe planned for today, else shows the plan.
//   - cooking returns to the recipe cooked last, else is today.
func (d *Display) mealsShow(page, recipe string) (any, error) {
	if !config.Get().Mealie.Set() {
		return nil, errors.New(mealsActionOff)
	}
	page = strings.ToLower(strings.TrimSpace(page))
	recipe = strings.TrimSpace(recipe)
	switch page {
	case "shopping", "plan", "today", "recipe", "cooking":
	default:
		return nil, fmt.Errorf("meals: unknown page %q", page)
	}
	if page == "recipe" && recipe == "" {
		return nil, errors.New("meals: page=recipe needs a recipe")
	}
	slog.Info("meals: asked to show", "page", page)

	ctx, cancel := context.WithTimeout(context.Background(), mealsActionMost)
	defer cancel()
	c := mealsClient()

	var ans mealsAnswer
	switch page {
	case "shopping":
		d.mealsView("shopping")
		ans = mealsAnswer{Page: "shopping", Title: mealsListName(ctx, c)}
	case "plan":
		d.mealsView("plan")
		ans = mealsAnswer{Page: "plan"}
	case "recipe":
		r, err := c.Recipe(ctx, recipe)
		switch {
		case errors.Is(err, mealie.ErrNotFound):
			return nil, fmt.Errorf("meals: Mealie has no recipe %q", recipe)
		case err != nil:
			// The recipe is opened all the same: the cooking view says what it could not load, in words
			// on the screen. Only the name is missing from the answer.
			slog.Warn("meals: a recipe's name could not be read", "kind", mealsKind(err))
		}
		if !d.openRecipe(recipe, 1) {
			return nil, errors.New(mealsActionOff)
		}
		ans = mealsAnswer{Page: "cooking", Title: r.Name}
	case "cooking":
		if slug, scale, ok := mealsOpenRecipe(); ok {
			if !d.openRecipe(slug, scale) {
				return nil, errors.New(mealsActionOff)
			}
			ans = mealsAnswer{Page: "cooking", Title: mealsRecipeName(ctx, c, slug)}
			break
		}
		fallthrough
	case "today":
		var err error
		if ans, err = d.mealsToday(ctx, c); err != nil {
			return nil, err
		}
	}
	d.askedAtNight(time.Now())
	d.wake()
	return ans, nil
}

// mealsToday opens what is for dinner today: the dinner's recipe, else the first recipe planned for the
// day, else the plan, for there is nothing to cook.
func (d *Display) mealsToday(ctx context.Context, c mealie.Client) (mealsAnswer, error) {
	now := time.Now()
	entries, err := c.Plan(ctx, now, now, false)
	if err != nil {
		slog.Warn("meals: the day's plan could not be read", "kind", mealsKind(err))
		return mealsAnswer{}, errors.New("meals: could not read today's meal plan from Mealie")
	}
	mealie.SortDay(entries)
	var pick *mealie.Entry
	for i, e := range entries {
		if !e.Cookable() {
			continue
		}
		if pick == nil {
			pick = &entries[i]
		}
		if e.Type == "dinner" {
			pick = &entries[i]
			break
		}
	}
	if pick == nil {
		d.mealsView("plan")
		return mealsAnswer{Page: "plan"}, nil
	}
	if !d.openRecipe(pick.Recipe.Slug, pick.StartScale()) {
		return mealsAnswer{}, errors.New(mealsActionOff)
	}
	return mealsAnswer{Page: "cooking", Title: pick.Name()}, nil
}

// mealsView puts the page up at a view, as a tap on the switcher would, and wakes the screen.
func (d *Display) mealsView(id string) {
	meals.show(id)
	d.mu.Lock()
	up := d.customs.shown == mealsID
	d.mu.Unlock()
	if !up {
		d.openCustom(mealsID)
		return
	}
	d.wake()
}

// mealsRecipeName is the name of the recipe being cooked, from the cooking state when it is the one asked
// for (the page may not have drawn it yet), else from the server; "" when neither can say.
func mealsRecipeName(ctx context.Context, c mealie.Client, slug string) string {
	if k := mealsCookKept(); k != nil && (k.recipe.Slug == slug || k.recipe.ID == slug) {
		return k.recipe.Name
	}
	r, err := c.Recipe(ctx, slug)
	if err != nil {
		return ""
	}
	return r.Name
}

// mealsListName is the name of the shopping list the page shows: the one the setup page chose, else the
// first. It is "" when the server cannot say, and the list's own name when it has none is the screen's
// word for it.
func mealsListName(ctx context.Context, c mealie.Client) string {
	lists, err := c.Lists(ctx)
	if err != nil || len(lists) == 0 {
		return ""
	}
	pick := lists[0]
	for _, l := range lists {
		if want := config.Get().Mealie.List; want != "" && l.ID == want {
			pick = l
		}
	}
	if strings.TrimSpace(pick.Name) == "" {
		return tr("meals_shopping_list")
	}
	return pick.Name
}

// mealsStep is the meals_step action. It moves only a recipe that is on the page: the Cooking view is the
// view on the screen, whether the screen is lit or not, and the recipe is the one the cook asked for.
func (d *Display) mealsStep(step string) (any, error) {
	if !config.Get().Mealie.Set() {
		return nil, errors.New(mealsActionOff)
	}
	c := d.mealsCooked()
	if c == nil {
		return nil, errors.New("meals: nothing is being cooked on this device")
	}
	step = strings.ToLower(strings.TrimSpace(step))
	switch step {
	case "next":
		c.Next()
	case "back":
		c.Back()
	case "first":
		c.Go(1)
	case "overview":
		c.Go(0)
	case "timer":
		pills := c.Pills()
		if len(pills) == 0 {
			return nil, errors.New("meals: this step has no timer")
		}
		started := false
		for i, p := range pills {
			if p.Started.IsZero() {
				started = c.StartPill(i)
				break
			}
		}
		if !started {
			return nil, errors.New("meals: this step's timer is already running")
		}
	default:
		n, err := strconv.Atoi(step)
		if err != nil {
			return nil, fmt.Errorf("meals: unknown step %q", step)
		}
		if of := c.Steps(); n < 1 || n > of {
			return nil, fmt.Errorf("meals: step %d is not in this recipe, which has %d", n, of)
		}
		c.Go(n)
	}
	slog.Info("meals: asked to step", "step", step)
	d.askedAtNight(time.Now())
	d.wake()
	return mealsStepAnswer{Step: c.Step(), Of: c.Steps(), Text: mealie.Plain(c.Text())}, nil
}

// mealsCooked is the recipe being cooked when the Cooking view is the one on the screen, else nil. The
// recipe a cook walked away from (the page put away, or on another view) is not moved from a voice in the
// next room.
func (d *Display) mealsCooked() *mealsCook {
	d.mu.Lock()
	up := d.customs.up == mealsID || d.customs.shown == mealsID
	d.mu.Unlock()
	if !up {
		return nil
	}
	meals.mu.Lock()
	v, ok := meals.current()
	meals.mu.Unlock()
	if !ok || v.id != "cooking" {
		return nil
	}
	return mealsCookedOpen()
}
