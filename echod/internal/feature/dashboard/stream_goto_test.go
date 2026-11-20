//go:build !dot

package dashboard

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/flynn/noise"

	"github.com/HuskerMinion/techo5/echod/internal/config"
)

// helloDashcast is a dashcast server that takes every connection and keeps it, sending nothing: the
// hello of each goes to hellos, and drop closes the last one, as a connection lost.
func helloDashcast(t *testing.T, key string) (addr string, hellos <-chan map[string]any, drop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan map[string]any, 8)
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			go func() {
				hs, err := noise.NewHandshakeState(noise.Config{
					CipherSuite: suite(), Random: rand.Reader, Pattern: noise.HandshakeNN, Initiator: false,
					Prologue: []byte(prologue), PresharedKey: psk(key), PresharedKeyPlacement: 0,
				})
				if err != nil {
					return
				}
				first, err := readFrame(c)
				if err != nil {
					return
				}
				if _, _, _, err := hs.ReadMessage(nil, first); err != nil {
					return
				}
				reply, fromDevice, toDevice, err := hs.WriteMessage(nil, nil)
				if err != nil || writeFrame(c, reply) != nil {
					return
				}
				r := bufio.NewReader(&secureConn{Conn: c, send: toDevice, recv: fromDevice})
				line, err := r.ReadBytes('\n')
				if err != nil {
					return
				}
				var h map[string]any
				_ = json.Unmarshal(line, &h)
				got <- h
				for { // what else the device says, until the connection goes
					if _, err := r.ReadBytes('\n'); err != nil {
						return
					}
				}
			}()
		}
	}()
	drop = func() {
		mu.Lock()
		defer mu.Unlock()
		if len(conns) > 0 {
			conns[len(conns)-1].Close()
		}
	}
	return ln.Addr().String(), got, drop
}

// A deck button for a streamed dashboard shows the view it names, even when the page is already at
// that path: a tap on the streamed page takes the browser to another view without the device knowing,
// and the stream, or dashcast's tab parked after it, came back there instead. Each GoTo
// connects again and asks dashcast to go to the path ("go"); a connection that is not one - the first,
// one lost and made again - picks the page up where it was. Drawn, the kept path is where the page
// is, and the same path is left alone.
func TestGoToAsksAStreamedPageForItsView(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	const key = "a-long-enough-test-key"
	addr, hellos, drop := helloDashcast(t, key)
	if err := config.Set().Dashboard().Server(addr, key); err != nil {
		t.Fatal(err)
	}
	if err := config.Set().Dashboard().Mode(config.DashboardStreamed); err != nil {
		t.Fatal(err)
	}
	if err := config.Set().Dashboard().Path("dash/a"); err != nil {
		t.Fatal(err)
	}
	f := pickerFeature() // SetPath lists the dashboards
	defer f.Close()
	next := func(what, path string, goes bool) {
		t.Helper()
		select {
		case h := <-hellos:
			if h["path"] != path || (h["go"] == true) != goes {
				t.Errorf("%s: hello %v, want path %s and go %v", what, h, path, goes)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: no connection", what)
		}
	}
	none := func(what string) {
		t.Helper()
		select {
		case h := <-hellos:
			t.Errorf("%s: connected again: %v", what, h)
		case <-time.After(300 * time.Millisecond):
		}
	}

	f.Stream(480, 320)
	next("opened", "/dash/a", false)
	f.Stream(480, 320)
	none("asked for again")

	if err := f.GoTo("dash/a"); err != nil {
		t.Fatal(err)
	}
	f.Stream(480, 320)
	next("a button for the path it is at", "/dash/a", true)

	if err := f.GoTo("/dash/b/"); err != nil {
		t.Fatal(err)
	}
	if p := config.Get().Dashboard.Path; p != "dash/b" {
		t.Errorf("the path kept is %q", p)
	}
	f.Stream(480, 320)
	next("a button for another path", "/dash/b", true)

	drop()
	next("the connection lost and made again", "/dash/b", false)

	f.Close()
	f.Stream(480, 320)
	next("opened again by hand", "/dash/b", false)

	// Drawn: the same path leaves the page as it is, and another one is kept as SetPath keeps it.
	if err := config.Set().Dashboard().Mode(config.DashboardDrawn); err != nil {
		t.Fatal(err)
	}
	f.Close()
	s := &session{f: f}
	f.mu.Lock()
	f.drawn, f.drawnPath = s, "dash/b"
	f.mu.Unlock()
	if err := f.GoTo("dash/b"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	kept, goTo := f.drawn == s, f.goTo
	f.mu.Unlock()
	if !kept || goTo {
		t.Errorf("drawn, a button for the path it is at: session kept %v, go asked for %v", kept, goTo)
	}
}
