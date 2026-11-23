package mealie

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// serve is a server that answers a path with a fixture, and any other with 404.
func serve(t *testing.T, routes map[string]string) (*httptest.Server, Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture(t, f))
	}))
	t.Cleanup(srv.Close)
	return srv, Client{URL: srv.URL, Token: "tok-123"}
}

func TestClientSendsBearer(t *testing.T) {
	var got, about string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/app/about" {
			about = r.Header.Get("Authorization")
			w.Write(fixture(t, "about.json"))
			return
		}
		got = r.Header.Get("Authorization")
		w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()
	c := Client{URL: srv.URL + "/", Token: "tok-123"}
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer tok-123" {
		t.Errorf("authorization = %q", got)
	}
	if _, err := c.About(context.Background()); err != nil {
		t.Fatal(err)
	}
	if about != "" {
		t.Errorf("the version check sent %q; it needs no token", about)
	}
}

func TestClientMapsErrors(t *testing.T) {
	status := 200
	body := `{"items":[]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	c := Client{URL: srv.URL, Token: "t"}
	for _, tc := range []struct {
		status int
		body   string
		want   error
	}{
		{401, `{"detail":"Could not validate credentials"}`, ErrToken},
		{403, ``, ErrToken},
		{404, ``, ErrNotFound},
		{500, `{"detail":{"message":"Server Error"}}`, ErrUnreachable},
		{502, `<html>bad gateway</html>`, ErrUnreachable},
		{200, `<html>a login page</html>`, ErrUnreadable},
	} {
		status, body = tc.status, tc.body
		_, err := c.Lists(context.Background())
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d: err = %v, want %v", tc.status, err, tc.want)
		}
	}
	srv.Close()
	if _, err := c.Lists(context.Background()); !errors.Is(err, ErrUnreachable) {
		t.Errorf("closed server: err = %v", err)
	}
	if err := (Client{}).Check(context.Background()); !errors.Is(err, ErrUnreachable) {
		t.Errorf("no address: %v", err)
	}
}

func TestClientRefusesOtherHostRedirect(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/api/households/shopping/lists", http.StatusFound)
	}))
	defer srv.Close()
	_, err := Client{URL: srv.URL, Token: "tok-123"}.Lists(context.Background())
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("err = %v", err)
	}
	if hits.Load() != 0 {
		t.Error("the token was sent to another host")
	}
}

func TestClientReportsHTTPSRedirect(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "https://"+r.Host+r.URL.Path, http.StatusMovedPermanently)
	}))
	defer srv.Close()
	_, err := Client{URL: srv.URL, Token: "tok-123"}.Lists(context.Background())
	if !errors.Is(err, ErrUseHTTPS) {
		t.Errorf("err = %v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("the server saw %d requests, want the one", hits.Load())
	}
}

func TestClientFollowsSameHostRedirect(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mealie/api/households/shopping/lists" {
			http.Redirect(w, r, "/mealie/api/households/shopping/lists/", http.StatusMovedPermanently)
			return
		}
		auth = r.Header.Get("Authorization")
		w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()
	if _, err := (Client{URL: srv.URL + "/mealie", Token: "tok-123"}).Lists(context.Background()); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer tok-123" {
		t.Errorf("after the redirect: %q", auth)
	}
}

func TestParseURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://mealie.lan:9000":          "http://mealie.lan:9000",
		"  https://Mealie.Example.com/  ": "https://mealie.example.com",
		"https://home.example.com/meals/": "https://home.example.com/meals",
		"https://h.example.com/a/b?x=1#y": "https://h.example.com/a/b",
	} {
		got, err := ParseURL(in)
		if err != nil || got != want {
			t.Errorf("ParseURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "   ", "mealie.lan", "ftp://mealie.lan", "file:///etc/passwd", "https://", "http://:9000",
		"https://user:pw@mealie.lan", "https://user@mealie.lan", "javascript:alert(1)",
	} {
		if got, err := ParseURL(in); err == nil {
			t.Errorf("ParseURL(%q) = %q, want an error", in, got)
		}
	}
}

func TestAboutVersion(t *testing.T) {
	_, c := serve(t, map[string]string{"/api/app/about": "about.json"})
	v, err := c.About(context.Background())
	if err != nil || v != "3.30.0" {
		t.Errorf("About = %q, %v", v, err)
	}
}

func TestTooOldAcceptsUnparseable(t *testing.T) {
	for v, want := range map[string]bool{
		"3.30.0": false, "v3.30.0": false, "2.0.0": false, "2.0.0-rc1": false, "10.1": false,
		"1.12.0": true, "v1.0.0": true, "0.5": true, "1": true,
		"": false, "nightly": false, "develop-abc123": false, "x.1": false, "1x": false, "v": false,
	} {
		if got := TooOld(v); got != want {
			t.Errorf("TooOld(%q) = %v, want %v", v, got, want)
		}
	}
}

// Nothing a call logs holds the token or the address; an unreachable server, a refused token and an
// answer that cannot be read each log, and the address of a test server is the thing to look for.
func TestNoSecretsInLogs(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(old)

	status, body := 200, ``
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	c := Client{URL: srv.URL, Token: "SECRET-TOKEN-VALUE"}
	ctx := context.Background()
	for _, s := range []struct {
		status int
		body   string
	}{{200, `not json`}, {500, `oops`}, {401, ``}, {404, ``}, {200, `{"items":[]}`}} {
		status, body = s.status, s.body
		c.Lists(ctx)
		c.List(ctx, "abc")
		c.Plan(ctx, mustDate(t, "2026-11-23"), mustDate(t, "2026-11-29"), true)
		c.Recipe(ctx, "lasagne")
		c.Check(ctx)
		c.About(ctx)
		c.SetChecked(ctx, Item{}, true)
	}
	addr := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()
	c.Lists(ctx)
	c.Recipe(ctx, "lasagne")
	c2 := Client{URL: "http://127.0.0.1:1", Token: "SECRET-TOKEN-VALUE"}
	c2.Check(ctx)

	log := buf.String()
	if log == "" {
		t.Fatal("expected some log lines")
	}
	for _, bad := range []string{"SECRET-TOKEN-VALUE", addr, "127.0.0.1", "perPage", "start_date", "http://"} {
		if strings.Contains(log, bad) {
			t.Errorf("the log holds %q:\n%s", bad, log)
		}
	}
}
