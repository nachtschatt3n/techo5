package mealie

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// Label is a shopping label ("Produce"), with the color Mealie draws it in.
type Label struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// HexColor is the label's color as "#rrggbb", or the grey Mealie uses when it has none.
func (l Label) HexColor() string {
	if l.Color == "" {
		return "#959595"
	}
	return l.Color
}

// LabelSetting is where a label sits in one list: Mealie lets each list order its labels.
type LabelSetting struct {
	LabelID  string `json:"labelId"`
	Position int    `json:"position"`
	Label    Label  `json:"label"`
}

// ListSummary is a shopping list without its items.
type ListSummary struct {
	ID string `json:"id"`
	// Name is empty when the list has none (Mealie allows that); the screen says "Shopping list".
	Name          string         `json:"name"`
	LabelSettings []LabelSetting `json:"labelSettings"`
}

// List is a shopping list with all its items.
type List struct {
	ListSummary
	Items []Item `json:"listItems"`
}

// Item is one line of a shopping list. The server's whole object is kept in raw, because checking an
// item sends it back (Mealie's PUT replaces the item, and a body with only the check would reset the
// amount, the food and the label).
type Item struct {
	ID        string  `json:"id"`
	ListID    string  `json:"shoppingListId"`
	Checked   bool    `json:"checked"`
	Position  int     `json:"position"`
	Quantity  float64 `json:"quantity"`
	Note      string  `json:"note"`
	Display   string  `json:"display"`
	Label     *Label  `json:"label"`
	CreatedAt string  `json:"createdAt"`
	Food      *struct {
		Name  string `json:"name"`
		Label *Label `json:"label"`
	} `json:"food"`

	raw json.RawMessage
}

// UnmarshalJSON decodes the item and keeps the bytes it came from.
func (it *Item) UnmarshalJSON(b []byte) error {
	type plain Item
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*it = Item(p)
	it.raw = append(json.RawMessage(nil), b...)
	// The server fills an item's label from its food; this does it for one it did not.
	if it.Label == nil && it.Food != nil && it.Food.Label != nil {
		it.Label = it.Food.Label
	}
	return nil
}

// Text is what the screen shows for the item: the server's own line with its fractions made plain, or
// the note when there is no line. It is empty for an item with neither, which a screen skips.
func (it Item) Text() string {
	if t := Normalize(it.Display); t != "" {
		return t
	}
	return strings.TrimSpace(it.Note)
}

// Lists is the household's shopping lists, oldest first (the first is the default one).
func (c Client) Lists(ctx context.Context) ([]ListSummary, error) {
	var out struct {
		Items []ListSummary `json:"items"`
	}
	q := url.Values{"perPage": {"-1"}, "orderBy": {"createdAt"}, "orderDirection": {"asc"}}
	if err := c.get(ctx, "/api/households/shopping/lists", q, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// List is one shopping list with its items.
func (c Client) List(ctx context.Context, id string) (List, error) {
	var out List
	err := c.get(ctx, "/api/households/shopping/lists/"+url.PathEscape(id), nil, &out)
	return out, err
}

// SetChecked checks or unchecks an item. The body is the item as it was fetched with only "checked"
// changed. The server may merge an unchecked item into an identical one, so the caller reads the list
// again afterward instead of trusting its own copy.
func (c Client) SetChecked(ctx context.Context, it Item, checked bool) error {
	if len(it.raw) == 0 {
		// An item built by hand has no stored form, and a minimal body would clobber the real one.
		return ErrUnreadable
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(it.raw, &obj); err != nil {
		return ErrUnreadable
	}
	obj["checked"] = json.RawMessage(map[bool]string{true: "true", false: "false"}[checked])
	return c.do(ctx, http.MethodPut, "/api/households/shopping/items", nil, true, []map[string]json.RawMessage{obj}, nil, bodyMost)
}

// Group is the unchecked items of one label, in the order Mealie's own page shows them. Label is nil for
// the items without one.
type Group struct {
	Label *Label
	Items []Item
}

// Group sorts a list the way Mealie's page does. Unchecked items go in groups by label: the items
// without a label first, then the labels in the order the list sets them, then any other label by name;
// within a group by position, then by when they were added. The checked items come back apart, in the
// same order, for the Done block at the bottom.
func (l List) Group() (groups []Group, done []Item) {
	rank := map[string]int{}
	settings := append([]LabelSetting(nil), l.LabelSettings...)
	sort.SliceStable(settings, func(i, j int) bool { return settings[i].Position < settings[j].Position })
	for i, s := range settings {
		id := s.LabelID
		if id == "" {
			id = s.Label.ID
		}
		if _, ok := rank[id]; !ok {
			rank[id] = i
		}
	}
	by := map[string]*Group{}
	var none *Group
	var keys []string
	for _, it := range l.Items {
		if it.Checked {
			done = append(done, it)
			continue
		}
		if it.Label == nil || it.Label.ID == "" && it.Label.Name == "" {
			if none == nil {
				none = &Group{}
			}
			none.Items = append(none.Items, it)
			continue
		}
		key := it.Label.ID
		if key == "" {
			key = "name:" + it.Label.Name
		}
		g := by[key]
		if g == nil {
			lab := *it.Label
			g = &Group{Label: &lab}
			by[key] = g
			keys = append(keys, key)
		}
		g.Items = append(g.Items, it)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		ri, iok := rank[keys[i]]
		rj, jok := rank[keys[j]]
		switch {
		case iok && jok:
			return ri < rj
		case iok != jok:
			return iok
		}
		a, b := strings.ToLower(by[keys[i]].Label.Name), strings.ToLower(by[keys[j]].Label.Name)
		if a != b {
			return a < b
		}
		return keys[i] < keys[j]
	})
	if none != nil {
		groups = append(groups, *none)
	}
	for _, k := range keys {
		groups = append(groups, *by[k])
	}
	for i := range groups {
		inOrder(groups[i].Items)
	}
	inOrder(done)
	return groups, done
}

func inOrder(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Position != items[j].Position {
			return items[i].Position < items[j].Position
		}
		return items[i].CreatedAt < items[j].CreatedAt
	})
}
