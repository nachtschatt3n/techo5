package config

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Mealie is there to ask only with both an address and a token.
func TestMealieSetNeedsURLAndToken(t *testing.T) {
	for _, c := range []struct {
		m    Mealie
		want bool
	}{
		{Mealie{}, false},
		{Mealie{URL: "http://192.0.2.20:9925"}, false},
		{Mealie{Token: "t"}, false},
		{Mealie{URL: "http://192.0.2.20:9925", Token: "t"}, true},
		{Mealie{URL: "http://192.0.2.20:9925", Token: "t", List: "abc", Sleeps: true}, true},
	} {
		if got := c.m.Set(); got != c.want {
			t.Errorf("%+v is set: %v, want %v", c.m, got, c.want)
		}
	}
}

// The writer keeps a saved token when it is given none, replaces it when given one, and takes it away
// when given an empty one; the list and the sleep choice are their own.
func TestMealieWriterKeepsToken(t *testing.T) {
	Use(filepath.Join(t.TempDir(), "state.json"))
	tok := "one-token"
	if err := Set().Mealie().Server("http://192.0.2.20:9925", &tok); err != nil {
		t.Fatal(err)
	}
	if err := Set().Mealie().Server("http://192.0.2.21:9925", nil); err != nil {
		t.Fatal(err)
	}
	if m := Get().Mealie; m.URL != "http://192.0.2.21:9925" || m.Token != "one-token" {
		t.Errorf("a nil token changed what was saved: %+v", m)
	}
	other := "two-token"
	if err := Set().Mealie().Server("http://192.0.2.21:9925", &other); err != nil || Get().Mealie.Token != "two-token" {
		t.Errorf("a new token left %q (%v)", Get().Mealie.Token, err)
	}
	empty := ""
	if err := Set().Mealie().Server("http://192.0.2.21:9925", &empty); err != nil || Get().Mealie.Token != "" {
		t.Errorf("an empty token left %q (%v)", Get().Mealie.Token, err)
	}
	if err := Set().Mealie().List("list-1"); err != nil || Get().Mealie.List != "list-1" {
		t.Errorf("the list is %q (%v)", Get().Mealie.List, err)
	}
	if err := Set().Mealie().Sleeps(true); err != nil || !Get().Mealie.Sleeps {
		t.Errorf("sleeps is %v (%v)", Get().Mealie.Sleeps, err)
	}
	// The screen stays on while cooking unless it was said otherwise: a state.json with no Mealie in it
	// reads as that.
	if Defaults().Mealie.Sleeps {
		t.Error("the screen sleeps while cooking by default")
	}
}

// A settings file leaves with no Mealie token in it.
func TestClearSecretsClearsMealie(t *testing.T) {
	c := Defaults()
	c.Mealie = Mealie{URL: "http://192.0.2.20:9925", Token: "mealie-secret", List: "abc"}
	ClearSecrets(&c)
	if c.Mealie.Token != "" || c.Mealie.URL == "" || c.Mealie.List != "abc" {
		t.Errorf("after ClearSecrets: %+v", c.Mealie)
	}
	file, err := Export(Config{Mealie: Mealie{URL: "http://192.0.2.20:9925", Token: "mealie-secret"}}, "m", "v0")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(file); strings.Contains(got, "mealie-secret") {
		t.Errorf("the token is in the settings file: %s", got)
	}
}

// A restore keeps this device's Mealie token for the same server, and never gives it to another: the
// file's address is where the token would go.
func TestImportKeepsMealieTokenForSameURL(t *testing.T) {
	here := Defaults()
	here.Mealie = Mealie{URL: "http://192.0.2.20:9925", Token: "mealie-secret"}

	same := Defaults()
	same.Mealie.URL = here.Mealie.URL
	file, _ := Export(same, "m", "v0")
	r, err := Restored(here, file, "m")
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Mealie.Token != "mealie-secret" || slices.Contains(r.Reenter, "Mealie's token") {
		t.Errorf("the token for the same server: %q, reenter %v", r.Config.Mealie.Token, r.Reenter)
	}

	elsewhere := Defaults()
	elsewhere.Mealie.URL = "http://198.51.100.7:9925"
	file, _ = Export(elsewhere, "m", "v0")
	r, err = Restored(here, file, "m")
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Mealie.Token != "" || !slices.Contains(r.Reenter, "Mealie's token") {
		t.Errorf("the token followed the file to its server: %q, reenter %v", r.Config.Mealie.Token, r.Reenter)
	}

	// A crafted file with a token of its own does not get it in.
	crafted := []byte(`{"format":"techo5-settings","version":1,"settings":{"mealie":{"url":"http://198.51.100.7:9925","token":"theirs"}}}`)
	r, err = Restored(here, crafted, "m")
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Mealie.Token == "theirs" {
		t.Error("a file put its own Mealie token in")
	}
}
