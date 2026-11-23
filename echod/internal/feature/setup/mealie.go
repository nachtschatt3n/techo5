package setup

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/lib/hook"
	"github.com/HuskerMinion/techo5/echod/internal/lib/mealie"
	"github.com/HuskerMinion/techo5/echod/internal/lib/safe"
	"github.com/HuskerMinion/techo5/echod/internal/update"
)

// The Meals card: the Mealie server (config.Mealie) whose shopping list, meal plan and recipes the
// screen shows. Like Music Assistant's, its token is never shown: it is written, or left alone. The
// draw is in mealie_show.go, since only a Show has the page; what is saved and asked is here, where a
// test on any build reaches it.

// mealieWait is the most a draw waits for what Mealie says of itself. Shorter than the lookup's own
// wait: the card is one among many on the tab, and a server that is slow shows its answer on the next
// draw. A variable for the tests.
var mealieWait = 100 * time.Millisecond

// mealieAnswer is what Mealie said: how it sees this device's token, and the shopping lists it has.
type mealieAnswer struct {
	status string
	lists  []mealie.ListSummary
	// listed is whether lists is the server's answer, an empty one included; false when it was not
	// asked or would not answer, and the saved choice is all the dropdown has.
	listed bool
}

// MealieNote is what the screen is told of the Meals settings: that they were saved here, or the shopping
// lists Mealie answered with. The device's own entities (feature/display/meals_entities.go) listen, so
// the list the select offers is the one this card was just shown, without a request of their own.
type MealieNote struct {
	// Lists are the shopping lists Mealie named; only meaningful when Listed.
	Lists  []mealie.ListSummary
	Listed bool
}

// MealieNoted fires after a settings save and after each lookup that got Mealie's lists.
var MealieNoted hook.Hook[MealieNote]

// mealieLookup keeps Mealie's answer (lookup.go): up to three questions, bounded together.
var mealieLookup = &lookup[mealieAnswer]{ask: 8 * time.Second}

