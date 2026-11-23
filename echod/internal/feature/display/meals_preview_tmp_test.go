//go:build !dot && !spot

package display

import (
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

func prevSave(t *testing.T, name string, r *renderer) {
	// The screen is black behind the views, so a preview is painted over black.
	out := image.NewRGBA(r.dst.Bounds())
	draw.Draw(out, out.Bounds(), image.Black, image.Point{}, draw.Src)
	draw.Draw(out, out.Bounds(), r.dst, r.dst.Bounds().Min, draw.Over)
	f, err := os.Create(filepath.Join(os.Getenv("SHOW_PREVIEW"), name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, out); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewMealsTmp(t *testing.T) {
	if os.Getenv("SHOW_PREVIEW") == "" {
		t.Skip()
	}
	sizes := map[string]image.Point{"show5": {960, 480}, "show8": {1280, 800}}
	// One moment for the clock and for when the data arrived.
	at := time.Date(2026, 11, 23, 9, 30, 0, 0, time.UTC)
	for tag, size := range sizes {
		// shopping, with every view the switcher offers while no recipe is open
		mealsSetUp(t)
		shopFresh(t)
		registerMealsView(mealsShoppingView())
		registerMealsView(mealsPlanView())
		items := []map[string]any{
			shopJSON("a", "2 onions", "", false, "produce|Produce|", 0),
			shopJSON("a2", "500 g tomatoes", "", false, "produce|Produce|", 1),
			shopJSON("b", "butter", "", false, "dairy|Dairy|#2196f3", 0),
			shopJSON("b2", "1 l milk", "", false, "dairy|Dairy|#2196f3", 1),
			shopJSON("c", "tape", "", false, "", 0),
			shopJSON("d", "salt", "", true, "other|Baking|#ff0000", 0),
		}
		r := newRenderer(image.NewRGBA(image.Rectangle{Max: size}))
		p := mealsPane{Data: mealsShop{List: shopListOf(t, items...), Lists: []mealie.ListSummary{{ID: "L1"}, {ID: "L2"}}}, Loaded: true, Fetched: at}
		f := mealsFrame{view: mealsShoppingView(), tabs: []mealsTab{{id: "shopping", label: "Shopping", on: true}, {id: "plan", label: "Plan"}}, pane: p}
		mealsDraw(r, scene{now: at, phase: "idle", custom: customView{name: mealsID, data: f}})
		prevSave(t, "meals-shopping-"+tag, r)
		// plan: drawn until every picture it asked for has come and is kept
		mealsPlanSetUp(t)
		var r2 *renderer
		for range 3 {
			r2, _ = mealsPlanDrawn(mealsPlanFixture(), size.X, size.Y)
			mealsWaitFor(t, func() bool {
				libPics.mu.Lock()
				defer libPics.mu.Unlock()
				return len(libPics.asked) == 0
			})
		}
		r2, _ = mealsPlanDrawn(mealsPlanFixture(), size.X, size.Y)
		prevSave(t, "meals-plan-"+tag, r2)
		// cooking
		d, _ := cookSetUp(t, size.X, size.Y)
		c, data := cookOpen(t, cookRecipe(), 1)
		for _, page := range []int{0, 2} {
			c.Go(page)
			draw.Draw(d.r.dst, d.r.dst.Bounds(), image.Transparent, image.Point{}, draw.Src)
			cookDrawn(d.r, data)
			prevSave(t, fmt.Sprintf("meals-cook-%d-%s", page, tag), d.r)
		}
	}
}
