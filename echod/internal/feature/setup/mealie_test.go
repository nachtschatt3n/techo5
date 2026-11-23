package setup

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
)

// fakeMealie is a Mealie that answers like one: its version without a token, and with the right one its
// shopping lists. asked counts every request, to say a draw made none.
type fakeMealie struct {
	version string
	token   string
	lists   string // the shopping lists' JSON
	asked   atomic.Int32
}

func (f *fakeMealie) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.asked.Add(1)
	if r.URL.Path == "/api/app/about" {
		fmt.Fprintf(w, `{"version":%q}`, f.version)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}
	if f.lists == "" {
		f.lists = `{"items":[]}`
	}
	fmt.Fprint(w, f.lists)
}

func serveMealie(t *testing.T, f *fakeMealie) string {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv.URL
}

func postMealie(v url.Values) string {
	v.Set("token", "the-pages-own-token")
	v.Set("what", "mealie")
	return saveMealie(form(v))
}

// The address is cleaned the way the client reads it, the token is its own field (the page's is never
// taken for it), a field left empty keeps the saved token, and the choices ride along.
func TestSaveMealieValidates(t *testing.T) {
	if !hasMeals {
		t.Skip("no Meals page on this build")
	}
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	if p := postMealie(url.Values{"url": {"HTTP://Mealie.Example:9925/"}, "mealietoken": {"mealie-secret"}, "list": {"3f2b-aa"}, "mealiesleeps": {"1"}, "awake": {"yes"}}); p != "" {
		t.Fatal(p)
	}
	m := config.Get().Mealie
	if m.URL != "http://mealie.example:9925" || m.Token != "mealie-secret" || m.List != "3f2b-aa" || m.Sleeps {
		t.Errorf("saved %+v", m)
	}
	// Saved again with the token field left empty: the saved token stays. The tick-box is read only from
	// the form that has it, and unticked it lets the screen sleep.
	if p := postMealie(url.Values{"url": {"http://mealie.example:9925"}, "mealiesleeps": {"1"}}); p != "" {
		t.Fatal(p)
	}
	if m := config.Get().Mealie; m.Token != "mealie-secret" || !m.Sleeps || m.List != "3f2b-aa" {
		t.Errorf("an empty field changed %+v", m)
	}
	// A form that carries neither the list nor the box leaves both.
	if p := postMealie(url.Values{"url": {"http://mealie.example:9925"}}); p != "" {
		t.Fatal(p)
	}
	if m := config.Get().Mealie; m.List != "3f2b-aa" || !m.Sleeps {
		t.Errorf("a form without them changed %+v", m)
	}
	before := config.Get().Mealie
	for _, bad := range []url.Values{
		{"url": {"http://user:pw@mealie.example"}},
		{"url": {"ftp://mealie.example"}},
		{"url": {"mealie.example"}},
		{"url": {"http://mealie.example"}, "mealietoken": {"two\twords"}},
		{"url": {"http://mealie.example"}, "list": {"<script>"}},
		{"url": {"http://mealie.example"}, "list": {strings.Repeat("a", 65)}},
	} {
		if postMealie(bad) == "" {
			t.Errorf("%v was saved", bad)
		}
	}
	if config.Get().Mealie != before {
		t.Errorf("a refused form changed %+v to %+v", before, config.Get().Mealie)
	}
}

// The token can be taken away on its own, and what is typed with the box ticked is not kept.
func TestSaveMealieNoTokenClears(t *testing.T) {
	if !hasMeals {
		t.Skip("no Meals page on this build")
	}
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	if p := postMealie(url.Values{"url": {"http://mealie.example:9925"}, "mealietoken": {"mealie-secret"}}); p != "" {
		t.Fatal(p)
	}
	if p := postMealie(url.Values{"url": {"http://mealie.example:9925"}, "mealietoken": {"typed-too"}, "notoken": {"yes"}}); p != "" {
		t.Fatal(p)
	}
	if m := config.Get().Mealie; m.Token != "" || m.URL != "http://mealie.example:9925" {
		t.Errorf("remove left %+v", m)
	}
}

