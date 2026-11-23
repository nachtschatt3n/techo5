//go:build !dot && !spot

package display

import (
	"encoding/json"
	"fmt"
	"image"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/feature/voice"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/touch"
	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

func init() { mealsPillKeys["meals_undo"] = shopUndoW }

// shopJSON is a made-up item as the server sends it. label is "" for none, else "id|name|color".
func shopJSON(id, display, note string, checked bool, label string, pos int) map[string]any {
	it := map[string]any{
		"id": id, "shoppingListId": "L1", "checked": checked, "position": pos, "quantity": 1,
		"note": note, "display": display, "extras": map[string]any{"keep": "me"},
		"createdAt": fmt.Sprintf("2026-11-%02dT10:00:00", 1+pos%28),
	}
	if label != "" {
		p := strings.SplitN(label, "|", 3)
		var color any
		if p[2] != "" {
			color = p[2]
		}
		it["label"] = map[string]any{"id": p[0], "name": p[1], "color": color}
	}
	return it
}

// shopListOf is a list made of such items, as the client decodes it.
func shopListOf(t *testing.T, items ...map[string]any) mealie.List {
	t.Helper()
	doc := map[string]any{
		"id": "L1", "name": "Weekly", "listItems": items,
		"labelSettings": []any{
			map[string]any{"labelId": "dairy", "position": 1, "label": map[string]any{"id": "dairy", "name": "Dairy", "color": "#2196f3"}},
			map[string]any{"labelId": "produce", "position": 0, "label": map[string]any{"id": "produce", "name": "Produce", "color": nil}},
		},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var l mealie.List
	if err := json.Unmarshal(b, &l); err != nil {
		t.Fatal(err)
	}
	return l
}

// shopFake is a made-up Mealie with one shopping list (and, when more is set, others).
type shopFake struct {
	mu      sync.Mutex
	items   []map[string]any
	more    []string // names of further lists, which are empty
	puts    [][]map[string]any
	lists   atomic.Int32 // reads of the list
	failPut atomic.Bool
	gate    chan struct{} // when set, a write waits for it to close
}

// newShopFake starts the fake, points the page at it and puts everything back after the test.
func newShopFake(t *testing.T, items ...map[string]any) *shopFake {
	t.Helper()
	f := &shopFake{items: items}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		gate := f.gate
		f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/households/shopping/lists":
			out := []map[string]any{{"id": "L1", "name": "Weekly"}}
			f.mu.Lock()
			more := f.more
			f.mu.Unlock()
			for i, n := range more {
				out = append(out, map[string]any{"id": fmt.Sprintf("L%d", i+2), "name": n})
			}
			json.NewEncoder(w).Encode(map[string]any{"items": out})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/households/shopping/lists/"):
			f.lists.Add(1)
			id := strings.TrimPrefix(r.URL.Path, "/api/households/shopping/lists/")
			f.mu.Lock()
			defer f.mu.Unlock()
			items := f.items
			if id != "L1" {
				items = nil
			}
			json.NewEncoder(w).Encode(map[string]any{"id": id, "name": "List " + id, "listItems": items})
		case r.Method == http.MethodPut && r.URL.Path == "/api/households/shopping/items":
			if gate != nil {
				<-gate
			}
			var body []map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			if f.failPut.Load() {
				http.Error(w, "down", http.StatusInternalServerError)
				return
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			f.puts = append(f.puts, body)
			for _, b := range body {
				for i, it := range f.items {
					if it["id"] == b["id"] {
						f.items[i] = b
					}
				}
			}
			w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	was := mealsClient
	mealsClient = func() mealie.Client { return mealie.Client{URL: srv.URL, Token: "token-for-a-test"} }
	t.Cleanup(func() { mealsClient = was })
	return f
}

func (f *shopFake) putCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.puts)
}

// shopPage is a display with the page up on the Shopping view, over the fake.
func shopPage(t *testing.T, w, h int, items ...map[string]any) (*Display, *shopFake) {
	t.Helper()
	d := noticeDisplay(t, w, h)
	mealsFresh(t)
	was := mealsShopping
	mealsShopping = &shopState{}
	t.Cleanup(func() { meals.wg.Wait(); mealsShopping = was })
	registerMealsView(mealsShoppingView())
	f := newShopFake(t, items...)
	d.view = voice.State{Phase: "idle"}
	d.mu.Lock()
	d.customs.up, d.customs.shown = mealsID, mealsID
	d.mu.Unlock()
	// The first frame fetches, the second has what came.
	mealsFrameOf(t)
	meals.wg.Wait()
	return d, f
}

// shopDrawn draws the page as of now, and makes what it drew what a tap is aimed at.
func shopDrawn(t *testing.T, d *Display, now time.Time) []customZone {
	t.Helper()
	s := scene{now: now, phase: "idle", custom: customView{name: mealsID, data: mealsFrameOf(t)}}
	zones := mealsDraw(d.r, s)
	setCustomZones(zones)
	return zones
}

func zoneOf(zones []customZone, kind, id string) (customZone, bool) {
	for _, z := range zones {
		if z.kind == kind && (id == "" || z.id == id) {
			return z, true
		}
	}
	return customZone{}, false
}

func tapZone(t *testing.T, d *Display, zones []customZone, kind, id string) {
	t.Helper()
	z, ok := zoneOf(zones, kind, id)
	if !ok {
		t.Fatalf("no %s zone for %q in %v", kind, id, zones)
	}
	tapAt(d, z.r.Min.Add(image.Pt(z.r.Dx()/2, z.r.Dy()/2)))
}

// shopFresh gives the view a state of its own for a test, and puts the old one back after it.
func shopFresh(t *testing.T) {
	t.Helper()
	was := mealsShopping
	mealsShopping = &shopState{}
	t.Cleanup(func() { mealsShopping = was })
}

// viaJSON is a made-up item as it is after a trip through JSON, which is how the fake server keeps it.
func viaJSON(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func shopRowKinds(m shopModel) []int {
	var out []int
	for _, r := range m.rows {
		out = append(out, r.kind)
	}
	return out
}

func shopThree() []map[string]any {
	return []map[string]any{
		shopJSON("milk", "1 l milk", "", false, "", 0),
		shopJSON("bread", "bread", "", false, "", 1),
		shopJSON("eggs", "6 eggs", "", false, "", 2),
	}
}

func shopRowPrimary(z customZone) bool {
	return strings.HasPrefix(z.kind, "meals:shop:row") || z.kind == "meals:shop:done"
}

// The list at every size: its zones are on the canvas, apart and big enough; and with 30 items to buy
// the rows that are wholly in view are as many as the layout promises.
func TestMealsShoppingLayout(t *testing.T) {
	mealsSetUp(t)
	shopFresh(t)
	registerMealsView(mealsShoppingView())
	var thirty []map[string]any
	for i := range 30 {
		thirty = append(thirty, shopJSON(fmt.Sprintf("i%d", i), fmt.Sprintf("item number %d", i), "", false, "", i))
	}
	mixed := []map[string]any{
		shopJSON("a", "2 onions", "", false, "produce|Produce|", 0),
		shopJSON("b", "butter", "", false, "dairy|Dairy|#2196f3", 0),
		shopJSON("c", "tape", "", false, "", 0),
		shopJSON("d", "salt", "", true, "other|Baking|#ff0000", 0),
		shopJSON("e", "flour", "", true, "other|Baking|#ff0000", 1),
	}
	want := map[image.Point]int{{960, 480}: 10, {1920, 1080}: 12, {1280, 800}: 14, {800, 1280}: 20}
	for _, size := range mealsSizes {
		r := newRenderer(image.NewRGBA(image.Rectangle{Max: size}))
		draw := func(items []map[string]any, open bool, offline bool) []customZone {
			mealsShopping.mu.Lock()
			mealsShopping.doneOpen = open
			mealsShopping.scroll = 0
			mealsShopping.mu.Unlock()
			p := mealsPane{Data: mealsShop{List: shopListOf(t, items...), Lists: []mealie.ListSummary{{ID: "L1"}, {ID: "L2"}}}, Loaded: true, Fetched: time.Now()}
			if offline {
				p.Err = mealie.ErrUnreachable
			}
			f := mealsFrame{view: mealsShoppingView(), tabs: []mealsTab{{id: "shopping", label: "Shopping", on: true}}, pane: p}
			s := scene{now: time.Date(2026, 11, 23, 9, 30, 0, 0, time.UTC), phase: "idle", custom: customView{name: mealsID, data: f}}
			zones := mealsDraw(r, s)
			assertLayout(t, zones, size.X, size.Y, func(z customZone) bool { return shopRowPrimary(z) })
			return zones
		}
		n := 0
		for _, z := range draw(thirty, false, false) {
			if z.kind == "meals:shop:row" {
				n++
			}
		}
		if n < want[size] {
			t.Errorf("%v: %d rows in view, want at least %d", size, n, want[size])
		}
		mealsShopping.mu.Lock()
		more := mealsShopping.maxScroll > 0
		mealsShopping.mu.Unlock()
		if !more {
			t.Errorf("%v: 30 items fit with no scrolling", size)
		}
		draw(mixed, false, false)
		zones := draw(mixed, true, false)
		if _, ok := zoneOf(zones, "meals:shop:done", ""); !ok {
			t.Errorf("%v: no Done heading to tap", size)
		}
		if _, ok := zoneOf(zones, "meals:shop:list", ""); !ok {
			t.Errorf("%v: no list switcher with two lists", size)
		}
		draw(mixed, true, true)
	}
}

// The rows are in Mealie's order, the Done block is folded until opened, and a tap on its heading opens it.
func TestMealsShoppingGroupsAndDone(t *testing.T) {
	mealsSetUp(t)
	l := shopListOf(t,
		shopJSON("w", "", "gift wrap", false, "", 0),
		shopJSON("g", "glue", "", false, "zeta|Zeta|", 0),
		shopJSON("b", "butter", "", false, "dairy|Dairy|#2196f3", 0),
		shopJSON("o", "2 onions", "", false, "produce|Produce|", 0),
		shopJSON("p", "potatoes", "", false, "produce|Produce|", 1),
		shopJSON("s", "salt", "", true, "other|Baking|", 0),
		shopJSON("f", "flour", "", true, "other|Baking|", 1),
	)
	m := buildShop(mealsShop{List: l}, nil, false)
	var heads []string
	for _, r := range m.rows {
		if r.kind == shopHead {
			heads = append(heads, r.text)
		}
	}
	if !reflect.DeepEqual(heads, []string{"Produce", "Dairy", "Zeta"}) {
		t.Errorf("headings %v: the list's own label order, then the others", heads)
	}
	if m.rows[0].kind != shopItem || m.rows[0].text != "gift wrap" {
		t.Errorf("the items with no label come first and have no heading: %+v", m.rows[0])
	}
	if m.toBuy != 5 || m.name != "Weekly" {
		t.Errorf("%d to buy on %q", m.toBuy, m.name)
	}
	if last := m.rows[len(m.rows)-1]; last.kind != shopDoneHead || last.text != "Done (2)" {
		t.Errorf("the block ends with %+v, want the folded Done heading", last)
	}
	for _, r := range m.rows {
		if r.kind == shopHead && r.text == "Produce" && r.dot.R != 0x95 {
			t.Errorf("a label with no color is %v, want grey", r.dot)
		}
		if r.kind == shopHead && r.text == "Dairy" && (r.dot.R != 0x21 || r.dot.B != 0xf3) {
			t.Errorf("Dairy's dot is %v", r.dot)
		}
	}
	open := buildShop(mealsShop{List: l}, nil, true)
	if got := shopRowKinds(open); got[len(got)-1] != shopDoneItem || len(open.rows) != len(m.rows)+2 {
		t.Errorf("open Done block: %v", got)
	}

	// The same through a tap on the heading.
	d := noticeDisplay(t, 960, 480)
	mealsFresh(t)
	registerMealsView(mealsShoppingView())
	shopFresh(t)
	d.view = voice.State{Phase: "idle"}
	d.mu.Lock()
	d.customs.up, d.customs.shown = mealsID, mealsID
	d.mu.Unlock()
	p := mealsPane{Data: mealsShop{List: l}, Loaded: true}
	f := mealsFrame{view: mealsShoppingView(), tabs: []mealsTab{{id: "shopping", label: "Shopping", on: true}}, pane: p}
	tapZone(t, d, shopDrawnFor(d, f), "meals:shop:done", "")
	if !mealsShopping.isDoneOpen() {
		t.Error("a tap on the Done heading did not open the block")
	}
	tapZone(t, d, shopDrawnFor(d, f), "meals:shop:done", "")
	if mealsShopping.isDoneOpen() {
		t.Error("a second tap did not fold it")
	}
}

// shopDrawnFor draws a frame f and aims taps at it.
func shopDrawnFor(d *Display, f mealsFrame) []customZone {
	zones := mealsDraw(d.r, scene{now: time.Now(), phase: "idle", custom: customView{name: mealsID, data: f}})
	setCustomZones(zones)
	return zones
}

// A list with nothing on it says so, at every size, and one with only checked items says it over the
// Done heading.
func TestMealsShoppingEmpty(t *testing.T) {
	mealsSetUp(t)
	shopFresh(t)
	m := buildShop(mealsShop{}, nil, false)
	if !m.empty || m.toBuy != 0 || len(m.rows) != 0 || m.name != "Shopping list" {
		t.Errorf("an empty server: %+v", m)
	}
	allDone := buildShop(mealsShop{List: shopListOf(t, shopJSON("s", "salt", "", true, "", 0))}, nil, false)
	if allDone.empty || !reflect.DeepEqual(shopRowKinds(allDone), []int{shopNone, shopDoneHead}) {
		t.Errorf("only checked items: %+v", allDone)
	}
	for _, size := range mealsSizes {
		r := newRenderer(image.NewRGBA(image.Rectangle{Max: size}))
		for _, data := range []mealsShop{{}, {List: shopListOf(t)}, {List: shopListOf(t, shopJSON("s", "salt", "", true, "", 0))}} {
			p := mealsPane{Data: data, Loaded: true, Fetched: time.Now()}
			f := mealsFrame{view: mealsShoppingView(), tabs: []mealsTab{{id: "shopping", label: "Shopping", on: true}}, pane: p}
			s := scene{now: time.Now(), phase: "idle", custom: customView{name: mealsID, data: f}}
			assertLayout(t, mealsDraw(r, s), size.X, size.Y, shopRowPrimary)
		}
	}
}

// An item with only a note shows the note, one with neither is left out, and the server's fractions
// are made plain.
func TestMealsShoppingNoteOnly(t *testing.T) {
	mealsSetUp(t)
	l := shopListOf(t,
		shopJSON("a", "", "gift wrap", false, "", 0),
		shopJSON("b", "", "", false, "", 1),
		shopJSON("c", "1 ¹/₂ kg potatoes", "", false, "", 2),
	)
	m := buildShop(mealsShop{List: l}, nil, false)
	var got []string
	for _, r := range m.rows {
		got = append(got, r.text)
	}
	if !reflect.DeepEqual(got, []string{"gift wrap", "1 1/2 kg potatoes"}) || m.toBuy != 2 {
		t.Errorf("rows %q, %d to buy", got, m.toBuy)
	}
}

// A tap on a row flips its box at once, before the server has answered, and then the server is told
// the item as it was fetched with only the check changed, and the list is read again.
func TestMealsTapChecksOptimistically(t *testing.T) {
	d, f := shopPage(t, 960, 480, shopThree()...)
	f.mu.Lock()
	f.gate = make(chan struct{})
	f.mu.Unlock()
	zones := shopDrawn(t, d, time.Now())
	if _, ok := zoneOf(zones, "meals:shop:row", "milk"); !ok {
		t.Fatalf("no row for the milk: %v", zones)
	}
	before := f.lists.Load()
	tapZone(t, d, zones, "meals:shop:row", "milk")

	mealsShopping.mu.Lock()
	o := mealsShopping.over["milk"]
	mealsShopping.mu.Unlock()
	if o == nil || !o.checked {
		t.Fatal("the box did not flip before the server answered")
	}
	if n := f.putCount(); n != 0 {
		t.Fatalf("%d writes before the gate opened", n)
	}
	zones = shopDrawn(t, d, time.Now())
	if _, ok := zoneOf(zones, "meals:shop:row", "milk"); ok {
		t.Error("the checked item is still among those to buy")
	}
	if _, ok := zoneOf(zones, "meals:shop:done", ""); !ok {
		t.Error("the checked item did not go to the Done block")
	}

	close(f.gate)
	meals.wg.Wait()
	f.mu.Lock()
	puts := f.puts
	f.mu.Unlock()
	if len(puts) != 1 || len(puts[0]) != 1 {
		t.Fatalf("writes: %v", puts)
	}
	want := viaJSON(t, shopThree()[0])
	want["checked"] = true
	if !reflect.DeepEqual(puts[0][0], want) {
		t.Errorf("the item sent is %v, want %v", puts[0][0], want)
	}
	if f.lists.Load() <= before {
		t.Error("the list was not read again after the write")
	}
	mealsShopping.mu.Lock()
	left := len(mealsShopping.over)
	mealsShopping.mu.Unlock()
	if left != 0 {
		t.Errorf("%d checks still laid over a list that shows them", left)
	}

	// A tap that lights a dark screen is not a tap on the row.
	zones = shopDrawn(t, d, time.Now())
	d.mu.Lock()
	d.on = false
	d.mu.Unlock()
	tapZone(t, d, zones, "meals:shop:row", "bread")
	meals.wg.Wait()
	if n := f.putCount(); n != 1 {
		t.Fatalf("the tap that lit the screen wrote: %d writes", n)
	}
	d.mu.Lock()
	on := d.on
	d.mu.Unlock()
	if !on {
		t.Error("the screen was not lit")
	}
}

// A write that fails puts the box back and says so for a few seconds, and then says nothing.
func TestMealsCheckFailureReverts(t *testing.T) {
	d, f := shopPage(t, 960, 480, shopThree()...)
	f.failPut.Store(true)
	zones := shopDrawn(t, d, time.Now())
	tapZone(t, d, zones, "meals:shop:row", "milk")
	meals.wg.Wait()
	mealsShopping.mu.Lock()
	left, until := len(mealsShopping.over), mealsShopping.failUntil
	mealsShopping.mu.Unlock()
	if left != 0 {
		t.Fatalf("the box is still flipped: %d checks laid over", left)
	}
	if !time.Now().Before(until) || until.After(time.Now().Add(shopNoteFor+time.Second)) {
		t.Errorf("the note is up until %v", until)
	}
	zones = shopDrawn(t, d, time.Now())
	if _, ok := zoneOf(zones, "meals:shop:row", "milk"); !ok {
		t.Error("the item is not back among those to buy")
	}
	if _, ok := zoneOf(zones, "meals:shop:undo", ""); ok {
		t.Error("Undo is offered for a check that never happened")
	}
	if n := f.putCount(); n != 0 {
		t.Errorf("%d writes went through", n)
	}
	// Drawn at both times, with and without the note.
	shopDrawn(t, d, time.Now().Add(shopNoteFor+time.Second))
}

// After a check Undo is offered for a few seconds; it unchecks the item, and is gone afterward.
func TestMealsCheckUndo(t *testing.T) {
	d, f := shopPage(t, 960, 480, shopThree()...)
	zones := shopDrawn(t, d, time.Now())
	if _, ok := zoneOf(zones, "meals:shop:undo", ""); ok {
		t.Fatal("Undo before anything was checked")
	}
	tapZone(t, d, zones, "meals:shop:row", "milk")
	meals.wg.Wait()
	zones = shopDrawn(t, d, time.Now())
	if _, ok := zoneOf(zones, "meals:shop:undo", ""); !ok {
		t.Fatal("no Undo after a check")
	}
	if _, ok := zoneOf(shopDrawn(t, d, time.Now().Add(shopUndoFor+time.Second)), "meals:shop:undo", ""); ok {
		t.Error("Undo is still offered after its time")
	}
	zones = shopDrawn(t, d, time.Now())
	tapZone(t, d, zones, "meals:shop:undo", "")
	meals.wg.Wait()
	f.mu.Lock()
	puts := f.puts
	checked := f.items[0]["checked"]
	f.mu.Unlock()
	if len(puts) != 2 || puts[1][0]["id"] != "milk" || puts[1][0]["checked"] != false || checked != false {
		t.Errorf("writes %v, milk checked %v", puts, checked)
	}
	zones = shopDrawn(t, d, time.Now())
	if _, ok := zoneOf(zones, "meals:shop:undo", ""); ok {
		t.Error("Undo is offered again after it was used")
	}
	if _, ok := zoneOf(zones, "meals:shop:row", "milk"); !ok {
		t.Error("the item is not back among those to buy")
	}
}

// Two taps on a row within 250 ms are one; a tap after that is a tap.
func TestMealsCheckDebounce(t *testing.T) {
	d, f := shopPage(t, 960, 480, shopThree()...)
	zones := shopDrawn(t, d, time.Now())
	tapZone(t, d, zones, "meals:shop:row", "milk")
	tapZone(t, d, zones, "meals:shop:row", "milk")
	meals.wg.Wait()
	if n := f.putCount(); n != 1 {
		t.Fatalf("a wet finger's two taps made %d writes", n)
	}
	time.Sleep(mealsBounce + 20*time.Millisecond)
	tapZone(t, d, zones, "meals:shop:row", "milk")
	meals.wg.Wait()
	f.mu.Lock()
	n, last := len(f.puts), f.puts[len(f.puts)-1][0]["checked"]
	f.mu.Unlock()
	if n != 2 || last != false {
		t.Errorf("a later tap made %d writes, the last with checked %v", n, last)
	}
	// A different row is not held up by the first's.
	tapZone(t, d, zones, "meals:shop:row", "bread")
	meals.wg.Wait()
	if n := f.putCount(); n != 3 {
		t.Errorf("another row's tap made %d writes in all", n)
	}
}

// Nothing reads the list while the page is not on the screen, and it is read when it is.
func TestMealsNoPollWhenClosed(t *testing.T) {
	d := noticeDisplay(t, 960, 480)
	mealsFresh(t)
	was := mealsShopping
	mealsShopping = &shopState{}
	t.Cleanup(func() { meals.wg.Wait(); mealsShopping = was })
	registerMealsView(mealsShoppingView())
	f := newShopFake(t, shopThree()...)
	if got := mealsViews[0].every; got != 30*time.Second {
		t.Errorf("the list is read again every %v, want 30s", got)
	}
	d.view = voice.State{Phase: "idle"}
	for range 3 {
		s := scene{now: time.Now(), phase: "idle"}
		d.customScene(&s, s.now)
		time.Sleep(30 * time.Millisecond)
	}
	meals.wg.Wait()
	if n := f.lists.Load(); n != 0 {
		t.Fatalf("%d reads with the page closed", n)
	}
	d.mu.Lock()
	d.customs.up, d.customs.touched = mealsID, time.Now()
	d.mu.Unlock()
	s := scene{now: time.Now(), phase: "idle"}
	d.customScene(&s, s.now)
	meals.wg.Wait()
	if n := f.lists.Load(); n != 1 {
		t.Fatalf("%d reads on the first frame of the open page, want 1", n)
	}
}

// The list switcher turns to the next list, and the one the setup page names is the one shown first.
func TestMealsShoppingListSwitcher(t *testing.T) {
	d, f := shopPage(t, 960, 480, shopThree()...)
	f.mu.Lock()
	f.more = []string{"Party"}
	f.mu.Unlock()
	meals.reload("shopping")
	meals.wg.Wait()
	zones := shopDrawn(t, d, time.Now())
	tapZone(t, d, zones, "meals:shop:list", "")
	meals.wg.Wait()
	if data, _ := meals.paneData("shopping").(mealsShop); data.List.ID != "L2" {
		t.Fatalf("after a tap the list is %q, want L2", data.List.ID)
	}
	tapZone(t, d, shopDrawn(t, d, time.Now()), "meals:shop:list", "")
	meals.wg.Wait()
	if data, _ := meals.paneData("shopping").(mealsShop); data.List.ID != "L1" {
		t.Fatalf("after another tap the list is %q, want L1", data.List.ID)
	}
	if got := config.Get().Mealie.List; got != "" {
		t.Errorf("the switcher changed the setting to %q", got)
	}
}

// A swipe scrolls the list by a notch, not past either end.
func TestMealsShoppingScrolls(t *testing.T) {
	d := noticeDisplay(t, 960, 480)
	mealsFresh(t)
	registerMealsView(mealsShoppingView())
	was := mealsShopping
	mealsShopping = &shopState{}
	t.Cleanup(func() { mealsShopping = was })
	d.view = voice.State{Phase: "idle"}
	var items []map[string]any
	for i := range 30 {
		items = append(items, shopJSON(fmt.Sprintf("i%d", i), fmt.Sprintf("item %d", i), "", false, "", i))
	}
	f := mealsFrame{view: mealsShoppingView(), tabs: []mealsTab{{id: "shopping", label: "Shopping", on: true}},
		pane: mealsPane{Data: mealsShop{List: shopListOf(t, items...)}, Loaded: true}}
	shopDrawnFor(d, f)
	mealsShopping.mu.Lock()
	most := mealsShopping.maxScroll
	mealsShopping.mu.Unlock()
	if most <= 0 {
		t.Fatal("nothing to scroll")
	}
	shopGesture(d, touch.Gesture{Kind: touch.SwipeUp}, image.Pt(480, 300))
	if got := mealsShopping.scroll; got != d.r.s(notchPx) {
		t.Errorf("one swipe notch scrolled %d, want %d", got, d.r.s(notchPx))
	}
	for range 100 {
		shopGesture(d, touch.Gesture{Kind: touch.SwipeUp}, image.Pt(480, 300))
	}
	if mealsShopping.scroll != most {
		t.Errorf("scrolled to %d, want the end %d", mealsShopping.scroll, most)
	}
	for range 100 {
		shopGesture(d, touch.Gesture{Kind: touch.SwipeDown}, image.Pt(480, 300))
	}
	if mealsShopping.scroll != 0 {
		t.Errorf("scrolled back to %d, want 0", mealsShopping.scroll)
	}
}
