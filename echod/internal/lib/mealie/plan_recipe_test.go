package mealie

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"
)

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func planClient(t *testing.T, q *url.Values) Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/households/mealplans" {
			http.NotFound(w, r)
			return
		}
		*q = r.URL.Query()
		w.Write(fixture(t, "mealplans.json"))
	}))
	t.Cleanup(srv.Close)
	return Client{URL: srv.URL, Token: "t"}
}

func TestPlanQuery(t *testing.T) {
	var q url.Values
	c := planClient(t, &q)
	entries, err := c.Plan(context.Background(), mustDate(t, "2026-11-23"), mustDate(t, "2026-11-23").AddDate(0, 0, 6), true)
	if err != nil || len(entries) != 4 {
		t.Fatalf("Plan = %d, %v", len(entries), err)
	}
	want := url.Values{
		"start_date": {"2026-11-23"}, "end_date": {"2026-11-29"}, "include_week_entries": {"true"},
		"perPage": {"-1"}, "orderBy": {"date"}, "orderDirection": {"asc"},
	}
	if !reflect.DeepEqual(q, want) {
		t.Errorf("query = %v, want %v", q, want)
	}
	c.Plan(context.Background(), mustDate(t, "2026-11-23"), mustDate(t, "2026-11-24"), false)
	if q.Has("include_week_entries") {
		t.Errorf("without weeks the flag is not sent: %v", q)
	}
	// The entries read as they were planned.
	if e := entries[0]; e.Name() != "Pudding" || !e.Cookable() || e.StartScale() != 1.5 || e.Week() {
		t.Errorf("pudding: %+v", e)
	}
	if e := entries[1]; e.StartScale() != 1 || e.Recipe.Image != "" || e.Recipe.TotalTimeSeconds != 2700 {
		t.Errorf("lasagne: %+v", e)
	}
	if e := entries[2]; e.Name() != "Out for brunch" || e.Cookable() || e.Text != "table at 10" {
		t.Errorf("note: %+v", e)
	}
	if e := entries[3]; !e.Week() || e.Name() != "Leftovers" {
		t.Errorf("week entry: %+v", e)
	}
}

func TestPlanSortsMealTypes(t *testing.T) {
	var q url.Values
	entries, _ := planClient(t, &q).Plan(context.Background(), mustDate(t, "2026-11-23"), mustDate(t, "2026-11-23"), false)
	entries = append(entries, Entry{ID: 11, Type: "snack"}, Entry{ID: 12, Type: "mystery"}, Entry{ID: 13, Type: "lunch"})
	SortDay(entries)
	var got []string
	for _, e := range entries {
		got = append(got, e.Type)
	}
	want := []string{"breakfast", "lunch", "dinner", "dinner", "snack", "dessert", "mystery"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func recipeFixture(t *testing.T) Recipe {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/recipes/lasagne" {
			http.NotFound(w, r)
			return
		}
		w.Write(fixture(t, "recipe.json"))
	}))
	defer srv.Close()
	r, err := Client{URL: srv.URL, Token: "t"}.Recipe(context.Background(), "lasagne")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRecipeDecodesV330(t *testing.T) {
	r := recipeFixture(t)
	if r.Name != "Lasagne" || r.ID != "r2" || r.Image != "k3y" || r.TotalTimeSeconds != 2700 || r.PrepTimeSeconds != 900 || r.PerformTimeSeconds != 0 {
		t.Errorf("head: %+v", r.RecipeSummary)
	}
	if len(r.Ingredients) != 3 || len(r.Steps) != 4 {
		t.Fatalf("%d ingredients, %d steps", len(r.Ingredients), len(r.Steps))
	}
	i := r.Ingredients[0]
	if i.Title != "Sauce" || i.Quantity != 500 || i.Unit == nil || !i.Unit.UseAbbreviation || i.Unit.Fraction || i.Food.Name != "minced beef" {
		t.Errorf("ingredient: %+v", i)
	}
	if r.Ingredients[2].Quantity != 0 || r.Ingredients[2].Food != nil {
		t.Errorf("free text: %+v", r.Ingredients[2])
	}
	if r.Steps[0].DurationSeconds != 0 || r.Steps[1].DurationSeconds != 600 {
		t.Errorf("durations: %d %d", r.Steps[0].DurationSeconds, r.Steps[1].DurationSeconds)
	}
	if _, err := (Client{URL: "http://127.0.0.1:1"}).Recipe(context.Background(), "x"); err == nil {
		t.Error("no server, no recipe")
	}
}

// A step may have only a section title or only a summary; those are what it shows.
func TestRecipeStepTitleOrSummaryOnly(t *testing.T) {
	r := recipeFixture(t)
	for i, want := range []string{"Fry the onion for 10 minutes.", "Rest", "Bake", ""} {
		if got := r.Steps[i].Body(); got != want {
			t.Errorf("step %d body = %q, want %q", i, got, want)
		}
	}
}

func TestRecipeMissingReferenceIgnored(t *testing.T) {
	r := recipeFixture(t)
	// Step 1 refers to the onion (index 1) and to an ingredient the recipe no longer has.
	if got := r.Uses(r.Steps[0]); !reflect.DeepEqual(got, []int{1}) {
		t.Errorf("uses = %v", got)
	}
	if got := r.Uses(r.Steps[1]); len(got) != 0 {
		t.Errorf("uses = %v", got)
	}
}

func TestServingsBase(t *testing.T) {
	for _, tc := range []struct {
		servings, yield, want float64
	}{{4, 6, 4}, {0, 6, 6}, {0, 0, 0}, {-1, 2, 2}, {2.5, 0, 2.5}} {
		r := Recipe{RecipeSummary: RecipeSummary{RecipeServings: tc.servings, RecipeYieldQuantity: tc.yield}}
		if got := Servings(r); got != tc.want {
			t.Errorf("servings %v yield %v: %v, want %v", tc.servings, tc.yield, got, tc.want)
		}
	}
	if got := recipeFixture(t).Base(); got != 6 {
		t.Errorf("fixture base = %v", got)
	}
}

func TestImageURL(t *testing.T) {
	c := Client{URL: "https://m.example.com/meals/"}
	if got, want := c.ImageURL("abc-123", "k3y", Tiny), "https://m.example.com/meals/api/media/recipes/abc-123/images/tiny-original.webp?v=k3y"; got != want {
		t.Errorf("got %s", got)
	}
	if got := c.ImageURL("abc", "a b", Original); got != "https://m.example.com/meals/api/media/recipes/abc/images/original.webp?v=a+b" {
		t.Errorf("got %s", got)
	}
	if c.ImageURL("abc", "", Min) != "" || c.ImageURL("", "k", Min) != "" {
		t.Error("no image, no address")
	}
}
