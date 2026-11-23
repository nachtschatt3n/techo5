//go:build !dot && !spot

package display

import (
	"strings"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

// cookRecipe is a made-up recipe of three steps and four ingredients; the step texts have times in
// them, the ingredients have one that cannot be scaled.
func cookRecipe() mealie.Recipe {
	var r mealie.Recipe
	r.ID, r.Slug, r.Name, r.RecipeServings = "id-soup", "tomato-soup", "Tomato soup", 4
	r.Ingredients = []mealie.Ingredient{
		{ReferenceID: "a", Quantity: 2, Food: &mealie.Food{Name: "onion", PluralName: "onions"}, Display: "2 onions"},
		{ReferenceID: "b", Quantity: 500, Unit: &mealie.Unit{Name: "gram", PluralName: "grams", Abbreviation: "g", UseAbbreviation: true}, Food: &mealie.Food{Name: "tomatoes"}, Display: "500 g tomatoes"},
		{ReferenceID: "c", Quantity: 1, Display: "a pinch of salt"},
		{ReferenceID: "d", Title: "To serve"},
	}
	step := func(text string, refs ...string) mealie.Step {
		s := mealie.Step{Text: text}
		for _, id := range refs {
			s.IngredientReferences = append(s.IngredientReferences, struct {
				ReferenceID string `json:"referenceId"`
			}{id})
		}
		return s
	}
	r.Steps = []mealie.Step{
		step("Fry the **onions** for 10 minutes.", "a", "gone"),
		step("Add the tomatoes and simmer 20 Min. or eine halbe Stunde.", "b"),
		step("Serve."),
	}
	return r
}

// cookClockAt makes the model's time a clock the test moves, and puts the real one back.
func cookClockAt(t *testing.T) *time.Time {
	t.Helper()
	now := time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)
	was := mealsNow
	mealsNow = func() time.Time { return now }
	t.Cleanup(func() { mealsNow = was; mealsCookClose() })
	return &now
}

func TestCookStepBounds(t *testing.T) {
	cookClockAt(t)
	c := newMealsCook(cookRecipe(), 1)
	if c.Step() != 0 || c.Back() {
		t.Fatalf("a new cook starts on the overview and cannot go back, at %d", c.Step())
	}
	for i := 1; i <= 3; i++ {
		if !c.Next() || c.Step() != i {
			t.Fatalf("Next %d gave step %d", i, c.Step())
		}
	}
	if c.Next() || c.Step() != 3 || !c.Last() {
		t.Fatalf("Next past the last step moved to %d, or is not Last", c.Step())
	}
	if c.Go(99); c.Step() != 3 {
		t.Fatalf("Go(99) is the last step, got %d", c.Step())
	}
	if c.Go(-5); c.Step() != 0 {
		t.Fatalf("Go(-5) is the overview, got %d", c.Step())
	}

	// No steps: only the overview, and every move is safe.
	empty := newMealsCook(mealie.Recipe{RecipeSummary: mealie.RecipeSummary{Name: "Empty"}}, 1)
	if empty.Steps() != 0 || empty.Next() || empty.Back() || empty.Step() != 0 || !empty.Last() {
		t.Fatal("a recipe with no steps moves off its overview")
	}
	if empty.Text() != "" || len(empty.Pills()) != 0 || len(empty.Uses()) != 0 {
		t.Fatal("the overview of an empty recipe has a step's things")
	}

	// One step.
	one := cookRecipe()
	one.Steps = one.Steps[:1]
	o := newMealsCook(one, 1)
	if !o.Next() || o.Next() || o.Step() != 1 || !o.Last() {
		t.Fatalf("one step: at %d", o.Step())
	}
}

func TestCookScaleFromPlan(t *testing.T) {
	cookClockAt(t)
	c := newMealsCook(cookRecipe(), 1.5)
	if n, ok := c.Servings(); !ok || n != 6 {
		t.Fatalf("servings %v %v, want 6", n, ok)
	}
	c.Less()
	if n, _ := c.Servings(); n != 5 {
		t.Fatalf("a serving less is %v", n)
	}
	for range 40 {
		c.More()
	}
	if n, _ := c.Servings(); n != 16 || c.Scale() != 4 {
		t.Fatalf("most is 4x: %v servings, scale %v", n, c.Scale())
	}
	if c.More() {
		t.Fatal("More moved past the most")
	}
	for range 40 {
		c.Less()
	}
	if n, _ := c.Servings(); n != 1 {
		t.Fatalf("least is one serving, got %v", n)
	}
	if c.Less() {
		t.Fatal("Less moved past the least")
	}
	for _, in := range []float64{0, -2} {
		if got := newMealsCook(cookRecipe(), in).Scale(); got != 1 {
			t.Fatalf("scale %v starts at %v, want 1", in, got)
		}
	}
	if got := newMealsCook(cookRecipe(), 10).Scale(); got != 4 {
		t.Fatalf("scale 10 starts at %v, want 4", got)
	}
	lines := newMealsCook(cookRecipe(), 2).Lines()
	if lines[0].Text != "4 onions" || !lines[0].Scaled {
		t.Fatalf("2 onions doubled: %+v", lines[0])
	}
}

