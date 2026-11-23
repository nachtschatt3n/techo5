package mealie

import (
	"testing"
)

func ing(q float64, unit *Unit, food, note, display string) Ingredient {
	i := Ingredient{Quantity: q, Unit: unit, Note: note, Display: display}
	if food != "" {
		i.Food = &Food{Name: food}
	}
	return i
}

var (
	gram = &Unit{Name: "gram", PluralName: "grams", Abbreviation: "g", PluralAbbreviation: "g", UseAbbreviation: true}
	cup  = &Unit{Name: "cup", PluralName: "cups", Fraction: true}
)

func TestAmountScales(t *testing.T) {
	onion := ing(1, nil, "onion", "diced", "1 onion diced")
	onion.Food.PluralName = "onions"
	for _, tc := range []struct {
		in    Ingredient
		scale float64
		want  string
	}{
		{ing(500, gram, "minced beef", "", "500 g minced beef"), 1, "500 g minced beef"},
		{ing(500, gram, "minced beef", "", "500 g minced beef"), 1.5, "750 g minced beef"},
		{ing(500, gram, "flour", "", "500 g flour"), 0.5, "250 g flour"},
		{ing(250, gram, "flour", "", "250 g flour"), 0.35, "87.5 g flour"},
		{ing(1, cup, "milk", "", "1 cup milk"), 1.5, "1 1/2 cups milk"},
		{ing(1, cup, "milk", "", "1 cup milk"), 0.5, "1/2 cup milk"},
		{ing(1, cup, "milk", "", "1 cup milk"), 4, "4 cups milk"},
		{ing(0.75, cup, "milk", "", ""), 2, "1 1/2 cups milk"},
		{onion, 3, "3 onions diced"},
		{onion, 2, "2 onions diced"},
		{onion, 1, "1 onion diced"},
		{ing(2, nil, "egg", "", ""), 2.5, "5 egg"},
	} {
		got, scaled := Amount(tc.in, tc.scale)
		if got != tc.want || !scaled {
			t.Errorf("Amount(%v, %v) = %q, %v; want %q", tc.in.Display, tc.scale, got, scaled, tc.want)
		}
	}
}

func TestAmountEdges(t *testing.T) {
	// Not scaled: no amount, no food or unit, a line that is not a number, or a note only.
	for _, tc := range []struct {
		in   Ingredient
		want string
	}{
		{ing(0, nil, "", "a pinch of salt", "a pinch of salt"), "a pinch of salt"},
		{ing(0, nil, "salt", "", "salt"), "salt"},
		{ing(1, nil, "", "to taste", "to taste"), "to taste"},
		{ing(1, nil, "salt", "", "a pinch salt"), "a pinch salt"},
		{ing(0, nil, "", "serve warm", ""), "serve warm"},
		{ing(0, nil, "", "", ""), ""},
	} {
		for _, scale := range []float64{1, 2, 0.5} {
			if got, scaled := Amount(tc.in, scale); got != tc.want || scaled {
				t.Errorf("Amount(%q, %v) = %q, %v; want %q unscaled", tc.in.Display, scale, got, scaled, tc.want)
			}
		}
	}
	two := ing(2, gram, "flour", "", "2 g flour")
	// A scale of 0 or less, or one that is not a number, is the recipe as written.
	for _, scale := range []float64{0, -3, nan(), inf()} {
		if got, scaled := Amount(two, scale); got != "2 g flour" || !scaled {
			t.Errorf("scale %v: %q, %v", scale, got, scaled)
		}
	}
	// Without the server's line the amount is composed even at 1.
	if got, _ := Amount(ing(2, gram, "flour", "", ""), 1); got != "2 g flour" {
		t.Errorf("composed at 1: %q", got)
	}
	// Huge and tiny amounts stay readable, with no exponent.
	if got, _ := Amount(ing(3000, gram, "sand", "", "3000 g sand"), 5000); got != "15000000 g sand" {
		t.Errorf("huge: %q", got)
	}
	if got, _ := Amount(ing(1, gram, "salt", "", "1 g salt"), 0.001); got != "0.01 g salt" {
		t.Errorf("tiny: %q", got)
	}
	// Unit names: the plural from more than one, the abbreviation when the unit uses it.
	tbsp := &Unit{Name: "tablespoon", PluralName: "tablespoons", Fraction: true}
	if got, _ := Amount(ing(1, tbsp, "oil", "", "1 tablespoon oil"), 2); got != "2 tablespoons oil" {
		t.Errorf("plural unit: %q", got)
	}
	if got, _ := Amount(ing(1, tbsp, "oil", "", "1 tablespoon oil"), 1.5); got != "1 1/2 tablespoons oil" {
		t.Errorf("plural unit: %q", got)
	}
}

func TestFractions(t *testing.T) {
	for _, tc := range []struct {
		q        float64
		fraction bool
		want     string
	}{
		{0.5, true, "1/2"}, {1.5, true, "1 1/2"}, {2, true, "2"}, {0.25, true, "1/4"}, {0.75, true, "3/4"},
		{0.333, true, "1/3"}, {0.667, true, "2/3"}, {1.125, true, "1 1/8"}, {0.375, true, "3/8"},
		{0.99, true, "1"}, {2.999, true, "3"}, {0.7, true, "0.7"}, {1.1, true, "1.1"}, {9.875, true, "9 7/8"},
		{12, true, "12"}, {12.5, true, "12.5"}, {12.04, true, "12"}, {99.96, true, "100"},
		{9999.5, true, "9999.5"}, {10000.4, true, "10000"}, {123456789.2, true, "123456789"},
		{0.5, false, "0.5"}, {1.5, false, "1.5"}, {1.25, false, "1.25"}, {0.333, false, "0.33"}, {2, false, "2"},
		{1.005, false, "1"}, {0.004, false, "0"}, {1234.56, false, "1234.6"},
	} {
		got := formatQuantity(tc.q, tc.fraction)
		if tc.q == 0.004 {
			// Under a hundredth is shown as one rather than as nothing.
			tc.want = "0.01"
		}
		if got != tc.want {
			t.Errorf("formatQuantity(%v, %v) = %q, want %q", tc.q, tc.fraction, got, tc.want)
		}
	}
}

func TestNormalizeDisplay(t *testing.T) {
	for in, want := range map[string]string{
		"1 ¹/₂ cup flour":    "1 1/2 cup flour",
		"¾ tsp salt":         "3/4 tsp salt",
		"1½ kg potatoes":     "1 1/2 kg potatoes",
		"2 ¹⁰/₁₁ cups":       "2 10/11 cups",
		"1¹/₂ l water":       "1 1/2 l water",
		"1⁄2 cup":            "1/2 cup",
		"3   onions,  diced": "3 onions, diced",
		"  ":                 "",
		"plain 12 text":      "plain 12 text",
		"Zöpfe ⅓ Zucker":     "Zöpfe 1/3 Zucker",
		"²/₃ cup":            "2/3 cup",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func nan() float64 { var z float64; return z / z }
func inf() float64 { var z float64; return 1 / z }
