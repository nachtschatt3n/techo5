//go:build !dot && !spot

package display

import (
	"context"
	"errors"
	"image"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/touch"
	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

// Meals: a custom screen (custom_screens.go) that shows a Mealie server's shopping list, its meal plan
// and a recipe cooked step by step. This file is the page itself: what registers it, what it keeps between
// frames, how its views are fetched and how often, and what a finger on it does before a view's own
// code is asked. meals_draw.go draws what is shared between the views (the switcher, the messages for a
// page still loading or that cannot load). Each view is a file of its own that adds itself to
// mealsViews from its init and brings its own data, drawing and touch, so no view edits this file.
//
// The page is offered only when the setup page has a Mealie address and token (config.Mealie.Set).
// Nothing here reaches the network until then, and nothing is fetched while the page is not on the
// screen: a frame of it asking for its data (mealsFill) is how the page knows somebody is looking.

const (
	// mealsID is what the config and the voice assistant's pages call the screen.
	mealsID = "meals"
	// mealsKeep is how long the page remembers the view it was on, and the recipe, once it is put away.
	mealsKeep = 30 * time.Minute
	// mealsAway is how long without a frame makes the next one a reopening: what is on the page is
	// fetched again at once rather than when it is next due.
	mealsAway = 15 * time.Second
	// mealsLoadMost is how long one fetch of a view may take, over the client's own timeouts.
	mealsLoadMost = 30 * time.Second
	// mealsBounce is the time in which a second activation of the same button is taken as a wet
	// finger's second tap and ignored.
	mealsBounce = 250 * time.Millisecond
)

// mealsClient is the server the page talks to, from the setup page's settings. A test swaps it.
var mealsClient = func() mealie.Client {
	m := config.Get().Mealie
	return mealie.Client{URL: m.URL, Token: m.Token}
}

// mealsView is one view of the page: the Shopping list, the Plan, the recipe being cooked.
type mealsView struct {
	id    string        // what a view is called by Home Assistant's actions and the switcher's zones
	label func() string // the switcher's segment, in the screen's language: tr of a key of lib/locale
	rank  int           // its place in the switcher, the lowest first

	// every is how often its data is fetched again while it is on the screen; 0 for never (a view
	// whose data is opened from elsewhere, like a recipe).
	every time.Duration
	// fetch gets its data, off the display's goroutines. The answer is kept as it is and handed to
	// draw; an error is one of the mealie package's (the page says a sentence for each).
	fetch func(ctx context.Context, c mealie.Client) (any, error)
	// shown is whether the switcher offers it now (a recipe's view only while one is open); nil for
	// always.
	shown func() bool
	// draw draws the view into body, below the page's bar, and returns where a tap means something.
	// It is asked only when the view has data: a page still loading, or one that could not, draws a
	// message of its own (meals_draw.go). A failed refresh leaves the data from before in place, with
	// a note over it that says since when.
	draw func(r *renderer, s scene, p mealsPane, body image.Rectangle) []customZone
	// gesture is a finger on the view that no zone of the page's own took.
	gesture func(d *Display, g touch.Gesture, at image.Point)
	// awake is whether the view wants the device held awake (customScreen.awake); nil for no.
	awake func() bool
	// night is whether the view keeps a page asked for at night lit (customScreen.night); nil for no.
	night func() bool
}

// mealsViews are the views, in their ranks' order. Each view's file adds its own from its init.
var mealsViews []mealsView

// registerMealsView adds a view to the page.
func registerMealsView(v mealsView) {
	mealsViews = append(mealsViews, v)
	slices.SortStableFunc(mealsViews, func(x, y mealsView) int { return x.rank - y.rank })
}

// mealsPane is what a view's draw is given: its data as it was last fetched, and how the last attempt
// went.
type mealsPane struct {
	Data    any
	Loaded  bool      // the data has arrived at least once
	Loading bool      // a fetch is on its way
	Err     error     // the last fetch's error, nil when it went well
	Fetched time.Time // when Data arrived
}

// mealsFeed is the page's side of one view's data.
type mealsFeed struct {
	mealsPane
	gen    uint64    // the fetch whose answer is wanted: an older one's is dropped
	nextAt time.Time // when it is next fetched by itself
	fails  int       // fetches in a row that failed
}

// mealsHub is everything the page keeps between frames, and between its being put away and opened again.
type mealsHub struct {
	mu sync.Mutex
	// wg counts the fetches on their way.
	wg sync.WaitGroup

	view    string                // the view on the screen, "" for the first
	seen    time.Time             // the last frame
	server  string                // the server these feeds are from, to drop them when it changes
	seq     uint64                // the fetches started, for gen
	feeds   map[string]*mealsFeed // by view id
	changed func()                // wakes the screen when data arrives

	recipe struct {
		slug  string
		scale float64
		at    time.Time
	}
}

// meals is the page's one set of state: there is only the one page.
var meals = &mealsHub{}

func init() {
	registerScreen(customScreen{
		id: mealsID, label: "Meals", title: func() string { return tr("meals") }, rank: 30,
		offered: func() bool { return config.Get().Mealie.Set() },
		listen:  func(changed func()) { meals.mu.Lock(); meals.changed = changed; meals.mu.Unlock() },
		fill:    mealsFill,
		draw:    mealsDraw,
		gesture: mealsGesture,
		awake:   func() bool { return meals.held(func(v mealsView) func() bool { return v.awake }) },
		night:   func() bool { return meals.held(func(v mealsView) func() bool { return v.night }) },
	})
}

// mealsBackoff is how long a view that has failed n times in a row waits before it asks again: 30
// seconds, then 60, 120 and from there every 5 minutes.
func mealsBackoff(n int) time.Duration {
	switch {
	case n <= 1:
		return 30 * time.Second
	case n == 2:
		return time.Minute
	case n == 3:
		return 2 * time.Minute
	}
	return 5 * time.Minute
}

// mealsServer is what names the server and the choices the feeds are made from: when it changes what
// was fetched is not the new setting's.
func mealsServer(m config.Mealie) string { return m.URL + "\x00" + m.Token + "\x00" + m.List }

// shownViews are the views the switcher offers, in order. Called with h.mu held or not: it reads only
// the registry and each view's own shown.
func shownViews() []mealsView {
	var out []mealsView
	for _, v := range mealsViews {
		if v.shown == nil || v.shown() {
			out = append(out, v)
		}
	}
	return out
}

// current is the view on the screen: the one chosen if the switcher still offers it, else the first.
// Called with h.mu held.
func (h *mealsHub) current() (mealsView, bool) {
	all := shownViews()
	if len(all) == 0 {
		return mealsView{}, false
	}
	if i := slices.IndexFunc(all, func(v mealsView) bool { return v.id == h.view }); i >= 0 {
		return all[i], true
	}
	return all[0], true
}

// held is whether the view on the screen has the hook pick takes from it and that hook says yes. The
// display calls it with its own lock held, so nothing here calls back into the display.
func (h *mealsHub) held(pick func(mealsView) func() bool) bool {
	h.mu.Lock()
	v, ok := h.current()
	h.mu.Unlock()
	if !ok {
		return false
	}
	f := pick(v)
	return f != nil && f()
}

// feed is the feed of a view, made the first time it is asked for. Called with h.mu held.
func (h *mealsHub) feed(id string) *mealsFeed {
	if h.feeds == nil {
		h.feeds = map[string]*mealsFeed{}
	}
	f := h.feeds[id]
	if f == nil {
		f = &mealsFeed{}
		h.feeds[id] = f
	}
	return f
}

// show turns the page to the view id, if it has it.
func (h *mealsHub) show(id string) {
	h.mu.Lock()
	h.view = id
	h.mu.Unlock()
}

// start fetches a view's data in the background, unless a fetch is on its way and force is not set.
// A forced one (Retry, or a write that changed what the server has) takes the answer over: the one
// that was on its way is dropped when it lands. Nothing happens, and nothing is asked of the network,
// while the page is not set up.
func (h *mealsHub) start(id string, force bool) {
	if !config.Get().Mealie.Set() {
		return
	}
	c := mealsClient()
	var fetch func(context.Context, mealie.Client) (any, error)
	for _, v := range mealsViews {
		if v.id == id {
			fetch = v.fetch
		}
	}
	if fetch == nil {
		return
	}
	h.mu.Lock()
	f := h.feed(id)
	if f.Loading && !force {
		h.mu.Unlock()
		return
	}
	h.seq++
	gen := h.seq
	f.gen, f.Loading = gen, true
	h.wg.Add(1)
	h.mu.Unlock()

	go func() {
		defer h.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), mealsLoadMost)
		defer cancel()
		data, err := fetch(ctx, c)
		h.landed(id, gen, data, err)
	}()
}

