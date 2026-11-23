//go:build dot || spot

package setup

import (
	"context"
	"net/http"
)

// hasMeals: the Meals page is the Show's alone, so a Dot and a Spot draw no card and refuse a form
// posted for one.
const hasMeals = false

func mealieSection(context.Context, http.ResponseWriter, string) {}
