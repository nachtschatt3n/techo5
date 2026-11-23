//go:build !dot && !spot

package display

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"maps"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/touch"
	"github.com/HuskerMinion/techo5/echod/internal/lib/locale"
	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

// The Meals page's Plan view (meals.go): the next seven days of the Mealie meal plan, and the entries
// planned for the whole week, as a strip of day cards that scrolls sideways on a landscape screen and as an
// agenda that scrolls down on one held upright. A tap on an entry that has a recipe opens it for
// cooking, at the scale the plan gives it. Nothing here writes to the server.
//
// Sizes are in the Show 5's pixels, as the rest of the package's are, and r.s scales them.

const (
	// mealsPlanDays is how many days the view shows, today first.
	mealsPlanDays = 7
	// mealsPlanCardW is a day's card, and mealsPlanGap the room between cards and around the strip. A
	// Show 5 holds three whole cards and the edge of a fourth, which says there are more.
	mealsPlanCardW, mealsPlanGap = 300, 8
	// mealsPlanHeadH is a card's heading, mealsPlanRowH an entry's place in a card (the row itself
	// leaves mealsPlanRowGap of it), and mealsPlanAgendaRowH one in the agenda.
	mealsPlanHeadH, mealsPlanRowH, mealsPlanRowGap = 48, 116, 6
	mealsPlanAgendaRowH                            = 88
	// mealsPlanThumb is an entry's picture, a square.
	mealsPlanThumb = 72
	// mealsPlanThumbsMost is how many thumbnails the view holds at once. Each is a picture decoded in
	// memory, and a device with little to spare keeps a few.
	mealsPlanThumbsMost = 6
	// mealsPlanNotch is how far a vertical swipe scrolls for each step it reports, in the units the
	// finger travels: the touch layer's own notch, 40 on the Show 5.
	mealsPlanNotch = 40
	// mealsPlanEvery is how often the plan is fetched again while the view is up.
	mealsPlanEvery = 5 * time.Minute
	// mealsPlanDate is how the server writes a day.
	mealsPlanDate = "2006-01-02"
)

// mealsPlanNow is the time the plan is fetched for; a test sets it.
var mealsPlanNow = time.Now

func init() { registerMealsView(mealsPlanView()) }

// mealsPlanView is the Plan view. It is made by a function so that a test can register it on a page of
// its own.
func mealsPlanView() mealsView {
	return mealsView{
		id: "plan", label: func() string { return tr("meals_plan") }, rank: 2,
		every:   mealsPlanEvery,
		fetch:   mealsPlanFetch,
		draw:    mealsPlanDraw,
		gesture: mealsPlanGesture,
	}
}

// mealsPlanEntry is an entry of the plan with the address of its recipe's thumbnail, made when the plan
// is fetched, which is where the server's address is known.
type mealsPlanEntry struct {
	mealie.Entry
	Thumb string
}

// mealsPlanDay is one day and what is planned for it, a day's meals in the order of a day.
type mealsPlanDay struct {
	Date    time.Time
	Entries []mealsPlanEntry
}

// mealsPlanData is what the view is fetched with: the days from today on, and the entries planned for the
// whole week.
type mealsPlanData struct {
	From time.Time // midnight of the first day, in the device's own zone
	Week []mealsPlanEntry
	Days []mealsPlanDay
}

// empty is whether nothing is planned, neither for a day nor for the week.
func (d mealsPlanData) empty() bool {
	if len(d.Week) > 0 {
		return false
	}
	for _, day := range d.Days {
		if len(day.Entries) > 0 {
			return false
		}
	}
	return true
}

// mealsPlanDayStart is midnight of t's day, in t's zone.
func mealsPlanDayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// mealsPlanFetch gets the plan for the days from today.
func mealsPlanFetch(ctx context.Context, c mealie.Client) (any, error) {
	from := mealsPlanDayStart(mealsPlanNow())
	entries, err := c.Plan(ctx, from, from.AddDate(0, 0, mealsPlanDays-1), true)
	if err != nil {
		return nil, err
	}
	return mealsPlanBuild(c, from, entries), nil
}

