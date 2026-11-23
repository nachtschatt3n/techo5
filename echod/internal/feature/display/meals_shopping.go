//go:build !dot && !spot

package display

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"slices"
	"sync"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/touch"
	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

// The Meals page's Shopping view: one of the Mealie server's shopping lists, grouped by label as
// Mealie's own page groups it, with the items still to buy in the room above and the ones already
// checked in a block at the bottom that stays folded until it is tapped. A tap on an item checks it, or
// unchecks it in the block below. That is the one thing this device writes to the server, and it writes
// it as the person sees it: the box flips at once, the server is told in the background, the list is
// read again afterward (an unchecked item may have been merged into an identical one), and a write that
// fails turns the box back with a few words that say so.
//
// What is kept between frames is here and not in the hub (meals.go), because it is this view's: how far
// the list is scrolled, whether the Done block is open, which list the switcher has turned to, and the
// writes still on their way.

const (
	// shopRowH is an item's row: a floury finger has to hit it, so it is as tall as the least a primary
	// target may be. shopHeadH is a label's heading, which nothing taps; shopHeaderH the strip over the
	// list. Its zones start a little below the strip's top, clear of the bar's tabs, which reach past the
	// bar, and are still the least height a zone may have.
	shopRowH, shopHeadH, shopHeaderH = mealsTarget, 40, 64
	// shopUndoW is the Undo pill's width, and shopListMost the widest the list's name goes.
	shopUndoW, shopListMost = 200, 300
	// shopEvery is how often the list is read again while it is on the screen.
	shopEvery = 30 * time.Second
	// shopNoteFor is how long "Couldn't save" stays in the strip, and shopUndoFor how long Undo is offered.
	shopNoteFor, shopUndoFor = 5 * time.Second, 5 * time.Second
	// shopWriteMost is how long one write may take.
	shopWriteMost = 15 * time.Second
)

// mealsShop is what the Shopping view's fetch gives the page: every list the household has (the
// switcher cycles through them) and the one on show with its items.
type mealsShop struct {
	Lists []mealie.ListSummary
	List  mealie.List
}

// shopOver is a check the person made that the server has not shown back yet.
type shopOver struct {
	checked bool
	// done is a write that went through: the entry stays until a fetch that began after it has landed, so
	// the box does not flicker back on a list read before the write.
	done bool
}

// shopState is what the view keeps between frames.
type shopState struct {
	mu sync.Mutex
	// wmu keeps the writes in the order they were made.
	wmu sync.Mutex

	scroll, maxScroll int  // pixels, and how far the list can go
	doneOpen          bool // the Done block is unfolded

	pick, pickBase string // the list the switcher chose, and the setting it was chosen against

	over      map[string]*shopOver
	deb       map[string]*mealsDebounce // a debounce for each item
	undo      mealie.Item               // the item last checked, to be unchecked by Undo
	undoUntil time.Time
	failUntil time.Time
}

// mealsShopping is the view's one set of state: there is only the one page.
var mealsShopping = &shopState{}

func init() { registerMealsView(mealsShoppingView()) }

// mealsShoppingView is the view as the page registers it. A test registers its own copy.
func mealsShoppingView() mealsView {
	return mealsView{
		id: "shopping", label: func() string { return tr("meals_shopping") }, rank: 1,
		every: shopEvery, fetch: shopFetch, draw: shopDraw, gesture: shopGesture,
	}
}

// current is the list to show out of the household's: the one the switcher chose, else the one the setup
// page names, else the first (the oldest, which is Mealie's own default). "" when there is none.
func (s *shopState) current(lists []mealie.ListSummary) string {
	if len(lists) == 0 {
		return ""
	}
	base := config.Get().Mealie.List
	s.mu.Lock()
	pick, pickBase := s.pick, s.pickBase
	s.mu.Unlock()
	has := func(id string) bool {
		return id != "" && slices.ContainsFunc(lists, func(l mealie.ListSummary) bool { return l.ID == id })
	}
	switch {
	case pickBase == base && has(pick):
		return pick
	case has(base):
		return base
	}
	return lists[0].ID
}

