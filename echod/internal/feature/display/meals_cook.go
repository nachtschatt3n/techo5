//go:build !dot && !spot

package display

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/feature/timer"
	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

// The cooking model: what the Meals page remembers about the recipe being cooked. It is the state the
// Cooking view draws from and the Home Assistant actions move, kept apart from both so that it has no
// drawing, no touch and no text in it: which step the cook is at, how many servings they asked for,
// which ingredients the step uses or the cook has set aside, which timers a step offers and which of
// them have been started, and whether the screen should be held awake meanwhile.

const (
	// cookMaxScale is the most a recipe is multiplied by, four times what it is written for.
	cookMaxScale = 4
	// cookAwakeMost is how long the screen is held awake without a touch or a step action. A cook who
	// walked away from a lit screen should not leave it lit all night.
	cookAwakeMost = 3 * time.Hour
	// cookPills is the most timer buttons a step offers.
	cookPills = 3
	// cookNameMost is the longest a timer's name is, in characters: the timer's own screen is small.
	cookNameMost = 24
)

// mealsNow is the time the cooking model goes by. A test sets it.
var mealsNow = time.Now

// mealsTimerStart sets a timer of the device's own, and returns its id. A test swaps it.
var mealsTimerStart = func(name string, d time.Duration) string { return timer.Get().Start(name, d) }

// cookPill is one timer a step offers.
type cookPill struct {
	// Text is the time as the step wrote it ("10-15 Min."), or the step's duration as a clock when the
	// recipe gave one.
	Text string
	// D is how long the timer runs.
	D time.Duration
	// Started is when it was started here; the zero time while it has not been.
	Started time.Time
}

// Left is how much of the pill's time is left at now; 0 when it was not started or has run out.
func (p cookPill) Left(now time.Time) time.Duration {
	if p.Started.IsZero() {
		return 0
	}
	return max(p.D-now.Sub(p.Started), 0)
}

// cookLine is one line of the ingredients.
type cookLine struct {
	// Section is the heading the recipe puts above the line, "" for none.
	Section string
	// Text is the line at the chosen scale; empty for a heading with no ingredient under it.
	Text string
	// Scaled is whether Text is the line's amount at the scale. A line that cannot be scaled keeps the
	// words the server wrote.
	Scaled bool
	// Used is whether the step on the screen refers to the line.
	Used bool
	// Ticked is whether the cook has set the line aside.
	Ticked bool
}

// mealsCook is one recipe being cooked.
type mealsCook struct {
	mu     sync.Mutex
	recipe mealie.Recipe
	step   int     // 0 is the overview, 1 to len(steps) the steps
	scale  float64 // the multiple of the recipe's own amounts
	ticked map[int]bool
	pills  map[[2]int]time.Time // (step, pill) to when its timer was started
	// starting are the pills whose timer is being set as the device's own: the write to flash and the
	// publish to Home Assistant are done with no lock held, and a second tap meanwhile finds it taken.
	starting map[[2]int]bool
	touched  time.Time // the last touch or action: the keep-awake cap counts from here
}

// newMealsCook starts on the overview at scale times the recipe's amounts. A scale of 0 or less is 1,
// and one past what the screen allows is cut to it.
func newMealsCook(r mealie.Recipe, scale float64) *mealsCook {
	c := &mealsCook{recipe: r, ticked: map[int]bool{}, pills: map[[2]int]time.Time{}, touched: mealsNow()}
	c.scale = c.clamp(scale)
	return c
}

// clamp is a scale the screen allows: at least one serving when the recipe says how many it makes,
// else half the recipe, and at most four times it. Anything that is not a number is 1.
func (c *mealsCook) clamp(s float64) float64 {
	if s <= 0 || math.IsNaN(s) || math.IsInf(s, 0) {
		s = 1
	}
	return min(max(s, c.floor()), cookMaxScale)
}

// floor is the least the scale goes to: one serving when the recipe says how many it makes, else half
// the recipe.
func (c *mealsCook) floor() float64 {
	if b := c.recipe.Base(); b > 0 {
		return min(1/b, 1)
	}
	return 0.5
}