// mealsPlanBuild sorts what the server sent into days. An entry with neither a recipe nor a title has
// nothing to show and is left out, and so is one for a day the view does not cover.
func mealsPlanBuild(c mealie.Client, from time.Time, entries []mealie.Entry) mealsPlanData {
	data := mealsPlanData{From: from}
	for i := range mealsPlanDays {
		data.Days = append(data.Days, mealsPlanDay{Date: from.AddDate(0, 0, i)})
	}
	// A stable sort by meal type, before the entries are dealt out to their days, leaves each day's in
	// the order of a day.
	entries = slices.Clone(entries)
	mealie.SortDay(entries)
	for _, e := range entries {
		if e.Name() == "" {
			continue
		}
		pe := mealsPlanEntry{Entry: e}
		if e.Recipe != nil {
			pe.Thumb = c.ImageURL(e.Recipe.ID, e.Recipe.Image, mealie.Tiny)
		}
		if e.Week() {
			data.Week = append(data.Week, pe)
			continue
		}
		for i := range data.Days {
			if data.Days[i].Date.Format(mealsPlanDate) == e.Date {
				data.Days[i].Entries = append(data.Days[i].Entries, pe)
				break
			}
		}
	}
	return data
}

// mealsPlanCol is one card of the strip, or one heading of the agenda.
type mealsPlanCol struct {
	key     string // what its scroll is kept under
	date    time.Time
	week    bool
	today   bool
	entries []mealsPlanEntry
}

// cols are the cards to show on the day today: the week's first, if there is anything for it, then the
// days from today (a plan fetched before midnight still has yesterday in it, which is not shown).
func (d mealsPlanData) cols(today time.Time) []mealsPlanCol {
	var out []mealsPlanCol
	if len(d.Week) > 0 {
		out = append(out, mealsPlanCol{key: "week", week: true, entries: d.Week})
	}
	for _, day := range d.Days {
		if mealsPlanDayStart(day.Date).Before(mealsPlanDayStart(today)) {
			continue
		}
		out = append(out, mealsPlanCol{key: day.Date.Format(mealsPlanDate), date: day.Date, today: sameDay(day.Date, today), entries: day.Entries})
	}
	return out
}

// head is what a card or a heading is called: "This week", "Today", "Tomorrow", or the day by its name and,
// on a card, its number. A heading in the agenda has room for the whole date.
func (c mealsPlanCol) head(today time.Time, long bool) string {
	lang := screenLang()
	var name string
	switch {
	case c.week:
		return tr("meals_this_week")
	case sameDay(c.date, today):
		name = tr("meals_today")
	case sameDay(c.date, today.AddDate(0, 0, 1)):
		name = tr("meals_tomorrow")
	case long:
		name = locale.Weekday(c.date, lang)
	default:
		return locale.ShortWeekday(c.date, lang) + " " + strconv.Itoa(c.date.Day())
	}
	if long {
		name += ", " + locale.MonthDay(c.date, lang)
	}
	return name
}

// mealsPlanTime is how long a recipe takes, in words, or "" when it does not say.
func mealsPlanTime(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	m := (seconds + 30) / 60
	switch {
	case m < 1:
		m = 1
		fallthrough
	case m < 60:
		return fmt.Sprintf(tr("meals_minutes"), m)
	case m%60 == 0:
		return fmt.Sprintf(tr("meals_hours"), m/60)
	}
	return fmt.Sprintf(tr("meals_hours_minutes"), m/60, m%60)
}

// mealsPlanType is the chip for an entry's meal type, or "" for a type this device has no word for.
func mealsPlanType(t string) string {
	switch t {
	case "breakfast":
		return tr("meals_breakfast")
	case "lunch":
		return tr("meals_lunch")
	case "dinner":
		return tr("meals_dinner")
	case "side":
		return tr("meals_side")
	case "snack":
		return tr("meals_snack")
	case "drink":
		return tr("meals_drink")
	case "dessert":
		return tr("meals_dessert")
	}
	return ""
}

