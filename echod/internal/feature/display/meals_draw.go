//go:build !dot && !spot

package display

import (
	"errors"
	"fmt"
	"image"

	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

// What the Meals page draws that is shared between its views (meals.go): the switcher in the top bar,
// the messages for a page that is still loading or that cannot load, the note over data that could not be
// refreshed, the pill, and the layout class every view branches on. Every size is in the Show 5's
// pixels, as the rest of the package's are, and r.s scales it.

const (
	// mealsSegW is a switcher segment, and mealsRetryW the Retry pill.
	mealsSegW, mealsRetryW = 150, 200
	// mealsPillPad is the room kept at each side of a pill's words.
	mealsPillPad = 16
	// mealsTarget is the least height of anything a floury finger taps; mealsTargetMin of anything else.
	mealsTarget, mealsTargetMin = 64, 56
	// mealsNoteH is the note over data that could not be refreshed.
	mealsNoteH = 36
)

// mealsClass is how a page is laid out on the screen it is on. Every landscape screen is 960 units wide
// in the Show 5's pixels, so what differs is how tall it is in them.
type mealsClass int

const (
	mealsShort    mealsClass = iota // the Show 5: 960x480
	mealsTall                       // the Show 8 and the Skylight: 520 units or more
	mealsPortrait                   // a panel held upright
)

// mealsTallFrom is the height, in the Show 5's units, from which a landscape screen is tall.
const mealsTallFrom = 520

// mealsClassOf is the class of a w x h screen.
func mealsClassOf(w, h int) mealsClass {
	switch {
	case h > w:
		return mealsPortrait
	case h*drawnFor/w >= mealsTallFrom:
		return mealsTall
	}
	return mealsShort
}

// mealsClass is the class of the screen drawn on.
func (r *renderer) mealsClass() mealsClass { return mealsClassOf(r.w, r.h) }

// mealsUnitsHigh is how tall the screen is in the Show 5's units: what a view's rows are counted in.
func (r *renderer) mealsUnitsHigh() int { return r.h * drawnFor / r.w }

// mealsDraw draws the page: the common bar with the switcher in it, then the view on the screen, or what
// stands in for it while it has nothing to show.
func mealsDraw(r *renderer, s scene) []customZone {
	f, _ := s.custom.data.(mealsFrame)
	zones := r.customBar(s, "", false, false)
	zones = append(zones, r.mealsSwitcher(s, f)...)
	body := image.Rect(0, r.s(customBarH), r.w, r.h)
	if f.view.draw == nil {
		return zones
	}
	p := f.pane
	if !p.Loaded || p.Data == nil {
		return append(zones, r.mealsState(body, f)...)
	}
	if p.Err != nil {
		// The data from before stays, with a note over it.
		r.mealsNote(body, p)
		body.Min.Y += r.s(mealsNoteH)
	}
	return append(zones, f.view.draw(r, s, p, body)...)
}

// mealsBarRoom is the room in the top bar between the tabs and the time, where the switcher goes. It
// follows customBar's own layout.
func (r *renderer) mealsBarRoom(s scene) (x0, x1 int) {
	barH := r.s(customBarH)
	side := barH - r.s(16)
	closeX := r.w - r.s(customIn) - side
	timeX := closeX - r.s(18) - r.width(r.small, clockText(s.now))
	return r.s(customIn) + len(offeredScreens())*r.s(customTabW) + r.s(20), timeX - r.s(24)
}

// mealsSwitcher draws the segmented control that turns between the views, as the bar's tabs are drawn, and
// returns a zone for each segment.
func (r *renderer) mealsSwitcher(s scene, f mealsFrame) []customZone {
	n := len(f.tabs)
	if n == 0 {
		return nil
	}
	fc := r.faces()
	x0, x1 := r.mealsBarRoom(s)
	barH := r.s(customBarH)
	w := min(r.s(mealsSegW), (x1-x0)/n)
	seg := image.Rect(x0, r.s(8), x0+n*w, barH-r.s(8))
	segRad := float64(seg.Dy()) / 2
	r.roundFill(seg, segRad, surface(1), surface(1))
	r.roundStroke(seg, segRad, r.sf(1), ember)
	inset := r.s(4)
	var zones []customZone
	for i, t := range f.tabs {
		b := image.Rect(seg.Min.X+i*w+inset, seg.Min.Y+inset, seg.Min.X+(i+1)*w-inset, seg.Max.Y-inset)
		fg, face := lerp(dim, cream, 0.3), fc.nav
		if t.on {
			rad := float64(b.Dy()) / 2
			r.roundFill(b, rad, shift(amber, 14), shift(amber, -14))
			r.roundHighlight(b, rad)
			fg, face = onAccent(), fc.navBold
		}
		word := r.fit(face, t.label, w-2*r.s(mealsPillPad))
		r.text(face, word, b.Min.X+(b.Dx()-r.width(face, word))/2, b.Min.Y+b.Dy()/2+r.s(9), fg)
		zones = append(zones, customZone{r: image.Rect(seg.Min.X+i*w, 0, seg.Min.X+(i+1)*w, barH+r.s(6)), kind: "meals:view", id: t.id})
	}
	return zones
}

// mealsProblem is what to say about a fetch that failed: the icon, the sentence in the screen's language,
// and whether asking again can help (a refused token and the wrong address have to be put right on the
// setup page first). The sentence is never the error's own text.
func mealsProblem(err error) (icon, words string, retry bool) {
	switch {
	case errors.Is(err, mealie.ErrToken):
		return "key-alert-outline", tr("meals_token_refused"), false
	case errors.Is(err, mealie.ErrUseHTTPS):
		return "lock-alert-outline", tr("meals_use_https"), false
	}
	return "cloud-off-outline", tr("meals_unreachable"), true
}

// mealsState is the page of a view that has no data: it is loading, or it could not be.
func (r *renderer) mealsState(body image.Rectangle, f mealsFrame) []customZone {
	p := f.pane
	if p.Err == nil {
		return r.mealsMessage(body, "timer-sand", tr("meals_loading"), "")
	}
	icon, words, retry := mealsProblem(p.Err)
	if !retry {
		return r.mealsMessage(body, icon, words, "")
	}
	return r.mealsMessage(body, icon, words, f.view.id)
}

// mealsMessage draws an icon and a sentence in the middle of body, and under them a Retry pill for the
// view retry names, unless that is empty. It returns the pill's zone.
func (r *renderer) mealsMessage(body image.Rectangle, icon, msg, retry string) []customZone {
	fc := r.faces()
	lines := r.wrapLines(fc.label, msg, body.Dx()*4/5)
	side := r.s(64)
	line := r.s(36)
	total := side + r.s(16) + len(lines)*line
	if retry != "" {
		total += r.s(24) + r.s(mealsTarget)
	}
	y := body.Min.Y + max(r.s(8), (body.Dy()-total)/2)
	mid := body.Min.X + body.Dx()/2
	r.mdiIconIn(icon, image.Rect(mid-side/2, y, mid+side/2, y+side), 56, lerp(walnut, dim, 0.8))
	y += side + r.s(16)
	for _, l := range lines {
		y += line
		r.text(fc.label, l, mid-r.width(fc.label, l)/2, y-r.s(8), cream)
	}
	if retry == "" {
		return nil
	}
	y += r.s(24)
	w := r.s(mealsRetryW)
	b := image.Rect(mid-w/2, y, mid+w/2, y+r.s(mealsTarget))
	r.mealsPill(b, tr("meals_retry"), true)
	return []customZone{{r: b.Inset(-r.s(8)), kind: "meals:retry", id: retry}}
}

// mealsPill draws a pill with words in it, centered and cut to fit: lit in the accent for the one that is
// the page's main button.
func (r *renderer) mealsPill(b image.Rectangle, words string, lit bool) {
	fc := r.faces()
	rad := float64(b.Dy()) / 2
	fg := cream
	if lit {
		r.roundFill(b, rad, shift(amber, 14), shift(amber, -14))
		r.roundHighlight(b, rad)
		fg = onAccent()
	} else {
		r.roundFill(b, rad, surface(7), surface(5))
		r.roundHighlight(b, rad)
	}
	words = r.fit(fc.navBold, words, b.Dx()-2*r.s(mealsPillPad))
	r.text(fc.navBold, words, b.Min.X+(b.Dx()-r.width(fc.navBold, words))/2, b.Min.Y+b.Dy()/2+r.s(9), fg)
}

// mealsNote is the strip over data that could not be refreshed: since when the data is, or what is wrong
// that the setup page can put right.
func (r *renderer) mealsNote(body image.Rectangle, p mealsPane) {
	fc := r.faces()
	strip := image.Rect(body.Min.X, body.Min.Y, body.Max.X, body.Min.Y+r.s(mealsNoteH))
	r.fillRect(strip, surface(2))
	words := fmt.Sprintf(tr("meals_offline_as_of"), clockText(p.Fetched))
	if _, problem, retry := mealsProblem(p.Err); !retry {
		words = problem
	}
	words = r.fit(fc.value, words, strip.Dx()-2*r.s(customIn))
	r.text(fc.value, words, strip.Min.X+r.s(customIn), strip.Min.Y+r.s(mealsNoteH)/2+r.s(9), amber)
}