// A build with no Meals page refuses the form, saving none of it.
func TestSaveMealieRefusedWithoutThePage(t *testing.T) {
	if hasMeals {
		t.Skip("this build has the Meals page")
	}
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	if postMealie(url.Values{"url": {"http://mealie.example:9925"}, "mealietoken": {"x"}}) == "" || config.Get().Mealie.URL != "" {
		t.Errorf("a build without the page saved %+v", config.Get().Mealie)
	}
}

// Every answer Mealie can give, said in this card's own words. Nothing of the server's own text is shown.
func TestMealieStatusWords(t *testing.T) {
	ok := &fakeMealie{version: "3.30.0", token: "good"}
	okURL := serveMealie(t, ok)
	// An http address that the server sends to https, the way a reverse proxy does.
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://"+r.Host+r.URL.Path, http.StatusMovedPermanently)
	}))
	t.Cleanup(redirect.Close)
	old := serveMealie(t, &fakeMealie{version: "1.12.0", token: "good"})
	odd := serveMealie(t, &fakeMealie{version: "nightly", token: "good"})
	gone := httptest.NewServer(http.NotFoundHandler())
	goneURL := gone.URL
	gone.Close()

	for _, c := range []struct {
		name, url, token, want string
		signedIn               bool
	}{
		{"signed in", okURL, "good", "Mealie 3.30.0, signed in", true},
		{"refused", okURL, "bad", "reachable, but it refused the token", false},
		{"no token yet", okURL, "", "Mealie 3.30.0, reachable: add a token", false},
		{"https", redirect.URL, "good", "use the https address", false},
		{"too old", old, "good", "Mealie 1.12.0 is too old: this needs Mealie 2.0 or later", false},
		{"unparseable version", odd, "good", "Mealie nightly, signed in", true},
		{"not reachable", goneURL, "good", "not reachable", false},
	} {
		got, signedIn := mealieStatus(context.Background(), config.Mealie{URL: c.url, Token: c.token})
		if got != c.want || signedIn != c.signedIn {
			t.Errorf("%s: %q (%v), want %q (%v)", c.name, got, signedIn, c.want, c.signedIn)
		}
		if strings.Contains(got, c.url) {
			t.Errorf("%s: the address is in the status %q", c.name, got)
		}
	}
}

// The card is drawn without waiting for a Mealie that does not answer, and says so.
func TestMealieStatusNeverBlocks(t *testing.T) {
	if !hasMeals {
		t.Skip("no Meals page on this build")
	}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	tok := "mealie-secret"
	if err := config.Set().Mealie().Server(srv.URL, &tok); err != nil {
		t.Fatal(err)
	}
	mealieLookup.forget()
	if mealieWait > 100*time.Millisecond {
		t.Errorf("the card waits %s for Mealie", mealieWait)
	}
	w := httptest.NewRecorder()
	start := time.Now()
	mealieSection(context.Background(), w, "tok")
	if took := time.Since(start); took > 400*time.Millisecond {
		t.Errorf("the card waited %s for a Mealie that did not answer", took)
	}
	if page := w.Body.String(); !strings.Contains(page, "still being asked") || !strings.Contains(page, `name="mealietoken"`) {
		t.Errorf("a slow Mealie drew: %s", first(w.Body.String()))
	}
}