func TestCookScaleNoServings(t *testing.T) {
	cookClockAt(t)
	r := cookRecipe()
	r.RecipeServings = 0
	c := newMealsCook(r, 1)
	if _, ok := c.Servings(); ok {
		t.Fatal("a recipe with no servings reports some")
	}
	var seen []float64
	for range 8 {
		c.More()
		seen = append(seen, c.Scale())
	}
	want := []float64{1.5, 2, 2.5, 3, 3.5, 4, 4, 4}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("steps up %v, want %v", seen, want)
		}
	}
	for range 20 {
		c.Less()
	}
	if c.Scale() != 0.5 {
		t.Fatalf("least is half a recipe, got %v", c.Scale())
	}
	// The yield quantity stands in for servings.
	r.RecipeYieldQuantity = 8
	if n, ok := newMealsCook(r, 0.5).Servings(); !ok || n != 4 {
		t.Fatalf("yield quantity as servings: %v %v", n, ok)
	}
}

func TestCookUnparsedOnlyNote(t *testing.T) {
	cookClockAt(t)
	c := newMealsCook(cookRecipe(), 1)
	if c.NotScaled() {
		t.Fatal("the note shows at scale 1")
	}
	c.More()
	if !c.NotScaled() {
		t.Fatal("the pinch of salt is not scaled and the note does not say so")
	}
	if l := c.Lines(); l[2].Scaled || l[2].Text != "a pinch of salt" {
		t.Fatalf("an unparsed line changed: %+v", l[2])
	}

	// Only unparsed lines: the stepper still has servings, and the note shows.
	r := cookRecipe()
	r.Ingredients = r.Ingredients[2:3]
	only := newMealsCook(r, 2)
	if _, ok := only.Servings(); !ok || !only.NotScaled() {
		t.Fatal("a recipe of unparsed lines lost its stepper or its note")
	}
	// A heading alone does not count.
	r.Ingredients = []mealie.Ingredient{{Title: "To serve"}, cookRecipe().Ingredients[0]}
	if newMealsCook(r, 2).NotScaled() {
		t.Fatal("a heading counts as an unscaled amount")
	}
}

func TestCookHighlights(t *testing.T) {
	cookClockAt(t)
	c := newMealsCook(cookRecipe(), 1)
	if len(c.Uses()) != 0 {
		t.Fatal("the overview uses ingredients")
	}
	c.Go(1)
	if u := c.Uses(); len(u) != 1 || u[0] != 0 {
		t.Fatalf("step 1 uses %v, want [0] (the reference to a missing id is ignored)", u)
	}
	l := c.Lines()
	if !l[0].Used || l[1].Used {
		t.Fatalf("used marks: %+v", l)
	}
	c.Go(3)
	if len(c.Uses()) != 0 {
		t.Fatal("step 3 uses ingredients")
	}
	if !c.Tick(1) || !c.Lines()[1].Ticked {
		t.Fatal("a tap does not tick an ingredient")
	}
	c.Tick(1)
	if c.Lines()[1].Ticked {
		t.Fatal("a second tap does not take the tick back")
	}
	if c.Tick(-1) || c.Tick(40) {
		t.Fatal("a tick out of range counted")
	}
}

func TestCookTimerFromDurationSeconds(t *testing.T) {
	cookClockAt(t)
	r := cookRecipe()
	r.Steps[0].DurationSeconds = 600 // wins over the "10 minutes" in the text
	r.Steps[1].DurationSeconds = 5400
	c := newMealsCook(r, 1)
	c.Go(1)
	p := c.Pills()
	if len(p) != 1 || p[0].D != 10*time.Minute || p[0].Text != "10:00" {
		t.Fatalf("pills %+v", p)
	}
	c.Go(2)
	if p := c.Pills(); len(p) != 1 || p[0].Text != "1:30:00" {
		t.Fatalf("pills %+v", p)
	}
}

func TestCookTimerFromText(t *testing.T) {
	cookClockAt(t)
	c := newMealsCook(cookRecipe(), 1)
	c.Go(1)
	if p := c.Pills(); len(p) != 1 || p[0].D != 10*time.Minute || p[0].Text != "10 minutes" {
		t.Fatalf("step 1 pills %+v", p)
	}
	c.Go(2)
	p := c.Pills()
	if len(p) != 2 || p[0].D != 20*time.Minute || p[1].D != 30*time.Minute {
		t.Fatalf("step 2 pills %+v", p)
	}
	c.Go(3)
	if len(c.Pills()) != 0 {
		t.Fatal("a step with no time has pills")
	}
	r := cookRecipe()
	r.Steps[0].Text = "1 min, 2 min, 3 min, 4 min, 5 min"
	m := newMealsCook(r, 1)
	m.Go(1)
	if len(m.Pills()) != 3 {
		t.Fatalf("%d pills, want at most 3", len(m.Pills()))
	}
}

