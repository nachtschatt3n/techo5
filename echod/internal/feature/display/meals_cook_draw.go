//go:build !dot && !spot

package display

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/font"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/touch"
	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

// The Meals page's Cooking view: one recipe, an overview first (the photo, the servings, the ingredients)
// and then a page for each step, with the ingredients the step uses marked, the times in it as buttons
// that set a timer of the device, and Back and Next at the foot where a floury thumb finds them. What the
// view remembers about the recipe (the step, the servings, the timers started, the keep-awake hold) is
// the cooking model's (meals_cook.go) and has nothing of the screen in it; this file is what draws that
// model and what a finger does to it.
//
// A step's text scrolls when it is longer than the room it has, and the buttons around it stay where they
// are. A swipe sideways turns the page and a swipe up or down scrolls, and a finger that began by
// scrolling does not turn the page when it ends a little sideways. Nothing here writes to the server.
//
// Sizes are in the Show 5's pixels, as the rest of the package's are, and r.s scales them.

const (
	// mealsCookID is what the page and the switcher call the view.
	mealsCookID = "cooking"

	// cookHeroW and cookHeroH are the photo on the overview of a landscape screen, 16:9; cookBandW and
	// cookBandH the narrower band over a step's ingredients on a tall one; cookHeroPortraitH the photo
	// across a screen held upright, which is as wide as the screen.
	cookHeroW, cookHeroH = 370, 208
	cookBandW, cookBandH = 345, 194
	cookHeroPortraitH    = 400
	// cookLeftW is the column of ingredients beside a step, about a third of the screen.
	cookLeftW = 345
	// cookNavW is Back and Next, and cookStartW the button of the overview. cookDotW is a dot's room: a
	// dot is a place to tap, so it is as wide as the least a finger may be given.
	cookNavW, cookStartW, cookDotW = 160, 200, 56
	// cookGap is the room between buttons that are tapped one after the other.
	cookGap = 8
	// cookTextLine is a line of a step's text on a short screen, and cookTextLineTall on a tall one, where the
	// face is larger. cookRowLine is a line of an ingredient.
	cookTextLine, cookTextLineTall, cookRowLine = 38, 44, 34
	// cookRowLeast is the least height of an ingredient that is not tapped, cookHeadH a section's heading.
	cookRowLeast, cookHeadH = 40, 36
	// cookAxisHold is how long after a scroll a swipe sideways is taken as the same finger's slant and
	// not a turn of the page.
	cookAxisHold = 600 * time.Millisecond
	// cookIngredientsMost is the most lines of an ingredient that are shown.
	cookIngredientsMost = 2
)

// cookHeroGrace is how long the photo is held after the last frame that drew it: a page put away has no
// frames, so this is what tells that it has been. A test sets it.
var cookHeroGrace = 10 * time.Second

// mealsCookData is what the view's fetch gives the page: the recipe as the cooking model keeps it, and
// where its photo is, or that the server no longer has it.
type mealsCookData struct {
	Slug string     // the recipe that was asked for, to see the page has not been turned to another
	Gone bool       // the server answered that there is no such recipe
	Cook *mealsCook // nil when Gone
	Min  string     // the photo at the middle size, "" when the recipe has none
	Full string     // and at full size, for a screen that can use it
}

// cookUIState is what the view keeps between frames: where its parts were drawn, so that a swipe knows
// what it scrolls, how far each is scrolled, and the photo it holds.
type cookUIState struct {
	mu sync.Mutex

	// pane is where a swipe sideways turns the page, text where a swipe up or down scrolls the step,
	// and ing where it scrolls the ingredients: from the frame last drawn.
	pane, text, ing image.Rectangle

	textScroll, textMax int
	ingScroll, ingMax   int
	sheet               bool // the ingredients stand over the step, on a screen held upright
	jump                bool // the ingredients scroll to the first one the step uses
	forCook             *mealsCook
	forStep             int // the page the scrolls belong to

	deb      map[string]*mealsDebounce
	lastVert time.Time // the last scroll, for the axis lock

	hero  cookHero
	timer *time.Timer
}

// cookHero is the photo the view holds: where it is and at what size. alt is the other size of the same
// photo, drawn while the one that is asked for is not there.
type cookHero struct {
	url, alt string
	w, h     int
}

// mealsCookUI is the view's one set of state: there is only the one page.
var mealsCookUI = &cookUIState{}

func init() { registerMealsView(mealsCookingView()) }

// mealsCookingView is the view as the page registers it. A test registers its own copy.
func mealsCookingView() mealsView {
	return mealsView{
		id: mealsCookID, label: func() string { return tr("meals_cooking") }, rank: 3,
		fetch: cookFetch, draw: cookDraw, gesture: cookGesture,
		shown: cookShown, awake: mealsCookAwake, night: mealsCookNight,
	}
}

// mealsKeepOpenRecipe counts the recipe that is open as used just now, so that it is not taken for put
// away after mealsKeep while it is being cooked: the Cooking view calls it when it draws and when it is
// touched. A recipe that was already put away is not brought back by it.
func mealsKeepOpenRecipe() {
	meals.mu.Lock()
	defer meals.mu.Unlock()
	if meals.recipe.slug != "" && time.Since(meals.recipe.at) <= mealsKeep {
		meals.recipe.at = time.Now()
	}
}

