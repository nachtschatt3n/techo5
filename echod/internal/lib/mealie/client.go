// Package mealie reads a Mealie server (https://mealie.io), the recipe manager: its shopping lists, its
// meal plan and its recipes, over the REST API with a long-lived token (Mealie 2.0 and later). The only
// thing it writes is the checkmark of a shopping item. Everything the screen shows is decoded here
// tolerantly (every field is optional except the ids, unknown fields are ignored), and the cooking
// helpers that need no network, scaling an amount and finding a time in a step, live here too so they
// test on any machine.
package mealie

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is one server: its address (https://host, a reverse proxy's sub-path kept) and a token.
type Client struct {
	URL, Token string
}

// The errors a screen tells apart. A screen shows a sentence for each and never an error's own text,
// which is why the details of a failure go to the log (and never the token or the address with its query).
var (
	// ErrUnreachable is a server that did not answer, answered something else than an API, or sent this
	// device to another host.
	ErrUnreachable = errors.New("mealie is not reachable")
	// ErrToken is the server refusing the token (401 or 403).
	ErrToken = errors.New("mealie refused the token")
	// ErrNotFound is a list or recipe the server does not have (404).
	ErrNotFound = errors.New("mealie does not have that")
	// ErrUnreadable is an answer this device could not read.
	ErrUnreadable = errors.New("mealie sent something this device could not read")
	// ErrUseHTTPS is an http address the server answered with a redirect to https. The request is not
	// sent again, since the token would go out over a connection that was never meant to carry it.
	ErrUseHTTPS = errors.New("mealie wants the https address")
)

const (
	// bodyMost caps what is read from one answer; a recipe, which carries its comments, gets more.
	bodyMost   = 4 << 20
	recipeMost = 8 << 20
)

var hc = &http.Client{Timeout: 15 * time.Second, CheckRedirect: followSameHost}

// followSameHost lets a request follow a redirect on the same host and scheme (a trailing slash is the
// usual one), and nothing else. A hop to another host would carry the token there; a hop from http to
// https is reported, so the person puts the right address on the setup page. A write is followed only
// when the redirect keeps its method (307 and 308): the client turns a 301 or 302 of a PUT into a GET
// with no body, which the server answers with a list, and the write would never have been made.
func followSameHost(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return ErrUnreachable
	}
	first, prev := via[0], via[len(via)-1]
	if first.Method != http.MethodGet && first.Method != http.MethodHead && req.Method != first.Method {
		return ErrUnreachable
	}
	// The host is a name, which is not case sensitive, and the port is looked at after the scheme: a server
	// on its own port that sends this device to https on the usual one wants the https address.
	if !strings.EqualFold(req.URL.Hostname(), first.URL.Hostname()) {
		return ErrUnreachable
	}
	if req.URL.Scheme != prev.URL.Scheme {
		if prev.URL.Scheme == "http" && req.URL.Scheme == "https" {
			return ErrUseHTTPS
		}
		return ErrUnreachable
	}
	if req.URL.Port() != first.URL.Port() {
		return ErrUnreachable
	}
	return nil
}

// do runs one request and decodes the answer into out, when out is not nil. auth says whether the token
// goes along: the version check and the pictures need none. in, when not nil, is the JSON body.
func (c Client) do(ctx context.Context, method, path string, q url.Values, auth bool, in, out any, most int64) error {
	target := strings.TrimRight(c.URL, "/") + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		// The address on the setup page went through ParseURL, so this is a path that is not one.
		slog.Warn("mealie: a request that could not be made", "path", path)
		return ErrUnreachable
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := hc.Do(req)
	if err != nil {
		switch {
		case errors.Is(err, ErrUseHTTPS):
			return ErrUseHTTPS
		case ctx.Err() != nil:
			return ctx.Err()
		}
		// An error from the transport names the address it dialed, so only its kind is logged.
		slog.Warn("mealie: no answer", "method", method, "path", path, "kind", failKind(err))
		return ErrUnreachable
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, most))
	if err != nil {
		slog.Warn("mealie: an answer that stopped", "method", method, "path", path, "kind", failKind(err))
		return ErrUnreachable
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ErrToken
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	default:
		// The status only: what the server says about it is not for the screen.
		slog.Warn("mealie: an answer that is not one", "method", method, "path", path, "status", resp.StatusCode)
		return ErrUnreachable
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		// What did not fit is for the log; a screen says only that it could not be read.
		slog.Warn("mealie: an answer that could not be read", "method", method, "path", path, "err", err)
		return ErrUnreadable
	}
	return nil
}

// failKind says what went wrong with a request without the address in the error's text.
func failKind(err error) string {
	var dns *net.DNSError
	var op *net.OpError
	var ne net.Error
	switch {
	case errors.As(err, &dns):
		return "name not found"
	case errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case errors.As(err, &op) && op.Err != nil:
		return op.Op + ": " + op.Err.Error()
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return "connection closed"
	}
	return "other"
}

func (c Client) get(ctx context.Context, path string, q url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, q, true, nil, out, bodyMost)
}
