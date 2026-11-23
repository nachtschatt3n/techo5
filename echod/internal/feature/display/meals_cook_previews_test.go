//go:build !dot && !spot

package display

import (
	"fmt"
	"image"
	"image/draw"
	"os"
	"testing"
)

// With SHOW_PREVIEW set, the Meals page's Cooking view is written there on the Show 5 and the Show 8: the
// overview of a made-up recipe, and its third step.
func TestMealsCookPreviews(t *testing.T) {
	if os.Getenv("SHOW_PREVIEW") == "" {
		t.Skip("SHOW_PREVIEW is not set")
	}
	for _, panel := range []struct {
		name       string
		wide, high int
	}{{"show5", 960, 480}, {"show8", show8Wide, show8High}} {
		d, _ := cookSetUp(t, panel.wide, panel.high)
		recipe := cookRecipe()
		recipe.Steps[1].Text = "Add the tomatoes and simmer for 20 minutes."
		c, data := cookOpen(t, recipe, 1)
		for _, page := range []int{0, 2} {
			c.Go(page)
			draw.Draw(d.r.dst, d.r.dst.Bounds(), image.Transparent, image.Point{}, draw.Src)
			cookDrawn(d.r, data)
			mealsPreview(t, d.r, fmt.Sprintf("meals-cook-%d-%s.png", page, panel.name))
		}
	}
}