// cookShown is whether a recipe is open, which is when the switcher offers the view. The page asks it
// with its own lock held, as it does everything it asks of a view, so it reads what the page keeps of
// the recipe as it is.
func cookShown() bool {
	return meals.recipe.slug != "" && time.Since(meals.recipe.at) <= mealsKeep
}

// cookFetch reads the recipe that was opened and starts, or goes on with, cooking it. A recipe the
// server does not have is not a failure to try again but an answer, and the view says so.
func cookFetch(ctx context.Context, c mealie.Client) (any, error) {
	slug, scale, ok := mealsOpenRecipe()
	if !ok {
		return mealsCookData{Gone: true}, nil
	}
	r, err := c.Recipe(ctx, slug)
	if errors.Is(err, mealie.ErrNotFound) {
		return mealsCookData{Slug: slug, Gone: true}, nil
	}
	if err != nil {
		return nil, err
	}
	// A recipe that is not the open one by now is not opened: the fetch's answer is dropped on landing, and
	// the recipe being cooked is not replaced by it.
	return mealsCookData{
		Slug: slug, Cook: mealsCookOpenIf(slug, r, scale),
		Min:  c.ImageURL(r.ID, r.Image, mealie.Min),
		Full: c.ImageURL(r.ID, r.Image, mealie.Original),
	}, nil
}

// Name is the recipe's name.
func (c *mealsCook) Name() string { return strings.TrimSpace(c.recipe.Name) }

// Section is the heading the recipe puts over the step on the screen, "" for none.
func (c *mealsCook) Section() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.current(); ok {
		return mealie.Plain(s.Title)
	}
	return ""
}

// cookNum is a number of servings: whole when it is, else with one decimal.
func cookNum(f float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(math.Round(f*10)/10, 'f', 1, 64), ".0")
}

// page tells the view that a frame is of the page the cook is on, and puts the scrolls back to the top
// when it is not the page the last frame was of.
func (u *cookUIState) page(c *mealsCook) {
	step := c.Step()
	u.mu.Lock()
	defer u.mu.Unlock()
	// Where the parts are drawn is the frame's own: the parts that are not in it are nowhere.
	u.ing, u.text = image.Rectangle{}, image.Rectangle{}
	if u.forCook != c || u.forStep != step {
		u.forCook, u.forStep = c, step
		u.textScroll, u.ingScroll, u.sheet, u.jump = 0, 0, false, true
	}
}

// reset puts the view's state back as it is before the page is first drawn.
func (u *cookUIState) reset() {
	u.letGoHero()
	u.mu.Lock()
	if u.timer != nil {
		u.timer.Stop()
	}
	u.pane, u.text, u.ing = image.Rectangle{}, image.Rectangle{}, image.Rectangle{}
	u.textScroll, u.textMax, u.ingScroll, u.ingMax = 0, 0, 0, 0
	u.sheet, u.jump, u.forCook, u.forStep = false, false, nil, 0
	u.deb, u.lastVert, u.hero, u.timer = nil, time.Time{}, cookHero{}, nil
	u.mu.Unlock()
}

// holdHero says which photo this frame draws, and which other size of it stands in while it is not there.
// What was held before and is neither now is let go of, and the timer that lets go of it when frames
// stop is set again.
func (u *cookUIState) holdHero(url, alt string, w, h int) {
	now := cookHero{url, alt, w, h}
	u.mu.Lock()
	was := u.hero
	u.hero = now
	if u.timer == nil {
		u.timer = time.AfterFunc(cookHeroGrace, u.letGoHero)
	} else {
		u.timer.Reset(cookHeroGrace)
	}
	u.mu.Unlock()
	for _, old := range []string{was.url, was.alt} {
		if old != "" && (was.w != now.w || was.h != now.h || (old != now.url && old != now.alt)) {
			cookForgetPicture(cookHero{url: old, w: was.w, h: was.h})
		}
	}
}

// letGoHero frees the photo held: when the recipe is put away, when the page has had no frame for
// cookHeroGrace (it is closed, or turned to another view) and when a frame wants another.
func (u *cookUIState) letGoHero() {
	u.mu.Lock()
	was := u.hero
	u.hero = cookHero{}
	if u.timer != nil {
		u.timer.Stop()
	}
	u.mu.Unlock()
	cookForgetPicture(was)
}

// cookForgetPicture lets the library's store go of the photo, in both sizes, when it is kept.
func cookForgetPicture(h cookHero) {
	for _, u := range []string{h.url, h.alt} {
		if u != "" {
			libPics.forgetFit(u, h.w, h.h)
		}
	}
}

// debounce says whether a second activation of the button named key is a wet finger's: the first of a
// pair counts and the one within mealsBounce of it does not.
func (u *cookUIState) debounce(key string, now time.Time) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.deb == nil {
		u.deb = map[string]*mealsDebounce{}
	}
	if u.deb[key] == nil {
		u.deb[key] = &mealsDebounce{}
	}
	return u.deb[key].ok(now)
}