// finished are the writes that went through at this moment: a fetch that starts now has them.
func (s *shopState) finished() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id, o := range s.over {
		if o.done {
			ids = append(ids, id)
		}
	}
	return ids
}

// forget drops the checks that a fetch which began after them has shown back.
func (s *shopState) forget(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if o := s.over[id]; o != nil && o.done {
			delete(s.over, id)
		}
	}
}

// shopFetch reads the lists, and the one to show.
func shopFetch(ctx context.Context, c mealie.Client) (any, error) {
	s := mealsShopping
	was := s.finished()
	lists, err := c.Lists(ctx)
	if err != nil {
		return nil, err
	}
	// The select in Home Assistant offers the names the page has just read.
	mealsListsRead(lists)
	id := s.current(lists)
	if id == "" {
		s.forget(was)
		return mealsShop{}, nil
	}
	list, err := c.List(ctx, id)
	if err != nil {
		return nil, err
	}
	s.forget(was)
	return mealsShop{Lists: lists, List: list}, nil
}

// Row kinds of the list as it is laid out.
const (
	shopHead     = iota // a label's heading
	shopItem            // an item still to buy
	shopDoneHead        // the Done block's heading, which folds and unfolds it
	shopDoneItem        // a checked item, under the open Done block
	shopNone            // "Nothing to buy", over a list whose items are all checked
)

// shopRow is one row of the list.
type shopRow struct {
	kind int
	text string
	id   string     // the item, for the rows that are one
	dot  color.RGBA // a heading's label color
}

// shopModel is the list as the page draws it.
type shopModel struct {
	name  string
	rows  []shopRow
	toBuy int
	empty bool // nothing on the list at all
	many  bool // the household has more than one list
}

// shopItemsOf is the list's items with the person's own checks laid over what the server said.
func shopItemsOf(l mealie.List, over map[string]bool) mealie.List {
	l.Items = slices.Clone(l.Items)
	for i := range l.Items {
		if c, ok := over[l.Items[i].ID]; ok {
			l.Items[i].Checked = c
		}
	}
	return l
}

// shopColor is a label's color for its dot: the one the server gave, else a neutral gray.
func shopColor(l *mealie.Label) color.RGBA {
	if l != nil {
		if c, err := parseHex(l.HexColor()); err == nil {
			return c
		}
	}
	return color.RGBA{0x95, 0x95, 0x95, 0xff}
}

// buildShop lays the list out in rows: the unchecked items by label, the way the server's own page
// groups them (the items with no label first, with no heading), then the Done block, folded unless
// doneOpen. An item with neither a line nor a note has nothing to show and is left out.
func buildShop(d mealsShop, over map[string]bool, doneOpen bool) shopModel {
	m := shopModel{name: d.List.Name, many: len(d.Lists) > 1}
	if m.name == "" {
		m.name = tr("meals_shopping_list")
	}
	groups, done := shopItemsOf(d.List, over).Group()
	for _, g := range groups {
		var rows []shopRow
		for _, it := range g.Items {
			if t := it.Text(); t != "" {
				rows = append(rows, shopRow{kind: shopItem, text: t, id: it.ID})
			}
		}
		if len(rows) == 0 {
			continue
		}
		if g.Label != nil {
			m.rows = append(m.rows, shopRow{kind: shopHead, text: g.Label.Name, dot: shopColor(g.Label)})
		}
		m.rows = append(m.rows, rows...)
		m.toBuy += len(rows)
	}
	var checked []shopRow
	for _, it := range done {
		if t := it.Text(); t != "" {
			checked = append(checked, shopRow{kind: shopDoneItem, text: t, id: it.ID})
		}
	}
	m.empty = m.toBuy == 0 && len(checked) == 0
	if m.toBuy == 0 && len(checked) > 0 {
		m.rows = append(m.rows, shopRow{kind: shopNone, text: tr("meals_nothing_to_buy")})
	}
	if len(checked) > 0 {
		m.rows = append(m.rows, shopRow{kind: shopDoneHead, text: fmt.Sprintf(tr("meals_done_count"), len(checked))})
		if doneOpen {
			m.rows = append(m.rows, checked...)
		}
	}
	return m
}

