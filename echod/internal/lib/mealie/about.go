package mealie

import (
	"context"
	"net/url"
	"strings"
)

// About is the version the server reports ("3.30.0", without the "v"), read without the token: it
// shows whether the address is a Mealie at all before the token is tried.
func (c Client) About(ctx context.Context) (string, error) {
	var out struct {
		Version string `json:"version"`
	}
	if err := c.do(ctx, "GET", "/api/app/about", nil, false, nil, &out, bodyMost); err != nil {
		return "", err
	}
	return strings.TrimPrefix(strings.TrimSpace(out.Version), "v"), nil
}

// Check asks for one shopping list with the token: the cheapest call that fails for a bad token.
func (c Client) Check(ctx context.Context) error {
	return c.get(ctx, "/api/households/shopping/lists", url.Values{"perPage": {"1"}}, nil)
}

// TooOld is whether the version is one this code does not talk to: before 2.0, where the shopping and
// meal plan routes were not yet under /api/households. A version that cannot be read is accepted, since
// a development build or a fork names itself freely.
func TooOld(version string) bool {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	n := 0
	digits := 0
	for digits < len(v) && v[digits] >= '0' && v[digits] <= '9' && digits < 6 {
		n = n*10 + int(v[digits]-'0')
		digits++
	}
	if digits == 0 || (digits < len(v) && !strings.ContainsRune(".-+", rune(v[digits]))) {
		return false
	}
	return n < 2
}
