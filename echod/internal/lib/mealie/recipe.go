package mealie

import (
	"context"
	"net/url"
	"strings"
)

// RecipeSummary is what the meal plan says of a recipe, and the head of a full one.
type RecipeSummary struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	// Image is a key that changes with the picture, not an address; empty when there is no picture.
	Image string `json:"image"`
	// RecipeServings is 0 when the recipe does not say; then RecipeYieldQuantity may.
	RecipeServings      float64 `json:"recipeServings"`
	RecipeYieldQuantity float64 `json:"recipeYieldQuantity"`
	RecipeYield         string  `json:"recipeYield"`
	TotalTimeSeconds    int     `json:"totalTimeSeconds"`
	Description         string  `json:"description"`
}

// Base is the number of servings the recipe is written for: its servings, else its yield quantity, else
// 0 for a recipe that says neither (the screen then scales by a factor, not by servings).
func (r RecipeSummary) Base() float64 {
	switch {
	case r.RecipeServings > 0:
		return r.RecipeServings
	case r.RecipeYieldQuantity > 0:
		return r.RecipeYieldQuantity
	}
	return 0
}

// Recipe is a full recipe, as far as cooking needs it.
type Recipe struct {
	RecipeSummary
	PrepTimeSeconds    int          `json:"prepTimeSeconds"`
	PerformTimeSeconds int          `json:"performTimeSeconds"`
	Ingredients        []Ingredient `json:"recipeIngredient"`
	Steps              []Step       `json:"recipeInstructions"`
}

// Servings is the number of servings the recipe is written for, 0 when it does not say.
func Servings(r Recipe) float64 { return r.Base() }

// Unit is a unit of measure with its names and whether its amounts are written as fractions.
type Unit struct {
	Name               string `json:"name"`
	PluralName         string `json:"pluralName"`
	Abbreviation       string `json:"abbreviation"`
	PluralAbbreviation string `json:"pluralAbbreviation"`
	UseAbbreviation    bool   `json:"useAbbreviation"`
	Fraction           bool   `json:"fraction"`
}

// Food is an ingredient's food.
type Food struct {
	Name       string `json:"name"`
	PluralName string `json:"pluralName"`
}

// Ingredient is one line of a recipe's ingredients. A line with a Title also starts a section.
type Ingredient struct {
	ReferenceID string  `json:"referenceId"`
	Title       string  `json:"title"`
	Quantity    float64 `json:"quantity"`
	Unit        *Unit   `json:"unit"`
	Food        *Food   `json:"food"`
	Note        string  `json:"note"`
	Display     string  `json:"display"`
}

// Step is one step of the instructions. Title is the name of a section, not of the step.
type Step struct {
	ID                   string `json:"id"`
	Title                string `json:"title"`
	Summary              string `json:"summary"`
	Text                 string `json:"text"`
	IngredientReferences []struct {
		ReferenceID string `json:"referenceId"`
	} `json:"ingredientReferences"`
	// DurationSeconds is the time the author gave the step; 0 when none.
	DurationSeconds int `json:"durationSeconds"`
}

// Body is what the screen shows for the step: its text without the markdown, else its title, else its
// summary.
func (s Step) Body() string {
	for _, t := range []string{s.Text, s.Title, s.Summary} {
		if p := Plain(t); p != "" {
			return p
		}
	}
	return ""
}

// Uses is the indexes of the recipe's ingredients the step refers to. A reference to an ingredient that
// is not in the list (an edited recipe can leave one) is ignored.
func (r Recipe) Uses(s Step) []int {
	var out []int
	for i, ing := range r.Ingredients {
		if ing.ReferenceID == "" {
			continue
		}
		for _, ref := range s.IngredientReferences {
			if ref.ReferenceID == ing.ReferenceID {
				out = append(out, i)
				break
			}
		}
	}
	return out
}

// Recipe is the full recipe, by its slug or its id.
func (c Client) Recipe(ctx context.Context, slugOrID string) (Recipe, error) {
	var out Recipe
	err := c.do(ctx, "GET", "/api/recipes/"+url.PathEscape(slugOrID), nil, true, nil, &out, recipeMost)
	return out, err
}

// ImageSize is one of the three sizes the server keeps of a recipe's picture.
type ImageSize string

// The sizes: a thumbnail, a small one and the original.
const (
	Tiny     ImageSize = "tiny-original"
	Min      ImageSize = "min-original"
	Original ImageSize = "original"
)

// ImageURL is where a recipe's picture is, or "" when the recipe has none (an empty cache key). The
// server needs no token for it, and the key after "v" only keeps a changed picture from being served
// from a cache.
func (c Client) ImageURL(id, cacheKey string, size ImageSize) string {
	if id == "" || cacheKey == "" {
		return ""
	}
	return strings.TrimRight(c.URL, "/") + "/api/media/recipes/" + url.PathEscape(id) + "/images/" +
		string(size) + ".webp?v=" + url.QueryEscape(cacheKey)
}
