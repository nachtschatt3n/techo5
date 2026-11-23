//go:build !dot && !spot

package display

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

// Fork only, never part of a pull request: made-up data meant to be looked at in an issue (a real recipe,
// real photos, a plausible shopping list), where the fixtures of the other previews are meant for tests.

//go:embed testdata/issue-photos/*.jpg
var issuePhotos embed.FS

// issueFetch is the fake Mealie's image route: the photo of the recipe whose id is in the address.
func issueFetch(_ context.Context, u string) ([]byte, int, error) {
	for _, slug := range []string{"tomato-soup", "lemon-chicken", "pancakes", "veg-curry", "pasta"} {
		if strings.Contains(u, "/id-"+slug+"/") {
			b, err := issuePhotos.ReadFile("testdata/issue-photos/" + slug + ".jpg")
			return b, http.StatusOK, err
		}
	}
	return nil, http.StatusNotFound, nil
}

func issueRecipe() mealie.Recipe {
	var r mealie.Recipe
	r.ID, r.Slug, r.Name, r.RecipeServings = "id-tomato-soup", "tomato-soup", "Tomato soup with basil", 4
	r.Image = "k-tomato-soup"
	r.Description = "A smooth, creamy soup of ripe tomatoes, finished with fresh basil."
	r.PrepTimeSeconds, r.PerformTimeSeconds, r.TotalTimeSeconds = 15*60, 30*60, 45*60
	ing := func(id, display string) mealie.Ingredient {
		return mealie.Ingredient{ReferenceID: id, Display: display}
	}
	r.Ingredients = []mealie.Ingredient{
		ing("oil", "2 tbsp olive oil"),
		ing("onion", "1 onion, chopped"),
		ing("garlic", "2 cloves garlic, minced"),
		ing("tomatoes", "800 g ripe tomatoes, quartered"),
		ing("stock", "500 ml vegetable stock"),
		ing("sugar", "1 tsp sugar"),
		ing("cream", "100 ml cream"),
		ing("basil", "a handful of fresh basil"),
		ing("salt", "salt and black pepper"),
	}
	step := func(text string, refs ...string) mealie.Step {
		s := mealie.Step{Text: text}
		for _, id := range refs {
			s.IngredientReferences = append(s.IngredientReferences, struct {
				ReferenceID string `json:"referenceId"`
			}{id})
		}
		return s
	}
	r.Steps = []mealie.Step{
		step("Heat the olive oil in a large pot over medium heat.", "oil"),
		step("Add the onion and garlic and cook for 5 minutes, until soft.", "onion", "garlic"),
		step("Stir in the tomatoes, stock and sugar, and bring to a boil.", "tomatoes", "stock", "sugar"),
		step("Simmer for 20 minutes, until the tomatoes have broken down.", "tomatoes"),
		step("Blend until smooth, then stir in the cream and season with salt and pepper.", "cream", "salt"),
		step("Serve in warm bowls with torn basil leaves.", "basil"),
	}
	return r
}

