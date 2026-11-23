package mealie

import (
	"errors"
	"net/url"
	"strings"
)

// ParseURL turns what was typed on the setup page into the address the client uses: http or https, a
// host, an optional port and an optional sub-path (a reverse proxy may serve Mealie under one). A
// trailing slash, a query and a fragment are dropped, and an address with a user name or password in it
// is refused, since the token is the only credential and it does not belong in a URL.
func ParseURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("no address")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", errors.New("not an address")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", errors.New("the address starts with http:// or https://")
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", errors.New("no host in the address")
	}
	if u.User != nil {
		return "", errors.New("no user name or password in the address")
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + path, nil
}
