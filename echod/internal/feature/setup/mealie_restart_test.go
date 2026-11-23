package setup

import (
	"net/url"
	"path/filepath"
	"slices"
	"testing"

	"github.com/HuskerMinion/techo5/echod/internal/config"
)

// The card's Save restarts the daemon when it sets Mealie up, an address and a token both saved, and when
// it takes either away, since Home Assistant reads which Meals entities the device has only as the daemon
// starts. A save that leaves it as it was restarts nothing, and the page says which happened.
func TestMealieSaveRestartsWhenSetUpOrRemoved(t *testing.T) {
	if !hasMeals {
		t.Skip("no Meals page on this build")
	}
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	var restarts []string
	was := mealieRestart
	mealieRestart = func(why string) { restarts = append(restarts, why) }
	t.Cleanup(func() { mealieRestart = was })

	post := func(v url.Values) string {
		t.Helper()
		v.Set("token", "the-pages-own-token")
		v.Set("what", "mealie")
		problem, done := mealieCardSave(form(v))
		if problem != "" {
			t.Fatal(problem)
		}
		return done
	}
	for _, c := range []struct {
		what string
		form url.Values
		want []string
	}{
		{"an address alone", url.Values{"url": {"http://mealie.example:9925"}}, nil},
		{"the token too", url.Values{"url": {"http://mealie.example:9925"}, "mealietoken": {"mealie-secret"}}, []string{"mealie set up"}},
		{"saved again as it is", url.Values{"url": {"http://mealie.example:9925"}, "mealiesleeps": {"1"}}, []string{"mealie set up"}},
		{"the token taken away", url.Values{"url": {"http://mealie.example:9925"}, "notoken": {"yes"}}, []string{"mealie set up", "mealie removed"}},
		{"set up once more", url.Values{"url": {"http://mealie.example:9925"}, "mealietoken": {"mealie-secret"}}, []string{"mealie set up", "mealie removed", "mealie set up"}},
		{"the address taken away", url.Values{"url": {""}}, []string{"mealie set up", "mealie removed", "mealie set up", "mealie removed"}},
	} {
		before := len(restarts)
		done := post(c.form)
		if !slices.Equal(restarts, c.want) {
			t.Fatalf("%s: restarts %q, want %q", c.what, restarts, c.want)
		}
		restarted := len(restarts) > before
		if restarted != (done == "restarting") {
			t.Errorf("%s: restarted %v, but the page is told %q", c.what, restarted, done)
		}
	}
	if doneWords("restarting") == doneWords("1") {
		t.Error("the page does not say the device restarts")
	}
}

// A save the card refuses changes nothing, so it restarts nothing.
func TestMealieRefusedSaveDoesNotRestart(t *testing.T) {
	if !hasMeals {
		t.Skip("no Meals page on this build")
	}
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	restarted := false
	was := mealieRestart
	mealieRestart = func(string) { restarted = true }
	t.Cleanup(func() { mealieRestart = was })

	v := url.Values{"url": {"not an address"}, "mealietoken": {"mealie-secret"}, "token": {"the-pages-own-token"}, "what": {"mealie"}}
	if problem, done := mealieCardSave(form(v)); problem == "" || done != "" {
		t.Errorf("a bad address was taken: problem %q, done %q", problem, done)
	}
	if restarted || config.Get().Mealie.Set() {
		t.Error("a refused save set Mealie up or restarted")
	}
}