// mealsPlanHit is where a card is on the screen, for a finger that scrolls it.
type mealsPlanHit struct {
	x0, x1 int
	key    string
}

// mealsPlanState is what the view keeps between frames: how far the strip is scrolled and how far each
// card is, and where the last frame put things, which the touch goroutine reads.
type mealsPlanState struct {
	sync.Mutex
	x      int            // how far the strip is scrolled, in pixels
	y      map[string]int // how far each card is, by its key; the agenda's one is "agenda"
	placed string         // the day and shape the strip was put on today's card for

	// From the last frame.
	hits     []mealsPlanHit
	maxY     map[string]int
	maxX     int
	stepX    int
	stepY    int
	top      int // the first pixel row a finger scrolls something on
	portrait bool
}

// mealsPlanUI is the view's state: there is only the one page.
var mealsPlanUI mealsPlanState

// mealsPlanRolled is the day the plan was last asked for again because the day changed under it.
var mealsPlanRolled atomic.Value

// mealsPlanRoll has the plan fetched again when the day has changed since it was, once for each day.
// Until it lands the view shows the days that are still ahead.
func mealsPlanRoll(today time.Time) {
	key := today.Format(mealsPlanDate)
	if v, _ := mealsPlanRolled.Load().(string); v == key {
		return
	}
	mealsPlanRolled.Store(key)
	meals.reload("plan")
}

// mealsPlanDraw draws the view: the strip of cards, the agenda on a screen held upright, or a sentence
// when nothing is planned.
func mealsPlanDraw(r *renderer, s scene, p mealsPane, body image.Rectangle) []customZone {
	data, ok := p.Data.(mealsPlanData)
	if !ok {
		return nil
	}
	today := s.now
	if !sameDay(data.From, today) {
		mealsPlanRoll(today)
	}
	cols := data.cols(today)
	if data.empty() || len(cols) == 0 {
		return r.mealsMessage(body, "calendar-blank-outline", tr("meals_nothing_week"), "")
	}
	mealsPlanThumbs.begin()
	defer mealsPlanThumbs.end()
	if r.mealsClass() == mealsPortrait {
		return r.mealsPlanAgenda(body, today, cols)
	}
	return r.mealsPlanStrip(body, today, cols)
}

// mealsPlanClip draws with fn into what is inside the rectangle clip, and nothing outside it.
func (r *renderer) mealsPlanClip(clip image.Rectangle, fn func()) {
	full := r.dst
	r.dst = full.SubImage(clip.Intersect(full.Rect)).(*image.RGBA)
	fn()
	r.dst = full
}

