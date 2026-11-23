//go:build !dot && !spot

package display

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/touch"
	"github.com/HuskerMinion/techo5/echod/internal/lib/locale"
	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

// mealsPlanToday is the made-up day every test of the Plan view is on: a Monday, in the middle of the
// morning.
var mealsPlanToday = time.Date(2026, 11, 23, 9, 30, 0, 0, time.UTC)

// reset puts the view's state back as it is before the page is first drawn.
func (s *mealsPlanState) reset() {
	s.Lock()
	s.x, s.y, s.placed, s.hits, s.maxY, s.maxX, s.stepX, s.stepY, s.top, s.portrait = 0, nil, "", nil, nil, 0, 0, 0, 0, false
	s.Unlock()
}

// mealsPlanSetUp is a page with the Plan view on it and nothing drawn yet, for a test of the view, with
// pictures served from memory, and puts everything back after it.
func mealsPlanSetUp(t *testing.T) {
	t.Helper()
	mealsSetUp(t)
	registerMealsView(mealsPlanView())
	mealsPlanUI.reset()
	mealsPlanThumbs.Lock()
	mealsPlanThumbs.held, mealsPlanThumbs.frame = nil, nil
	mealsPlanThumbs.Unlock()
	wasPics, wasFetch := libPics, libFetch
	libPics = &libPictures{}
	libFetch = func(context.Context, string) ([]byte, int, error) { return mealsPlanPNG(), http.StatusOK, nil }
	t.Cleanup(func() { libPics, libFetch = wasPics, wasFetch; mealsPlanUI.reset() })
}