func TestCookTimerStartsOnce(t *testing.T) {
	now := cookClockAt(t)
	type started struct {
		name string
		d    time.Duration
	}
	var got []started
	was := mealsTimerStart
	mealsTimerStart = func(name string, d time.Duration) string { got = append(got, started{name, d}); return "t1" }
	t.Cleanup(func() { mealsTimerStart = was })

	r := cookRecipe()
	r.Steps[1].Summary = "Simmer"
	c := newMealsCook(r, 1)
	c.Go(1)
	if !c.StartPill(0) || c.StartPill(0) {
		t.Fatal("a pill starts once, and a second tap does nothing")
	}
	if len(got) != 1 || got[0].name != "Tomato soup 1" || got[0].d != 10*time.Minute {
		t.Fatalf("timers started: %+v", got)
	}
	if c.StartPill(5) || c.StartPill(-1) {
		t.Fatal("a pill out of range started")
	}
	*now = now.Add(3 * time.Minute)
	if left := c.Pills()[0].Left(*now); left != 7*time.Minute {
		t.Fatalf("left %v, want 7m", left)
	}
	// Another step, another pill; and the first stays started on the way back.
	c.Go(2)
	if !c.StartPill(1) || got[1].name != "Simmer" || got[1].d != 30*time.Minute {
		t.Fatalf("second timer: %+v", got)
	}
	c.Go(1)
	if c.Pills()[0].Started.IsZero() || c.StartPill(0) {
		t.Fatal("a started pill came back startable")
	}
	// A name is cut to 24 characters, and a long summary is not used.
	if n := cookTimerName(mealie.Step{Summary: strings.Repeat("x", 30)}, "A very long recipe name indeed", 2); len([]rune(n)) > 24 || n == "" {
		t.Fatalf("name %q", n)
	}
	// A timer that the device refused is not marked started.
	mealsTimerStart = func(string, time.Duration) string { return "" }
	c.Go(2)
	if c.StartPill(0) || !c.Pills()[0].Started.IsZero() {
		t.Fatal("a refused timer is marked started")
	}
}

func TestCookKeepsAwake(t *testing.T) {
	cookClockAt(t)
	mealsSetUp(t)
	if mealsCookAwake() || mealsCookNight() {
		t.Fatal("the screen is held with nothing being cooked")
	}
	mealsCookOpen(cookRecipe(), 1)
	if !mealsCookAwake() || !mealsCookNight() {
		t.Fatal("cooking does not hold the screen")
	}
	mealsCookClose()
	if mealsCookAwake() {
		t.Fatal("the screen is held after the recipe was put away")
	}
}

func TestCookSleepsSettingOff(t *testing.T) {
	cookClockAt(t)
	mealsSetUp(t)
	if err := config.Set().Mealie().Sleeps(true); err != nil {
		t.Fatal(err)
	}
	mealsCookOpen(cookRecipe(), 1)
	if mealsCookAwake() || mealsCookNight() {
		t.Fatal("the screen is held although the setup page lets it sleep")
	}
	if err := config.Set().Mealie().Sleeps(false); err != nil {
		t.Fatal(err)
	}
	if !mealsCookAwake() {
		t.Fatal("the setting is not read again")
	}
}

func TestCookAwakeCap(t *testing.T) {
	now := cookClockAt(t)
	mealsSetUp(t)
	c := mealsCookOpen(cookRecipe(), 1)
	*now = now.Add(cookAwakeMost - time.Minute)
	if !mealsCookAwake() {
		t.Fatal("the hold ended early")
	}
	// A step moved, or a touch, starts the count again.
	c.Next()
	*now = now.Add(cookAwakeMost - time.Minute)
	if !mealsCookAwake() {
		t.Fatal("a step did not restart the count")
	}
	c.Touched()
	*now = now.Add(cookAwakeMost - time.Minute)
	if !mealsCookAwake() {
		t.Fatal("a touch did not restart the count")
	}
	*now = now.Add(2 * time.Minute)
	if mealsCookAwake() || mealsCookNight() {
		t.Fatal("the hold outlived the cap")
	}
}

func TestCookStateKept30Min(t *testing.T) {
	now := cookClockAt(t)
	c := mealsCookOpen(cookRecipe(), 1)
	c.Go(2)
	c.More()
	*now = now.Add(mealsKeep - time.Minute)
	again := mealsCookOpen(cookRecipe(), 3)
	if again != c || again.Step() != 2 || again.Scale() != 1.25 {
		t.Fatalf("reopened within 30 minutes: step %d scale %v", again.Step(), again.Scale())
	}
	if mealsCooking() != c || mealsCookKept() != c {
		t.Fatal("the cook is not the one kept")
	}
	// Another recipe starts over.
	other := cookRecipe()
	other.ID, other.Slug = "id-pancakes", "pancakes"
	if o := mealsCookOpen(other, 1); o == c || o.Step() != 0 {
		t.Fatal("another recipe took the first one's state")
	}
	// After the time is up it is gone.
	mealsCooking().Go(1)
	*now = now.Add(mealsKeep + time.Minute)
	if mealsCookKept() != nil {
		t.Fatal("the state outlived 30 minutes")
	}
	if n := mealsCookOpen(other, 1); n.Step() != 0 || n.Scale() != 1 {
		t.Fatal("a reopening after 30 minutes kept the step")
	}
}