// cookDraw draws the view into body and returns where a tap means something.
func cookDraw(r *renderer, s scene, p mealsPane, body image.Rectangle) []customZone {
	data, _ := p.Data.(mealsCookData)
	ui := mealsCookUI
	slug, _, open := mealsOpenRecipe()
	c := data.Cook
	// What was fetched is not the recipe that is open now: another was opened, or the one that was being
	// cooked was finished and is opened again. The right one is fetched, and meanwhile the page says so.
	if !open || data.Slug != slug || (!data.Gone && (c == nil || c != mealsCooking())) {
		ui.letGoHero()
		if p.Err != nil {
			return r.mealsState(body, mealsFrame{view: mealsView{id: mealsCookID}, pane: mealsPane{Err: p.Err}})
		}
		meals.start(mealsCookID, false)
		return r.mealsMessage(body, "timer-sand", tr("meals_loading"), "")
	}
	if data.Gone {
		ui.letGoHero()
		return r.cookGone(body)
	}
	ui.page(c)
	mealsKeepOpenRecipe()
	class := r.mealsClass()
	var zones []customZone
	if c.Step() == 0 {
		zones = r.cookOverview(body, c, data, class)
	} else {
		zones = r.cookStep(body, c, data, class, s.now)
	}
	return zones
}

// cookGone is the page for a recipe the server no longer has: what happened, and Back to the plan.
func (r *renderer) cookGone(body image.Rectangle) []customZone {
	room := r.s(mealsTarget) + r.s(24)
	r.mealsMessage(image.Rect(body.Min.X, body.Min.Y, body.Max.X, body.Max.Y-room), "silverware-fork-knife", tr("meals_recipe_gone"), "")
	w := r.s(cookStartW)
	b := image.Rect(body.Min.X+(body.Dx()-w)/2, body.Max.Y-room, body.Min.X+(body.Dx()+w)/2, body.Max.Y-room+r.s(mealsTarget))
	r.mealsPill(b, tr("meals_back"), true)
	return []customZone{{r: b, kind: "meals:cook:leave", id: "leave"}}
}

// cookHeroDraw draws the recipe's photo in box once it has come, and until then, or for a recipe that has
// none, a color of its own with the icon of a dish.
func (r *renderer) cookHeroDraw(box image.Rectangle, c *mealsCook, data mealsCookData) {
	ui := mealsCookUI
	url, alt, most := data.Min, "", mealsPicMaxPixels
	// The original is for a box wider than the middle size is. A photo taken on a phone is bigger than the
	// original's limit, though, and one that would not load for another reason is no better: for as long as
	// the original is not to be had, the middle size is what is asked for and drawn in its place.
	if box.Dx() > 640 && data.Full != "" {
		url, alt, most = data.Full, data.Min, mealsPicOriginalMaxPixels
		if data.Min != "" && libPics.fitFailed(data.Full, box.Dx(), box.Dy()) {
			url, alt, most = data.Min, data.Full, mealsPicMaxPixels
		}
	}
	if url != "" {
		ui.holdHero(url, alt, box.Dx(), box.Dy())
		meals.mu.Lock()
		changed := meals.changed
		meals.mu.Unlock()
		if changed != nil {
			libPics.setChanged(changed)
		}
		if img := libPics.pictureFitMost(url, box.Dx(), box.Dy(), most); img != nil {
			r.drawLibPicture(img, box)
			return
		}
		// The original is asked for again once its time out is over, or is still on its way: the middle
		// size, when it is already here, stays on the screen meanwhile instead of the placeholder.
		if img := libPics.pictureFitKept(alt, box.Dx(), box.Dy()); img != nil {
			r.drawLibPicture(img, box)
			return
		}
	} else {
		ui.letGoHero()
	}
	hue := libHue(c.Name())
	top, bottom := lerp(walnut, hue, 0.55), lerp(walnut, hue, 0.22)
	r.roundFill(box, float64(max(box.Dx()/14, 2)), top, bottom)
	r.mdiIconIn("silverware-fork-knife", image.Rect(box.Min.X+box.Dx()/2-r.s(40), box.Min.Y+box.Dy()/2-r.s(40), box.Min.X+box.Dx()/2+r.s(40), box.Min.Y+box.Dy()/2+r.s(40)), 64, lerp(bottom, cream, 0.6))
}