// shopSlot is a row where the layout put it: its column, how far down the column it starts and how tall
// it is, in pixels.
type shopSlot struct{ i, col, y, h int }

// shopPlace puts the rows in one column, or two read down the first and then down the second. The two
// are made as near the same height as the rows allow, and a heading is never left at the bottom of the
// first, away from its items. Both scroll together.
func shopPlace(rows []shopRow, hs func(shopRow) int, cols int) []shopSlot {
	split := len(rows)
	if cols == 2 {
		total := 0
		for _, r := range rows {
			total += hs(r)
		}
		split = 0
		for acc := 0; split < len(rows) && acc+hs(rows[split]) <= (total+1)/2; split++ {
			acc += hs(rows[split])
		}
		split = max(split, 1)
		for split > 0 && (rows[split-1].kind == shopHead || rows[split-1].kind == shopDoneHead) {
			split--
		}
		if split == 0 {
			split = len(rows)
		}
	}
	return shopPlaceAt(rows, hs, split)
}

// shopPlaceAt is shopPlace's second half: the rows before split in the first column, the rest in the
// second.
func shopPlaceAt(rows []shopRow, hs func(shopRow) int, split int) []shopSlot {
	var slots []shopSlot
	y := [2]int{}
	for i, r := range rows {
		c := 0
		if i >= split {
			c = 1
		}
		slots = append(slots, shopSlot{i: i, col: c, y: y[c], h: hs(r)})
		y[c] += hs(r)
	}
	return slots
}

// shopHeight is how tall the taller column is.
func shopHeight(slots []shopSlot) int {
	h := 0
	for _, s := range slots {
		h = max(h, s.y+s.h)
	}
	return h
}

