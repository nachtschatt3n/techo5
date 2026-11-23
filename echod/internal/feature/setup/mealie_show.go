//go:build !dot && !spot

package setup

import (
	"context"
	"fmt"
	"html"
	"net/http"

	"github.com/HuskerMinion/techo5/echod/internal/config"
)

// hasMeals: the Meals page is the Show's alone (the Spot has no page for it), so a Dot and a Spot draw
// no card and refuse a form posted for one.
const hasMeals = true

// mealieSection is the Mealie server the screen's Meals page reads: its shopping list, the meal plan
// and the recipes, to cook from. The token is never shown. Only the checkmark of a shopping item is ever
// written to Mealie.
func mealieSection(ctx context.Context, w http.ResponseWriter, token string) {
	m := config.Get().Mealie
	sectionOpen(w, "mealie", "Meals (Mealie)", mealieSummary(m)...)
	fmt.Fprint(w, `<form method="post" action="/setup/save">`)
	hidden(w, token, "mealie")
	var a mealieAnswer
	if m.URL != "" {
		// Asked through a lookup (lookup.go), so a Mealie that is slow or down does not hold the tab up
		// on every draw. A token is not needed to say whether the server is there.
		wait, cancel := context.WithTimeout(waitFor(ctx, w), mealieWait)
		defer cancel()
		got, ok := mealieLookup.get(wait, lookupKey(m.URL, m.Token), func(ctx context.Context) mealieAnswer { return askMealie(ctx, m) })
		if a = got; !ok {
			a = mealieAnswer{status: "still being asked; reload the page in a moment for its answer"}
		}
		fmt.Fprintf(w, `<p style="margin-top:0"><b>Now:</b> %s</p>`, html.EscapeString(a.status))
	}
	fmt.Fprintf(w, `<p class="note" style="margin-top:0">Shows your Mealie shopping list, this week's meal plan and the
	  recipes, to cook from, on a Meals page of this screen. Only the tick of a shopping item is ever written to
	  Mealie. In Mealie: your profile, <strong>API Tokens</strong>, a new token. A token is one person's, and so
	  one household's.</p>
	 <label for="mealieurl">Address</label>
	 <input id="mealieurl" name="url" type="url" inputmode="url" value="%s" placeholder="https://mealie.example.com" autocomplete="off">
	 %s`,
		html.EscapeString(m.URL),
		secretField("mealietoken", "API token", "mealietoken", m.Token != "", "notoken"))
	if m.Set() {
		mealieListField(w, m, a)
	}
	fmt.Fprintf(w, `<input type="hidden" name="mealiesleeps" value="1">
	 <p><label><input type="checkbox" name="awake" value="yes" style="width:auto"%s> Keep the screen on while cooking</label></p>
	 <p class="note">While a recipe is open the screen stays lit, for up to three hours, so it can be read with
	 floury hands. Untick it to let the screen dim and go dark as it does anywhere else.</p>`, checkedIf(!m.Sleeps))
	fmt.Fprint(w, `<p class="note">Troubleshooting: "refused the token" means the token was not accepted, or was made for
	 another Mealie user; make a new one and save it here. "Use the https address" means the server sent this
	 address to https: put that address in. Lists and plans are those of the token's household; to see another
	 household's, make the token in it.</p>
	 <p><button type="submit">Save</button></p></form>`)
	sectionClose(w)
}