// landed takes a fetch's answer in, unless a newer fetch of the view, or a change of server, has made
// it the wrong one.
func (h *mealsHub) landed(id string, gen uint64, data any, err error) {
	now := time.Now()
	h.mu.Lock()
	f := h.feeds[id]
	if f == nil || f.gen != gen {
		h.mu.Unlock()
		return
	}
	f.Loading = false
	if err == nil {
		f.Data, f.Loaded, f.Err, f.Fetched, f.fails = data, true, nil, now, 0
		if every := h.every(id); every > 0 {
			f.nextAt = now.Add(every)
		} else {
			f.nextAt = now.Add(100 * 365 * 24 * time.Hour) // a view that is not refreshed by itself
		}
	} else {
		f.Err = err
		f.fails++
		f.nextAt = now.Add(mealsBackoff(f.fails))
		// What went wrong, as a kind: the client has logged the details, which never hold the token.
		slog.Warn("meals: a view could not be loaded", "view", id, "kind", mealsKind(err), "tries", f.fails)
	}
	changed := h.changed
	h.mu.Unlock()
	if changed != nil {
		changed()
	}
}

// every is how often a view is fetched again. Called with h.mu held or not.
func (h *mealsHub) every(id string) time.Duration {
	for _, v := range mealsViews {
		if v.id == id {
			return v.every
		}
	}
	return 0
}