// Steps is the number of steps; the overview is not one of them.
func (c *mealsCook) Steps() int { return len(c.recipe.Steps) }

// Step is the page the cook is on: 0 for the overview, then 1 to Steps.
func (c *mealsCook) Step() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.step
}

// Go moves to a page and reports whether it moved. The index is cut to the overview and the last step,
// never wrapped, so a recipe with no steps has only the overview.
func (c *mealsCook) Go(n int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.goLocked(n)
}

// goLocked is Go with c.mu held.
func (c *mealsCook) goLocked(n int) bool {
	c.touched = mealsNow()
	n = min(max(n, 0), len(c.recipe.Steps))
	if n == c.step {
		return false
	}
	c.step = n
	return true
}

// Next moves on a page. It reports false on the last one, where the view's button is "Finish". The page
// it is on and the page it moves to are read and written in one hold, so a tap and the voice assistant's
// "next" at the same moment are two pages and not one.
func (c *mealsCook) Next() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.goLocked(c.step + 1)
}

// Back moves back a page, false on the overview.
func (c *mealsCook) Back() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.goLocked(c.step - 1)
}

// Last is whether the cook is on the last page, the one where Next finishes.
func (c *mealsCook) Last() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.step >= len(c.recipe.Steps)
}

// Text is the words of the page the cook is on: the step's text, else its title, else its summary; ""
// for the overview.
func (c *mealsCook) Text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.current(); ok {
		return s.Body()
	}
	return ""
}

// Summary is the step's own short heading, "" when it has none or on the overview.
func (c *mealsCook) Summary() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.current(); ok {
		return strings.TrimSpace(s.Summary)
	}
	return ""
}

// current is the step on the screen. Called with c.mu held.
func (c *mealsCook) current() (mealie.Step, bool) {
	if c.step < 1 || c.step > len(c.recipe.Steps) {
		return mealie.Step{}, false
	}
	return c.recipe.Steps[c.step-1], true
}

// Scale is the multiple of the recipe's amounts.
func (c *mealsCook) Scale() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scale
}

// Servings is the number of servings at the scale, and false for a recipe that does not say how many it
// makes: the view then shows the multiple ("x1.5") instead.
func (c *mealsCook) Servings() (float64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.recipe.Base()
	return b * c.scale, b > 0
}

// More raises the scale a step, and Less lowers it: a serving when the recipe says how many it makes,
// else half a recipe. They report whether the scale moved.
func (c *mealsCook) More() bool { return c.rescale(1) }

// Less lowers the scale a step. See More.
func (c *mealsCook) Less() bool { return c.rescale(-1) }

func (c *mealsCook) rescale(dir float64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.touched = mealsNow()
	var next float64
	if b := c.recipe.Base(); b > 0 {
		// Whole servings, from the one the scale is at: a recipe for 4 asked for 6 and then lowered is 5.
		// Lowered past the last serving it stays there: clamp would take a scale of 0 for 1.
		next = c.clamp(max((math.Round(b*c.scale)+dir)/b, c.floor()))
	} else {
		next = c.clamp(max(c.scale+dir/2, c.floor()))
	}
	if next == c.scale {
		return false
	}
	c.scale = next
	return true
}

// Lines are the ingredients at the scale, with the ones the step on the screen refers to marked.
func (c *mealsCook) Lines() []cookLine {
	c.mu.Lock()
	defer c.mu.Unlock()
	var used map[int]bool
	if s, ok := c.current(); ok {
		used = map[int]bool{}
		for _, i := range c.recipe.Uses(s) {
			used[i] = true
		}
	}
	out := make([]cookLine, len(c.recipe.Ingredients))
	for i, ing := range c.recipe.Ingredients {
		text, scaled := mealie.Amount(ing, c.scale)
		out[i] = cookLine{Section: strings.TrimSpace(ing.Title), Text: text, Scaled: scaled, Used: used[i], Ticked: c.ticked[i]}
	}
	return out
}