// The shopping list dropdown names the lists Mealie has (one with no name is a Shopping list), chooses the
// saved one, and keeps a saved one it cannot offer, so saving never blanks the choice.
func TestMealieListKeepsSavedWhenUnreachable(t *testing.T) {
	if !hasMeals {
		t.Skip("no Meals page on this build")
	}
	draw := func(m config.Mealie, a mealieAnswer) string {
		w := httptest.NewRecorder()
		mealieListField(w, m, a)
		return w.Body.String()
	}
	lists := []mealie.ListSummary{{ID: "id-1", Name: "Weekly"}, {ID: "id-2"}}
	page := draw(config.Mealie{List: "id-2"}, mealieAnswer{status: "Mealie, signed in", lists: lists, listed: true})
	for _, want := range []string{`<option value="id-1">Weekly</option>`, `<option value="id-2" selected>Shopping list</option>`, `<option value="">The first list</option>`} {
		if !strings.Contains(page, want) {
			t.Errorf("the dropdown lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "the one saved") || strings.Contains(page, "did not list") {
		t.Errorf("a listed choice is said to be missing:\n%s", page)
	}
	// Unreachable: only the saved id, kept and chosen.
	page = draw(config.Mealie{List: "id-9"}, mealieAnswer{status: "not reachable"})
	if !strings.Contains(page, `<option value="id-9" selected>the one saved</option>`) || !strings.Contains(page, "did not list") {
		t.Errorf("an unreachable Mealie drew:\n%s", page)
	}
	// And a save of that form keeps it.
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	if err := config.Set().Mealie().List("id-9"); err != nil {
		t.Fatal(err)
	}
	if p := postMealie(url.Values{"url": {"http://mealie.example"}, "list": {"id-9"}}); p != "" || config.Get().Mealie.List != "id-9" {
		t.Errorf("saved %q (%s)", config.Get().Mealie.List, p)
	}
	// Asked through a real server: the names come from it.
	f := &fakeMealie{version: "3.30.0", token: "good", lists: `{"items":[{"id":"id-1","name":"Weekly"},{"id":"id-2","name":null}]}`}
	a := askMealie(context.Background(), config.Mealie{URL: serveMealie(t, f), Token: "good"})
	if !a.listed || len(a.lists) != 2 || mealieListName(a.lists[1]) != "Shopping list" {
		t.Errorf("asked: %+v", a)
	}
}

// The card is a Show's alone: a Dot and a Spot draw nothing, and a Show draws it, with no token shown.
func TestMealieSectionOnlyWithScreen(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	w := httptest.NewRecorder()
	mealieSection(context.Background(), w, "tok")
	page := w.Body.String()
	if hasMeals != (page != "") {
		t.Errorf("hasMeals is %v and the card is %d bytes", hasMeals, len(page))
	}
	if !hasMeals {
		return
	}
	for _, want := range []string{`id="mealieurl"`, `name="mealietoken"`, `type="password"`, "Keep the screen on while cooking", "Not set up"} {
		if !strings.Contains(page, want) {
			t.Errorf("the card lacks %q", want)
		}
	}
	if strings.Contains(page, `name="list"`) {
		t.Error("the card offers lists with no server set")
	}
	// It lives on the Screen tab and can be found.
	if tabFor["mealie"] != "screen" {
		t.Errorf("the card is on tab %q", tabFor["mealie"])
	}
}

// With no server set up, drawing the card and saving an empty form ask nothing of the network.
func TestMealieNotSetUpMakesNoCall(t *testing.T) {
	f := &fakeMealie{version: "3.30.0", token: "good"}
	serveMealie(t, f)
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	w := httptest.NewRecorder()
	mealieSection(context.Background(), w, "tok")
	postMealie(url.Values{})
	if n := f.asked.Load(); n != 0 {
		t.Errorf("Mealie was asked %d times with none set up", n)
	}
	if got := mealieSummary(config.Get().Mealie); len(got) != 1 || got[0] != "Not set up" {
		t.Errorf("the summary says %v", got)
	}
	if got := mealieSummary(config.Mealie{URL: "http://x"}); got[0] != "No token yet" {
		t.Errorf("a server with no token says %v", got)
	}
	if got := mealieSummary(config.Mealie{URL: "http://x", Token: "t"}); got[0] != "Set up" {
		t.Errorf("a set up server says %v", got)
	}
}

// The part of a settings file that is Mealie's is named the way the page names it.
func TestSettingsFileNamesMealie(t *testing.T) {
	if got := partName("mealie"); got != "Meals (Mealie)" {
		t.Errorf("the part is called %q", got)
	}
	c := config.Defaults()
	c.Mealie.URL = "http://192.0.2.20:9925"
	file, err := config.Export(c, "m", "v0")
	if err != nil {
		t.Fatal(err)
	}
	r, err := config.Restored(config.Defaults(), file, "m")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, part := range r.Changed {
		found = found || part == "mealie"
	}
	if !found {
		t.Errorf("a file with a Mealie server in it changes %v", r.Changed)
	}
}