// cookOverview draws step 0: the photo, the recipe's name and servings, the ingredients, and Start.
func (r *renderer) cookOverview(body image.Rectangle, c *mealsCook, data mealsCookData, class mealsClass) []customZone {
	m := r.s(customIn)
	inner := body.Inset(m)
	var zones []customZone

	foot := image.Rect(inner.Max.X-r.s(cookStartW), inner.Max.Y-r.s(mealsTarget), inner.Max.X, inner.Max.Y)
	label := tr("meals_start")
	if c.Steps() == 0 {
		label = tr("meals_finish")
	}
	r.mealsPill(foot, label, true)
	zones = append(zones, customZone{r: foot, kind: "meals:cook:start", id: "start"})
	above := foot.Min.Y - r.s(cookGap)

	var right image.Rectangle
	desc := true
	switch class {
	case mealsPortrait:
		hero := image.Rect(body.Min.X, body.Min.Y, body.Max.X, body.Min.Y+r.s(cookHeroPortraitH))
		r.cookHeroDraw(hero, c, data)
		right = image.Rect(inner.Min.X, hero.Max.Y+m, inner.Max.X, above)
	default:
		hero := image.Rect(inner.Min.X, inner.Min.Y, inner.Min.X+r.s(cookHeroW), inner.Min.Y+r.s(cookHeroH))
		r.cookHeroDraw(hero, c, data)
		lines := 3
		if class == mealsTall {
			lines = 6
		}
		r.cookDescription(c, image.Rect(hero.Min.X, hero.Max.Y+r.s(10), hero.Max.X, inner.Max.Y), lines)
		right = image.Rect(hero.Max.X+m, inner.Min.Y, inner.Max.X, above)
		desc = false
	}

	fc := r.faces()
	y := right.Min.Y
	r.text(fc.header, r.fit(fc.header, c.Name(), right.Dx()), right.Min.X, y+r.s(34), cream)
	y += r.s(46)
	if desc {
		if h := r.cookDescription(c, image.Rect(right.Min.X, y, right.Max.X, y+r.s(3*30)), 3); h > 0 {
			y += h + r.s(8)
		}
	}
	stepper := image.Rect(right.Min.X, y, right.Max.X, y+r.s(mealsTarget))
	zones = append(zones, r.cookStepper(stepper, c)...)
	y = stepper.Max.Y + r.s(cookGap)
	y += r.cookNotScaled(c, image.Rect(right.Min.X, y, right.Max.X, right.Max.Y))
	list := image.Rect(right.Min.X, y, right.Max.X, right.Max.Y)
	zones = append(zones, r.cookIngredients(list, c, false)...)

	mealsCookUI.mu.Lock()
	mealsCookUI.pane, mealsCookUI.text = body, image.Rectangle{}
	mealsCookUI.mu.Unlock()
	return zones
}

// cookDescription draws the recipe's own few words in box, at most lines of them, the last cut with "…"
// when there is more, and returns how tall it is.
func (r *renderer) cookDescription(c *mealsCook, box image.Rectangle, lines int) int {
	text := mealie.Plain(c.recipe.Description)
	if text == "" || box.Dy() <= 0 {
		return 0
	}
	fc := r.faces()
	line := r.s(30)
	lines = min(lines, box.Dy()/line)
	if lines <= 0 {
		return 0
	}
	all := r.wrapLines(fc.nav, strings.Join(strings.Fields(text), " "), box.Dx())
	if len(all) > lines {
		all[lines-1] = r.fit(fc.nav, strings.Join(all[lines-1:], " "), box.Dx())
		all = all[:lines]
	}
	ink := lerp(dim, cream, 0.5)
	for i, l := range all {
		r.text(fc.nav, l, box.Min.X, box.Min.Y+(i+1)*line-r.s(6), ink)
	}
	return len(all) * line
}

// cookServings is the stepper's words for sv servings: "1 serving", and
// otherwise "4 servings".
func cookServings(sv float64) string {
	n := cookNum(sv)
	if n == "1" {
		return fmt.Sprintf(tr("meals_serving"), n)
	}
	return fmt.Sprintf(tr("meals_servings"), n)
}

// cookStepper draws [-] 4 servings [+] in row and returns the two buttons' zones. A recipe that does not
// say how many it makes shows the multiple instead ("x1.5").
func (r *renderer) cookStepper(row image.Rectangle, c *mealsCook) []customZone {
	fc := r.faces()
	words := ""
	if sv, ok := c.Servings(); ok {
		words = cookServings(sv)
	} else {
		words = "x" + cookNum(c.Scale())
	}
	side := r.s(mealsTarget)
	room := r.width(fc.labelBold, words) + 2*r.s(mealsPillPad)
	w := min(row.Dx(), 2*side+max(room, r.s(120)))
	row.Max.X = row.Min.X + w
	minus := image.Rect(row.Min.X, row.Min.Y, row.Min.X+side, row.Min.Y+side)
	plus := image.Rect(row.Max.X-side, row.Min.Y, row.Max.X, row.Min.Y+side)
	for _, b := range []struct {
		r    image.Rectangle
		icon string
	}{{minus, "minus"}, {plus, "plus"}} {
		r.roundFill(b.r, float64(side)/2, surface(7), surface(5))
		r.roundHighlight(b.r, float64(side)/2)
		r.mdiIconIn(b.icon, b.r, 28, cream)
	}
	mid := image.Rect(minus.Max.X, row.Min.Y, plus.Min.X, row.Max.Y)
	words = r.fit(fc.labelBold, words, mid.Dx()-r.s(8))
	r.text(fc.labelBold, words, mid.Min.X+(mid.Dx()-r.width(fc.labelBold, words))/2, mid.Min.Y+mid.Dy()/2+r.s(10), cream)
	return []customZone{
		{r: minus, kind: "meals:cook:less", id: "less"},
		{r: plus, kind: "meals:cook:more", id: "more"},
	}
}

// cookNotScaled says in a line or two that some amounts were left as they are, when the model says so, and
// returns the height it took.
func (r *renderer) cookNotScaled(c *mealsCook, box image.Rectangle) int {
	if !c.NotScaled() || box.Dy() < r.s(30) {
		return 0
	}
	fc := r.faces()
	lines := r.wrapLines(fc.nav, tr("meals_not_scaled"), box.Dx())
	if len(lines) > 2 {
		lines[1] = r.fit(fc.nav, strings.Join(lines[1:], " "), box.Dx())
		lines = lines[:2]
	}
	for i, l := range lines {
		r.text(fc.nav, l, box.Min.X, box.Min.Y+(i+1)*r.s(28)-r.s(6), amber)
	}
	return len(lines)*r.s(28) + r.s(cookGap)
}

