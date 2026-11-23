//go:build !dot && !spot

package display

import (
	"context"
	"errors"
	"image"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/feature/voice"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/touch"
	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

// mealsSetUp is a clean config with Mealie set up (a made-up address and token), and then mealsFresh.
func mealsSetUp(t *testing.T) {
	t.Helper()
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	mealsFresh(t)
}

// mealsFresh gives the page a hub and a set of views of its own for a test, set up as the config in use
// says, and puts them back after it.
func mealsFresh(t *testing.T) {
	t.Helper()
	tok := "token-for-a-test"
	if err := config.Set().Mealie().Server("https://mealie.example.test", &tok); err != nil {
		t.Fatal(err)
	}
	wasHub, wasViews := meals, mealsViews
	meals, mealsViews = &mealsHub{}, nil
	t.Cleanup(func() {
		meals.wg.Wait()
		meals, mealsViews = wasHub, wasViews
		setCustomZones(nil)
		_ = config.Set().Screen().Language("")
	})
}

// mealsTestView is a view that fetches with fetch, and records what it is asked to draw.
func mealsTestView(id string, rank int, fetch func(context.Context, mealie.Client) (any, error), drawn *[]any) mealsView {
	return mealsView{
		id: id, label: func() string { return strings.ToUpper(id[:1]) + id[1:] }, rank: rank, every: time.Minute, fetch: fetch,
		draw: func(_ *renderer, _ scene, p mealsPane, body image.Rectangle) []customZone {
			if drawn != nil {
				*drawn = append(*drawn, p.Data)
			}
			return nil
		},
	}
}

func mealsFrameOf(t *testing.T) mealsFrame {
	t.Helper()
	var v customView
	mealsFill(nil, &v)
	f, ok := v.data.(mealsFrame)
	if !ok {
		t.Fatalf("the page's data is a %T", v.data)
	}
	return f
}

// The page is a choice of Idle screen and a tab only once the setup page has a Mealie address and a
// token.
func TestMealsOfferedOnlyWhenSetUp(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	if _, ok := customByID(mealsID); ok {
		t.Fatal("Meals is offered with nothing set up")
	}
	if got := idleScreenOptions(); !slices.Equal(got, []string{"Clock"}) {
		t.Fatalf("idle screen options %v", got)
	}
	tok := "token-for-a-test"
	if err := config.Set().Mealie().Server("https://mealie.example.test", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := customByID(mealsID); ok {
		t.Fatal("Meals is offered with an address and no token")
	}
	if err := config.Set().Mealie().Server("https://mealie.example.test", &tok); err != nil {
		t.Fatal(err)
	}
	if c, ok := customByID(mealsID); !ok || c.label != "Meals" {
		t.Fatalf("Meals is not offered once set up: %+v %v", c, ok)
	}
	if got := idleScreenOptions(); !slices.Equal(got, []string{"Clock", "Meals"}) {
		t.Fatalf("idle screen options %v", got)
	}
	none := ""
	if err := config.Set().Mealie().Server("https://mealie.example.test", &none); err != nil {
		t.Fatal(err)
	}
	if _, ok := customByID(mealsID); ok {
		t.Fatal("Meals is offered after the token was removed")
	}
}

// With Mealie not set up nothing is asked of the network: not by a frame, nor by Retry, nor by opening
// a recipe, nor by a frame of the page that is somehow up.
func TestMealsNotSetUpMakesNoCall(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	var calls atomic.Int32
	wasHub, wasViews := meals, mealsViews
	meals = &mealsHub{}
	mealsViews = []mealsView{mealsTestView("shopping", 1, func(context.Context, mealie.Client) (any, error) {
		calls.Add(1)
		return "x", nil
	}, nil)}
	t.Cleanup(func() { meals.wg.Wait(); meals, mealsViews = wasHub, wasViews })

	d := &Display{poke: make(chan struct{}, 1)}
	f := mealsFrameOf(t)
	if f.view.draw != nil || len(f.tabs) != 0 {
		t.Fatalf("a page that is not set up has a view: %+v", f)
	}
	meals.start("shopping", true)
	meals.retry("shopping")
	meals.reload("shopping")
	if d.openRecipe("tomato-soup", 1) {
		t.Fatal("a recipe was opened with Mealie not set up")
	}
	d.customs.up = mealsID
	s := scene{now: time.Now(), phase: "idle"}
	d.customScene(&s, s.now)
	if s.custom.name != "" {
		t.Fatalf("the page is drawn with Mealie not set up: %q", s.custom.name)
	}
	meals.wg.Wait()
	if n := calls.Load(); n != 0 {
		t.Fatalf("%d fetches with Mealie not set up", n)
	}
}

// Every failure has a sentence of its own, in every language and never the error's own words; and it is
// drawn, with Retry where asking again can help, at every size.
func TestMealsStatesTranslated(t *testing.T) {
	mealsSetUp(t)
	secret := "dial tcp 10.9.8.7:9925: connection refused"
	errs := []error{mealie.ErrToken, mealie.ErrUseHTTPS, mealie.ErrUnreachable, mealie.ErrUnreadable, errors.New(secret)}
	seen := map[string]bool{}
	for _, err := range errs {
		_, english, _ := mealsProblem(err)
		seen[english] = true
		for _, lang := range completeLangs {
			if err := config.Set().Screen().Language(lang); err != nil {
				t.Fatal(err)
			}
			if _, got, _ := mealsProblem(err); got == "" || got == english || strings.HasPrefix(got, "meals_") || strings.Contains(got, secret) {
				t.Errorf("%v in %s: %q", err, lang, got)
			}
		}
		_ = config.Set().Screen().Language("")
	}
	if len(seen) != 3 {
		t.Errorf("the failures share %d sentences, want 3", len(seen))
	}
	if _, _, retry := mealsProblem(mealie.ErrToken); retry {
		t.Error("Retry is offered for a refused token")
	}
	if _, _, retry := mealsProblem(mealie.ErrUnreachable); !retry {
		t.Error("no Retry for a server that is not there")
	}

	registerMealsView(mealsTestView("shopping", 1, nil, nil))
	for _, size := range mealsSizes {
		r := newRenderer(image.NewRGBA(image.Rectangle{Max: size}))
		for _, c := range []struct {
			name  string
			pane  mealsPane
			retry string
		}{
			{"loading", mealsPane{Loading: true}, ""},
			{"unreachable", mealsPane{Err: mealie.ErrUnreachable}, "shopping"},
			{"unreadable", mealsPane{Err: mealie.ErrUnreadable}, "shopping"},
			{"token", mealsPane{Err: mealie.ErrToken}, ""},
			{"https", mealsPane{Err: mealie.ErrUseHTTPS}, ""},
			{"odd", mealsPane{Err: errors.New(secret)}, "shopping"},
		} {
			f := mealsFrame{view: mealsViews[0], tabs: []mealsTab{{id: "shopping", label: "Shopping", on: true}}, pane: c.pane}
			s := scene{now: time.Date(2026, 11, 23, 9, 30, 0, 0, time.UTC), phase: "idle", custom: customView{name: mealsID, data: f}}
			zones := mealsDraw(r, s)
			assertLayout(t, zones, size.X, size.Y, func(z customZone) bool { return z.kind == "meals:retry" })
			var got string
			for _, z := range zones {
				if z.kind == "meals:retry" {
					got = z.id
				}
			}
			if got != c.retry {
				t.Errorf("%v %s: Retry for %q, want %q", size, c.name, got, c.retry)
			}
		}
	}
}

// A refresh that fails leaves the data from before on the page, with the error beside it: the view is
// drawn from the old data, and the time it is from is kept.
func TestMealsOfflineKeepsLastGood(t *testing.T) {
	mealsSetUp(t)
	var fail atomic.Bool
	var drawn []any
	registerMealsView(mealsTestView("shopping", 1, func(context.Context, mealie.Client) (any, error) {
		if fail.Load() {
			return nil, mealie.ErrUnreachable
		}
		return "milk", nil
	}, &drawn))

	f := mealsFrameOf(t)
	if !f.pane.Loading {
		t.Fatal("the first frame did not start a fetch")
	}
	meals.wg.Wait()
	f = mealsFrameOf(t)
	if f.pane.Data != "milk" || !f.pane.Loaded || f.pane.Err != nil || f.pane.Fetched.IsZero() {
		t.Fatalf("after a good fetch: %+v", f.pane)
	}
	was := f.pane.Fetched

	fail.Store(true)
	meals.reload("shopping")
	meals.wg.Wait()
	f = mealsFrameOf(t)
	if f.pane.Data != "milk" || !f.pane.Loaded || !errors.Is(f.pane.Err, mealie.ErrUnreachable) || !f.pane.Fetched.Equal(was) {
		t.Fatalf("after a failed one: %+v", f.pane)
	}
	for _, size := range mealsSizes {
		r := newRenderer(image.NewRGBA(image.Rectangle{Max: size}))
		s := scene{now: time.Date(2026, 11, 23, 9, 30, 0, 0, time.UTC), phase: "idle", custom: customView{name: mealsID, data: f}}
		drawn = nil
		assertLayout(t, mealsDraw(r, s), size.X, size.Y, nil)
		if len(drawn) != 1 || drawn[0] != "milk" {
			t.Fatalf("%v: the view drew %v, not the data from before", size, drawn)
		}
	}
}

// A view that fails waits 30 seconds, then 60, 120 and from there 300; one that goes well, or Retry,
// starts over.
func TestMealsBackoff(t *testing.T) {
	for n, want := range []time.Duration{30, 30, 60, 120, 300, 300, 300} {
		if got := mealsBackoff(n); got != want*time.Second {
			t.Errorf("after %d failures %v, want %vs", n, got, want)
		}
	}

	mealsSetUp(t)
	var fail atomic.Bool
	fail.Store(true)
	registerMealsView(mealsTestView("shopping", 1, func(context.Context, mealie.Client) (any, error) {
		if fail.Load() {
			return nil, mealie.ErrUnreachable
		}
		return "ok", nil
	}, nil))
	wait := func(want time.Duration) {
		t.Helper()
		meals.wg.Wait()
		meals.mu.Lock()
		f := meals.feed("shopping")
		left := time.Until(f.nextAt)
		meals.mu.Unlock()
		if left > want || left < want-5*time.Second {
			t.Errorf("next try in %v, want %v", left, want)
		}
	}
	meals.start("shopping", false)
	wait(30 * time.Second)
	for _, want := range []time.Duration{60, 120, 300, 300} {
		meals.reload("shopping")
		wait(want * time.Second)
	}
	// Retry forgets the failures.
	meals.retry("shopping")
	wait(30 * time.Second)
	// A success does too, and then the next fetch is the view's own pace.
	fail.Store(false)
	meals.reload("shopping")
	meals.wg.Wait()
	meals.mu.Lock()
	f := meals.feed("shopping")
	fails, left := f.fails, time.Until(f.nextAt)
	meals.mu.Unlock()
	if fails != 0 || left > time.Minute || left < time.Minute-5*time.Second {
		t.Errorf("after a success: %d failures, next in %v", fails, left)
	}
	// While one is due a frame starts it, and not before.
	meals.mu.Lock()
	meals.seen = time.Now() // the page is open: a frame is not a reopening
	meals.server = mealsServer(config.Get().Mealie)
	meals.feed("shopping").nextAt = time.Now().Add(time.Hour)
	meals.mu.Unlock()
	if f := mealsFrameOf(t); f.pane.Loading {
		t.Fatal("a fetch started before it was due")
	}
}

// An answer that lands after a newer fetch of the view, or after the server was changed, is dropped.
func TestMealsStaleLoadIgnored(t *testing.T) {
	mealsSetUp(t)
	type answer struct {
		data any
		err  error
	}
	calls := make(chan chan answer, 4)
	registerMealsView(mealsTestView("shopping", 1, func(context.Context, mealie.Client) (any, error) {
		ch := make(chan answer)
		calls <- ch
		a := <-ch
		return a.data, a.err
	}, nil))

	meals.start("shopping", false)
	first := <-calls
	meals.start("shopping", false) // one is on its way: no second
	select {
	case <-calls:
		t.Fatal("a second fetch started while one was on its way")
	case <-time.After(50 * time.Millisecond):
	}
	meals.start("shopping", true) // forced: the first one's answer is not wanted any more
	second := <-calls
	second <- answer{data: "new"}
	mealsWaitFor(t, func() bool { return meals.paneData("shopping") == "new" })
	first <- answer{data: "old"}
	meals.wg.Wait()
	if got := meals.paneData("shopping"); got != "new" {
		t.Fatalf("the late answer replaced the data: %v", got)
	}

	// A fetch on its way when the server changes is the old server's.
	meals.start("shopping", true)
	third := <-calls
	tok := "another-token"
	if err := config.Set().Mealie().Server("https://other.example.test", &tok); err != nil {
		t.Fatal(err)
	}
	var v customView
	mealsFill(nil, &v)
	// The frame started a fetch for the new server; the old server's answer lands first, then the new one's.
	var fresh chan answer
	select {
	case fresh = <-calls:
	case <-time.After(5 * time.Second):
		t.Fatal("no fetch for the new server")
	}
	third <- answer{data: "from the old server"}
	waitFor := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(waitFor) {
		if got := meals.paneData("shopping"); got == "from the old server" {
			t.Fatal("an answer from the old server was kept")
		}
		time.Sleep(5 * time.Millisecond)
	}
	fresh <- answer{data: "from the new server"}
	meals.wg.Wait()
	if got := meals.paneData("shopping"); got != "from the new server" {
		t.Fatalf("data %v", got)
	}
}

func (h *mealsHub) paneData(id string) any {
	h.mu.Lock()
	defer h.mu.Unlock()
	if f := h.feeds[id]; f != nil {
		return f.Data
	}
	return nil
}

func mealsWaitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out")
}

// A tap on a dark screen only lights it: it is not a tap on the page's switcher or on the view. The same
// tap on a lit one is, and a tap past the page's own zones is the view's.
func TestMealsWakeTapOnlyLights(t *testing.T) {
	d := noticeDisplay(t, 960, 480)
	mealsFresh(t)
	var taps atomic.Int32
	shopping := mealsTestView("shopping", 1, func(context.Context, mealie.Client) (any, error) { return "x", nil }, nil)
	shopping.gesture = func(*Display, touch.Gesture, image.Point) { taps.Add(1) }
	registerMealsView(shopping)
	registerMealsView(mealsTestView("plan", 2, func(context.Context, mealie.Client) (any, error) { return "y", nil }, nil))
	meals.wg.Wait()
	d.view = voice.State{Phase: "idle"}
	d.mu.Lock()
	d.customs.up, d.customs.shown = mealsID, mealsID
	d.mu.Unlock()

	// What the page drew: the switcher, whose Plan segment is aimed at.
	f := mealsFrameOf(t)
	meals.wg.Wait()
	s := scene{now: time.Date(2026, 11, 23, 9, 30, 0, 0, time.UTC), phase: "idle", custom: customView{name: mealsID, data: f}}
	zones := mealsDraw(d.r, s)
	setCustomZones(zones)
	var plan image.Point
	for _, z := range zones {
		if z.kind == "meals:view" && z.id == "plan" {
			plan = z.r.Min.Add(image.Pt(10, 10))
		}
	}
	if plan == (image.Point{}) {
		t.Fatalf("no zone for the Plan segment in %v", zones)
	}
	body := image.Pt(480, 300)

	var view string
	for _, at := range []image.Point{plan, body} {
		d.mu.Lock()
		d.on = false
		d.mu.Unlock()
		tapAt(d, at)
		d.mu.Lock()
		on := d.on
		d.mu.Unlock()
		if !on {
			t.Fatalf("a tap at %v on a dark screen did not light it", at)
		}
		meals.mu.Lock()
		view = meals.view
		meals.mu.Unlock()
		if view == "plan" || taps.Load() != 0 {
			t.Fatalf("the tap at %v that lit the screen was also a tap on the page: view %q, %d view taps", at, view, taps.Load())
		}
	}

	tapAt(d, body)
	if taps.Load() != 1 {
		t.Fatalf("a tap on the lit page reached the view %d times", taps.Load())
	}
	tapAt(d, plan)
	meals.mu.Lock()
	view = meals.view
	meals.mu.Unlock()
	if view != "plan" {
		t.Fatalf("a tap on the Plan segment left the page on %q", view)
	}
}

// A view hands the page what it wants of the screen: kept awake while it is the one shown, and not when
// another is.
func TestMealsKeepsAwakeFromTheView(t *testing.T) {
	mealsSetUp(t)
	var awake atomic.Bool
	cooking := mealsTestView("cooking", 2, nil, nil)
	cooking.awake, cooking.night = awake.Load, awake.Load
	registerMealsView(mealsTestView("shopping", 1, nil, nil))
	registerMealsView(cooking)
	d := &Display{poke: make(chan struct{}, 1)}
	d.customs.up = mealsID
	c, _ := customByID(mealsID)

	meals.show("cooking")
	if d.customAwake() || c.awake() {
		t.Fatal("kept awake by a view that does not want it")
	}
	awake.Store(true)
	if !d.customAwake() || !c.night() {
		t.Fatal("not kept awake by the view that wants it")
	}
	meals.show("shopping")
	if d.customAwake() {
		t.Fatal("kept awake by a view that is not on the screen")
	}
	// A view the switcher does not offer is not the view shown.
	cooking.shown = func() bool { return false }
	mealsViews = []mealsView{mealsViews[0], cooking}
	meals.show("cooking")
	if cur, _ := meals.current(); cur.id != "shopping" {
		t.Fatalf("a view the switcher does not offer is shown: %q", cur.id)
	}
}

// The recipe opened is kept for the page to take, and a page that is put away for long enough starts
// again on its first view.
func TestMealsOpenRecipeAndMemory(t *testing.T) {
	d := noticeDisplay(t, 960, 480)
	mealsFresh(t)
	registerMealsView(mealsTestView("shopping", 1, nil, nil))
	registerMealsView(mealsTestView("cooking", 2, nil, nil))
	if _, _, ok := mealsOpenRecipe(); ok {
		t.Fatal("a recipe is open at the start")
	}
	if d.openRecipe("", 1) {
		t.Fatal("a recipe with no name was opened")
	}
	if !d.openRecipe("tomato-soup", 0) {
		t.Fatal("the recipe was not opened")
	}
	if slug, scale, ok := mealsOpenRecipe(); !ok || slug != "tomato-soup" || scale != 1 {
		t.Fatalf("recipe %q at %v, %v", slug, scale, ok)
	}
	d.mu.Lock()
	up := d.customs.up
	d.mu.Unlock()
	if up != mealsID {
		t.Fatalf("the page is not up: %q", up)
	}
	if cur, _ := meals.current(); cur.id != "cooking" {
		t.Fatalf("view %q", cur.id)
	}

	meals.mu.Lock()
	meals.seen = time.Now().Add(-mealsKeep - time.Minute)
	meals.recipe.at = time.Now().Add(-mealsKeep - time.Minute)
	meals.mu.Unlock()
	if _, _, ok := mealsOpenRecipe(); ok {
		t.Fatal("a recipe from long ago is still open")
	}
	var v customView
	mealsFill(nil, &v)
	if cur, _ := meals.current(); cur.id != "shopping" {
		t.Fatalf("after a long while the page is on %q", cur.id)
	}
}

// A second tap of the same button within 250 ms is a wet finger's.
func TestMealsDebounce(t *testing.T) {
	var b mealsDebounce
	now := time.Now()
	for i, c := range []struct {
		after time.Duration
		want  bool
	}{{0, true}, {100 * time.Millisecond, false}, {249 * time.Millisecond, false}, {260 * time.Millisecond, true}, {300 * time.Millisecond, false}} {
		if got := b.ok(now.Add(c.after)); got != c.want {
			t.Errorf("tap %d after %v: %v, want %v", i, c.after, got, c.want)
		}
	}
}

// The switcher offers each view the page has, and a view that is not offered is not in it.
func TestMealsSwitcherZones(t *testing.T) {
	mealsSetUp(t)
	registerMealsView(mealsTestView("shopping", 1, nil, nil))
	registerMealsView(mealsTestView("plan", 2, nil, nil))
	hidden := mealsTestView("cooking", 3, nil, nil)
	hidden.shown = func() bool { return false }
	registerMealsView(hidden)
	f := mealsFrameOf(t)
	meals.wg.Wait()
	if len(f.tabs) != 2 || f.tabs[0].id != "shopping" || !f.tabs[0].on || f.tabs[1].on {
		t.Fatalf("tabs %+v", f.tabs)
	}
	for _, size := range mealsSizes {
		r := newRenderer(image.NewRGBA(image.Rectangle{Max: size}))
		s := scene{now: time.Date(2026, 11, 23, 9, 30, 0, 0, time.UTC), phase: "idle", custom: customView{name: mealsID, data: f}}
		zones := mealsDraw(r, s)
		assertLayout(t, zones, size.X, size.Y, func(z customZone) bool { return z.kind == "meals:view" })
		var ids []string
		for _, z := range zones {
			if z.kind == "meals:view" {
				ids = append(ids, z.id)
			}
		}
		if !slices.Equal(ids, []string{"shopping", "plan"}) {
			t.Errorf("%v: switcher zones %v", size, ids)
		}
	}
}

// Opening another recipe forgets what went wrong with the one before, so that its error is not shown over
// the new one, and has the new one fetched at once rather than after the backoff.
func TestMealsOpenRecipeForgetsTheOthersError(t *testing.T) {
	d := noticeDisplay(t, 960, 480)
	mealsFresh(t)
	var fetched atomic.Int32
	registerMealsView(mealsTestView("shopping", 1, nil, nil))
	registerMealsView(mealsTestView("cooking", 2, func(context.Context, mealie.Client) (any, error) {
		fetched.Add(1)
		return mealsCookData{Slug: "pancakes"}, nil
	}, nil))
	if !d.openRecipe("tomato-soup", 1) {
		t.Fatal("the recipe was not opened")
	}
	meals.wg.Wait()
	meals.mu.Lock()
	f := meals.feed("cooking")
	f.Err, f.fails, f.nextAt = mealie.ErrUnreachable, 3, time.Now().Add(2*time.Minute)
	meals.mu.Unlock()
	fetched.Store(0)

	// The same recipe again: nothing is forgotten.
	d.openRecipe("tomato-soup", 1)
	meals.wg.Wait()
	meals.mu.Lock()
	kept := meals.feed("cooking").Err
	meals.mu.Unlock()
	if kept == nil || fetched.Load() != 0 {
		t.Fatalf("reopening the same recipe: error %v, %d fetches", kept, fetched.Load())
	}

	d.openRecipe("pancakes", 1)
	meals.wg.Wait()
	meals.mu.Lock()
	f = meals.feed("cooking")
	err, fails, loaded := f.Err, f.fails, f.Loaded
	meals.mu.Unlock()
	if err != nil || fails != 0 || !loaded || fetched.Load() != 1 {
		t.Fatalf("after opening another recipe: error %v after %d fails, loaded %v, %d fetches", err, fails, loaded, fetched.Load())
	}
}
