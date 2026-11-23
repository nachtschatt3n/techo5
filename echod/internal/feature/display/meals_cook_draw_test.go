//go:build !dot && !spot

package display

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/touch"
	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

func init() {
	// The words on the Cooking view's buttons have to fit them in every complete catalog.
	mealsPillKeys["meals_start"] = cookStartW
	mealsPillKeys["meals_next"] = cookNavW
	mealsPillKeys["meals_back"] = cookNavW
	mealsPillKeys["meals_finish"] = cookNavW
}

// cookToday is the made-up moment every drawing of the view is of.
var cookToday = time.Date(2026, 11, 23, 18, 30, 0, 0, time.UTC)

// cookPNG is a small made-up picture, 16:9, so that fitting it to a box crops nothing.
func cookPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 160, 90))
	for y := range 90 {
		for x := range 160 {
			img.SetRGBA(x, y, color.RGBA{200, uint8(2 * x), uint8(2 * y), 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// cookSetUp is a page with the Cooking view on it, beside two stand-ins for the others, drawn on a screen
// w by h, with photos served from memory (the count of the ones asked for is returned), and puts
// everything back after the test.
func cookSetUp(t *testing.T, w, h int) (*Display, *atomic.Int32) {
	t.Helper()
	d := noticeDisplay(t, w, h)
	mealsFresh(t)
	registerMealsView(mealsTestView("shopping", 1, nil, nil))
	registerMealsView(mealsTestView("plan", 2, nil, nil))
	registerMealsView(mealsCookingView())
	mealsCookUI.reset()
	asked := &atomic.Int32{}
	wasPics, wasFetch, wasGrace, wasTimer := libPics, libFetch, cookHeroGrace, mealsTimerStart
	libPics = &libPictures{}
	libFetch = func(context.Context, string) ([]byte, int, error) {
		asked.Add(1)
		return cookPNG(), http.StatusOK, nil
	}
	mealsTimerStart = func(string, time.Duration) string { return "t1" }
	t.Cleanup(func() {
		mealsCookUI.reset()
		mealsCookClose()
		libPics, libFetch, cookHeroGrace, mealsTimerStart = wasPics, wasFetch, wasGrace, wasTimer
	})
	return d, asked
}

// cookStepsRecipe is the made-up recipe with n steps of a few words each.
func cookStepsRecipe(n int) mealie.Recipe {
	r := cookRecipe()
	r.Image = "k-soup"
	r.Description = "A thick soup of ripe tomatoes, with a little cream."
	r.Steps = nil
	for i := 1; i <= n; i++ {
		r.Steps = append(r.Steps, mealie.Step{Text: fmt.Sprintf("Do the thing number %d.", i)})
	}
	return r
}

// cookOpen opens the recipe on the page as the plan does, and returns its model and what the view's fetch
// would have given. A recipe with no picture has no addresses.
func cookOpen(t *testing.T, r mealie.Recipe, scale float64) (*mealsCook, mealsCookData) {
	t.Helper()
	mealsCookClose()
	meals.mu.Lock()
	meals.recipe.slug, meals.recipe.scale, meals.recipe.at, meals.view = r.Slug, scale, time.Now(), mealsCookID
	meals.mu.Unlock()
	c := mealsCookOpen(r, scale)
	data := mealsCookData{Slug: r.Slug, Cook: c}
	if r.Image != "" {
		data.Min = "https://mealie.example.test/api/media/recipes/" + r.ID + "/images/min-original.webp?v=" + r.Image
		data.Full = "https://mealie.example.test/api/media/recipes/" + r.ID + "/images/original.webp?v=" + r.Image
	}
	return c, data
}

// cookDrawn draws the page on the Cooking view with data on r, as the display does, and makes what it
// drew the page's taps.
func cookDrawn(r *renderer, data mealsCookData) []customZone {
	f := mealsFrame{
		view: mealsCookingView(),
		tabs: []mealsTab{{id: "shopping", label: "Shopping"}, {id: "plan", label: "Plan"}, {id: mealsCookID, label: "Cooking", on: true}},
		pane: mealsPane{Data: data, Loaded: true, Fetched: cookToday},
	}
	zones := mealsDraw(r, scene{now: cookToday, phase: "idle", custom: customView{name: mealsID, data: f}})
	setCustomZones(zones)
	return zones
}

// cookZones are the zones of one kind.
func cookZones(zones []customZone, kind string) []customZone {
	var out []customZone
	for _, z := range zones {
		if z.kind == kind {
			out = append(out, z)
		}
	}
	return out
}

// cookZone is the zone of a kind and an id, which the test needs.
func cookZone(t *testing.T, zones []customZone, kind, id string) customZone {
	t.Helper()
	for _, z := range zones {
		if z.kind == kind && z.id == id {
			return z
		}
	}
	t.Fatalf("no %s zone %q among %d zones", kind, id, len(zones))
	return customZone{}
}

func cookMid(z customZone) image.Point { return z.r.Min.Add(z.r.Size().Div(2)) }

// cookTapOn taps the zone, through the view's own gesture as the page hands it.
func cookTapOn(d *Display, z customZone) {
	at := cookMid(z)
	cookGesture(d, touch.Gesture{Kind: touch.Tap, X: at.X, Y: at.Y}, at)
}

// cookPrimary says which zones are the ones a floury thumb works.
func cookPrimary(z customZone) bool {
	switch z.kind {
	case "meals:cook:back", "meals:cook:next", "meals:cook:start", "meals:cook:pill", "meals:cook:more", "meals:cook:less", "meals:cook:leave", "meals:view":
		return true
	}
	return false
}

// Every page of the view is laid out on the four canvases: each zone is on the canvas, none overlaps
// another, none is too small for a finger, and Back and Next stand at the two ends of their row with room
// between them and the timers over them.
func TestCookLayout(t *testing.T) {
	for _, size := range mealsSizes {
		w, h := size.X, size.Y
		su := func(n int) int { return (n*w + 480) / 960 }
		d, _ := cookSetUp(t, w, h)
		c, data := cookOpen(t, cookRecipe(), 1)
		for page := 0; page <= c.Steps(); page++ {
			c.Go(page)
			zones := cookDrawn(d.r, data)
			assertLayout(t, zones, w, h, cookPrimary)
			if page == 0 {
				if len(cookZones(zones, "meals:cook:start")) != 1 {
					t.Errorf("%v overview: no Start", size)
				}
				continue
			}
			back, next := cookZone(t, zones, "meals:cook:back", "back"), cookZone(t, zones, "meals:cook:next", "next")
			if back.r.Dy() < su(mealsTarget) || next.r.Dy() < su(mealsTarget) || back.r.Dx() < su(mealsTarget) || next.r.Dx() < su(mealsTarget) {
				t.Errorf("%v step %d: Back %v or Next %v is under 64", size, page, back.r, next.r)
			}
			if back.r.Max.X > next.r.Min.X-su(8) || next.r.Min.X-back.r.Max.X < su(200) {
				t.Errorf("%v step %d: Back %v and Next %v are not at the two ends of their row", size, page, back.r, next.r)
			}
			for _, p := range cookZones(zones, "meals:cook:pill") {
				if gap := back.r.Min.Y - p.r.Max.Y; gap < su(8) {
					t.Errorf("%v step %d: timer %v is %d from the buttons under it", size, page, p.r, gap)
				}
			}
		}
		// Past the last step the button says Finish, and the page is the same.
		if !c.Last() {
			t.Errorf("%v: the cook is not on the last page", size)
		}
	}
}

// The ingredients stand over the step on a screen held upright, and are as easy to hit as anywhere.
func TestCookPortraitSheet(t *testing.T) {
	w, h := 800, 1280
	d, _ := cookSetUp(t, w, h)
	c, data := cookOpen(t, cookRecipe(), 1)
	c.Go(1)
	zones := cookDrawn(d.r, data)
	if len(cookZones(zones, "meals:cook:pill")) == 0 || len(cookZones(zones, "meals:cook:tick")) != 0 {
		t.Fatalf("the step page has %d timers and %d ingredients to tick", len(cookZones(zones, "meals:cook:pill")), len(cookZones(zones, "meals:cook:tick")))
	}
	cookTapOn(d, cookZone(t, zones, "meals:cook:toggle", "toggle"))
	zones = cookDrawn(d.r, data)
	assertLayout(t, zones, w, h, cookPrimary)
	if len(cookZones(zones, "meals:cook:pill")) != 0 || len(cookZones(zones, "meals:cook:tick")) == 0 {
		t.Fatalf("with the sheet up there are %d timers and %d ingredients", len(cookZones(zones, "meals:cook:pill")), len(cookZones(zones, "meals:cook:tick")))
	}
	// Turning the page puts the sheet away.
	cookTapOn(d, cookZone(t, zones, "meals:cook:next", "next"))
	zones = cookDrawn(d.r, data)
	if len(cookZones(zones, "meals:cook:tick")) != 0 {
		t.Fatal("the sheet stayed over the next step")
	}
}

// A step's words are read in 29 units on a short screen and 34 on a tall one and on one held upright, and
// a line is as tall as its face needs.
func TestCookStepFaces(t *testing.T) {
	for _, size := range mealsSizes {
		r := newRenderer(image.NewRGBA(image.Rectangle{Max: size}))
		class := mealsClassOf(size.X, size.Y)
		face, line := r.cookStepFace(class)
		want := r.small
		units := cookTextLineTall
		if class == mealsShort {
			want, units = r.faces().label, cookTextLine
		}
		if face.Metrics().Height != want.Metrics().Height || line != r.s(units) {
			t.Errorf("%v: face %v and line %d, want %v and %d", size, face.Metrics().Height, line, want.Metrics().Height, r.s(units))
		}
		if class != mealsShort && face.Metrics().Height <= r.faces().label.Metrics().Height {
			t.Errorf("%v: a tall screen's step face is no larger than a short one's", size)
		}
	}
}

// A step of 900 characters on a Show 5 scrolls: there is more of it than the room it has, the swipe moves
// it, and the buttons and the timers around it stay where they were.
func TestCookLongStepScrolls(t *testing.T) {
	d, _ := cookSetUp(t, 960, 480)
	r := cookRecipe()
	r.Steps[1].Text = strings.TrimSpace(strings.Repeat("Stir the soup gently and taste it now and then. ", 19)) + " Simmer 20 Min."
	if len(r.Steps[1].Text) < 900 {
		t.Fatalf("the step is %d characters", len(r.Steps[1].Text))
	}
	c, data := cookOpen(t, r, 1)
	c.Go(2)
	zones := cookDrawn(d.r, data)
	ui := mealsCookUI
	ui.mu.Lock()
	text, most := ui.text, ui.textMax
	ui.mu.Unlock()
	if most <= 0 || text.Empty() {
		t.Fatalf("the text scrolls %d in a pane %v: it fits", most, text)
	}
	fixed := func(zones []customZone) []customZone {
		var out []customZone
		for _, z := range zones {
			switch z.kind {
			case "meals:cook:back", "meals:cook:next", "meals:cook:pill", "meals:cook:dot":
				out = append(out, z)
			}
		}
		return out
	}
	before := fixed(zones)
	if len(before) < 4 {
		t.Fatalf("%d fixed zones", len(before))
	}
	at := cookMid(customZone{r: text})
	for range 3 {
		cookGesture(d, touch.Gesture{Kind: touch.SwipeUp, X: at.X, Y: at.Y}, at)
	}
	ui.mu.Lock()
	scrolled := ui.textScroll
	ui.mu.Unlock()
	if scrolled != min(3*d.r.s(notchPx), most) {
		t.Fatalf("scrolled %d, want %d", scrolled, min(3*d.r.s(notchPx), most))
	}
	after := fixed(cookDrawn(d.r, data))
	if len(after) != len(before) {
		t.Fatalf("%d fixed zones after scrolling, %d before", len(after), len(before))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("zone %s %q moved from %v to %v", before[i].kind, before[i].id, before[i].r, after[i].r)
		}
	}
	// And back up past the top stops there.
	for range 40 {
		cookGesture(d, touch.Gesture{Kind: touch.SwipeDown, X: at.X, Y: at.Y}, at)
	}
	ui.mu.Lock()
	scrolled = ui.textScroll
	ui.mu.Unlock()
	if scrolled != 0 {
		t.Fatalf("scrolled %d above the top", scrolled)
	}
}

// Three timers with long words do not fit one row on a Show 5: they go on to a second, and no two touch.
func TestCookTimerPillsWrap(t *testing.T) {
	d, _ := cookSetUp(t, 960, 480)
	r := cookRecipe()
	r.Steps[0].Text = "A 10-15 minutes simmer, then a 20-25 minutes rest, then a 1 hour 30 minutes bake."
	c, data := cookOpen(t, r, 1)
	c.Go(1)
	if n := len(c.Pills()); n != 3 {
		t.Fatalf("%d timers found in the step: %+v", n, c.Pills())
	}
	zones := cookDrawn(d.r, data)
	assertLayout(t, zones, 960, 480, cookPrimary)
	pills := cookZones(zones, "meals:cook:pill")
	if len(pills) != 3 {
		t.Fatalf("%d timer buttons", len(pills))
	}
	rows := map[int]bool{}
	for _, p := range pills {
		rows[p.r.Min.Y] = true
		if p.r.Min.X < 0 || p.r.Max.X > 960 {
			t.Errorf("timer %v is off the screen", p.r)
		}
	}
	if len(rows) < 2 {
		t.Errorf("three long timers are all on one row: %v", pills)
	}
}

// A swipe sideways turns the page: left is the next one, right the one before. The dots are places to tap,
// and a recipe with more pages than there is room for shows the ones around where the cook is.
func TestCookSwipeAndDots(t *testing.T) {
	d, _ := cookSetUp(t, 960, 480)
	now := cookClockAt(t)
	c, data := cookOpen(t, cookStepsRecipe(20), 1)
	c.Go(5)
	zones := cookDrawn(d.r, data)
	ui := mealsCookUI
	ui.mu.Lock()
	pane := ui.pane
	ui.mu.Unlock()
	at := cookMid(customZone{r: pane})
	step := func(by time.Duration) { *now = now.Add(by) }
	cookGesture(d, touch.Gesture{Kind: touch.SwipeLeft, X: at.X, Y: at.Y}, at)
	if c.Step() != 6 {
		t.Fatalf("a swipe left went to step %d", c.Step())
	}
	step(time.Second)
	cookGesture(d, touch.Gesture{Kind: touch.SwipeRight, X: at.X, Y: at.Y}, at)
	step(time.Second)
	cookGesture(d, touch.Gesture{Kind: touch.SwipeRight, X: at.X, Y: at.Y}, at)
	if c.Step() != 4 {
		t.Fatalf("two swipes right went to step %d", c.Step())
	}

	// The dots: as many as fit, the page the cook is on among them, each a place to tap.
	zones = cookDrawn(d.r, data)
	dots := cookZones(zones, "meals:cook:dot")
	if len(dots) < 3 || len(dots)*d.r.s(cookDotW) > d.r.s(573) {
		t.Fatalf("%d dots", len(dots))
	}
	found := false
	for _, z := range dots {
		if z.id == "4" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the dot of the page the cook is on is not among %v", dots)
	}
	step(time.Second)
	last := dots[len(dots)-1]
	cookTapOn(d, last)
	if fmt.Sprint(c.Step()) != last.id {
		t.Fatalf("a tap on the dot %q went to step %d", last.id, c.Step())
	}
	// A swipe sideways on the last step does not finish the recipe.
	c.Go(20)
	zones = cookDrawn(d.r, data)
	step(time.Second)
	cookGesture(d, touch.Gesture{Kind: touch.SwipeLeft, X: at.X, Y: at.Y}, at)
	if c.Step() != 20 || mealsCooking() == nil {
		t.Fatalf("a swipe on the last step: step %d, cooking %v", c.Step(), mealsCooking())
	}
}

// A finger that began by scrolling does not turn the page when it ends a little sideways, and a swipe
// sideways over the ingredients is not a turn of the page either.
func TestCookAxisLock(t *testing.T) {
	d, _ := cookSetUp(t, 960, 480)
	now := cookClockAt(t)
	c, data := cookOpen(t, cookStepsRecipe(5), 1)
	c.Go(2)
	zones := cookDrawn(d.r, data)
	ui := mealsCookUI
	ui.mu.Lock()
	pane := ui.pane
	ui.mu.Unlock()
	at := cookMid(customZone{r: pane})

	cookGesture(d, touch.Gesture{Kind: touch.SwipeUp, X: at.X, Y: at.Y}, at)
	*now = now.Add(200 * time.Millisecond)
	cookGesture(d, touch.Gesture{Kind: touch.SwipeLeft, X: at.X, Y: at.Y}, at)
	if c.Step() != 2 {
		t.Fatalf("a scroll that ended sideways turned the page to %d", c.Step())
	}
	*now = now.Add(time.Second)
	cookGesture(d, touch.Gesture{Kind: touch.SwipeLeft, X: at.X, Y: at.Y}, at)
	if c.Step() != 3 {
		t.Fatalf("a swipe left a second after a scroll went to %d", c.Step())
	}
	// Over the ingredients, at the left, there is no page to turn.
	*now = now.Add(time.Second)
	left := cookMid(customZone{r: image.Rect(14, 200, 300, 300)})
	if left.In(pane) {
		t.Fatalf("the ingredients' column %v is in the pane %v", left, pane)
	}
	cookGesture(d, touch.Gesture{Kind: touch.SwipeLeft, X: left.X, Y: left.Y}, left)
	if c.Step() != 3 {
		t.Fatalf("a swipe over the ingredients turned the page to %d", c.Step())
	}
	_ = zones
}

// A second activation within 250 ms is a wet finger's: Next does not skip a step, a swipe does not turn two
// pages, an ingredient is not set aside and taken back, and a button works again after the time has passed.
func TestCookDebounce(t *testing.T) {
	d, _ := cookSetUp(t, 960, 480)
	now := cookClockAt(t)
	c, data := cookOpen(t, cookStepsRecipe(10), 1)
	c.Go(1)
	zones := cookDrawn(d.r, data)
	next := cookZone(t, zones, "meals:cook:next", "next")
	cookTapOn(d, next)
	cookTapOn(d, next)
	if c.Step() != 2 {
		t.Fatalf("two taps at once went to step %d", c.Step())
	}
	*now = now.Add(100 * time.Millisecond)
	cookTapOn(d, next)
	if c.Step() != 2 {
		t.Fatalf("a tap 100 ms later went to step %d", c.Step())
	}
	*now = now.Add(300 * time.Millisecond)
	cookTapOn(d, next)
	if c.Step() != 3 {
		t.Fatalf("a tap 400 ms later went to step %d", c.Step())
	}
	// Swipes share Next's debounce: a button and a swipe in the same moment are one turn.
	ui := mealsCookUI
	ui.mu.Lock()
	pane := ui.pane
	ui.mu.Unlock()
	at := cookMid(customZone{r: pane})
	*now = now.Add(time.Second)
	cookGesture(d, touch.Gesture{Kind: touch.SwipeLeft, X: at.X, Y: at.Y}, at)
	cookGesture(d, touch.Gesture{Kind: touch.SwipeLeft, X: at.X, Y: at.Y}, at)
	if c.Step() != 4 {
		t.Fatalf("two swipes at once went to step %d", c.Step())
	}
	// An ingredient is a button of its own.
	zones = cookDrawn(d.r, data)
	tick := cookZones(zones, "meals:cook:tick")
	if len(tick) == 0 {
		t.Fatal("no ingredient to tick")
	}
	*now = now.Add(time.Second)
	cookTapOn(d, tick[0])
	cookTapOn(d, tick[0])
	i := 0
	fmt.Sscan(tick[0].id, &i)
	if !c.Lines()[i].Ticked {
		t.Fatal("two taps at once left the ingredient not set aside")
	}
	*now = now.Add(300 * time.Millisecond)
	cookTapOn(d, tick[0])
	if c.Lines()[i].Ticked {
		t.Fatal("a tap 300 ms later did not take it back")
	}
}

// The servings buttons change the amounts, and the first tap on a timer sets one.
func TestCookServingsAndTimers(t *testing.T) {
	d, _ := cookSetUp(t, 960, 480)
	cookClockAt(t)
	var started []time.Duration
	mealsTimerStart = func(_ string, d time.Duration) string { started = append(started, d); return "t1" }
	c, data := cookOpen(t, cookRecipe(), 1)
	c.Go(1)
	zones := cookDrawn(d.r, data)
	cookTapOn(d, cookZone(t, zones, "meals:cook:more", "more"))
	if sv, _ := c.Servings(); sv != 5 {
		t.Fatalf("one more serving made it %v", sv)
	}
	cookTapOn(d, cookZone(t, zones, "meals:cook:less", "less"))
	if sv, _ := c.Servings(); sv != 4 {
		t.Fatalf("one less made it %v", sv)
	}
	cookTapOn(d, cookZone(t, zones, "meals:cook:pill", "0"))
	if len(started) != 1 || started[0] != 10*time.Minute {
		t.Fatalf("timers started: %v", started)
	}
	// The pill now shows it running, and a second tap does nothing.
	zones = cookDrawn(d.r, data)
	cookTapOn(d, cookZone(t, zones, "meals:cook:pill", "0"))
	if len(started) != 1 {
		t.Fatalf("a second tap started %d timers", len(started))
	}
}

// A recipe with no steps has only the overview, and its button reads Finish and puts the recipe away.
func TestCookNoSteps(t *testing.T) {
	for _, size := range mealsSizes {
		d, _ := cookSetUp(t, size.X, size.Y)
		r := cookStepsRecipe(0)
		c, data := cookOpen(t, r, 1)
		zones := cookDrawn(d.r, data)
		assertLayout(t, zones, size.X, size.Y, cookPrimary)
		if len(cookZones(zones, "meals:cook:dot")) != 0 || len(cookZones(zones, "meals:cook:back")) != 0 {
			t.Fatalf("%v: a recipe with no steps has dots or a Back", size)
		}
		if c.Next() || c.Step() != 0 {
			t.Fatalf("%v: a recipe with no steps moved to %d", size, c.Step())
		}
		cookTapOn(d, cookZone(t, zones, "meals:cook:start", "start"))
		meals.mu.Lock()
		view, slug := meals.view, meals.recipe.slug
		meals.mu.Unlock()
		if view != "plan" || slug != "" || mealsCooking() != nil {
			t.Fatalf("%v: Finish left the page on %q with recipe %q, cooking %v", size, view, slug, mealsCooking())
		}
	}
}

// A recipe with no photo, and one whose photo is not there yet, draw their place with an icon and ask
// nothing of the network; one with a photo asks once, and shows it when it has come.
func TestCookNoPhoto(t *testing.T) {
	d, asked := cookSetUp(t, 960, 480)
	r := cookRecipe() // no image
	_, data := cookOpen(t, r, 1)
	zones := cookDrawn(d.r, data)
	assertLayout(t, zones, 960, 480, cookPrimary)
	if asked.Load() != 0 || len(libPics.kept) != 0 {
		t.Fatalf("a recipe with no photo asked for %d", asked.Load())
	}
	hero := image.Rect(14, 74, 14+370, 74+208)
	placeholder := d.r.dst.RGBAAt(hero.Min.X+20, hero.Min.Y+20)
	if placeholder == (color.RGBA{}) || placeholder == d.r.dst.RGBAAt(hero.Max.X+4, hero.Max.Y+4) {
		t.Fatalf("no placeholder in the photo's place: %v", placeholder)
	}

	// With a photo.
	_, data = cookOpen(t, cookStepsRecipe(3), 1)
	cookDrawn(d.r, data)
	mealsWaitFor(t, func() bool {
		libPics.mu.Lock()
		defer libPics.mu.Unlock()
		return len(libPics.kept) == 1
	})
	cookDrawn(d.r, data)
	if asked.Load() != 1 {
		t.Fatalf("%d photos asked for", asked.Load())
	}
	if got := d.r.dst.RGBAAt(hero.Min.X+hero.Dx()/2, hero.Min.Y+hero.Dy()/2); got == placeholder || got.A != 255 {
		t.Fatalf("the photo is not drawn: %v", got)
	}
}

// A recipe the server no longer has says so, with Back to the plan, on every canvas.
func TestCookRecipeGone(t *testing.T) {
	for _, size := range mealsSizes {
		d, _ := cookSetUp(t, size.X, size.Y)
		_, data := cookOpen(t, cookRecipe(), 1)
		gone := mealsCookData{Slug: data.Slug, Gone: true}
		zones := cookDrawn(d.r, gone)
		assertLayout(t, zones, size.X, size.Y, cookPrimary)
		leave := cookZone(t, zones, "meals:cook:leave", "leave")
		cookTapOn(d, leave)
		meals.mu.Lock()
		view, slug := meals.view, meals.recipe.slug
		meals.mu.Unlock()
		if view != "plan" || slug != "" {
			t.Fatalf("%v: Back left the page on %q with recipe %q", size, view, slug)
		}
	}
}

// What the fetch gives the page: the recipe as the cooking model, with where its photo is, or the answer
// that there is no such recipe.
func TestCookFetch(t *testing.T) {
	d, _ := cookSetUp(t, 960, 480)
	_ = d
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer token-for-a-test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch req.URL.Path {
		case "/api/recipes/tomato-soup":
			_, _ = w.Write([]byte(`{"id":"id-soup","slug":"tomato-soup","name":"Tomato soup","image":"k1","recipeServings":4,
				"recipeIngredient":[{"referenceId":"a","quantity":2,"display":"2 onions"}],
				"recipeInstructions":[{"text":"Fry the onions."}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	was := mealsClient
	mealsClient = func() mealie.Client { return mealie.Client{URL: srv.URL, Token: "token-for-a-test"} }
	t.Cleanup(func() { mealsClient = was })

	open := func(slug string) {
		meals.mu.Lock()
		meals.recipe.slug, meals.recipe.scale, meals.recipe.at = slug, 2, time.Now()
		meals.mu.Unlock()
	}
	mealsCookClose()
	open("tomato-soup")
	got, err := cookFetch(context.Background(), mealsClient())
	if err != nil {
		t.Fatal(err)
	}
	data := got.(mealsCookData)
	if data.Gone || data.Cook == nil || data.Slug != "tomato-soup" || data.Cook.Scale() != 2 || data.Cook.Steps() != 1 {
		t.Fatalf("fetched %+v", data)
	}
	if !strings.HasSuffix(data.Min, "/api/media/recipes/id-soup/images/min-original.webp?v=k1") || !strings.Contains(data.Full, "/images/original.webp") {
		t.Fatalf("photo addresses %q %q", data.Min, data.Full)
	}
	if data.Cook != mealsCooking() {
		t.Fatal("the fetched recipe is not the one being cooked")
	}
	open("apple-cake")
	got, err = cookFetch(context.Background(), mealsClient())
	if err != nil || !got.(mealsCookData).Gone || got.(mealsCookData).Slug != "apple-cake" {
		t.Fatalf("a recipe the server does not have: %+v, %v", got, err)
	}
	// A server that answers 500 is a failure to try again, not an answer.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	t.Cleanup(bad.Close)
	if _, err := cookFetch(context.Background(), mealie.Client{URL: bad.URL, Token: "x"}); err == nil {
		t.Fatal("a server error was taken as an answer")
	}
}

// Data that is not the recipe that is open (another was opened, or this one was finished and is opened
// again) is not drawn: the page says it is loading, and the right one is fetched.
func TestCookStaleDataIsFetchedAgain(t *testing.T) {
	d, _ := cookSetUp(t, 960, 480)
	var fetches atomic.Int32
	mealsViews = nil
	v := mealsCookingView()
	v.fetch = func(context.Context, mealie.Client) (any, error) { fetches.Add(1); return mealsCookData{}, nil }
	registerMealsView(v)
	_, data := cookOpen(t, cookRecipe(), 1)
	// Another recipe is opened.
	meals.mu.Lock()
	meals.recipe.slug = "apple-cake"
	meals.mu.Unlock()
	zones := cookDrawn(d.r, data)
	meals.wg.Wait()
	if len(cookZones(zones, "meals:cook:start")) != 0 || fetches.Load() == 0 {
		t.Fatalf("stale data was drawn (%d start zones) or not fetched again (%d)", len(cookZones(zones, "meals:cook:start")), fetches.Load())
	}
	// This one was finished, and is opened again.
	meals.mu.Lock()
	meals.recipe.slug = "tomato-soup"
	meals.mu.Unlock()
	mealsCookClose()
	fetches.Store(0)
	zones = cookDrawn(d.r, data)
	meals.wg.Wait()
	if len(cookZones(zones, "meals:cook:start")) != 0 || fetches.Load() == 0 {
		t.Fatalf("a finished recipe was drawn again from before (%d start zones, %d fetches)", len(cookZones(zones, "meals:cook:start")), fetches.Load())
	}
}

// A timer that rings is shown over the Meals page, as it is over every other, and what the cook was at is
// as it was when it is put away.
func TestCookRingingTimerShowsOverMeals(t *testing.T) {
	d, _ := cookSetUp(t, 960, 480)
	c, data := cookOpen(t, cookRecipe(), 1)
	c.Go(1)
	zones := cookDrawn(d.r, data)
	cookTapOn(d, cookZone(t, zones, "meals:cook:pill", "0"))
	if c.Pills()[0].Started.IsZero() {
		t.Fatal("the timer was not started")
	}
	f := mealsFrame{
		view: mealsCookingView(),
		tabs: []mealsTab{{id: mealsCookID, label: "Cooking", on: true}},
		pane: mealsPane{Data: data, Loaded: true, Fetched: cookToday},
	}
	page := scene{now: cookToday, phase: "idle", custom: customView{name: mealsID, data: f}}

	setCustomZones(nil)
	d.r.draw(page)
	if _, ok := customZoneAt(cookMid(cookZone(t, zones, "meals:cook:next", "next"))); !ok {
		t.Fatal("the page is not drawn without a ring")
	}
	quiet := append([]byte(nil), d.r.dst.Pix...)

	ringing := page
	ringing.ring = ringState{timer: "Tomato soup 1"}
	setCustomZones(nil)
	d.r.draw(ringing)
	if _, ok := customZoneAt(cookMid(cookZone(t, zones, "meals:cook:next", "next"))); ok {
		t.Fatal("the page's buttons are still there under a ringing timer")
	}
	if bytes.Equal(quiet, d.r.dst.Pix) {
		t.Fatal("a ringing timer is not on the screen")
	}
	if c.Step() != 1 || mealsCooking() != c {
		t.Fatalf("the recipe is at step %d, cooking %v, after a timer rang", c.Step(), mealsCooking())
	}
	// And the screen stays held while it rings.
	d.mu.Lock()
	d.customs.up = mealsID
	d.mu.Unlock()
	if !d.customAwake() {
		t.Fatal("the screen is not held awake while cooking")
	}
}

// The photo is let go of when the page has had no frame for a while (the page is closed, or turned to
// another view), and at once when the recipe is finished.
func TestCookHeroFreedOnClose(t *testing.T) {
	d, asked := cookSetUp(t, 960, 480)
	now := cookClockAt(t)
	kept := func() int {
		libPics.mu.Lock()
		defer libPics.mu.Unlock()
		return len(libPics.kept)
	}
	cookHeroGrace = time.Minute
	_, data := cookOpen(t, cookStepsRecipe(1), 1)
	cookDrawn(d.r, data)
	mealsWaitFor(t, func() bool { return kept() == 1 })
	cookDrawn(d.r, data)
	if kept() != 1 || asked.Load() != 1 {
		t.Fatalf("%d photos kept, %d asked for", kept(), asked.Load())
	}

	// No frame comes: the photo goes after the grace.
	cookHeroGrace = 30 * time.Millisecond
	cookDrawn(d.r, data)
	mealsWaitFor(t, func() bool { return kept() == 0 })

	// And at once on Finish.
	cookHeroGrace = time.Minute
	_, data = cookOpen(t, cookStepsRecipe(1), 1)
	cookDrawn(d.r, data)
	mealsWaitFor(t, func() bool { return kept() == 1 })
	zones := cookDrawn(d.r, data)
	cookTapOn(d, cookZone(t, zones, "meals:cook:start", "start"))
	*now = now.Add(time.Second)
	zones = cookDrawn(d.r, data)
	cookTapOn(d, cookZone(t, zones, "meals:cook:next", "next"))
	if kept() != 0 || mealsCooking() != nil {
		t.Fatalf("after Finish: %d photos kept, cooking %v", kept(), mealsCooking())
	}
}

// A recipe's photo as a phone took it is over the original's limit. A box wide enough to want the
// original draws the middle size instead, for good, and does not ask the server for the original again.
func TestCookHeroFallsBackToMiddleSize(t *testing.T) {
	d, _ := cookSetUp(t, 1920, 1080)
	var originals, middles atomic.Int32
	phone := bandsPNG(t, 4032, 3024) // 12 million pixels
	libFetch = func(_ context.Context, u string) ([]byte, int, error) {
		if strings.Contains(u, "/images/original.webp") {
			originals.Add(1)
			return phone, http.StatusOK, nil
		}
		middles.Add(1)
		return cookPNG(), http.StatusOK, nil
	}
	_, data := cookOpen(t, cookStepsRecipe(3), 1)
	hero := func() int {
		n := 0
		b := d.r.dst.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if c := d.r.dst.RGBAAt(x, y); c.A == 255 && c.R >= 195 && c.R <= 205 && c.G < 60 {
					n++
				}
			}
		}
		return n
	}
	// The photo's own red edge is wide on the biggest canvas; the placeholder and the page have none.
	mealsWaitFor(t, func() bool {
		cookDrawn(d.r, data)
		return hero() > 10000
	})
	if originals.Load() != 1 || middles.Load() != 1 {
		t.Fatalf("asked for %d originals and %d middle sizes, want one of each", originals.Load(), middles.Load())
	}
	libPics.mu.Lock()
	large := len(libPics.large)
	libPics.mu.Unlock()
	if large != 1 {
		t.Fatalf("%d pictures remembered as too large, want the original", large)
	}
	for range 5 {
		cookDrawn(d.r, data)
	}
	time.Sleep(20 * time.Millisecond)
	if originals.Load() != 1 || middles.Load() != 1 {
		t.Fatalf("after more frames: %d originals and %d middle sizes asked for", originals.Load(), middles.Load())
	}
}

// A picture is asked for with no token on it: Mealie's media needs none, and the address of a picture
// can be anyone's after a redirect.
func TestPictureRequestsCarryNoToken(t *testing.T) {
	var seen, bad atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/api/media/") {
			seen.Add(1)
			if req.Header.Get("Authorization") != "" {
				bad.Add(1)
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write(cookPNG())
			return
		}
		if req.Header.Get("Authorization") != "Bearer token-for-a-test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"id":"id-soup","slug":"tomato-soup","name":"Tomato soup","image":"k1","recipeServings":4,
			"recipeInstructions":[{"text":"Fry the onions."}]}`))
	}))
	t.Cleanup(srv.Close)
	real := libFetch
	d, _ := cookSetUp(t, 1920, 1080)
	// The guard that keeps a picture off the device's own addresses has no use for a test server on it.
	wasClient := libPicClient
	libPicClient = &http.Client{Timeout: 5 * time.Second}
	libFetch = real
	t.Cleanup(func() { libPicClient = wasClient })
	was := mealsClient
	mealsClient = func() mealie.Client { return mealie.Client{URL: srv.URL, Token: "token-for-a-test"} }
	t.Cleanup(func() { mealsClient = was })

	meals.mu.Lock()
	meals.recipe.slug, meals.recipe.scale, meals.recipe.at = "tomato-soup", 1, time.Now()
	meals.mu.Unlock()
	got, err := cookFetch(context.Background(), mealsClient())
	if err != nil {
		t.Fatal(err)
	}
	data := got.(mealsCookData)
	mealsWaitFor(t, func() bool {
		cookDrawn(d.r, data)
		libPics.mu.Lock()
		defer libPics.mu.Unlock()
		return len(libPics.kept) == 1
	})
	if seen.Load() == 0 || bad.Load() != 0 {
		t.Fatalf("%d picture requests, %d of them with an Authorization header", seen.Load(), bad.Load())
	}
}

// A fetch that was on its way when another recipe was opened does not take the place of the recipe being
// cooked: it lands with no model, and the page fetches again.
func TestCookFetchOfAnotherRecipeOpensNothing(t *testing.T) {
	cookSetUp(t, 960, 480)
	meals.mu.Lock()
	meals.recipe.slug, meals.recipe.scale, meals.recipe.at = "tomato-soup", 1, time.Now()
	meals.mu.Unlock()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// While the server is answering, the cook opens another recipe.
		meals.mu.Lock()
		meals.recipe.slug = "pancakes"
		meals.mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"id-soup","slug":"tomato-soup","name":"Tomato soup","recipeInstructions":[{"text":"Fry."}]}`))
	}))
	t.Cleanup(srv.Close)
	mealsCookClose()
	pancakes := mealsCookOpen(mealie.Recipe{RecipeSummary: mealie.RecipeSummary{ID: "id-pan", Slug: "pancakes", Name: "Pancakes"}}, 1)
	pancakes.Go(0)
	got, err := cookFetch(context.Background(), mealie.Client{URL: srv.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if data := got.(mealsCookData); data.Cook != nil || data.Slug != "tomato-soup" {
		t.Fatalf("the fetch of the recipe that is not open opened %+v", data)
	}
	if mealsCooking() != pancakes {
		t.Fatal("the recipe being cooked was replaced by a fetch of another")
	}
}