// shopDraw draws the view into body and returns where a tap means something.
func shopDraw(r *renderer, s scene, p mealsPane, body image.Rectangle) []customZone {
	data, _ := p.Data.(mealsShop)
	st := mealsShopping
	st.mu.Lock()
	over := make(map[string]bool, len(st.over))
	for id, o := range st.over {
		over[id] = o.checked
	}
	doneOpen, scroll := st.doneOpen, st.scroll
	canUndo := s.now.Before(st.undoUntil)
	failing := s.now.Before(st.failUntil)
	st.mu.Unlock()

	m := buildShop(data, over, doneOpen)
	fc := r.faces()
	margin := r.s(customIn)

	// The strip over the list: its name, how many are left to buy, Undo, and when it was read.
	hb := image.Rect(body.Min.X, body.Min.Y, body.Max.X, body.Min.Y+r.s(shopHeaderH))
	view := image.Rect(body.Min.X, hb.Max.Y, body.Max.X, body.Max.Y)
	var zones []customZone
	midY := hb.Min.Y + hb.Dy()/2
	x := hb.Min.X + margin
	pillH := r.s(48)
	name := m.name
	if m.many {
		w := min(r.width(fc.navBold, name)+2*r.s(mealsPillPad)+r.s(28), r.s(shopListMost))
		b := image.Rect(x, midY-pillH/2, x+w, midY+pillH/2)
		r.roundFill(b, float64(pillH)/2, surface(7), surface(5))
		r.roundHighlight(b, float64(pillH)/2)
		icon := image.Rect(b.Max.X-r.s(36), b.Min.Y, b.Max.X-r.s(8), b.Max.Y)
		r.mdiIconIn("chevron-down", icon, 20, cream)
		word := r.fit(fc.navBold, name, b.Dx()-2*r.s(mealsPillPad)-r.s(20))
		r.text(fc.navBold, word, b.Min.X+r.s(mealsPillPad), midY+r.s(9), cream)
		zones = append(zones, customZone{r: image.Rect(b.Min.X, hb.Min.Y+r.s(6), b.Max.X, hb.Max.Y), kind: "meals:shop:list", id: "list"})
		x = b.Max.X + r.s(16)
	} else {
		word := r.fit(fc.navBold, name, r.s(shopListMost)-r.s(mealsPillPad))
		r.text(fc.navBold, word, x, midY+r.s(9), cream)
		x += r.width(fc.navBold, word) + r.s(16)
	}
	if !failing {
		count := fmt.Sprintf(tr("meals_to_buy"), m.toBuy)
		r.text(fc.value, count, x, midY+r.s(9), lerp(dim, cream, 0.5))
		x += r.width(fc.value, count) + r.s(16)
	}
	if canUndo && !failing {
		b := image.Rect(x, midY-pillH/2, x+r.s(shopUndoW), midY+pillH/2)
		r.mealsPill(b, tr("meals_undo"), false)
		zones = append(zones, customZone{r: image.Rect(b.Min.X, hb.Min.Y+r.s(6), b.Max.X, hb.Max.Y), kind: "meals:shop:undo", id: "undo"})
		x = b.Max.X + r.s(16)
	}
	var note string
	var noteInk color.RGBA
	switch {
	case failing:
		note, noteInk = tr("meals_save_failed"), amber
	case !p.Fetched.IsZero():
		note, noteInk = fmt.Sprintf(tr("meals_updated"), clockText(p.Fetched)), dim
	}
	if room := hb.Max.X - margin - x; note != "" && room > r.s(80) {
		note = r.fit(fc.value, note, room)
		r.text(fc.value, note, hb.Max.X-margin-r.width(fc.value, note), midY+r.s(9), noteInk)
	}

	if m.empty {
		return append(zones, r.mealsMessage(view, "cart-outline", tr("meals_nothing_to_buy"), "")...)
	}

	// The rows.
	cols := 2
	if r.mealsClass() == mealsPortrait {
		cols = 1
	}
	hs := func(row shopRow) int {
		switch row.kind {
		case shopHead:
			return r.s(shopHeadH)
		}
		return r.s(shopRowH)
	}
	slots := shopPlace(m.rows, hs, cols)
	content := shopHeight(slots)
	maxScroll := max(content-view.Dy(), 0)
	scroll = min(max(scroll, 0), maxScroll)
	st.mu.Lock()
	st.scroll, st.maxScroll = scroll, maxScroll
	st.mu.Unlock()

	colW := (view.Dx() - (cols+1)*margin) / cols
	colX := func(c int) int { return view.Min.X + margin + c*(colW+margin) }
	full := r.dst
	r.dst = full.SubImage(view).(*image.RGBA)
	for _, sl := range slots {
		top := view.Min.Y + sl.y - scroll
		b := image.Rect(colX(sl.col), top, colX(sl.col)+colW, top+sl.h)
		if b.Max.Y <= view.Min.Y || b.Min.Y >= view.Max.Y {
			continue
		}
		r.shopRow(m.rows[sl.i], b)
	}
	r.dst = full
	for _, sl := range slots {
		row := m.rows[sl.i]
		if row.kind == shopHead || row.kind == shopNone {
			continue
		}
		top := view.Min.Y + sl.y - scroll
		// Only a row that is wholly in view is a target: a half one under the edge is not a safe one.
		if top < view.Min.Y || top+sl.h > view.Max.Y {
			continue
		}
		x0, x1 := view.Min.X, view.Max.X
		if cols == 2 {
			if sl.col == 0 {
				x1 = colX(0) + colW + margin/2
			} else {
				x0 = colX(1) - margin/2
			}
		}
		z := customZone{r: image.Rect(x0, top, x1, top+sl.h), kind: "meals:shop:row", id: row.id}
		if row.kind == shopDoneHead {
			z.kind, z.id = "meals:shop:done", "done"
		}
		zones = append(zones, z)
	}
	if maxScroll > 0 {
		// A thin thumb at the right edge, so a list that goes on is seen to.
		track := view.Dy() - 2*r.s(8)
		thumb := max(track*view.Dy()/content, r.s(24))
		y := view.Min.Y + r.s(8) + (track-thumb)*scroll/maxScroll
		tb := image.Rect(view.Max.X-r.s(8), y, view.Max.X-r.s(4), y+thumb)
		r.roundFill(tb, float64(tb.Dx())/2, surface(8), surface(8))
	}
	return zones
}

