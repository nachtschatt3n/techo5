//go:build !dot && !spot

package all

import (
	"slices"
	"testing"

	"github.com/HuskerMinion/techo5/echod/internal/component"
)

// The Meals page's two actions are on the devices that draw it: meals_show puts the page up and meals_step
// moves the recipe being cooked.
func TestTheMealsActionsRegister(t *testing.T) {
	var got []string
	for _, a := range component.Default().Actions() {
		got = append(got, a.Name)
	}
	for _, name := range []string{"meals_show", "meals_step"} {
		if !slices.Contains(got, name) {
			t.Errorf("action %q is not registered on this device", name)
		}
	}
}
