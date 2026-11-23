//go:build !dot && !spot

package display

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

func prevSave(t *testing.T, name string, r *renderer) {
	f, err := os.Create(filepath.Join(os.Getenv("SHOW_PREVIEW"), name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, r.dst); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewMealsTmp(t *testing.T) {
	if os.Getenv("SHOW_PREVIEW") == "" {
		t.Skip()
	}
	sizes := map[string]image.Point{"show5": {960, 480}, "show8": {1280, 800}}
	for tag, size := range sizes {
		// shopping
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
		r := newRenderer(image.NewRGBA(image.Rectangle{Max: size}))
		p := mealsPane{Data: mealsShop{List: shopListOf(t, items...), Lists: []mealie.ListSummary{{ID: "L1"}, {ID: "L2"}}}, Loaded: true, Fetched: time.Now()}
		f := mealsFrame{view: mealsShoppingView(), tabs: []mealsTab{{id: "shopping", label: "Shopping", on: true}}, pane: p}
		mealsDraw(r, scene{now: time.Date(2026, 11, 23, 9, 30, 0, 0, time.UTC), phase: "idle", custom: customView{name: mealsID, data: f}})
		prevSave(t, "meals-shopping-"+tag, r)
		// plan
		mealsPlanSetUp(t)
		r2, _ := mealsPlanDrawn(mealsPlanFixture(), size.X, size.Y)
		prevSave(t, "meals-plan-"+tag, r2)
		// cooking
		d, _ := cookSetUp(t, size.X, size.Y)
		c, data := cookOpen(t, cookRecipe(), 1)
		for _, page := range []int{0, 2} {
			c.Go(page)
			cookDrawn(d.r, data)
			prevSave(t, fmt.Sprintf("meals-cook-%d-%s", page, tag), d.r)
		}
	}
}