// shopRow draws one row in b.
func (r *renderer) shopRow(row shopRow, b image.Rectangle) {
	fc := r.faces()
	midY := b.Min.Y + b.Dy()/2
	switch row.kind {
	case shopHead:
		// The label's dot and its name.
		rad := r.s(8)
		r.disc(b.Min.X+r.s(12)+rad, midY+r.s(6), rad, row.dot)
		word := r.fit(fc.navBold, row.text, b.Dx()-r.s(60))
		r.text(fc.navBold, word, b.Min.X+r.s(12)+2*rad+r.s(12), midY+r.s(15), lerp(dim, cream, 0.5))
		return
	case shopNone:
		r.text(fc.label, r.fit(fc.label, row.text, b.Dx()-r.s(24)), b.Min.X+r.s(12), midY+r.s(10), dim)
	case shopDoneHead:
		word := r.fit(fc.navBold, row.text, b.Dx()-r.s(80))
		r.text(fc.navBold, word, b.Min.X+r.s(12), midY+r.s(9), lerp(dim, cream, 0.5))
		icon := "chevron-down"
		if mealsShopping.isDoneOpen() {
			icon = "chevron-up"
		}
		r.mdiIconIn(icon, image.Rect(b.Max.X-r.s(56), b.Min.Y, b.Max.X-r.s(12), b.Max.Y), 24, dim)
	default:
		checked := row.kind == shopDoneItem
		side := r.s(36)
		box := image.Rect(b.Min.X+r.s(12), midY-side/2, b.Min.X+r.s(12)+side, midY+side/2)
		if checked {
			r.roundFill(box, float64(r.s(8)), shift(amber, 14), shift(amber, -14))
			r.mdiIconIn("check", box, 24, onAccent())
		} else {
			r.roundStroke(box, float64(r.s(8)), r.sf(2), lerp(dim, cream, 0.5))
		}
		x := box.Max.X + r.s(16)
		ink := cream
		if checked {
			ink = dim
		}
		word := r.fit(fc.label, row.text, b.Max.X-r.s(12)-x)
		r.text(fc.label, word, x, midY+r.s(10), ink)
		if checked {
			r.fillRect(image.Rect(x, midY, x+r.width(fc.label, word), midY+max(r.s(2), 1)), dim)
		}
	}
	r.fillRect(image.Rect(b.Min.X+r.s(12), b.Max.Y-1, b.Max.X-r.s(12), b.Max.Y), surface(2))
}

// isDoneOpen is whether the Done block is unfolded. Drawing already holds no lock when it asks.
func (s *shopState) isDoneOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doneOpen
}

// shopGesture is a finger on the view that none of the page's own zones took: a swipe scrolls, a tap on
// a row checks it, and the strip's list name, Undo and the Done heading do what they say.
func shopGesture(d *Display, g touch.Gesture, at image.Point) {
	st := mealsShopping
	switch g.Kind {
	case touch.SwipeUp, touch.SwipeDown:
		if d.r == nil {
			return
		}
		by := d.r.s(notchPx)
		if g.Kind == touch.SwipeDown {
			by = -by
		}
		st.mu.Lock()
		st.scroll = min(max(st.scroll+by, 0), st.maxScroll)
		st.mu.Unlock()
		d.wake()
	case touch.Tap:
		z, ok := customZoneAt(at)
		if !ok {
			return
		}
		switch z.kind {
		case "meals:shop:row":
			st.toggle(d, z.id)
		case "meals:shop:done":
			st.mu.Lock()
			st.doneOpen = !st.doneOpen
			st.mu.Unlock()
		case "meals:shop:undo":
			st.undoLast(d)
		case "meals:shop:list":
			st.nextList()
		default:
			return
		}
		d.wake()
	}
}