// mealsPlanStrip draws the day cards side by side, scrolled across, and returns a zone for each recipe
// entry that is whole enough on the screen to be hit.
func (r *renderer) mealsPlanStrip(body image.Rectangle, today time.Time, cols []mealsPlanCol) []customZone {
	fc := r.faces()
	cw, gap := r.s(mealsPlanCardW), r.s(mealsPlanGap)
	step := cw + gap
	band := image.Rect(body.Min.X, body.Min.Y+r.s(8), body.Max.X, body.Max.Y-r.s(8))
	maxX := max(gap+len(cols)*step-body.Dx(), 0)

	todayAt := 0
	for i, c := range cols {
		if c.today {
			todayAt = i
		}
	}
	ui := &mealsPlanUI
	ui.Lock()
	if key := fmt.Sprintf("%s|%v", today.Format(mealsPlanDate), cols[0].week); ui.placed != key || ui.portrait {
		// The strip starts on today's card, and is put back there when a new day begins or the week's
		// card comes or goes.
		ui.placed, ui.x = key, todayAt*step
	}
	ui.x = min(max(ui.x, 0), maxX)
	x0 := ui.x
	scrolls := maps.Clone(ui.y)
	ui.Unlock()

	var zones []customZone
	var hits []mealsPlanHit
	maxY := map[string]int{}
	newY := maps.Clone(scrolls) // a card that is off the screen keeps where it was scrolled to
	if newY == nil {
		newY = map[string]int{}
	}
	rowH := r.s(mealsPlanRowH)
	r.mealsPlanClip(body, func() {
		for i, c := range cols {
			card := image.Rect(body.Min.X+gap+i*step-x0, band.Min.Y, body.Min.X+gap+i*step-x0+cw, band.Max.Y)
			vis := card.Intersect(body)
			if vis.Empty() {
				continue
			}
			rad := float64(r.s(14))
			r.roundFill(card, rad, surface(2), surface(2))
			if c.today {
				r.roundStroke(card, rad, r.sf(3), amber)
			} else {
				r.roundStroke(card, rad, r.sf(1), ember)
			}
			head := r.fit(fc.labelBold, c.head(today, false), card.Dx()-2*r.s(14))
			hc := cream
			if c.today {
				hc = amber
			}
			r.text(fc.labelBold, head, card.Min.X+r.s(14), card.Min.Y+r.s(mealsPlanHeadH)*7/10, hc)

			view := image.Rect(card.Min.X, card.Min.Y+r.s(mealsPlanHeadH), card.Max.X, card.Max.Y-r.s(8))
			content := len(c.entries)*rowH + r.s(4)
			most := max(content-view.Dy(), 0)
			maxY[c.key] = most
			scroll := min(max(scrolls[c.key], 0), most)
			newY[c.key] = scroll
			hits = append(hits, mealsPlanHit{x0: vis.Min.X, x1: vis.Max.X, key: c.key})
			clip := view.Intersect(body)
			if len(c.entries) == 0 {
				r.mealsPlanClip(clip, func() {
					for k, line := range r.wrapLines(fc.nav, tr("meals_nothing_planned"), view.Dx()-2*r.s(14)) {
						r.text(fc.nav, line, view.Min.X+r.s(14), view.Min.Y+r.s(36)+k*r.s(30), dim)
					}
				})
				continue
			}
			r.mealsPlanClip(clip, func() {
				for k, e := range c.entries {
					top := view.Min.Y + r.s(4) + k*rowH - scroll
					row := image.Rect(card.Min.X+r.s(8), top, card.Max.X-r.s(8), top+rowH-r.s(mealsPlanRowGap))
					if row.Max.Y <= clip.Min.Y || row.Min.Y >= clip.Max.Y {
						continue
					}
					r.mealsPlanRow(e, row, false)
					if z, ok := r.mealsPlanZone(e, row, clip); ok {
						zones = append(zones, z)
					}
				}
			})
		}
	})
	ui.Lock()
	ui.hits, ui.maxY, ui.maxX, ui.stepX, ui.stepY, ui.top, ui.portrait = hits, maxY, maxX, step, r.s(mealsPlanNotch), band.Min.Y, false
	ui.y = newY
	ui.Unlock()
	return zones
}