// cookRow is one row of the ingredients as laid out: an ingredient, or the heading of a section.
type cookRow struct {
	idx   int // the ingredient, or -1 for a heading
	words []string
	y, h  int
	line  cookLine
}

// cookRows lays the ingredients out in a column w wide. A row that is tapped is as tall as a finger needs.
func (r *renderer) cookRows(lines []cookLine, w int, tap bool) (rows []cookRow, total int) {
	fc := r.faces()
	least := r.s(cookRowLeast)
	if tap {
		least = r.s(mealsTargetMin)
	}
	room := w - r.s(36) - r.s(20)
	for i, l := range lines {
		if l.Section != "" {
			rows = append(rows, cookRow{idx: -1, words: []string{l.Section}, y: total, h: r.s(cookHeadH)})
			total += r.s(cookHeadH)
		}
		if l.Text == "" {
			continue
		}
		words := r.wrapLines(fc.label, l.Text, room)
		if len(words) > cookIngredientsMost {
			words[cookIngredientsMost-1] = r.fit(fc.label, strings.Join(words[cookIngredientsMost-1:], " "), room)
			words = words[:cookIngredientsMost]
		}
		h := max(least, len(words)*r.s(cookRowLine)+r.s(14))
		rows = append(rows, cookRow{idx: i, words: words, y: total, h: h, line: l})
		total += h
	}
	return rows, total
}

// cookIngredients draws the ingredients in list, scrolled, and returns the zones of the rows that a tap
// sets aside, when tap says they are tapped. The ones the step uses are drawn bold with a dot, and the list
// is brought to the first of them when the page was turned.
func (r *renderer) cookIngredients(list image.Rectangle, c *mealsCook, tap bool) []customZone {
	if list.Dy() <= 0 || list.Dx() <= 0 {
		return nil
	}
	ui := mealsCookUI
	fc := r.faces()
	rows, total := r.cookRows(c.Lines(), list.Dx(), tap)
	maxScroll := max(total-list.Dy(), 0)
	ui.mu.Lock()
	scroll := ui.ingScroll
	if ui.jump {
		ui.jump = false
		scroll = 0
		for _, row := range rows {
			if row.idx >= 0 && row.line.Used {
				scroll = row.y - r.s(cookGap)
				break
			}
		}
	}
	scroll = min(max(scroll, 0), maxScroll)
	ui.ingScroll, ui.ingMax, ui.ing = scroll, maxScroll, list
	ui.mu.Unlock()

	full := r.dst
	r.dst = full.SubImage(list).(*image.RGBA)
	for _, row := range rows {
		b := image.Rect(list.Min.X, list.Min.Y+row.y-scroll, list.Max.X, list.Min.Y+row.y-scroll+row.h)
		if b.Max.Y <= list.Min.Y || b.Min.Y >= list.Max.Y {
			continue
		}
		if row.idx < 0 {
			r.text(fc.navBold, r.fit(fc.navBold, row.words[0], b.Dx()-r.s(12)), b.Min.X+r.s(8), b.Min.Y+b.Dy()/2+r.s(8), amber)
			continue
		}
		face, ink := fc.label, cream
		if row.line.Used {
			r.roundFill(b.Inset(r.s(2)), float64(r.s(8)), surface(2), surface(2))
			face = fc.labelBold
			r.disc(b.Min.X+r.s(16), b.Min.Y+b.Dy()/2, r.s(6), amber)
		}
		if row.line.Ticked {
			ink = dim
		}
		top := b.Min.Y + (b.Dy()-len(row.words)*r.s(cookRowLine))/2
		for i, w := range row.words {
			x := b.Min.X + r.s(36)
			base := top + (i+1)*r.s(cookRowLine) - r.s(8)
			r.text(face, w, x, base, ink)
			if row.line.Ticked {
				r.fillRect(image.Rect(x, base-r.s(9), x+r.width(face, w), base-r.s(9)+max(r.s(2), 1)), dim)
			}
		}
		r.fillRect(image.Rect(b.Min.X+r.s(8), b.Max.Y-1, b.Max.X-r.s(8), b.Max.Y), surface(2))
	}
	r.dst = full

	var zones []customZone
	if tap {
		for _, row := range rows {
			b := image.Rect(list.Min.X, list.Min.Y+row.y-scroll, list.Max.X, list.Min.Y+row.y-scroll+row.h)
			// Only a row that is wholly in view is a target: a half one under the edge is not a safe one.
			if row.idx < 0 || b.Min.Y < list.Min.Y || b.Max.Y > list.Max.Y {
				continue
			}
			zones = append(zones, customZone{r: b, kind: "meals:cook:tick", id: strconv.Itoa(row.idx)})
		}
	}
	r.cookThumb(list, total, scroll)
	return zones
}