// retry is the Retry pill: it forgets how often a view has failed and asks again now.
func (h *mealsHub) retry(id string) {
	h.mu.Lock()
	f := h.feed(id)
	f.fails = 0
	h.mu.Unlock()
	h.start(id, true)
}

// reload has a view fetch its data again now, as after a write.
func (h *mealsHub) reload(id string) { h.start(id, true) }

// due is whether the view's data should be fetched now: it never was, or the time is up. Called with h.mu
// held.
func (f *mealsFeed) due(now time.Time) bool {
	if f.Loading {
		return false
	}
	if !f.Loaded && f.Err == nil && f.fails == 0 {
		return true
	}
	return !now.Before(f.nextAt)
}

// mealsKind is what went wrong, as a short word for the log.
func mealsKind(err error) string {
	switch {
	case errors.Is(err, mealie.ErrToken):
		return "token"
	case errors.Is(err, mealie.ErrUseHTTPS):
		return "https"
	case errors.Is(err, mealie.ErrNotFound):
		return "not found"
	case errors.Is(err, mealie.ErrUnreadable):
		return "unreadable"
	case errors.Is(err, mealie.ErrUnreachable):
		return "unreachable"
	}
	return "other"
}

// mealsTab is one of the switcher's segments.
type mealsTab struct {
	id, label string
	on        bool
}

// mealsFrame is what a frame of the page shows: filled in on the frame's goroutine (mealsFill) and
// drawn from (mealsDraw), so drawing takes no lock.
type mealsFrame struct {
	view mealsView // the view on the screen; zero when the page has none
	tabs []mealsTab
	pane mealsPane
}