// mealsPlanAgenda draws the days one under another, each under its heading, for a screen held upright.
func (r *renderer) mealsPlanAgenda(body image.Rectangle, today time.Time, cols []mealsPlanCol) []customZone {
	fc := r.faces()
	view := image.Rect(body.Min.X, body.Min.Y+r.s(8), body.Max.X, body.Max.Y)
	headH, rowH := r.s(mealsPlanHeadH), r.s(mealsPlanAgendaRowH)
	content := 0
	for _, c := range cols {
		content += headH + max(len(c.entries), 1)*rowH
	}
	most := max(content-view.Dy(), 0)
	ui := &mealsPlanUI
	ui.Lock()
	scroll := min(max(ui.y["agenda"], 0), most)
	ui.Unlock()

	var zones []customZone
	in := r.s(14)
	r.mealsPlanClip(view, func() {
		y := view.Min.Y - scroll
		for _, c := range cols {
			if y+headH > view.Min.Y && y < view.Max.Y {
				hc := cream
				if c.today {
					hc = amber
				}
				r.text(fc.labelBold, r.fit(fc.labelBold, c.head(today, true), view.Dx()-2*in), in, y+headH*7/10, hc)
			}
			y += headH
			if len(c.entries) == 0 {
				if y+rowH > view.Min.Y && y < view.Max.Y {
					r.text(fc.nav, tr("meals_nothing_planned"), in+r.s(8), y+rowH/2+r.s(8), dim)
				}
				y += rowH
				continue
			}
			for _, e := range c.entries {
				if y+rowH > view.Min.Y && y < view.Max.Y {
					row := image.Rect(in, y, view.Max.X-in, y+rowH-r.s(mealsPlanRowGap))
					r.mealsPlanRow(e, row, true)
					if z, ok := r.mealsPlanZone(e, row, view); ok {
						zones = append(zones, z)
					}
				}
				y += rowH
			}
		}
	})
	ui.Lock()
	ui.hits = []mealsPlanHit{{x0: view.Min.X, x1: view.Max.X, key: "agenda"}}
	ui.maxY, ui.maxX, ui.stepX, ui.stepY, ui.top, ui.portrait = map[string]int{"agenda": most}, 0, 0, r.s(mealsPlanNotch), view.Min.Y, true
	ui.y = map[string]int{"agenda": scroll}
	ui.Unlock()
	return zones
}

// mealsPlanZone is where a tap on the entry's row counts, for an entry with a recipe to open, as much of
// the row as is in clip. A row that has gone mostly under the edge is not a place to tap; it is scrolled
// first.
func (r *renderer) mealsPlanZone(e mealsPlanEntry, row, clip image.Rectangle) (customZone, bool) {
	if !e.Cookable() {
		return customZone{}, false
	}
	z := row.Intersect(clip)
	if z.Dx() < r.s(mealsTarget) || z.Dy() < r.s(mealsTarget) {
		return customZone{}, false
	}
	return customZone{r: z, kind: "meals:entry", id: strconv.Itoa(e.ID)}, true
}

// mealsPlanRow draws one entry in row: its picture, its meal type and how long it takes, and its name. One
// with a recipe has an arrow at its right end, since it can be opened. In the agenda, whose rows are low,
// the name keeps to a line.
func (r *renderer) mealsPlanRow(e mealsPlanEntry, row image.Rectangle, agenda bool) {
	fc := r.faces()
	bg := surface(4)
	r.roundFill(row, float64(r.s(10)), bg, bg)
	side := r.s(mealsPlanThumb)
	thumb := image.Rect(row.Min.X+r.s(8), row.Min.Y+(row.Dy()-side)/2, row.Min.X+r.s(8)+side, row.Min.Y+(row.Dy()-side)/2+side)
	r.mealsPlanThumb(e, thumb, bg)

	tx := thumb.Max.X + r.s(12)
	right := row.Max.X - r.s(10)
	if e.Cookable() {
		r.mdiIconIn("chevron-right", image.Rect(right-r.s(28), row.Min.Y, right, row.Max.Y), 28, dim)
		right -= r.s(28)
	}
	room := right - tx

	// The meal type and the time, on the first line: the time only if it fits beside the type.
	base := row.Min.Y + r.s(30)
	if agenda {
		base = row.Min.Y + r.s(32)
	}
	chip, took := mealsPlanType(e.Type), ""
	if e.Recipe != nil {
		took = mealsPlanTime(e.Recipe.TotalTimeSeconds)
	}
	if took != "" && r.width(fc.nav, chip)+r.s(16)+r.width(fc.nav, took) > room {
		took = ""
	}
	if chip != "" {
		r.text(fc.nav, r.fit(fc.nav, chip, room), tx, base, amber)
	}
	if took != "" {
		r.text(fc.nav, took, right-r.width(fc.nav, took), base, dim)
	}

	// The name, on two lines at most; a note with a few words of its own has one line and those words.
	lines := 2
	words := e.Text
	if e.Cookable() || words == "" {
		words = ""
	} else {
		lines = 1
	}
	name := r.wrapLines(fc.label, e.Name(), room)
	if agenda {
		lines = 1
		if words != "" {
			name, words = []string{e.Name() + " – " + words}, ""
		}
	}
	if len(name) > lines {
		name = append(name[:lines-1:lines-1], r.fit(fc.label, joinWords(name[lines-1:]), room))
	}
	y := base
	for _, l := range name {
		y += r.s(34)
		r.text(fc.label, r.fit(fc.label, l, room), tx, y, cream)
	}
	if words != "" {
		r.text(fc.nav, r.fit(fc.nav, words, room), tx, y+r.s(30), dim)
	}
}

