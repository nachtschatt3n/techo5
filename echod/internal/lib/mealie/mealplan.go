package mealie

import (
	"context"
	"net/url"
	"sort"
	"time"
)

// MealTypes is the order a day's entries are shown in.
var MealTypes = []string{"breakfast", "lunch", "dinner", "side", "snack", "drink", "dessert"}

// Entry is one thing on the meal plan: a recipe, or a note with only a title.
type Entry struct {
	ID int `json:"id"`
	// Date and EndDate are "2006-01-02". An entry with an EndDate is planned for the whole week.
	Date    string `json:"date"`
	EndDate string `json:"endDate"`
	// Type is one of MealTypes; the screen translates it, the server does not.
	Type     string         `json:"entryType"`
	Title    string         `json:"title"`
	Text     string         `json:"text"`
	RecipeID string         `json:"recipeId"`
	Scale    float64        `json:"recipeScale"`
	Recipe   *RecipeSummary `json:"recipe"`
}

// Name is what the entry is called: its recipe's name, else its title.
func (e Entry) Name() string {
	if e.Recipe != nil && e.Recipe.Name != "" {
		return e.Recipe.Name
	}
	return e.Title
}

// Cookable is whether the entry has a recipe to open; a note with a title does not.
func (e Entry) Cookable() bool { return e.Recipe != nil && e.Recipe.Slug != "" }

// Week is whether the entry is for the whole week, not a day.
func (e Entry) Week() bool { return e.EndDate != "" && e.EndDate != e.Date }

// StartScale is the scale to open the recipe at: what the plan says, or 1.
func (e Entry) StartScale() float64 {
	if e.Scale > 0 {
		return e.Scale
	}
	return 1
}

// Plan is the entries from one day to another, both included, in the device's own dates. With weeks
// the entries planned for a whole week come too; Mealie leaves them out otherwise.
func (c Client) Plan(ctx context.Context, from, to time.Time, weeks bool) ([]Entry, error) {
	q := url.Values{
		"start_date":     {from.Format("2006-01-02")},
		"end_date":       {to.Format("2006-01-02")},
		"perPage":        {"-1"},
		"orderBy":        {"date"},
		"orderDirection": {"asc"},
	}
	if weeks {
		q.Set("include_week_entries", "true")
	}
	var out struct {
		Items []Entry `json:"items"`
	}
	if err := c.get(ctx, "/api/households/mealplans", q, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// SortDay puts a day's entries in the order of MealTypes (an unknown type last), keeping the server's
// order within a type.
func SortDay(entries []Entry) {
	rank := func(t string) int {
		for i, m := range MealTypes {
			if m == t {
				return i
			}
		}
		return len(MealTypes)
	}
	sort.SliceStable(entries, func(i, j int) bool { return rank(entries[i].Type) < rank(entries[j].Type) })
}
