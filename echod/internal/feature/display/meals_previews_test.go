//go:build !dot && !spot

package display

import (
	"image"
	"image/draw"
	"os"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

// mealsPreview writes what the renderer drew, painted over the black the screen is behind the views, to
// SHOW_PREVIEW as name.
func mealsPreview(t *testing.T, r *renderer, name string) {
	t.Helper()
	out := image.NewRGBA(r.dst.Bounds())
	draw.Draw(out, out.Bounds(), image.Black, image.Point{}, draw.Src)
	draw.Draw(out, out.Bounds(), r.dst, r.dst.Bounds().Min, draw.Over)
	writePreview(t, out, name)
}

// With SHOW_PREVIEW set, the Meals page's Shopping and Plan views are written there on the Show 5 and the
// Show 8, from made-up lists and a made-up week.
func TestMealsPreviews(t *testing.T) {
	if os.Getenv("SHOW_PREVIEW") == "" {
		t.Skip("SHOW_PREVIEW is not set")
	}
	for _, panel := range []struct {
		name       string
		wide, high int
	}{{"show5", 960, 480}, {"show8", show8Wide, show8High}} {
		mealsSetUp(t)
		shopFresh(t)
		registerMealsView(mealsShoppingView())
		items := []map[string]any{
			shopJSON("a", "2 onions", "", false, "produce|Produce|", 0),
			shopJSON("a2", "500 g tomatoes", "", false, "produce|Produce|", 1),
			shopJSON("b", "butter", "", false, "dairy|Dairy|#2196f3", 0),
			shopJSON("b2", "1 l milk", "", false, "dairy|Dairy|#2196f3", 1),
			shopJSON("c", "tape", "", false, "", 0),
			shopJSON("d", "salt", "", true, "other|Baking|#ff0000", 0),
		}
		r := newRenderer(image.NewRGBA(image.Rect(0, 0, panel.wide, panel.high)))
		pane := mealsPane{Data: mealsShop{List: shopListOf(t, items...), Lists: []mealie.ListSummary{{ID: "L1"}, {ID: "L2"}}},
			Loaded: true, Fetched: time.Date(2026, 11, 23, 9, 30, 0, 0, time.UTC)}
		f := mealsFrame{view: mealsShoppingView(), tabs: []mealsTab{{id: "shopping", label: "Shopping", on: true}}, pane: pane}
		mealsDraw(r, scene{now: mealsPlanToday, phase: "idle", custom: customView{name: mealsID, data: f}})
		mealsPreview(t, r, "meals-shopping-"+panel.name+".png")

		mealsPlanSetUp(t)
		r, _ = mealsPlanDrawn(mealsPlanFixture(), panel.wide, panel.high)
		mealsPreview(t, r, "meals-plan-"+panel.name+".png")
	}
}