// nextList turns to the next of the household's lists, and the first after the last.
func (s *shopState) nextList() {
	data := shopData()
	if len(data.Lists) < 2 {
		return
	}
	cur := s.current(data.Lists)
	i := slices.IndexFunc(data.Lists, func(l mealie.ListSummary) bool { return l.ID == cur })
	next := data.Lists[(i+1)%len(data.Lists)].ID
	s.mu.Lock()
	s.pick, s.pickBase, s.scroll = next, config.Get().Mealie.List, 0
	s.mu.Unlock()
	meals.reload("shopping")
}

// shopData is what the view was last given by its fetch: nothing before the first has landed.
func shopData() mealsShop {
	meals.mu.Lock()
	defer meals.mu.Unlock()
	if f := meals.feeds["shopping"]; f != nil {
		d, _ := f.Data.(mealsShop)
		return d
	}
	return mealsShop{}
}

// itemNamed is the item with that id on the list as it was last fetched.
func itemNamed(id string) (mealie.Item, bool) {
	data := shopData()
	for _, it := range data.List.Items {
		if it.ID == id {
			return it, true
		}
	}
	return mealie.Item{}, false
}

// toggle checks an item that is not, and unchecks one that is. A second tap on the same row within the
// bounce is a wet finger's and does nothing.
func (s *shopState) toggle(d *Display, id string) {
	it, ok := itemNamed(id)
	if !ok {
		return
	}
	now := time.Now()
	s.mu.Lock()
	if s.deb == nil {
		s.deb = map[string]*mealsDebounce{}
	}
	if s.deb[id] == nil {
		s.deb[id] = &mealsDebounce{}
	}
	if !s.deb[id].ok(now) {
		s.mu.Unlock()
		return
	}
	checked := it.Checked
	if o := s.over[id]; o != nil {
		checked = o.checked
	}
	s.mu.Unlock()
	s.set(d, it, !checked, !checked)
}

// undoLast unchecks the item checked last.
func (s *shopState) undoLast(d *Display) {
	s.mu.Lock()
	it := s.undo
	s.undo, s.undoUntil = mealie.Item{}, time.Time{}
	s.mu.Unlock()
	if it.ID != "" {
		s.set(d, it, false, false)
	}
}

// set makes the item checked or not on the screen at once and tells the server in the background. offer
// is whether Undo is offered for it.
func (s *shopState) set(d *Display, it mealie.Item, checked, offer bool) {
	s.mu.Lock()
	if s.over == nil {
		s.over = map[string]*shopOver{}
	}
	o := &shopOver{checked: checked}
	s.over[it.ID] = o
	s.failUntil = time.Time{}
	if offer {
		s.undo, s.undoUntil = it, time.Now().Add(shopUndoFor)
		time.AfterFunc(shopUndoFor+50*time.Millisecond, d.wake)
	}
	s.mu.Unlock()

	meals.wg.Add(1)
	go func() {
		defer meals.wg.Done()
		s.wmu.Lock()
		defer s.wmu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), shopWriteMost)
		defer cancel()
		err := mealsClient().SetChecked(ctx, it, checked)
		s.mu.Lock()
		if err != nil {
			// The box goes back, and a few words say why.
			if s.over[it.ID] == o {
				delete(s.over, it.ID)
			}
			if s.undo.ID == it.ID {
				s.undo, s.undoUntil = mealie.Item{}, time.Time{}
			}
			s.failUntil = time.Now().Add(shopNoteFor)
			time.AfterFunc(shopNoteFor+50*time.Millisecond, d.wake)
		} else if s.over[it.ID] == o {
			o.done = true
		}
		s.mu.Unlock()
		if err == nil {
			// What the server has now, which may have merged the item with another.
			meals.reload("shopping")
		}
		d.wake()
	}()
}