// mealsPlanPNG is a small made-up picture.
func mealsPlanPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 120, 90))
	for y := range 90 {
		for x := range 120 {
			img.SetRGBA(x, y, color.RGBA{uint8(2 * x), uint8(2 * y), 90, 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// mealsPlanEntry is a made-up entry of the plan: a recipe of the made-up set, or with an empty slug a
// note.
func mealsPlanMake(id int, date, kind, name, slug string, scale float64) mealie.Entry {
	e := mealie.Entry{ID: id, Date: date, Type: kind, Scale: scale}
	if slug == "" {
		e.Title = name
		return e
	}
	e.RecipeID = "id-" + slug
	e.Recipe = &mealie.RecipeSummary{ID: "id-" + slug, Slug: slug, Name: name, Image: "k-" + slug, TotalTimeSeconds: 2700}
	return e
}

// mealsPlanEntries are the entries of a made-up week from today: this week's, five for today (sent in no
// order), two for each of the other days and a note.
func mealsPlanEntries() []mealie.Entry {
	week := mealsPlanMake(1, "2026-11-23", "dinner", "Apple cake", "apple-cake", 1)
	week.EndDate = "2026-11-29"
	entries := []mealie.Entry{
		week,
		mealsPlanMake(5, "2026-11-23", "dessert", "Apple cake", "apple-cake", 1),
		mealsPlanMake(4, "2026-11-23", "snack", "Pancakes", "pancakes", 1),
		mealsPlanMake(3, "2026-11-23", "dinner", "Lentil curry", "lentil-curry", 1),
		mealsPlanMake(2, "2026-11-23", "breakfast", "Pancakes", "pancakes", 1),
		mealsPlanMake(6, "2026-11-23", "lunch", "Tomato soup", "tomato-soup", 1),
	}
	for i := 1; i < mealsPlanDays; i++ {
		day := mealsPlanToday.AddDate(0, 0, i).Format(mealsPlanDate)
		entries = append(entries,
			mealsPlanMake(10+2*i, day, "dinner", "Lentil curry with rice and a long name that wraps", "lentil-curry", 1),
			mealsPlanMake(11+2*i, day, "lunch", "Tomato soup", "tomato-soup", 1))
	}
	// A note for the third day, and a day outside the seven, and an entry with nothing to show.
	entries = append(entries,
		mealsPlanMake(90, "2026-11-25", "side", "Eating out", "", 1),
		mealsPlanMake(91, "2026-12-30", "dinner", "Far away", "pancakes", 1),
		mealsPlanMake(92, "2026-11-24", "dinner", "", "", 1))
	return entries
}

// mealsPlanFixture is the plan of the made-up week.
func mealsPlanFixture() mealsPlanData {
	from := mealsPlanDayStart(mealsPlanToday)
	return mealsPlanBuild(mealie.Client{URL: "https://mealie.example.test"}, from, mealsPlanEntries())
}

// mealsPlanDrawn draws the page on the Plan view with data at w by h, as the display does, and returns
// what it drew and the zones.
func mealsPlanDrawn(data any, w, h int) (*renderer, []customZone) {
	r := newRenderer(image.NewRGBA(image.Rect(0, 0, w, h)))
	f := mealsFrame{
		view: mealsPlanView(),
		tabs: []mealsTab{{id: "shopping", label: "Shopping"}, {id: "plan", label: "Plan", on: true}},
		pane: mealsPane{Data: data, Loaded: true, Fetched: mealsPlanToday},
	}
	s := scene{now: mealsPlanToday, phase: "idle", custom: customView{name: mealsID, data: f}}
	return r, mealsDraw(r, s)
}

func mealsPlanEntryZones(zones []customZone) []customZone {
	var out []customZone
	for _, z := range zones {
		if z.kind == "meals:entry" {
			out = append(out, z)
		}
	}
	return out
}

// The cards hold three whole days and the edge of a fourth on every landscape screen, each at least 300
// units wide with rows at least 72 high, and a screen held upright shows an agenda of at least ten
// entries; every zone is on the screen, far enough apart and big enough for a finger.
func TestMealsPlanLayout(t *testing.T) {
	mealsPlanSetUp(t)
	data := mealsPlanFixture()
	for _, size := range mealsSizes {
		mealsPlanUI.reset()
		w, h := size.X, size.Y
		s := func(n int) int { return (n*w + 480) / 960 }
		_, zones := mealsPlanDrawn(data, w, h)
		assertLayout(t, zones, w, h, func(z customZone) bool { return z.kind == "meals:entry" || z.kind == "meals:view" })
		entries := mealsPlanEntryZones(zones)
		for _, z := range entries {
			if z.r.Dy() < s(64) {
				t.Errorf("%v: an entry's zone is %d high", size, z.r.Dy())
			}
		}
		mealsPlanUI.Lock()
		hits, portrait := slices.Clone(mealsPlanUI.hits), mealsPlanUI.portrait
		mealsPlanUI.Unlock()
		if portrait != (h > w) {
			t.Errorf("%v: agenda is %v", size, portrait)
		}
		if h > w {
			if len(entries) < 10 {
				t.Errorf("%v: %d entries in the agenda, want 10 or more", size, len(entries))
			}
			continue
		}
		whole, part := 0, 0
		for _, hit := range hits {
			switch width := hit.x1 - hit.x0; {
			case width >= s(mealsPlanCardW):
				whole++
			case width > 0:
				part++
			}
		}
		if whole != 3 || part < 1 {
			t.Errorf("%v: %d whole cards and %d part ones, want 3 and a part of a fourth", size, whole, part)
		}
		// Today's card is the first on the screen, and its rows are at least 72 units high.
		first := 0
		for _, z := range entries {
			if z.r.Max.X <= s(8+mealsPlanCardW) {
				first++
				if z.r.Dy() < s(72) {
					t.Errorf("%v: a row of the first card is %d high, under %d", size, z.r.Dy(), s(72))
				}
			}
		}
		if first < 3 {
			t.Errorf("%v: %d rows in the first card, want 3 or more", size, first)
		}
	}
}

// The plan is dealt out into the week's card and one for each day from today, today's marked and the strip
// scrolled to it, a day's entries in the order of a day, and what has nothing to show or is out of range is
// left out. The fetch asks the server for the seven days from today.
func TestMealsPlanCardsAndToday(t *testing.T) {
	mealsPlanSetUp(t)
	data := mealsPlanFixture()
	cols := data.cols(mealsPlanToday)
	if len(cols) != 1+mealsPlanDays || !cols[0].week || !cols[1].today || cols[2].today {
		t.Fatalf("cards %+v", cols)
	}
	for i, want := range []string{"This week", "Today", "Tomorrow", "Wed 25", "Thu 26"} {
		if got := cols[i].head(mealsPlanToday, false); got != want {
			t.Errorf("card %d is headed %q, want %q", i, got, want)
		}
	}
	if got := cols[3].head(mealsPlanToday, true); got != "Wednesday, November 25" {
		t.Errorf("an agenda heading is %q", got)
	}
	var kinds []string
	for _, e := range cols[1].entries {
		kinds = append(kinds, e.Type)
	}
	if !slices.Equal(kinds, []string{"breakfast", "lunch", "dinner", "snack", "dessert"}) {
		t.Errorf("today's entries are in the order %v", kinds)
	}
	if n := len(cols[3].entries); n != 3 { // the day's two, and the note
		t.Errorf("the third day has %d entries, want 3", n)
	}
	for _, c := range cols {
		for _, e := range c.entries {
			if e.Name() == "" || e.ID == 91 {
				t.Errorf("entry %d should be left out", e.ID)
			}
		}
	}
	if e := cols[1].entries[0]; e.Thumb != "https://mealie.example.test/api/media/recipes/id-pancakes/images/tiny-original.webp?v=k-pancakes" {
		t.Errorf("thumbnail %q", e.Thumb)
	}
	if cols[3].entries[2].Thumb != "" {
		t.Error("a note has a thumbnail")
	}
	// A plan fetched yesterday still has yesterday in it, which is not shown.
	if got := data.cols(mealsPlanToday.AddDate(0, 0, 2)); len(got) != 1+mealsPlanDays-2 || got[1].today != true {
		t.Errorf("two days on there are %d cards, today's second: %+v", len(got), got)
	}

	// The strip starts on today's card, and today's is outlined in the accent.
	r, _ := mealsPlanDrawn(data, 960, 480)
	mealsPlanUI.Lock()
	x, step := mealsPlanUI.x, mealsPlanUI.stepX
	mealsPlanUI.Unlock()
	if step == 0 || x != step {
		t.Fatalf("the strip is scrolled to %d, today's card is at %d", x, step)
	}
	dist := func(a, b color.RGBA) int {
		d := func(x, y uint8) int { return max(int(x)-int(y), int(y)-int(x)) }
		return d(a.R, b.R) + d(a.G, b.G) + d(a.B, b.B)
	}
	mid := 250
	if got := r.dst.RGBAAt(9, mid); dist(got, amber) > 30 {
		t.Errorf("today's card is outlined %v, want the accent %v", got, amber)
	}
	if got := r.dst.RGBAAt(317, mid); dist(got, amber) < 60 {
		t.Errorf("tomorrow's card is outlined in the accent: %v", got)
	}
	// Without anything for the week the strip starts at the first card.
	data.Week = nil
	mealsPlanUI.reset()
	mealsPlanDrawn(data, 960, 480)
	mealsPlanUI.Lock()
	x = mealsPlanUI.x
	mealsPlanUI.Unlock()
	if x != 0 {
		t.Errorf("without a week's card the strip is at %d", x)
	}

	// What is fetched: the seven days from today, the week's entries too.
	var query atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query.Store(r.URL.Query())
		fmt.Fprint(w, `{"items":[
			{"id":7,"date":"2026-11-24","entryType":"dinner","title":"","text":"","recipeId":"r1","recipeScale":2,
			 "recipe":{"id":"r1","slug":"lentil-curry","name":"Lentil curry","image":"abc","totalTimeSeconds":3600}},
			{"id":8,"date":"2026-11-24","entryType":"breakfast","title":"Fruit","text":"","recipeId":null}]}`)
	}))
	t.Cleanup(srv.Close)
	wasNow := mealsPlanNow
	mealsPlanNow = func() time.Time { return mealsPlanToday }
	t.Cleanup(func() { mealsPlanNow = wasNow })
	got, err := mealsPlanFetch(context.Background(), mealie.Client{URL: srv.URL, Token: "token-for-a-test"})
	if err != nil {
		t.Fatal(err)
	}
	q := query.Load().(url.Values)
	if q["start_date"][0] != "2026-11-23" || q["end_date"][0] != "2026-11-29" || q["include_week_entries"][0] != "true" {
		t.Errorf("the fetch asked for %v", q)
	}
	d := got.(mealsPlanData)
	if len(d.Days) != 7 || len(d.Days[1].Entries) != 2 || d.Days[1].Entries[0].Name() != "Fruit" || d.Days[1].Entries[1].StartScale() != 2 {
		t.Errorf("the second day is %+v", d.Days[1])
	}
}

// A card with nothing for its day says so, a plan with nothing in it at all says that in one sentence, and
// both are in every language.
func TestMealsPlanEmptyDayAndWeek(t *testing.T) {
	mealsPlanSetUp(t)
	from := mealsPlanDayStart(mealsPlanToday)
	empty := mealsPlanBuild(mealie.Client{}, from, nil)
	if !empty.empty() || mealsPlanFixture().empty() {
		t.Fatal("empty reads wrongly")
	}
	oneDay := mealsPlanBuild(mealie.Client{}, from, []mealie.Entry{mealsPlanMake(1, "2026-11-24", "dinner", "Pancakes", "pancakes", 1)})
	if oneDay.empty() {
		t.Fatal("a plan with an entry is empty")
	}
	for _, size := range mealsSizes {
		mealsPlanUI.reset()
		_, zones := mealsPlanDrawn(empty, size.X, size.Y)
		assertLayout(t, zones, size.X, size.Y, func(z customZone) bool { return z.kind == "meals:view" })
		if n := len(mealsPlanEntryZones(zones)); n != 0 {
			t.Errorf("%v: %d entries in an empty plan", size, n)
		}
		mealsPlanUI.reset()
		_, zones = mealsPlanDrawn(oneDay, size.X, size.Y)
		assertLayout(t, zones, size.X, size.Y, func(z customZone) bool { return z.kind == "meals:entry" || z.kind == "meals:view" })
		if n := len(mealsPlanEntryZones(zones)); n != 1 {
			t.Errorf("%v: %d entries for a plan of one", size, n)
		}
	}
	for _, key := range []string{"meals_nothing_planned", "meals_nothing_week", "meals_this_week", "meals_today", "meals_tomorrow",
		"meals_breakfast", "meals_lunch", "meals_dinner", "meals_side", "meals_snack", "meals_drink", "meals_dessert",
		"meals_minutes", "meals_hours", "meals_hours_minutes"} {
		english := locale.Text("", key)
		if english == key || english == "" {
			t.Errorf("%s has no English words", key)
		}
		for _, lang := range completeLangs {
			if got := locale.Text(lang, key); got == key || got == "" {
				t.Errorf("%s has no words in %s", key, lang)
			}
		}
	}
	for _, c := range []struct {
		seconds int
		want    string
	}{{0, ""}, {20, "1 min"}, {2700, "45 min"}, {3600, "1 h"}, {5400, "1 h 30 min"}} {
		if got := mealsPlanTime(c.seconds); got != c.want {
			t.Errorf("%d seconds are %q, want %q", c.seconds, got, c.want)
		}
	}
}

// An entry with no recipe, a note with a title, has no place to tap and opens nothing; one with a recipe
// has.
func TestMealsPlanTitleOnlyNotTappable(t *testing.T) {
	d := noticeDisplay(t, 960, 480)
	mealsPlanSetUp(t)
	from := mealsPlanDayStart(mealsPlanToday)
	note := mealsPlanMake(99, "2026-11-23", "lunch", "Eating out", "", 1)
	note.Text = "Pizza place"
	data := mealsPlanBuild(mealie.Client{}, from, []mealie.Entry{note, mealsPlanMake(98, "2026-11-23", "dinner", "Pancakes", "pancakes", 1)})
	meals.mu.Lock()
	meals.feed("plan").Data = data
	meals.mu.Unlock()

	_, zones := mealsPlanDrawn(data, 960, 480)
	setCustomZones(zones)
	entries := mealsPlanEntryZones(zones)
	if len(entries) != 1 || entries[0].id != "98" {
		t.Fatalf("zones %+v, want only the recipe's", entries)
	}
	// Even if a zone were somehow there for the note, the view does not open it.
	setCustomZones([]customZone{{r: image.Rect(0, 100, 960, 400), kind: "meals:entry", id: "99"}})
	mealsPlanGesture(d, touch.Gesture{Kind: touch.Tap, X: 100, Y: 200}, image.Pt(100, 200))
	if _, _, ok := mealsOpenRecipe(); ok {
		t.Fatal("a note was opened as a recipe")
	}
	setCustomZones(zones)
	at := entries[0].r.Min.Add(image.Pt(20, 20))
	mealsPlanGesture(d, touch.Gesture{Kind: touch.Tap, X: at.X, Y: at.Y}, at)
	if slug, _, ok := mealsOpenRecipe(); !ok || slug != "pancakes" {
		t.Fatalf("the recipe's row opened %q, %v", slug, ok)
	}
}

// A tap on a recipe opens it at the scale the plan gives it, and at 1 when the plan gives none or a scale
// that makes no sense.
func TestMealsPlanTapOpensAtScale(t *testing.T) {
	d := noticeDisplay(t, 960, 480)
	mealsPlanSetUp(t)
	from := mealsPlanDayStart(mealsPlanToday)
	entries := []mealie.Entry{
		mealsPlanMake(1, "2026-11-23", "breakfast", "Pancakes", "pancakes", 2.5),
		mealsPlanMake(2, "2026-11-23", "lunch", "Tomato soup", "tomato-soup", 0),
		mealsPlanMake(3, "2026-11-23", "dinner", "Lentil curry", "lentil-curry", -1),
	}
	data := mealsPlanBuild(mealie.Client{}, from, entries)
	meals.mu.Lock()
	meals.feed("plan").Data = data
	meals.mu.Unlock()
	_, zones := mealsPlanDrawn(data, 960, 480)
	setCustomZones(zones)
	rows := mealsPlanEntryZones(zones)
	if len(rows) != 3 {
		t.Fatalf("%d rows", len(rows))
	}
	for i, want := range []struct {
		slug  string
		scale float64
	}{{"pancakes", 2.5}, {"tomato-soup", 1}, {"lentil-curry", 1}} {
		at := rows[i].r.Min.Add(image.Pt(20, 20))
		mealsPlanGesture(d, touch.Gesture{Kind: touch.Tap, X: at.X, Y: at.Y}, at)
		if slug, scale, ok := mealsOpenRecipe(); !ok || slug != want.slug || scale != want.scale {
			t.Errorf("row %d opened %q at %v (%v), want %q at %v", i, slug, scale, ok, want.slug, want.scale)
		}
		meals.mu.Lock()
		view := meals.view
		meals.mu.Unlock()
		if view != "cooking" {
			t.Errorf("row %d left the page on %q", i, view)
		}
	}
	// A tap that is on no row opens nothing new.
	meals.mu.Lock()
	meals.recipe.slug = ""
	meals.mu.Unlock()
	mealsPlanGesture(d, touch.Gesture{Kind: touch.Tap, X: 5, Y: 300}, image.Pt(5, 300))
	if _, _, ok := mealsOpenRecipe(); ok {
		t.Error("a tap between the cards opened a recipe")
	}
}

// A swipe is one thing or the other: across moves the strip by a card and never scrolls a card, up or
// down scrolls the card it is on and never the strip, and neither starts in the bar or goes past the ends.
func TestMealsPlanAxisLock(t *testing.T) {
	d := noticeDisplay(t, 960, 480)
	mealsPlanSetUp(t)
	data := mealsPlanFixture()
	mealsPlanDrawn(data, 960, 480)
	ui := &mealsPlanUI
	read := func() (x int, y map[string]int) {
		ui.Lock()
		defer ui.Unlock()
		// Only the cards that are scrolled: one the page has drawn and left alone is 0, as one not drawn is.
		scrolled := map[string]int{}
		for k, v := range ui.y {
			if v != 0 {
				scrolled[k] = v
			}
		}
		return ui.x, scrolled
	}
	swipe := func(kind touch.Kind, at image.Point) {
		mealsPlanGesture(d, touch.Gesture{Kind: kind, X: at.X, Y: at.Y}, at)
		mealsPlanDrawn(data, 960, 480)
	}
	x0, _ := read()
	today := mealsPlanToday.Format(mealsPlanDate)

	// Up over today's card: it scrolls, a notch for each, and the strip does not move.
	swipe(touch.SwipeUp, image.Pt(100, 300))
	x, y := read()
	ui.Lock()
	notch := ui.stepY
	ui.Unlock()
	if x != x0 || y[today] != notch {
		t.Fatalf("after up: strip %d (was %d), cards %v", x, x0, y)
	}
	for k, v := range y {
		if k != today && v != 0 {
			t.Fatalf("after up the card of %s is scrolled to %d too", k, v)
		}
	}
	swipe(touch.SwipeUp, image.Pt(100, 300))
	if _, y = read(); y[today] != 2*notch {
		t.Fatalf("after a second up: %v", y)
	}
	swipe(touch.SwipeDown, image.Pt(100, 300))
	swipe(touch.SwipeDown, image.Pt(100, 300))
	swipe(touch.SwipeDown, image.Pt(100, 300)) // past the top
	if _, y = read(); y[today] != 0 {
		t.Fatalf("after down past the top: %v", y)
	}
	// In the bar, or between the cards, nothing scrolls.
	swipe(touch.SwipeUp, image.Pt(100, 20))
	swipe(touch.SwipeUp, image.Pt(5, 300))
	if _, y = read(); y[today] != 0 {
		t.Fatalf("after up in the bar: %v", y)
	}

	// Across: by a card, to the end and no further, and no card scrolls.
	swipe(touch.SwipeUp, image.Pt(100, 300))
	_, before := read()
	swipe(touch.SwipeLeft, image.Pt(500, 300))
	if x, y = read(); x != x0+stepOf(ui) || !maps.Equal(y, before) {
		t.Fatalf("after left: strip %d (was %d), cards %v (was %v)", x, x0, y, before)
	}
	for range 20 {
		swipe(touch.SwipeLeft, image.Pt(500, 300))
	}
	ui.Lock()
	most := ui.maxX
	ui.Unlock()
	if x, _ = read(); x != most {
		t.Fatalf("after many lefts the strip is at %d, the end is %d", x, most)
	}
	for range 20 {
		swipe(touch.SwipeRight, image.Pt(500, 300))
	}
	if x, y = read(); x != 0 || !maps.Equal(y, before) {
		t.Fatalf("after many rights: strip %d, cards %v", x, y)
	}

	// Held upright there is an agenda: down scrolls it, across does nothing.
	mealsPlanUI.reset()
	mealsPlanDrawn(data, 800, 1280)
	mealsPlanGesture(d, touch.Gesture{Kind: touch.SwipeLeft, X: 400, Y: 600}, image.Pt(400, 600))
	mealsPlanGesture(d, touch.Gesture{Kind: touch.SwipeUp, X: 400, Y: 600}, image.Pt(400, 600))
	mealsPlanDrawn(data, 800, 1280)
	if x, y = read(); x != 0 || y["agenda"] == 0 || len(y) != 1 {
		t.Fatalf("agenda: strip %d, scrolled %v", x, y)
	}
}

// stepOf is how far a swipe across moves the strip: a card and the room between cards.
func stepOf(s *mealsPlanState) int {
	s.Lock()
	defer s.Unlock()
	return s.stepX
}

// The view holds at most six thumbnails: a frame asks for six of the pictures it shows however many
// there are, and when the strip moves on the ones that are not shown any more are let go.
func TestMealsPlanThumbCap(t *testing.T) {
	d := noticeDisplay(t, 960, 480)
	mealsPlanSetUp(t)
	var fetched atomic.Int32
	libFetch = func(context.Context, string) ([]byte, int, error) {
		fetched.Add(1)
		return mealsPlanPNG(), http.StatusOK, nil
	}
	// Three recipes in each of the seven days, every one with a picture of its own.
	var entries []mealie.Entry
	for d := range mealsPlanDays {
		day := mealsPlanToday.AddDate(0, 0, d).Format(mealsPlanDate)
		for i, kind := range []string{"breakfast", "lunch", "dinner"} {
			n := d*3 + i
			entries = append(entries, mealsPlanMake(100+n, day, kind, "Recipe "+strconv.Itoa(n), "recipe-"+strconv.Itoa(n), 1))
		}
	}
	data := mealsPlanBuild(mealie.Client{URL: "https://mealie.example.test"}, mealsPlanDayStart(mealsPlanToday), entries)
	kept := func() int {
		libPics.mu.Lock()
		defer libPics.mu.Unlock()
		return len(libPics.kept)
	}
	held := func() int {
		mealsPlanThumbs.Lock()
		defer mealsPlanThumbs.Unlock()
		return len(mealsPlanThumbs.held)
	}

	mealsPlanDrawn(data, 960, 480) // nine rows are shown, and six pictures asked for
	mealsWaitFor(t, func() bool { return kept() == mealsPlanThumbsMost })
	for range 3 {
		mealsPlanDrawn(data, 960, 480)
		time.Sleep(20 * time.Millisecond)
	}
	if n := fetched.Load(); n != mealsPlanThumbsMost {
		t.Fatalf("%d pictures fetched for nine rows, want %d", n, mealsPlanThumbsMost)
	}
	if n := held(); n != mealsPlanThumbsMost || kept() != mealsPlanThumbsMost {
		t.Fatalf("held %d, kept %d", n, kept())
	}

	// Three cards on, none of the same rows is shown.
	for range 3 {
		mealsPlanGesture(d, touch.Gesture{Kind: touch.SwipeLeft, X: 500, Y: 300}, image.Pt(500, 300))
	}
	mealsPlanDrawn(data, 960, 480)
	mealsWaitFor(t, func() bool { return fetched.Load() == 2*mealsPlanThumbsMost })
	mealsWaitFor(t, func() bool { return kept() == mealsPlanThumbsMost })
	time.Sleep(20 * time.Millisecond)
	if n := held(); n > mealsPlanThumbsMost || kept() > mealsPlanThumbsMost {
		t.Fatalf("after scrolling on: held %d, kept %d", n, kept())
	}
}

// The thumbnails of the plan are asked for with no token on them: Mealie's media needs none, and the
// address of a picture can be anyone's after a redirect.
func TestMealsPlanThumbnailRequestsCarryNoToken(t *testing.T) {
	var seen, bad atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen.Add(1)
		if req.Header.Get("Authorization") != "" {
			bad.Add(1)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write(mealsPlanPNG())
	}))
	t.Cleanup(srv.Close)
	real := libFetch
	mealsPlanSetUp(t)
	wasClient := libPicClient
	// The guard that keeps a picture off the device's own addresses has no use for a test server on it.
	libPicClient = &http.Client{Timeout: 5 * time.Second}
	libFetch = real
	t.Cleanup(func() { libPicClient = wasClient })

	data := mealsPlanBuild(mealie.Client{URL: srv.URL, Token: "token-for-a-test"}, mealsPlanDayStart(mealsPlanToday), mealsPlanEntries())
	mealsWaitFor(t, func() bool {
		mealsPlanDrawn(data, 960, 480)
		libPics.mu.Lock()
		defer libPics.mu.Unlock()
		return len(libPics.kept) > 0
	})
	if seen.Load() == 0 || bad.Load() != 0 {
		t.Fatalf("%d picture requests, %d of them with an Authorization header", seen.Load(), bad.Load())
	}
}