// mealsFill gathers what a frame of the page shows, and has the view on the screen fetched when its
// time has come.
func mealsFill(_ *Display, v *customView) {
	cfg := config.Get().Mealie
	if !cfg.Set() {
		// Not offered, so not reached through the screens; and still nothing may be asked of a server
		// that is not there.
		v.data = mealsFrame{}
		return
	}
	now := time.Now()
	h := meals
	h.mu.Lock()
	if key := mealsServer(cfg); key != h.server {
		// Another server, or another list: what was fetched belongs to the one before. The fetches on their
		// way are dropped when they land (their gen is not any feed's now).
		h.server, h.feeds = key, nil
		h.recipe.slug = ""
	}
	reopened := now.Sub(h.seen) > mealsAway
	if !h.seen.IsZero() && now.Sub(h.seen) > mealsKeep {
		h.view, h.recipe.slug = "", ""
	}
	h.seen = now
	cur, ok := h.current()
	frame := mealsFrame{}
	for _, t := range shownViews() {
		frame.tabs = append(frame.tabs, mealsTab{id: t.id, label: t.label(), on: ok && t.id == cur.id})
	}
	start := false
	if ok {
		f := h.feed(cur.id)
		if reopened && !f.Loading && f.Loaded {
			f.nextAt = now // opened again: the data is fetched again at once, and shown meanwhile
		}
		start = f.due(now)
		frame.view, frame.pane = cur, f.mealsPane
	}
	h.mu.Unlock()
	if start {
		h.start(cur.id, false)
		h.mu.Lock()
		frame.pane = h.feed(cur.id).mealsPane
		h.mu.Unlock()
	}
	v.data = frame
}

// mealsGesture is a finger on the page past the tabs and the ×: the page's own zones (the switcher,
// Retry) first, and anything else is the view's.
func mealsGesture(d *Display, g touch.Gesture, at image.Point) {
	if g.Kind == touch.Tap {
		if z, ok := customZoneAt(at); ok {
			switch z.kind {
			case "meals:view":
				meals.show(z.id)
				d.wake()
				return
			case "meals:retry":
				meals.retry(z.id)
				d.wake()
				return
			}
		}
	}
	meals.mu.Lock()
	v, ok := meals.current()
	meals.mu.Unlock()
	if ok && v.gesture != nil {
		v.gesture(d, g, at)
	}
}

// openRecipe opens the page on a recipe, cooked at scale times its own amounts (a scale of 0 or less is
// 1). The cooking view takes the recipe from mealsOpenRecipe. It reports false, having done nothing, when
// Mealie is not set up.
func (d *Display) openRecipe(slug string, scale float64) bool {
	if !config.Get().Mealie.Set() || slug == "" {
		return false
	}
	if scale <= 0 {
		scale = 1
	}
	meals.mu.Lock()
	meals.recipe.slug, meals.recipe.scale, meals.recipe.at = slug, scale, time.Now()
	meals.view = "cooking"
	meals.mu.Unlock()
	d.mu.Lock()
	up := d.customs.shown == mealsID
	d.mu.Unlock()
	if !up {
		return d.openCustom(mealsID)
	}
	d.wake()
	return true
}

// mealsOpenRecipe is the recipe opened last, kept for mealsKeep after the page was put away.
func mealsOpenRecipe() (slug string, scale float64, ok bool) {
	meals.mu.Lock()
	defer meals.mu.Unlock()
	if meals.recipe.slug == "" || time.Since(meals.recipe.at) > mealsKeep {
		return "", 0, false
	}
	return meals.recipe.slug, meals.recipe.scale, true
}

// mealsDebounce ignores an activation that comes within mealsBounce of the one before: a wet or floury
// finger taps twice. Each button that needs it holds one.
type mealsDebounce struct{ last time.Time }

// ok reports whether an activation at now counts, and takes it in if so.
func (b *mealsDebounce) ok(now time.Time) bool {
	if !b.last.IsZero() && now.Sub(b.last) < mealsBounce {
		return false
	}
	b.last = now
	return true
}