func issueShopping(t *testing.T) mealie.List {
	t.Helper()
	items := []map[string]any{
		shopJSON("a1", "6 ripe tomatoes", "", false, "produce|Produce|#4caf50", 0),
		shopJSON("a2", "2 lemons", "", false, "produce|Produce|#4caf50", 1),
		shopJSON("a3", "1 bunch fresh basil", "", true, "produce|Produce|#4caf50", 2),
		shopJSON("a4", "1 kg onions", "", false, "produce|Produce|#4caf50", 3),
		shopJSON("b1", "250 ml cream", "", false, "dairy|Dairy and eggs|#2196f3", 0),
		shopJSON("b2", "1 l milk", "", true, "dairy|Dairy and eggs|#2196f3", 1),
		shopJSON("b3", "6 eggs", "", false, "dairy|Dairy and eggs|#2196f3", 2),
		shopJSON("c1", "800 g chicken thighs", "", false, "meat|Meat and fish|#e53935", 0),
		shopJSON("c2", "1 smoked salmon fillet", "", false, "meat|Meat and fish|#e53935", 1),
		shopJSON("d1", "500 g basmati rice", "", false, "pantry|Pantry|#ff9800", 0),
		shopJSON("d2", "1 l vegetable stock", "", true, "pantry|Pantry|#ff9800", 1),
		shopJSON("d3", "olive oil", "", false, "pantry|Pantry|#ff9800", 2),
	}
	label := func(id, name, color string, pos int) map[string]any {
		return map[string]any{"labelId": id, "position": pos, "label": map[string]any{"id": id, "name": name, "color": color}}
	}
	doc := map[string]any{
		"id": "L1", "name": "Weekly", "listItems": items,
		"labelSettings": []any{
			label("produce", "Produce", "#4caf50", 0),
			label("dairy", "Dairy and eggs", "#2196f3", 1),
			label("meat", "Meat and fish", "#e53935", 2),
			label("pantry", "Pantry", "#ff9800", 3),
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

func issueEntries() []mealie.Entry {
	m := func(id int, day int, kind, name, slug string, mins int) mealie.Entry {
		e := mealsPlanMake(id, mealsPlanToday.AddDate(0, 0, day).Format(mealsPlanDate), kind, name, slug, 1)
		if e.Recipe != nil {
			e.Recipe.TotalTimeSeconds = mins * 60
		}
		return e
	}
	return []mealie.Entry{
		m(1, 0, "breakfast", "Blueberry pancakes", "pancakes", 25),
		m(2, 0, "lunch", "Tomato soup with basil", "tomato-soup", 45),
		m(3, 0, "dinner", "Lemon herb chicken with rice", "lemon-chicken", 50),
		m(4, 1, "lunch", "Spaghetti al pomodoro", "pasta", 30),
		m(5, 1, "dinner", "Red lentil curry", "veg-curry", 40),
		m(6, 2, "dinner", "Eating out", "", 0),
		m(7, 3, "lunch", "Tomato soup with basil", "tomato-soup", 45),
		m(8, 3, "dinner", "Lemon herb chicken with rice", "lemon-chicken", 50),
		m(9, 4, "breakfast", "Blueberry pancakes", "pancakes", 25),
		m(10, 4, "dinner", "Spaghetti al pomodoro", "pasta", 30),
		m(11, 5, "dinner", "Red lentil curry", "veg-curry", 40),
		m(12, 6, "lunch", "Tomato soup with basil", "tomato-soup", 45),
		m(13, 6, "dinner", "Lemon herb chicken with rice", "lemon-chicken", 50),
	}
}

// With SHOW_PREVIEW set, writes the Meals screens for an issue: Shopping, Plan, and the Cooking overview
// and a step with a timer, on the Show 5 and the Show 8.
func TestMealsIssueScreenshots(t *testing.T) {
	if os.Getenv("SHOW_PREVIEW") == "" {
		t.Skip("SHOW_PREVIEW is not set")
	}
	for _, panel := range []struct {
		name       string
		wide, high int
	}{{"show5", 960, 480}, {"show8", show8Wide, show8High}} {
		// Shopping.
		mealsSetUp(t)
		shopFresh(t)
		registerMealsView(mealsShoppingView())
		r := newRenderer(image.NewRGBA(image.Rect(0, 0, panel.wide, panel.high)))
		pane := mealsPane{Data: mealsShop{List: issueShopping(t), Lists: []mealie.ListSummary{{ID: "L1"}, {ID: "L2"}}},
			Loaded: true, Fetched: time.Date(2026, 11, 23, 9, 30, 0, 0, time.UTC)}
		f := mealsFrame{view: mealsShoppingView(), tabs: []mealsTab{{id: "shopping", label: "Shopping", on: true}}, pane: pane}
		mealsDraw(r, scene{now: mealsPlanToday, phase: "idle", custom: customView{name: mealsID, data: f}})
		mealsPreview(t, r, "meals-shopping-"+panel.name+".png")

		// Plan.
		mealsPlanSetUp(t)
		libFetch = issueFetch
		plan := mealsPlanBuild(mealie.Client{URL: "https://mealie.example.test"}, mealsPlanDayStart(mealsPlanToday), issueEntries())
		mealsPlanDrawn(plan, panel.wide, panel.high)
		mealsWaitFor(t, func() bool {
			mealsPlanThumbs.Lock()
			held := len(mealsPlanThumbs.held)
			mealsPlanThumbs.Unlock()
			libPics.mu.Lock()
			kept := len(libPics.kept)
			libPics.mu.Unlock()
			return held > 0 && kept >= held
		})
		r, _ = mealsPlanDrawn(plan, panel.wide, panel.high)
		mealsPreview(t, r, "meals-plan-"+panel.name+".png")

		// Cooking.
		d, _ := cookSetUp(t, panel.wide, panel.high)
		libFetch = issueFetch
		c, data := cookOpen(t, issueRecipe(), 1)
		for _, page := range []int{0, 4} {
			c.Go(page)
			draw.Draw(d.r.dst, d.r.dst.Bounds(), image.Transparent, image.Point{}, draw.Src)
			cookDrawn(d.r, data)
			if page == 0 {
				mealsWaitFor(t, func() bool {
					cookDrawn(d.r, data)
					libPics.mu.Lock()
					defer libPics.mu.Unlock()
					return len(libPics.kept) > 0
				})
				draw.Draw(d.r.dst, d.r.dst.Bounds(), image.Transparent, image.Point{}, draw.Src)
				cookDrawn(d.r, data)
			}
			name := map[int]string{0: "cooking-overview", 4: "cooking-step"}[page]
			mealsPreview(t, d.r, fmt.Sprintf("meals-%s-%s.png", name, panel.name))
		}
	}
}