// Uses is the indexes of the ingredients the step on the screen refers to. A reference to an ingredient
// the recipe no longer has is ignored.
func (c *mealsCook) Uses() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.current(); ok {
		return c.recipe.Uses(s)
	}
	return nil
}

// NotScaled is whether the view says "Some amounts aren't scaled": the scale is not 1 and a line with
// something to read in it cannot be scaled. A line that is only a heading does not count.
func (c *mealsCook) NotScaled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.scale == 1 {
		return false
	}
	for _, ing := range c.recipe.Ingredients {
		if text, scaled := mealie.Amount(ing, c.scale); text != "" && !scaled {
			return true
		}
	}
	return false
}

// Tick sets an ingredient aside, or takes it back. It is the cook's own mise en place and is not saved.
func (c *mealsCook) Tick(i int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.touched = mealsNow()
	if i < 0 || i >= len(c.recipe.Ingredients) {
		return false
	}
	c.ticked[i] = !c.ticked[i]
	return true
}

// Pills are the timers the step on the screen offers: its own duration when the recipe gave one, else
// the times written in its text, three at most.
func (c *mealsCook) Pills() []cookPill {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pillsLocked()
}

func (c *mealsCook) pillsLocked() []cookPill {
	s, ok := c.current()
	if !ok {
		return nil
	}
	var out []cookPill
	if s.DurationSeconds > 0 {
		d := time.Duration(s.DurationSeconds) * time.Second
		out = []cookPill{{Text: cookClock(d), D: d}}
	} else {
		for _, f := range mealie.Durations(mealie.Plain(s.Text)) {
			out = append(out, cookPill{Text: f.Text, D: f.D})
		}
	}
	if len(out) > cookPills {
		out = out[:cookPills]
	}
	for i := range out {
		out[i].Started = c.pills[[2]int{c.step, i}]
	}
	return out
}

// StartPill starts the i'th timer of the step on the screen as a timer of the device, and reports
// whether it did. A pill that has been started once stays started for the recipe: a second tap does
// nothing, since cancelling is on the timers' own screen.
func (c *mealsCook) StartPill(i int) bool {
	c.mu.Lock()
	c.touched = mealsNow()
	pills := c.pillsLocked()
	key := [2]int{c.step, i}
	if i < 0 || i >= len(pills) || !pills[i].Started.IsZero() || c.starting[key] {
		c.mu.Unlock()
		return false
	}
	s, _ := c.current()
	name := cookTimerName(s, c.recipe.Name, c.step)
	d := pills[i].D
	if c.starting == nil {
		c.starting = map[[2]int]bool{}
	}
	c.starting[key] = true
	c.mu.Unlock()

	// Setting the timer saves the timers to flash and tells Home Assistant: the display asks this model
	// for its frame, so it does that with the lock let go.
	id := mealsTimerStart(name, d)

	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.starting, key)
	if id == "" {
		return false
	}
	c.pills[key] = mealsNow()
	return true
}

// cookTimerName is what a step's timer is called: the step's own short heading, else the recipe's name
// and the step's number. It is cut to cookNameMost characters.
func cookTimerName(s mealie.Step, recipe string, n int) string {
	name := strings.TrimSpace(s.Summary)
	if name == "" || len([]rune(name)) > cookNameMost {
		name = fmt.Sprintf("%s %d", strings.TrimSpace(recipe), n)
		if strings.TrimSpace(recipe) == "" {
			name = fmt.Sprintf("%d", n)
		}
	}
	if r := []rune(name); len(r) > cookNameMost {
		name = strings.TrimSpace(string(r[:cookNameMost]))
	}
	return name
}