// joinWords is the lines put back together as one.
func joinWords(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += " "
		}
		out += l
	}
	return out
}

// mealsPlanThumb draws an entry's picture in b once it has come, and until then, or for an entry with
// none, a soft color of its own with the icon of a recipe, or of a note.
func (r *renderer) mealsPlanThumb(e mealsPlanEntry, b image.Rectangle, bg color.RGBA) {
	// A picture is not asked for while it is under the edge of what is drawn: it would use up one of the few.
	if !b.Intersect(r.dst.Rect).Empty() {
		if img := mealsPlanThumbs.get(e.Thumb, b.Dx(), b.Dy()); img != nil {
			r.drawLibPicture(img, b)
			return
		}
	}
	hue := libHue(e.Name())
	top, bottom := lerp(bg, hue, 0.55), lerp(bg, hue, 0.22)
	r.roundFill(b, float64(max(b.Dx()/14, 2)), top, bottom)
	icon := "silverware-fork-knife"
	if !e.Cookable() {
		icon = "note-text-outline"
	}
	r.mdiIconIn(icon, b, 30, lerp(bottom, cream, 0.6))
}

// mealsPlanThumbHold is the thumbnails the view holds, newest first. The pictures are kept by the library's
// own store (library_pictures.go); this is the view's share of it: a frame asks for at most
// mealsPlanThumbsMost, and one that is not among the last such frames' asks is let go.
type mealsPlanThumbHold struct {
	sync.Mutex
	held  []mealsPlanPic // from earlier frames
	frame []mealsPlanPic // this one's, in the order they were asked for
}

type mealsPlanPic struct {
	url  string
	w, h int
}

// mealsPlanThumbs are the view's thumbnails: there is only the one page.
var mealsPlanThumbs mealsPlanThumbHold

// begin starts a frame's asking.
func (h *mealsPlanThumbHold) begin() {
	h.Lock()
	h.frame = h.frame[:0]
	h.Unlock()
}

// get is the thumbnail of u, w by h, when it is kept, and nil while it is on its way (it is asked for) or
// when this frame has already asked for as many as the view holds, in which case it is not asked for at
// all.
func (h *mealsPlanThumbHold) get(u string, w, hh int) *image.RGBA {
	if u == "" {
		return nil
	}
	t := mealsPlanPic{u, w, hh}
	h.Lock()
	if !slices.Contains(h.frame, t) {
		if len(h.frame) >= mealsPlanThumbsMost {
			h.Unlock()
			return nil
		}
		h.frame = append(h.frame, t)
	}
	h.Unlock()
	// The kept pictures wake the screen when one arrives, as the library's do.
	meals.mu.Lock()
	changed := meals.changed
	meals.mu.Unlock()
	if changed != nil {
		libPics.setChanged(changed)
	}
	return libPics.pictureFit(u, w, hh)
}