// cookThumb is the thin mark at a list's right edge that says it goes on.
func (r *renderer) cookThumb(view image.Rectangle, content, scroll int) {
	maxScroll := content - view.Dy()
	if maxScroll <= 0 {
		return
	}
	track := view.Dy() - 2*r.s(8)
	thumb := max(track*view.Dy()/content, r.s(24))
	y := view.Min.Y + r.s(8) + (track-thumb)*scroll/maxScroll
	tb := image.Rect(view.Max.X-r.s(8), y, view.Max.X-r.s(4), y+thumb)
	r.roundFill(tb, float64(tb.Dx())/2, surface(8), surface(8))
}

// cookStep draws a step's page.
func (r *renderer) cookStep(body image.Rectangle, c *mealsCook, data mealsCookData, class mealsClass, now time.Time) []customZone {
	m := r.s(customIn)
	inner := body.Inset(m)
	fc := r.faces()
	ui := mealsCookUI
	var zones []customZone

	var right image.Rectangle
	sheet := false
	switch class {
	case mealsPortrait:
		hero := image.Rect(body.Min.X, body.Min.Y, body.Max.X, body.Min.Y+r.s(cookHeroPortraitH))
		r.cookHeroDraw(hero, c, data)
		y := hero.Max.Y + m
		r.text(fc.labelBold, r.fit(fc.labelBold, c.Name(), inner.Dx()), inner.Min.X, y+r.s(30), cream)
		y += r.s(44)
		row := image.Rect(inner.Min.X, y, inner.Max.X, y+r.s(mealsTarget))
		zones = append(zones, r.cookStepper(row, c)...)
		// The ingredients' button, at the right end of the row.
		ui.mu.Lock()
		sheet = ui.sheet
		ui.mu.Unlock()
		n := 0
		for _, l := range c.Lines() {
			if l.Text != "" {
				n++
			}
		}
		words := fmt.Sprintf(tr("meals_ingredients_n"), n)
		w := min(r.width(fc.navBold, words)+2*r.s(mealsPillPad), row.Dx()/2)
		tb := image.Rect(row.Max.X-w, row.Min.Y, row.Max.X, row.Max.Y)
		r.mealsPill(tb, words, sheet)
		zones = append(zones, customZone{r: tb, kind: "meals:cook:toggle", id: "toggle"})
		right = image.Rect(inner.Min.X, row.Max.Y+r.s(cookGap), inner.Max.X, inner.Max.Y)
	default:
		left := image.Rect(inner.Min.X, inner.Min.Y, inner.Min.X+r.s(cookLeftW), inner.Max.Y)
		right = image.Rect(left.Max.X+m, inner.Min.Y, inner.Max.X, inner.Max.Y)
		y := left.Min.Y
		if class == mealsTall {
			band := image.Rect(left.Min.X, y, left.Min.X+r.s(cookBandW), y+r.s(cookBandH))
			r.cookHeroDraw(band, c, data)
			y = band.Max.Y + r.s(cookGap)
			r.text(fc.navBold, tr("meals_ingredients"), left.Min.X+r.s(4), y+r.s(22), amber)
			y += r.s(28)
		} else {
			// No photo on a short screen's step: its room is the ingredients'.
			ui.letGoHero()
		}
		stepper := image.Rect(left.Min.X, y, left.Max.X, y+r.s(mealsTarget))
		zones = append(zones, r.cookStepper(stepper, c)...)
		y = stepper.Max.Y + r.s(cookGap)
		y += r.cookNotScaled(c, image.Rect(left.Min.X, y, left.Max.X, left.Max.Y))
		zones = append(zones, r.cookIngredients(image.Rect(left.Min.X, y, left.Max.X, left.Max.Y), c, true)...)
	}
	zones = append(zones, r.cookStepRight(right, c, class, sheet, now)...)
	ui.mu.Lock()
	ui.pane = right
	ui.mu.Unlock()
	return zones
}

// cookStepRight draws what is at the right of a step, or under it on a screen held upright: where the
// cook is, the step's words, its timers, and at the foot Back, the dots and Next.
func (r *renderer) cookStepRight(rect image.Rectangle, c *mealsCook, class mealsClass, sheet bool, now time.Time) []customZone {
	fc := r.faces()
	ui := mealsCookUI
	var zones []customZone

	// The foot.
	navH := r.s(mealsTarget)
	navY := rect.Max.Y - navH
	back := image.Rect(rect.Min.X, navY, rect.Min.X+r.s(cookNavW), rect.Max.Y)
	next := image.Rect(rect.Max.X-r.s(cookNavW), navY, rect.Max.X, rect.Max.Y)
	r.mealsPill(back, tr("meals_back"), false)
	zones = append(zones, customZone{r: back, kind: "meals:cook:back", id: "back"})
	label := tr("meals_next")
	if c.Last() {
		label = tr("meals_finish")
	}
	r.mealsPill(next, label, true)
	zones = append(zones, customZone{r: next, kind: "meals:cook:next", id: "next"})
	zones = append(zones, r.cookDots(image.Rect(back.Max.X+r.s(cookGap), navY, next.Min.X-r.s(cookGap), rect.Max.Y), c)...)

	// Where the cook is.
	head := fmt.Sprintf(tr("meals_step_of"), c.Step(), c.Steps())
	if sub := firstWords(c.Summary(), c.Section()); sub != "" {
		head += " · " + sub
	}
	hh := r.s(36)
	r.text(fc.nav, r.fit(fc.nav, head, rect.Dx()), rect.Min.X, rect.Min.Y+r.s(26), lerp(dim, cream, 0.5))
	top := rect.Min.Y + hh + r.s(4)
	above := navY - r.s(cookGap)

	if sheet {
		// The ingredients stand over the step.
		box := image.Rect(rect.Min.X, top, rect.Max.X, above)
		r.roundFill(box, float64(r.s(12)), surface(1), surface(1))
		zones = append(zones, r.cookIngredients(box.Inset(r.s(6)), c, true)...)
		ui.mu.Lock()
		ui.text = image.Rectangle{}
		ui.mu.Unlock()
		return zones
	}

	// The timers, in rows from the foot upward, as many as the words need.
	boxes, pillsH := r.cookPillBoxes(c.Pills(), rect.Min.X, rect.Max.X, now)
	if len(boxes) > 0 {
		y0 := above - pillsH
		for _, b := range boxes {
			bb := b.b.Add(image.Pt(0, y0))
			r.cookPillDraw(bb, b)
			zones = append(zones, customZone{r: bb, kind: "meals:cook:pill", id: strconv.Itoa(b.i)})
		}
		above = y0 - r.s(cookGap)
	}

	face, line := r.cookStepFace(class)
	box := image.Rect(rect.Min.X, top, rect.Max.X, above)
	r.cookText(box, c.Text(), face, line)
	return zones
}

