//go:build !dot && !spot

package display

import (
	"image"
	"testing"

	"github.com/HuskerMinion/techo5/echod/internal/lib/locale"
)

// mealsSizes are the canvases every view is laid out and tested at: the Show 5, the Show 8, the Skylight
// and a panel held upright.
var mealsSizes = []image.Point{{960, 480}, {1280, 800}, {1920, 1080}, {800, 1280}}

// completeLangs are the screen languages translated in full (lib/locale), the ones a label has to fit in.
var completeLangs = []string{"ca", "de", "es", "fr", "gl", "it", "ja", "ro", "tr"}

// assertLayout is the rule every Meals view's zones keep, at the size w x h: each is on the canvas (a zone grown past an edge may run off it by 12 units), no
// two overlap, none is under the least a finger can hit (56 units, and 64 for a primary one, the ones a
// wet or floury finger works: primary says which, nil for none).
func assertLayout(t *testing.T, zones []customZone, w, h int, primary func(customZone) bool) {
	t.Helper()
	s := func(n int) int { return (n*w + 480) / 960 }
	canvas := image.Rect(0, 0, w, h)
	for i, z := range zones {
		least := s(mealsTargetMin)
		if primary != nil && primary(z) {
			least = s(mealsTarget)
		}
		switch {
		case z.r.Empty() || !z.r.In(canvas.Inset(-s(12))): // a grown zone may run a little off the edge
			t.Errorf("%dx%d: zone %s %q at %v is not on the canvas", w, h, z.kind, z.id, z.r)
		case z.r.Dx() < least || z.r.Dy() < least:
			t.Errorf("%dx%d: zone %s %q is %dx%d, under %d", w, h, z.kind, z.id, z.r.Dx(), z.r.Dy(), least)
		}
		for _, o := range zones[i+1:] {
			if z.r.Overlaps(o.r) {
				t.Errorf("%dx%d: zones %s %q %v and %s %q %v overlap", w, h, z.kind, z.id, z.r, o.kind, o.id, o.r)
			}
		}
	}
}

// mealsPillKeys are the words that go on a pill or in a segment, and the pill's width in units: each
// view's file adds its own from its test's init. The longest translation of each has to fit without being
// cut.
var mealsPillKeys = map[string]int{
	"meals_shopping": mealsSegW,
	"meals_plan":     mealsSegW,
	"meals_cooking":  mealsSegW,
	"meals_retry":    mealsRetryW,
}

// No pill's words are cut with "…" in any complete catalog, on the Show 5, where the room is least.
func TestMealsLabelsFit(t *testing.T) {
	r := newRenderer(image.NewRGBA(image.Rect(0, 0, 960, 480)))
	fc := r.faces()
	for key, wide := range mealsPillKeys {
		for _, lang := range append([]string{""}, completeLangs...) {
			words := locale.Text(lang, key)
			if words == key || words == "" {
				t.Errorf("%q: %s has no words", lang, key)
				continue
			}
			// The bold face is the wider, and is the one a lit pill is drawn in.
			if room := r.s(wide) - 2*r.s(mealsPillPad); r.width(fc.navBold, words) > room {
				t.Errorf("%q: %s %q is %d wide, room %d", lang, key, words, r.width(fc.navBold, words), room)
			}
		}
	}
}

// Every screen has a class, and the same one on the Show 5's two panels as the layouts assume.
func TestMealsLayoutClass(t *testing.T) {
	for _, c := range []struct {
		w, h  int
		class mealsClass
		units int
	}{
		{960, 480, mealsShort, 480},
		{1280, 800, mealsTall, 600},
		{1920, 1080, mealsTall, 540},
		{800, 1280, mealsPortrait, 1536},
		{1024, 600, mealsTall, 562}, // a panel held sideways
		{1280, 600, mealsShort, 450},
	} {
		if got := mealsClassOf(c.w, c.h); got != c.class {
			t.Errorf("%dx%d is class %d, want %d", c.w, c.h, got, c.class)
		}
		r := newRenderer(image.NewRGBA(image.Rect(0, 0, c.w, c.h)))
		if r.mealsClass() != c.class || r.mealsUnitsHigh() != c.units {
			t.Errorf("%dx%d: renderer says class %d, %d units high", c.w, c.h, r.mealsClass(), r.mealsUnitsHigh())
		}
	}
}