// askMealie asks Mealie who it is, whether the token works and which lists it has, each only when the
// one before it was answered, since asking a server that is not there would only wait the same time
// again.
func askMealie(ctx context.Context, m config.Mealie) mealieAnswer {
	status, ok := mealieStatus(ctx, m)
	a := mealieAnswer{status: status}
	if !ok {
		return a
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	lists, err := mealie.Client{URL: m.URL, Token: m.Token}.Lists(ctx)
	if err != nil {
		slog.Warn("setup page: mealie lists", "err", err)
		return a
	}
	a.lists, a.listed = lists, true
	MealieNoted.Emit(MealieNote{Lists: lists, Listed: true})
	return a
}

// mealieStatus is how Mealie answers this device: whether it is there, is a version this device talks
// to, and takes the token. ok is whether the token works, which is what asking for the lists needs.
// Every answer is a sentence of this file's: what the client says of a failure goes to the log, never
// to the page.
func mealieStatus(ctx context.Context, m config.Mealie) (status string, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	c := mealie.Client{URL: m.URL, Token: m.Token}
	version, err := c.About(ctx)
	switch {
	case errors.Is(err, mealie.ErrUseHTTPS):
		return "use the https address", false
	case err != nil:
		return "not reachable", false
	case mealie.TooOld(version):
		return fmt.Sprintf("Mealie %s is too old: this needs Mealie 2.0 or later", version), false
	}
	name := "Mealie"
	if version != "" {
		name += " " + version
	}
	if m.Token == "" {
		return name + ", reachable: add a token", false
	}
	switch err := c.Check(ctx); {
	case errors.Is(err, mealie.ErrToken):
		return "reachable, but it refused the token", false
	case errors.Is(err, mealie.ErrUseHTTPS):
		return "use the https address", false
	case err != nil:
		return "not reachable", false
	}
	return name + ", signed in", true
}

// mealieSummary is the line on the shut card: whether a server is set. What Mealie says of itself is
// asked later, and not waited for here; the token is never said.
func mealieSummary(m config.Mealie) []string {
	switch {
	case m.URL == "":
		return []string{"Not set up"}
	case m.Token == "":
		return []string{"No token yet"}
	}
	return []string{"Set up"}
}

// mealieListName is what a shopping list is called on the page: Mealie lets one have no name.
func mealieListName(l mealie.ListSummary) string {
	if strings.TrimSpace(l.Name) == "" {
		return "Shopping list"
	}
	return l.Name
}

// mealieIDOK is whether a shopping list's id is one that could be Mealie's (a UUID): the choice is
// posted from the page, which anybody can write, and is kept in the saved settings.
func mealieIDOK(v string) bool {
	if len(v) > 64 {
		return false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// mealieCardSave is the card's Save: saveMealie, and a restart of the daemon when the save set Mealie up
// or took it away. Home Assistant has the Meals entities, and Meals among the Idle screen's and Tap on the
// clock's choices, only on a device with Mealie set up, and reads which entities a device has only when the
// daemon starts (feature/display/meals_entities.go), so they come and go with the restart. A save that
// leaves it set up, or not, restarts nothing. done is what the page says of it: the restart, or nothing for
// the usual "Saved."
func mealieCardSave(r *http.Request) (problem, done string) {
	was := config.Get().Mealie.Set()
	if problem = saveMealie(r); problem != "" {
		return problem, ""
	}
	now := config.Get().Mealie.Set()
	if now == was {
		return "", ""
	}
	why := "mealie removed"
	if now {
		why = "mealie set up"
	}
	slog.Info("setup page: restarting so Home Assistant sees Meals", "set_up", now)
	mealieRestart(why)
	return "", "restarting"
}

// mealieRestart restarts the daemon a moment after the save, as a rename does (rename.go): long enough for
// the page to answer first. The daemon restarts, not the device. Replaced in tests.
var mealieRestart = func(why string) {
	safe.Go("restart for meals", func() {
		time.Sleep(3 * time.Second)
		update.Restart(why)
	})
}

func saveMealie(r *http.Request) string {
	if !hasMeals {
		return "this device has no Meals page"
	}
	addr := strings.TrimSpace(r.PostFormValue("url"))
	if addr != "" {
		var err error
		if addr, err = mealie.ParseURL(addr); err != nil {
			return "the address should be like http://192.0.2.20:9925 (" + err.Error() + ")"
		}
	}
	// The list is checked before anything is saved, so a refused form saves none of it.
	list, hasList := "", false
	if _, shown := r.PostForm["list"]; shown { // only a form that showed the choice carries it
		list, hasList = strings.TrimSpace(r.PostFormValue("list")), true
		if !mealieIDOK(list) {
			return "that is not one of Mealie's shopping lists"
		}
	}
	var tok *string
	// Not "token": that is the page's own, which every form carries (hidden).
	switch t := strings.TrimSpace(r.PostFormValue("mealietoken")); {
	case r.PostFormValue("notoken") == "yes":
		empty := ""
		tok = &empty
	case t != "":
		if strings.ContainsFunc(t, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) }) {
			return "a token is one word"
		}
		tok = &t
	}
	if err := config.Set().Mealie().Server(addr, tok); err != nil {
		return "could not save it: " + err.Error()
	}
	if hasList {
		if err := config.Set().Mealie().List(list); err != nil {
			return "could not save the shopping list: " + err.Error()
		}
	}
	// A checkbox that is not ticked posts nothing, so it is read only from the form that has it: the
	// page's own, which says so (mealiesleeps).
	if _, shown := r.PostForm["mealiesleeps"]; shown {
		if err := config.Set().Mealie().Sleeps(r.PostFormValue("awake") != "yes"); err != nil {
			return "could not save the screen setting: " + err.Error()
		}
	}
	mealieLookup.forget() // the next draw asks the server as it is now saved
	MealieNoted.Emit(MealieNote{})
	slog.Info("setup page: mealie set", "token", tok != nil, "list", list != "")
	return ""
}

// mealieListField is the shopping list shown: the lists Mealie has (asked in askMealie), the first of
// them when none is chosen. A saved choice Mealie does not list, or cannot be asked about now, is kept
// as an option, so saving never blanks it.
func mealieListField(w http.ResponseWriter, m config.Mealie, a mealieAnswer) {
	fmt.Fprint(w, `<label for="mealielist">Shopping list</label><select id="mealielist" name="list">`)
	option := func(value, label string) {
		sel := ""
		if value == m.List {
			sel = " selected"
		}
		fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, html.EscapeString(value), sel, html.EscapeString(label))
	}
	option("", "The first list")
	listed := m.List == ""
	for _, l := range a.lists {
		option(l.ID, mealieListName(l))
		listed = listed || l.ID == m.List
	}
	if !listed {
		option(m.List, "the one saved")
	}
	fmt.Fprint(w, `</select>`)
	if !a.listed && a.status != "" {
		fmt.Fprint(w, `<p class="note">Mealie did not list its shopping lists, so only the saved choice is shown.</p>`)
	}
}