// cookStepFace is the face a step's words are read in and the height of its line: 29 units on a short
// screen, where the room is least, and 34 on a tall one and on one held upright.
func (r *renderer) cookStepFace(class mealsClass) (font.Face, int) {
	if class == mealsShort {
		return r.faces().label, r.s(cookTextLine)
	}
	return r.small, r.s(cookTextLineTall)
}

// firstWords is the first of the words that is not empty.
func firstWords(w ...string) string {
	for _, s := range w {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// cookText draws a step's words in box, scrolled, with the lines cut between words, and notes how far they
// scroll and where, for a swipe. A line break in the step is a line break here, and a blank line is half
// a line of room.
func (r *renderer) cookText(box image.Rectangle, text string, face font.Face, line int) {
	ui := mealsCookUI
	if box.Dy() <= 0 {
		return
	}
	room := box.Dx() - r.s(20)
	var lines []string
	for _, para := range strings.Split(text, "\n") {
		if strings.TrimSpace(para) == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, r.wrapLines(face, para, room)...)
	}
	total := 0
	for _, l := range lines {
		total += cookLineH(l, line)
	}
	maxScroll := max(total-box.Dy(), 0)
	ui.mu.Lock()
	scroll := min(max(ui.textScroll, 0), maxScroll)
	ui.textScroll, ui.textMax, ui.text = scroll, maxScroll, box
	ui.mu.Unlock()

	full := r.dst
	r.dst = full.SubImage(box).(*image.RGBA)
	y := box.Min.Y - scroll
	for _, l := range lines {
		h := cookLineH(l, line)
		if l != "" && y+h > box.Min.Y && y < box.Max.Y {
			r.text(face, l, box.Min.X, y+h-r.s(10), cream)
		}
		y += h
	}
	r.dst = full
	r.cookThumb(box, total, scroll)
}

// cookLineH is the height of a line of a step's text, and of the blank one between two paragraphs.
func cookLineH(l string, line int) int {
	if l == "" {
		return line / 2
	}
	return line
}

// cookPillBox is a timer's button as laid out, with y from the top of the rows.
type cookPillBox struct {
	b     image.Rectangle
	i     int
	words string
	icon  string
	lit   bool
}

// cookPillBoxes lays the step's timers out left to right in rows between x0 and x1, a row at a time, a
// timer going to the next row when it would not fit in this one. It returns them with y counted from the
// top of the rows, and how tall the rows are.
func (r *renderer) cookPillBoxes(pills []cookPill, x0, x1 int, now time.Time) ([]cookPillBox, int) {
	if len(pills) == 0 {
		return nil, 0
	}
	fc := r.faces()
	h, gap, icon := r.s(mealsTarget), r.s(cookGap), r.s(28)
	var out []cookPillBox
	x, y := x0, 0
	for i, p := range pills {
		box := cookPillBox{i: i, words: p.Text, icon: "play"}
		switch left := p.Left(now); {
		case p.Started.IsZero():
		case left > 0:
			box.words, box.icon, box.lit = cookClock(left), "timer-outline", true
		default:
			box.icon = "check"
		}
		w := 2*r.s(mealsPillPad) + icon + r.s(8) + r.width(fc.navBold, box.words)
		w = min(max(w, r.s(120)), x1-x0)
		if x > x0 && x+w > x1 {
			x, y = x0, y+h+gap
		}
		box.b = image.Rect(x, y, x+w, y+h)
		out = append(out, box)
		x += w + gap
	}
	return out, y + h
}

// cookPillDraw draws a timer's button: its icon and its time, lit while it runs.
func (r *renderer) cookPillDraw(b image.Rectangle, p cookPillBox) {
	fc := r.faces()
	rad := float64(b.Dy()) / 2
	ink := cream
	if p.lit {
		r.roundFill(b, rad, shift(amber, 14), shift(amber, -14))
		ink = onAccent()
	} else {
		r.roundFill(b, rad, surface(7), surface(5))
	}
	r.roundHighlight(b, rad)
	icon := r.s(28)
	pad := r.s(mealsPillPad)
	r.mdiIconIn(p.icon, image.Rect(b.Min.X+pad, b.Min.Y, b.Min.X+pad+icon, b.Max.Y), 28, ink)
	x := b.Min.X + pad + icon + r.s(8)
	words := r.fit(fc.navBold, p.words, b.Max.X-pad-x)
	r.text(fc.navBold, words, x, b.Min.Y+b.Dy()/2+r.s(9), ink)
}

// cookDots draws a dot for each page, the overview first, in room, the one the cook is on lit, and returns
// their zones. A recipe with more pages than there is room for shows the ones around the page the cook is
// on.
func (r *renderer) cookDots(room image.Rectangle, c *mealsCook) []customZone {
	pages := c.Steps() + 1
	each := r.s(cookDotW)
	n := min(pages, room.Dx()/each)
	if n <= 1 {
		return nil
	}
	cur := c.Step()
	from := min(max(cur-n/2, 0), pages-n)
	x := room.Min.X + (room.Dx()-n*each)/2
	var zones []customZone
	for i := from; i < from+n; i++ {
		b := image.Rect(x, room.Min.Y, x+each, room.Max.Y)
		rad, ink := r.s(5), lerp(dim, cream, 0.3)
		if i == cur {
			rad, ink = r.s(8), amber
		}
		r.disc(b.Min.X+b.Dx()/2, b.Min.Y+b.Dy()/2, rad, ink)
		zones = append(zones, customZone{r: b, kind: "meals:cook:dot", id: strconv.Itoa(i)})
		x += each
	}
	return zones
}

// cookGesture is a finger on the view that none of the page's own zones took: a swipe up or down scrolls
// what is under it, a swipe sideways in the step's pane turns the page, and a tap does what the button it
// is on says.
func cookGesture(d *Display, g touch.Gesture, at image.Point) {
	c := mealsCooking()
	if c == nil {
		return
	}
	ui := mealsCookUI
	c.Touched()
	mealsKeepOpenRecipe()
	now := mealsNow()
	switch g.Kind {
	case touch.SwipeUp, touch.SwipeDown:
		if d.r == nil {
			return
		}
		by := d.r.s(notchPx)
		if g.Kind == touch.SwipeDown {
			by = -by
		}
		ui.mu.Lock()
		ui.lastVert = now
		if !at.In(ui.ing) && !ui.text.Empty() {
			ui.textScroll = min(max(ui.textScroll+by, 0), ui.textMax)
		} else {
			ui.ingScroll = min(max(ui.ingScroll+by, 0), ui.ingMax)
		}
		ui.mu.Unlock()
		d.wake()
	case touch.SwipeLeft, touch.SwipeRight:
		ui.mu.Lock()
		inPane := at.In(ui.pane)
		scrolled := !ui.lastVert.IsZero() && now.Sub(ui.lastVert) < cookAxisHold
		ui.mu.Unlock()
		// A finger that began by scrolling stays a scroll; one that began outside the step's pane
		// (over the ingredients) is not turning the page.
		if !inPane || scrolled || !ui.debounce("nav", now) {
			return
		}
		if g.Kind == touch.SwipeLeft {
			if !c.Last() {
				c.Next()
			}
		} else {
			c.Back()
		}
		d.wake()
	case touch.Tap:
		z, ok := customZoneAt(at)
		if !ok {
			return
		}
		cookTap(d, c, z, now)
	}
}

// cookTap is a tap on one of the view's buttons.
func cookTap(d *Display, c *mealsCook, z customZone, now time.Time) {
	ui := mealsCookUI
	n, _ := strconv.Atoi(z.id)
	switch z.kind {
	case "meals:cook:back":
		if !ui.debounce("nav", now) {
			return
		}
		c.Back()
	case "meals:cook:next":
		if !ui.debounce("nav", now) {
			return
		}
		if c.Last() {
			cookFinish(d)
			return
		}
		c.Next()
	case "meals:cook:start":
		if !ui.debounce("nav", now) {
			return
		}
		if c.Steps() == 0 {
			cookFinish(d)
			return
		}
		c.Next()
	case "meals:cook:leave":
		cookFinish(d)
		return
	case "meals:cook:dot":
		if !ui.debounce("nav", now) {
			return
		}
		c.Go(n)
	case "meals:cook:more":
		c.More()
	case "meals:cook:less":
		c.Less()
	case "meals:cook:pill":
		if !ui.debounce("pill:"+z.id, now) {
			return
		}
		c.StartPill(n)
	case "meals:cook:tick":
		if !ui.debounce("tick:"+z.id, now) {
			return
		}
		c.Tick(n)
	case "meals:cook:toggle":
		ui.mu.Lock()
		ui.sheet = !ui.sheet
		ui.jump = ui.sheet
		ui.mu.Unlock()
	default:
		return
	}
	d.wake()
}

// cookFinish puts the recipe away and turns the page to the plan: the last step's Finish, and Back from a
// recipe that is gone. The photo is let go of, and the switcher stops offering the view.
func cookFinish(d *Display) {
	mealsCookClose()
	meals.mu.Lock()
	meals.recipe.slug = ""
	meals.view = "plan"
	meals.mu.Unlock()
	mealsCookUI.reset()
	d.wake()
}
