//go:build !dot && !spot

package display

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

// mealsFakeServer fakes the parts of Mealie the actions read: the day's plan, a recipe's name, the lists. It
// counts what it is asked.
type mealsFakeServer struct {
	mu       sync.Mutex
	asked    []string
	plan     string // the plan's JSON items
	recipes  map[string]string
	listName string
}

func (f *mealsFakeServer) setPlan(items string) {
	f.mu.Lock()
	f.plan = items
	f.mu.Unlock()
}

func (f *mealsFakeServer) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

func (f *mealsFakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.asked = append(f.asked, r.URL.Path)
	f.mu.Unlock()
	switch p := r.URL.Path; {
	case p == "/api/households/mealplans":
		f.mu.Lock()
		plan := f.plan
		f.mu.Unlock()
		fmt.Fprintf(w, `{"items":%s}`, plan)
	case p == "/api/households/shopping/lists":
		fmt.Fprintf(w, `{"items":[{"id":"l1","name":%q}]}`, f.listName)
	case strings.HasPrefix(p, "/api/recipes/"):
		if body, ok := f.recipes[strings.TrimPrefix(p, "/api/recipes/")]; ok {
			fmt.Fprint(w, body)
			return
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

// mealsFake sets Mealie up against a fake server, with the page's views and state fresh, and returns the
// server and a Display. The recipes the fake has are Tomato soup and Pancakes.
func mealsFake(t *testing.T) (*mealsFakeServer, *Display) {
	t.Helper()
	mealsSetUp(t)
	cookClockAt(t)
	f := &mealsFakeServer{
		listName: "Groceries",
		recipes: map[string]string{
			"tomato-soup": `{"id":"id-soup","slug":"tomato-soup","name":"Tomato soup"}`,
			"pancakes":    `{"id":"id-pan","slug":"pancakes","name":"Pancakes"}`,
		},
		plan: `[]`,
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	was := mealsClient
	mealsClient = func() mealie.Client { return mealie.Client{URL: srv.URL, Token: "token-for-a-test"} }
	t.Cleanup(func() { mealsClient = was })
	for i, id := range []string{"shopping", "plan", "cooking"} {
		registerMealsView(mealsTestView(id, i+1, nil, nil))
	}
	return f, &Display{poke: make(chan struct{}, 4)}
}

// mealsRun calls one of the Meals actions as Home Assistant does.
func mealsRun(t *testing.T, d *Display, name string, args map[string]any) (any, error) {
	t.Helper()
	for _, a := range d.Actions() {
		if a.Name == name {
			c, err := esphome.NewCall(args)
			if err != nil {
				t.Fatal(err)
			}
			return a.Run(c)
		}
	}
	t.Fatalf("no action %q", name)
	return nil, nil
}

// cooked puts a recipe on the page the way the Cooking view does.
func cooked(d *Display, r mealie.Recipe) *mealsCook {
	meals.mu.Lock()
	meals.view = "cooking"
	meals.mu.Unlock()
	d.mu.Lock()
	d.customs.up, d.customs.shown = mealsID, mealsID
	d.mu.Unlock()
	return mealsCookOpen(r, 1)
}

// The Meals actions are the Show's: they are in what the display offers Home Assistant, and answer.
func TestMealsActionsRegisterOnScreenBuilds(t *testing.T) {
	d := &Display{}
	got := map[string]*esphome.Action{}
	for _, a := range d.Actions() {
		got[a.Name] = a
	}
	for _, name := range []string{"meals_show", "meals_step", "todo_show", "calendar_show"} {
		if got[name] == nil {
			t.Errorf("action %q is not offered", name)
		}
	}
	for _, name := range []string{"meals_show", "meals_step"} {
		if a := got[name]; a != nil && !a.Answers {
			t.Errorf("%s does not answer", name)
		}
	}
	for _, arg := range got["meals_show"].Args {
		if arg.Type != esphome.ArgString {
			t.Errorf("meals_show argument %s is not a string", arg.Name)
		}
	}
	if a := got["meals_step"].Args; len(a) != 1 || a[0].Name != "step" || a[0].Type != esphome.ArgString {
		t.Errorf("meals_step arguments %+v", a)
	}
}

// Five pages, each opening the page on its view; anything else is an error that says what was asked.
func TestMealsShowPages(t *testing.T) {
	srv, d := mealsFake(t)
	srv.setPlan(`[{"id":1,"entryType":"dinner","recipeScale":1.5,"recipe":{"id":"id-pan","slug":"pancakes","name":"Pancakes"}}]`)
	for _, c := range []struct {
		page, recipe, view, title string
	}{
		{"shopping", "", "shopping", "Groceries"},
		{"plan", "", "plan", ""},
		{"today", "", "cooking", "Pancakes"},
		{"recipe", "tomato-soup", "cooking", "Tomato soup"},
		{"cooking", "", "cooking", "Tomato soup"}, // the recipe opened last
	} {
		got, err := mealsRun(t, d, "meals_show", map[string]any{"page": c.page, "recipe": c.recipe})
		if err != nil {
			t.Fatalf("%s: %v", c.page, err)
		}
		if a := got.(mealsAnswer); a.Page != c.view || a.Title != c.title {
			t.Errorf("%s answered %+v, want page %q and title %q", c.page, a, c.view, c.title)
		}
		meals.mu.Lock()
		view := meals.view
		meals.mu.Unlock()
		d.mu.Lock()
		up := d.customs.up
		d.mu.Unlock()
		if view != c.view || up != mealsID {
			t.Errorf("%s left the page at %q, up %q", c.page, view, up)
		}
		if len(d.poke) == 0 {
			t.Errorf("%s did not wake the screen", c.page)
		}
		for len(d.poke) > 0 {
			<-d.poke
		}
	}
	_, err := mealsRun(t, d, "meals_show", map[string]any{"page": "dessert"})
	if err == nil || err.Error() != `meals: unknown page "dessert"` {
		t.Errorf("an unknown page: %v", err)
	}
}

// The recipe is read only for page recipe; for that page an empty one is an error, and one Mealie does not
// have is too.
func TestMealsShowRecipeArg(t *testing.T) {
	srv, d := mealsFake(t)
	if _, err := mealsRun(t, d, "meals_show", map[string]any{"page": "shopping", "recipe": "pancakes"}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := mealsOpenRecipe(); ok {
		t.Error("a recipe was opened by a page that is not recipe")
	}
	for _, c := range srv.calls() {
		if strings.HasPrefix(c, "/api/recipes/") {
			t.Errorf("a recipe was asked for by a page that is not recipe: %s", c)
		}
	}
	for _, empty := range []string{"", "  "} {
		_, err := mealsRun(t, d, "meals_show", map[string]any{"page": "recipe", "recipe": empty})
		if err == nil || err.Error() != "meals: page=recipe needs a recipe" {
			t.Errorf("recipe %q: %v", empty, err)
		}
	}
	if _, err := mealsRun(t, d, "meals_show", map[string]any{"page": "recipe", "recipe": "gone"}); err == nil {
		t.Error("a recipe Mealie does not have was opened")
	}
	if _, err := mealsRun(t, d, "meals_show", map[string]any{"page": "recipe", "recipe": "pancakes"}); err != nil {
		t.Fatal(err)
	}
	if slug, scale, ok := mealsOpenRecipe(); !ok || slug != "pancakes" || scale != 1 {
		t.Errorf("opened %q at %v (%v)", slug, scale, ok)
	}
}

// Today is the dinner's recipe, else the first recipe of the day, else the plan; a note is not a recipe.
func TestMealsShowToday(t *testing.T) {
	srv, d := mealsFake(t)
	soup := `"recipe":{"id":"id-soup","slug":"tomato-soup","name":"Tomato soup"}`
	pan := `"recipe":{"id":"id-pan","slug":"pancakes","name":"Pancakes"}`
	for _, c := range []struct {
		name, plan, slug, view, title string
		scale                         float64
	}{
		{"dinner wins", `[{"entryType":"breakfast",` + pan + `},{"entryType":"dinner","recipeScale":2,` + soup + `}]`, "tomato-soup", "cooking", "Tomato soup", 2},
		{"first recipe", `[{"entryType":"dinner","title":"Out"},{"entryType":"lunch",` + pan + `},{"entryType":"snack",` + soup + `}]`, "pancakes", "cooking", "Pancakes", 1},
		{"only notes", `[{"entryType":"dinner","title":"Out"}]`, "", "plan", "", 0},
		{"nothing", `[]`, "", "plan", "", 0},
	} {
		srv.setPlan(c.plan)
		meals.mu.Lock()
		meals.recipe.slug, meals.view = "", ""
		meals.mu.Unlock()
		got, err := mealsRun(t, d, "meals_show", map[string]any{"page": "today"})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if a := got.(mealsAnswer); a.Page != c.view || a.Title != c.title {
			t.Errorf("%s answered %+v", c.name, a)
		}
		slug, scale, ok := mealsOpenRecipe()
		if slug != c.slug || (ok && scale != c.scale) {
			t.Errorf("%s opened %q at %v", c.name, slug, scale)
		}
		meals.mu.Lock()
		view := meals.view
		meals.mu.Unlock()
		if view != c.view {
			t.Errorf("%s: the page is on %q, want %q", c.name, view, c.view)
		}
	}
}

// Cooking goes back to the recipe opened last, and is today when there is none.
func TestMealsShowCooking(t *testing.T) {
	srv, d := mealsFake(t)
	srv.setPlan(`[{"entryType":"dinner","recipe":{"id":"id-pan","slug":"pancakes","name":"Pancakes"}}]`)
	if _, err := mealsRun(t, d, "meals_show", map[string]any{"page": "recipe", "recipe": "tomato-soup"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mealsRun(t, d, "meals_show", map[string]any{"page": "plan"}); err != nil {
		t.Fatal(err)
	}
	got, err := mealsRun(t, d, "meals_show", map[string]any{"page": "cooking"})
	if err != nil {
		t.Fatal(err)
	}
	if slug, _, _ := mealsOpenRecipe(); slug != "tomato-soup" || got.(mealsAnswer).Page != "cooking" {
		t.Errorf("cooking opened %q, answered %+v", slug, got)
	}
}

// A day's plan that cannot be read is an error, and nothing is put up.
func TestMealsShowTodayUnreachable(t *testing.T) {
	_, d := mealsFake(t)
	mealsClient = func() mealie.Client { return mealie.Client{URL: "http://127.0.0.1:1", Token: "token-for-a-test"} }
	if _, err := mealsRun(t, d, "meals_show", map[string]any{"page": "today"}); err == nil {
		t.Fatal("a plan that could not be read opened something")
	}
	d.mu.Lock()
	up := d.customs.up
	d.mu.Unlock()
	if up != "" {
		t.Errorf("the page is up (%q) after an error", up)
	}
}

// Next and back, the first step and the overview, a step by number, and the step's timer; the answer is the
// page now on the screen.
func TestMealsStepAction(t *testing.T) {
	_, d := mealsFake(t)
	var timers []time.Duration
	was := mealsTimerStart
	mealsTimerStart = func(_ string, dur time.Duration) string { timers = append(timers, dur); return "t1" }
	t.Cleanup(func() { mealsTimerStart = was })

	// Not cooking: the page is not on a recipe, or not up at all.
	for _, up := range []bool{false, true} {
		if up {
			d.mu.Lock()
			d.customs.up = mealsID
			d.mu.Unlock()
			meals.show("plan")
		}
		if _, err := mealsRun(t, d, "meals_step", map[string]any{"step": "next"}); err == nil || err.Error() != "meals: nothing is being cooked on this device" {
			t.Errorf("up %v: %v", up, err)
		}
	}
	// A recipe kept from before, with the page on another view, is not moved either.
	c := cooked(d, cookRecipe())
	meals.show("plan")
	if _, err := mealsRun(t, d, "meals_step", map[string]any{"step": "next"}); err == nil || c.Step() != 0 {
		t.Errorf("a recipe on a view that is not up moved to %d (%v)", c.Step(), err)
	}
	cooked(d, cookRecipe())

	step := func(arg string) mealsStepAnswer {
		t.Helper()
		got, err := mealsRun(t, d, "meals_step", map[string]any{"step": arg})
		if err != nil {
			t.Fatalf("%s: %v", arg, err)
		}
		return got.(mealsStepAnswer)
	}
	if a := step("next"); a.Step != 1 || a.Of != 3 || a.Text != "Fry the onions for 10 minutes." {
		t.Errorf("next: %+v", a)
	}
	if a := step("3"); a.Step != 3 || a.Text != "Serve." {
		t.Errorf("3: %+v", a)
	}
	if a := step("next"); a.Step != 3 {
		t.Errorf("next on the last step: %+v", a)
	}
	if a := step("back"); a.Step != 2 {
		t.Errorf("back: %+v", a)
	}
	if a := step("overview"); a.Step != 0 || a.Text != "" || a.Of != 3 {
		t.Errorf("overview: %+v", a)
	}
	if a := step("back"); a.Step != 0 {
		t.Errorf("back on the overview: %+v", a)
	}
	if a := step(" First "); a.Step != 1 {
		t.Errorf("first: %+v", a)
	}
	if a := step("timer"); a.Step != 1 || len(timers) != 1 || timers[0] != 10*time.Minute {
		t.Errorf("timer: %+v, started %v", a, timers)
	}
	if _, err := mealsRun(t, d, "meals_step", map[string]any{"step": "timer"}); err == nil || len(timers) != 1 {
		t.Errorf("a second timer on the same step: %v, started %v", err, timers)
	}
	step("3")
	if _, err := mealsRun(t, d, "meals_step", map[string]any{"step": "timer"}); err == nil || len(timers) != 1 {
		t.Errorf("a timer on a step with none: %v, started %v", err, timers)
	}
	for _, bad := range []string{"0", "4", "-1", "sideways", ""} {
		if _, err := mealsRun(t, d, "meals_step", map[string]any{"step": bad}); err == nil {
			t.Errorf("step %q was taken", bad)
		}
	}
	if c := mealsCooking(); c.Step() != 3 {
		t.Errorf("a refused step moved the recipe to %d", c.Step())
	}

	// No steps: only the overview, and the numbers are all out of range.
	cooked(d, mealie.Recipe{RecipeSummary: mealie.RecipeSummary{ID: "empty", Name: "Empty"}})
	if a := step("next"); a.Step != 0 || a.Of != 0 {
		t.Errorf("next on a recipe with no steps: %+v", a)
	}
	if _, err := mealsRun(t, d, "meals_step", map[string]any{"step": "1"}); err == nil {
		t.Error("step 1 of a recipe with no steps")
	}
}

// A step asked for lights a screen the night put out, and by day wakes the page; and it counts as a touch
// for the keep-awake cap.
func TestMealsStepWakesScreen(t *testing.T) {
	_, d := mealsFake(t)
	h := time.Now().Hour()
	if err := config.Set().Screen().Night(fmt.Sprintf("%d-%d", (h+23)%24, (h+2)%24)); err != nil {
		t.Fatal(err)
	}
	if err := config.Set().Screen().NightLight(true); err != nil {
		t.Fatal(err)
	}
	c := cooked(d, cookRecipe())
	d.mu.Lock()
	d.on, d.nightGlow = true, true
	d.mu.Unlock()
	for len(d.poke) > 0 {
		<-d.poke
	}
	now := mealsNow()
	mealsNow = func() time.Time { return now.Add(10 * time.Minute) }
	if _, err := mealsRun(t, d, "meals_step", map[string]any{"step": "next"}); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	asked := d.askedNight
	d.mu.Unlock()
	if !asked {
		t.Error("the night's hold was not taken for the step")
	}
	if len(d.poke) == 0 {
		t.Error("the screen was not woken")
	}
	if got := c.lastTouched(); !got.Equal(now.Add(10 * time.Minute)) {
		t.Errorf("the step did not count as a touch: %v", got)
	}
}

// With Mealie not set up every action says so before anything is asked of the network.
func TestMealsActionsNotSetUp(t *testing.T) {
	srv, d := mealsFake(t)
	if err := config.Set().Mealie().Server("", new(string)); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string]map[string]any{
		"meals_show": {"page": "today"},
		"meals_step": {"step": "next"},
	} {
		_, err := mealsRun(t, d, name, args)
		if err == nil || err.Error() != "meals: Mealie is not set up on this device" {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, page := range []string{"shopping", "plan", "recipe", "cooking", "nonsense"} {
		if _, err := mealsRun(t, d, "meals_show", map[string]any{"page": page, "recipe": "pancakes"}); err == nil || !strings.Contains(err.Error(), "not set up") {
			t.Errorf("%s: %v", page, err)
		}
	}
	if got := srv.calls(); len(got) != 0 {
		t.Errorf("a device that is not set up asked the network: %v", got)
	}
	d.mu.Lock()
	up := d.customs.up
	d.mu.Unlock()
	if up != "" {
		t.Errorf("the page was put up (%q)", up)
	}
}