// A plan fetched before midnight is fetched again once the day has changed, one time for each day, and
// meanwhile shows the days that are still ahead.
func TestMealsPlanRollsAtMidnight(t *testing.T) {
	mealsPlanSetUp(t)
	var asked atomic.Int32
	var start atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		start.Store(r.URL.Query().Get("start_date"))
		fmt.Fprint(w, `{"items":[]}`)
	}))
	t.Cleanup(srv.Close)
	wasClient, wasNow := mealsClient, mealsPlanNow
	mealsClient = func() mealie.Client { return mealie.Client{URL: srv.URL, Token: "token-for-a-test"} }
	next := mealsPlanToday.AddDate(0, 0, 1)
	mealsPlanNow = func() time.Time { return next }
	mealsPlanRolled.Store("")
	t.Cleanup(func() { mealsClient, mealsPlanNow = wasClient, wasNow })

	data := mealsPlanFixture()
	r := newRenderer(image.NewRGBA(image.Rect(0, 0, 960, 480)))
	f := mealsFrame{view: mealsPlanView(), tabs: []mealsTab{{id: "plan", label: "Plan", on: true}}, pane: mealsPane{Data: data, Loaded: true}}
	for range 3 {
		mealsDraw(r, scene{now: next, phase: "idle", custom: customView{name: mealsID, data: f}})
	}
	meals.wg.Wait()
	if n := asked.Load(); n != 1 {
		t.Fatalf("the plan was fetched %d times after midnight, want once", n)
	}
	if got := start.Load(); got != "2026-11-24" {
		t.Fatalf("the plan was fetched from %v", got)
	}
	if cfg := config.Get().Mealie; !cfg.Set() {
		t.Fatal("Mealie lost its settings")
	}
}