// end finishes a frame: what it asked for is what is held, then what was held before while there is room;
// the rest is let go of.
func (h *mealsPlanThumbHold) end() {
	h.Lock()
	next := slices.Clone(h.frame)
	for _, t := range h.held {
		if len(next) < mealsPlanThumbsMost && !slices.Contains(next, t) {
			next = append(next, t)
		}
	}
	var drop []mealsPlanPic
	for _, t := range h.held {
		if !slices.Contains(next, t) {
			drop = append(drop, t)
		}
	}
	h.held = next
	h.Unlock()
	for _, t := range drop {
		libPics.forgetFit(t.url, t.w, t.h)
	}
}

// forgetFit lets go of the fitted picture of u, w by h, when it is kept.
func (p *libPictures) forgetFit(u string, w, h int) {
	key := libPicFitKey(u, w, h)
	p.mu.Lock()
	defer p.mu.Unlock()
	if el, ok := p.kept[key]; ok {
		pic := el.Value.(*libPic)
		p.order.Remove(el)
		delete(p.kept, key)
		p.bytes -= int64(len(pic.img.Pix))
	}
}

// mealsPlanFind is the entry the plan has under id.
func mealsPlanFind(id string) (mealsPlanEntry, bool) {
	meals.mu.Lock()
	var data mealsPlanData
	if f := meals.feeds["plan"]; f != nil {
		data, _ = f.Data.(mealsPlanData)
	}
	meals.mu.Unlock()
	all := slices.Clone(data.Week)
	for _, d := range data.Days {
		all = append(all, d.Entries...)
	}
	for _, e := range all {
		if strconv.Itoa(e.ID) == id {
			return e, true
		}
	}
	return mealsPlanEntry{}, false
}

// mealsPlanGesture is a finger on the view that the page's own zones did not take. A tap on an entry
// opens its recipe. A swipe across moves the strip by a card and a swipe up or down scrolls the card it
// is on (or the agenda): which of the two it is the touch layer has already settled, from the way the
// finger first moved, so a sideways swipe never also scrolls a card, nor a vertical one the strip.
func mealsPlanGesture(d *Display, g touch.Gesture, at image.Point) {
	switch g.Kind {
	case touch.Tap:
		z, ok := customZoneAt(at)
		if !ok || z.kind != "meals:entry" {
			return
		}
		if e, ok := mealsPlanFind(z.id); ok && e.Cookable() {
			d.openRecipe(e.Recipe.Slug, e.StartScale())
		}
	case touch.SwipeLeft, touch.SwipeRight:
		by := 1
		if g.Kind == touch.SwipeRight {
			by = -1
		}
		if mealsPlanUI.scrollAcross(by) {
			d.wake()
		}
	case touch.SwipeUp, touch.SwipeDown:
		by := 1 // a finger moving up shows what is further down
		if g.Kind == touch.SwipeDown {
			by = -1
		}
		if mealsPlanUI.scrollDown(at, by) {
			d.wake()
		}
	}
}

// scrollAcross moves the strip by a card, by cards forward or back, and says whether it moved. The
// agenda has no strip.
func (s *mealsPlanState) scrollAcross(by int) bool {
	s.Lock()
	defer s.Unlock()
	if s.portrait || s.stepX <= 0 {
		return false
	}
	was := s.x
	at := (s.x+s.stepX/2)/s.stepX + by
	s.x = min(max(at*s.stepX, 0), s.maxX)
	return s.x != was
}

// scrollDown scrolls the card under the finger at at by one notch, forward for by 1 and back for -1, and
// says whether it moved. A finger that came down above the cards, in the bar, scrolls nothing.
func (s *mealsPlanState) scrollDown(at image.Point, by int) bool {
	s.Lock()
	defer s.Unlock()
	if at.Y < s.top {
		return false
	}
	for _, h := range s.hits {
		if at.X < h.x0 || at.X >= h.x1 {
			continue
		}
		if s.y == nil {
			s.y = map[string]int{}
		}
		was := s.y[h.key]
		s.y[h.key] = min(max(was+by*s.stepY, 0), s.maxY[h.key])
		return s.y[h.key] != was
	}
	return false
}
