package mealie

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestListsAndList(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/households/shopping/lists":
			query = r.URL.RawQuery
			w.Write(fixture(t, "lists.json"))
		case "/api/households/shopping/lists/11111111-0000-0000-0000-000000000001":
			w.Write(fixture(t, "list.json"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := Client{URL: srv.URL, Token: "t"}
	lists, err := c.Lists(context.Background())
	if err != nil || len(lists) != 2 {
		t.Fatalf("Lists = %+v, %v", lists, err)
	}
	if lists[1].Name != "Party" || lists[0].ID != "11111111-0000-0000-0000-000000000001" {
		t.Errorf("lists = %+v", lists)
	}
	for _, want := range []string{"perPage=-1", "orderBy=createdAt", "orderDirection=asc"} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q lacks %s", query, want)
		}
	}
	l, err := c.List(context.Background(), lists[0].ID)
	if err != nil || len(l.Items) != 8 {
		t.Fatalf("List = %d items, %v", len(l.Items), err)
	}
	if _, err := c.List(context.Background(), "nope"); err != ErrNotFound {
		t.Errorf("missing list: %v", err)
	}
	if l.Items[1].Text() != "1 1/2 kg potatoes" || l.Items[3].Text() != "gift wrap" || l.Items[4].Text() != "" {
		t.Errorf("texts = %q %q %q", l.Items[1].Text(), l.Items[3].Text(), l.Items[4].Text())
	}
}

func TestListNullNameAndLabelColor(t *testing.T) {
	var lists struct{ Items []ListSummary }
	if err := json.Unmarshal(fixture(t, "lists.json"), &lists); err != nil {
		t.Fatal(err)
	}
	if lists.Items[0].Name != "" {
		t.Errorf("a null name is %q, want empty", lists.Items[0].Name)
	}
	var l List
	if err := json.Unmarshal(fixture(t, "list.json"), &l); err != nil {
		t.Fatal(err)
	}
	produce := l.LabelSettings[1].Label
	if produce.Color != "" || produce.HexColor() != "#959595" {
		t.Errorf("null color: %q, %q", produce.Color, produce.HexColor())
	}
	if c := l.LabelSettings[0].Label.HexColor(); c != "#2196f3" {
		t.Errorf("color = %q", c)
	}
	// The potatoes have no label of their own; their food's is used, as the server does.
	if l.Items[1].Label == nil || l.Items[1].Label.ID != "lab-produce" {
		t.Errorf("label from the food: %+v", l.Items[1].Label)
	}
}

// A check sends the item back as it was fetched, every field, with only "checked" changed: Mealie's PUT
// replaces the item, and a minimal body would reset its amount, food and label.
func TestSetCheckedEchoesTheWholeItem(t *testing.T) {
	var l List
	if err := json.Unmarshal(fixture(t, "list.json"), &l); err != nil {
		t.Fatal(err)
	}
	var method, path, auth string
	var sent []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		sent = nil
		json.Unmarshal(b, &sent)
		w.Write([]byte(`{"createdItems":[],"updatedItems":[],"deletedItems":[]}`))
	}))
	defer srv.Close()
	c := Client{URL: srv.URL, Token: "t"}
	for _, it := range l.Items[:2] {
		var orig map[string]any
		json.Unmarshal(it.raw, &orig)
		if err := c.SetChecked(context.Background(), it, true); err != nil {
			t.Fatal(err)
		}
		if method != "PUT" || path != "/api/households/shopping/items" || auth != "Bearer t" {
			t.Errorf("request: %s %s %q", method, path, auth)
		}
		if len(sent) != 1 || sent[0]["checked"] != true {
			t.Fatalf("sent %+v", sent)
		}
		orig["checked"] = true
		if !reflect.DeepEqual(sent[0], orig) {
			t.Errorf("sent body differs from the item:\n got %v\nwant %v", sent[0], orig)
		}
	}
	// And back: the unchecked body is again the original.
	if err := c.SetChecked(context.Background(), l.Items[5], false); err != nil || sent[0]["checked"] != false {
		t.Errorf("uncheck: %v %+v", err, sent)
	}
	if err := c.SetChecked(context.Background(), Item{ID: "x"}, true); err != ErrUnreadable {
		t.Errorf("an item with no stored form must not be sent: %v", err)
	}
}

func TestGroupLikeMealie(t *testing.T) {
	var l List
	if err := json.Unmarshal(fixture(t, "list.json"), &l); err != nil {
		t.Fatal(err)
	}
	groups, done := l.Group()
	var names []string
	for _, g := range groups {
		if g.Label == nil {
			names = append(names, "-")
		} else {
			names = append(names, g.Label.Name)
		}
	}
	// No label first, then the list's labels by position (Produce 0, Dairy 1), then the others by name.
	if want := []string{"-", "Produce", "Dairy", "alpha", "Zeta"}; !reflect.DeepEqual(names, want) {
		t.Errorf("groups = %v, want %v", names, want)
	}
	ids := func(items []Item) (out []string) {
		for _, it := range items {
			out = append(out, it.ID)
		}
		return
	}
	// Within a group: position, then created. The unlabeled i4 (position 5) comes after i5 (0).
	if got := ids(groups[0].Items); !reflect.DeepEqual(got, []string{"i5", "i4"}) {
		t.Errorf("no label: %v", got)
	}
	if got := ids(groups[1].Items); !reflect.DeepEqual(got, []string{"i2", "i1"}) {
		t.Errorf("produce: %v", got)
	}
	if got := ids(done); !reflect.DeepEqual(got, []string{"i6"}) {
		t.Errorf("done: %v", got)
	}
}