// cookClock is a duration as a clock, "10:00" or "1:30:00", the same in every language.
func cookClock(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// Touched records that somebody is at the screen, for the keep-awake cap; the view calls it for every
// touch it takes.
func (c *mealsCook) Touched() {
	c.mu.Lock()
	c.touched = mealsNow()
	c.mu.Unlock()
}

// holds is whether the screen is held awake for this recipe: the setup page lets it stay lit while
// cooking, and somebody has touched it or moved a step within cookAwakeMost.
func (c *mealsCook) holds() bool {
	if config.Get().Mealie.Sleeps {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return mealsNow().Sub(c.touched) < cookAwakeMost
}

// cooking is the recipe being cooked, kept while the page is put away and for mealsKeep after the last
// touch, so reopening lands on the same step; nil when there is none.
var cooking struct {
	mu  sync.Mutex
	now *mealsCook
}

// mealsCookOpen is the cooking state for a recipe: the one kept, when the recipe is the same and was
// touched within mealsKeep, else a new one on the overview at scale.
func mealsCookOpen(r mealie.Recipe, scale float64) *mealsCook {
	cooking.mu.Lock()
	defer cooking.mu.Unlock()
	return mealsCookOpenLocked(r, scale)
}

// mealsCookOpenIf is mealsCookOpen for a fetch of the recipe that was open as slug, and it opens nothing,
// returning nil, when that is not the open recipe any more: the fetch was on its way while another recipe
// was opened, and what it brings must not take the place of the one being cooked. The check and the
// replacing are one hold of the lock that keeps the recipe.
func mealsCookOpenIf(slug string, r mealie.Recipe, scale float64) *mealsCook {
	cooking.mu.Lock()
	defer cooking.mu.Unlock()
	if open, _, ok := mealsOpenRecipe(); !ok || open != slug {
		return nil
	}
	return mealsCookOpenLocked(r, scale)
}

// mealsCookOpenLocked is mealsCookOpen with cooking.mu held.
func mealsCookOpenLocked(r mealie.Recipe, scale float64) *mealsCook {
	if c := cooking.now; c != nil && c.sameRecipe(r) && mealsNow().Sub(c.lastTouched()) <= mealsKeep {
		c.Touched()
		return c
	}
	cooking.now = newMealsCook(r, scale)
	return cooking.now
}

// mealsCooking is the recipe being cooked, nil when there is none. It does not expire by itself: the
// keep-awake cap (cookAwakeMost) ends the hold of a screen nobody touches, and mealsKeep is only asked
// when a recipe is opened again.
func mealsCooking() *mealsCook {
	cooking.mu.Lock()
	defer cooking.mu.Unlock()
	return cooking.now
}

// mealsCookKept is the recipe being cooked if it was touched within mealsKeep, else nil: what an action
// that moves a step on a page that was put away may still reach.
func mealsCookKept() *mealsCook {
	c := mealsCooking()
	if c == nil || mealsNow().Sub(c.lastTouched()) > mealsKeep {
		return nil
	}
	return c
}

// mealsCookedOpen is the recipe being cooked if it is the one that is open on the page: while another
// recipe is being loaded the one kept is still the one from before, and an action must not move that.
func mealsCookedOpen() *mealsCook {
	c := mealsCookKept()
	if c == nil {
		return nil
	}
	meals.mu.Lock()
	slug := meals.recipe.slug
	meals.mu.Unlock()
	if slug == "" || (c.recipe.Slug != slug && c.recipe.ID != slug) {
		return nil
	}
	return c
}

// mealsCookClose forgets the recipe being cooked.
func mealsCookClose() {
	cooking.mu.Lock()
	cooking.now = nil
	cooking.mu.Unlock()
}

func (c *mealsCook) lastTouched() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.touched
}

func (c *mealsCook) sameRecipe(r mealie.Recipe) bool {
	if c.recipe.ID != "" || r.ID != "" {
		return c.recipe.ID == r.ID
	}
	return c.recipe.Slug == r.Slug
}

// mealsCookAwake and mealsCookNight are the Cooking view's hooks (mealsView.awake and .night): while a
// recipe is being cooked and the setup page has not let the screen sleep, the screen stays lit and a page
// asked for at night keeps the night hold, until the cap runs out.
func mealsCookAwake() bool {
	c := mealsCooking()
	return c != nil && c.holds()
}

func mealsCookNight() bool { return mealsCookAwake() }
