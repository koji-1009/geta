package geta_test

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// Upgrade.Accept hands the handshake to a library that performs it on an
// http.ResponseWriter. The two libraries below do what the WebSocket
// libraries do, on a line protocol: hijackFirst asserts http.Hijacker on the
// writer it is given and writes the 101 on the connection (gorilla/websocket's
// Upgrader); headerFirst finds the hijacker by unwrapping, writes the 101
// through the writer, then hijacks (coder/websocket's Accept).

// libHijackFirst is gorilla/websocket's way.
func libHijackFirst(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter, bool) {
	h, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "the writer is not an http.Hijacker", http.StatusNotImplemented)
		return nil, nil, false
	}
	conn, rw, err := h.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, nil, false
	}
	rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: echo\r\nConnection: Upgrade\r\nX-Lib: hijack-first\r\n\r\n")
	if rw.Flush() != nil {
		conn.Close()
		return nil, nil, false
	}
	return conn, rw, true
}

// libHeaderFirst is coder/websocket's way.
func libHeaderFirst(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter, bool) {
	var h http.Hijacker
	for rw := w; h == nil; {
		switch t := rw.(type) {
		case http.Hijacker:
			h = t
		case interface{ Unwrap() http.ResponseWriter }:
			rw = t.Unwrap()
		default:
			http.Error(w, "no http.Hijacker", http.StatusNotImplemented)
			return nil, nil, false
		}
	}
	w.Header().Set("Upgrade", "echo")
	w.Header().Set("Connection", "Upgrade")
	w.Header().Set("X-Lib", "header-first")
	w.WriteHeader(http.StatusSwitchingProtocols)
	conn, rw, err := h.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, nil, false
	}
	return conn, rw, true
}

type libAccept func(http.ResponseWriter, *http.Request) (net.Conn, *bufio.ReadWriter, bool)

// libEcho is an operation that switches with lib and echoes one line,
// recording whether the request still had a deadline once switched.
func libEcho(lib libAccept, deadline *atomic.Bool) func(context.Context, *struct{}) (*geta.Upgrade, error) {
	return func(context.Context, *struct{}) (*geta.Upgrade, error) {
		return &geta.Upgrade{Protocol: "echo", Accept: func(w http.ResponseWriter, r *http.Request) {
			conn, rw, ok := lib(w, r)
			if !ok {
				return
			}
			defer conn.Close()
			_, has := r.Context().Deadline()
			deadline.Store(has)
			line, _ := rw.ReadString('\n')
			rw.WriteString("echo: " + line)
			rw.Flush()
		}}, nil
	}
}

type libOK struct {
	OK bool `json:"ok"`
}

func libPlain(context.Context, *struct{}) (*libOK, error) { return &libOK{true}, nil }

func libVerifier(r *http.Request) (context.Context, error) {
	if r.Header.Get("Authorization") == "Bearer ok" {
		return r.Context(), nil
	}
	return nil, geta.ErrUnauthenticated
}

// libTable puts every geta writer and both shedding limits in front of the
// switch: the access log, CORS, Recover, one request in flight, a deadline,
// gzip, entity tags, and the gate.
func libTable(lib libAccept, deadline *atomic.Bool) geta.Table {
	log := slog.New(slog.DiscardHandler)
	return geta.Table{
		Root: geta.Scope{
			geta.AccessLog(log),
			geta.CORS(geta.AllowOrigins("*")),
			geta.Recover(log),
			geta.ConcurrencyLimit(1),
			geta.Timeout(time.Minute),
			geta.Gzip(),
			geta.ETag(),
			geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer}, Verifiers: map[string]geta.Verifier{"bearer": libVerifier}}),
		},
		Routes: []geta.Entry{
			{Path: "/ws", Route: geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, libEcho(lib, deadline), geta.Doc{})}},
			{Path: "/x", Route: geta.Route{Get: geta.Op(http.StatusOK, libPlain, geta.Doc{})}},
		},
	}
}

// Through every middleware's writer, a library finds the hijacker its way
// and switches; the switch frees the request's slot (the next request is
// not shed by ConcurrencyLimit(1)) and lifts its deadline.
func TestUpgradeAcceptHandsTheHandshakeToALibrary(t *testing.T) {
	for name, lib := range map[string]libAccept{"hijack-first": libHijackFirst, "header-first": libHeaderFirst} {
		t.Run(name, func(t *testing.T) {
			var deadline atomic.Bool
			deadline.Store(true)
			c := getatest.New(t, libTable(lib, &deadline))
			if u := c.Upgrade("/ws", "echo"); u.Switched || u.Response.Status != http.StatusUnauthorized {
				t.Fatalf("anonymous: %v %d", u.Switched, u.Response.Status)
			}
			c = c.Bearer("ok")
			u := c.Upgrade("/ws", "echo")
			if !u.Switched || u.Response.Header.Get("X-Lib") != name || u.Response.Header.Get("Upgrade") != "echo" {
				t.Fatalf("%v %d %v %s", u.Switched, u.Response.Status, u.Response.Header, u.Response.Body)
			}
			// The library holds the connection open, waiting for a line.
			if res := c.Get("/x"); res.Status != http.StatusOK {
				t.Fatalf("the switched connection holds the only slot: %d", res.Status)
			}
			io.WriteString(u.Conn, "hi\n")
			line, _ := bufio.NewReader(u.Conn).ReadString('\n')
			if line != "echo: hi\n" {
				t.Fatalf("%q", line)
			}
			if deadline.Load() {
				t.Error("the deadline outlived the switch")
			}
		})
	}
}

// geta answers a request that does not ask to switch 426 before the library
// sees it.
func TestUpgradeAcceptIsNotCalledWithoutAnUpgradeRequest(t *testing.T) {
	var called atomic.Bool
	h := func(context.Context, *struct{}) (*geta.Upgrade, error) {
		return &geta.Upgrade{Protocol: "echo", Accept: func(w http.ResponseWriter, r *http.Request) {
			called.Store(true)
			w.WriteHeader(http.StatusSwitchingProtocols)
		}}, nil
	}
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{{Path: "/ws", Route: geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, h, geta.Doc{})}}}})
	for _, proto := range []string{"", "other"} {
		u := c.Upgrade("/ws", proto)
		if u.Switched || u.Response.Status != http.StatusUpgradeRequired || u.Response.Header.Get("Upgrade") != "echo" {
			t.Fatalf("%q: %d %v", proto, u.Response.Status, u.Response.Header)
		}
	}
	if called.Load() {
		t.Fatal("Accept ran for a request that does not ask to switch")
	}
}

// Connection and Upgrade are lists of tokens (RFC 9110 §7.6.1, §7.8): an
// element is trimmed of spaces and tabs alone, and compared in ASCII case
// alone. A no-break space beside "upgrade", or a protocol written with the
// long s or the Kelvin sign, which Unicode folds to "s" and "k", asks for no
// switch: 426, and Accept does not run.
func TestUpgradeTokensAreReadAsTheGrammarWritesThem(t *testing.T) {
	var called atomic.Bool
	h := func(context.Context, *struct{}) (*geta.Upgrade, error) {
		return &geta.Upgrade{Protocol: "websocket", Accept: func(w http.ResponseWriter, r *http.Request) {
			called.Store(true)
			w.WriteHeader(http.StatusSwitchingProtocols)
		}}, nil
	}
	a := accepts(t, geta.Table{Routes: []geta.Entry{{Path: "/ws", Route: geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, h, geta.Doc{})}}}})
	for _, c := range []struct {
		connection, upgrade string
		switched            bool
	}{
		{"upgrade\xc2\xa0", "websocket", false},
		{"keep-alive,\xc2\xa0upgrade", "websocket", false},
		{"Upgrade", "web\xc5\xbfocket", false},
		{"Upgrade", "websoc\xe2\x84\xaaet", false},
		{"Upgrade", "websocket\xc2\xa0", false},
		{"keep-alive, \tUPGRADE ", "h2c, WebSocket/13", true},
	} {
		called.Store(false)
		r := do(t, a, "GET", "/ws", "Connection", c.connection, "Upgrade", c.upgrade)
		if want := map[bool]int{true: 101, false: 426}[c.switched]; r.Code != want || called.Load() != c.switched {
			t.Errorf("Connection %q, Upgrade %q: %d, Accept ran %v", c.connection, c.upgrade, r.Code, called.Load())
		}
	}
}

// A misbuilt Upgrade, and an Accept that neither switches nor answers, are
// defects: a 500. A library's own refusal goes out as the library wrote it.
func TestUpgradeAcceptDefectsAndRefusals(t *testing.T) {
	serve := func(net.Conn, *bufio.ReadWriter) {}
	accept := func(http.ResponseWriter, *http.Request) {}
	for _, tc := range []struct {
		name string
		u    geta.Upgrade
		want int
	}{
		{"neither", geta.Upgrade{Protocol: "echo"}, http.StatusInternalServerError},
		{"both", geta.Upgrade{Protocol: "echo", Accept: accept, Serve: serve}, http.StatusInternalServerError},
		{"no protocol", geta.Upgrade{Accept: accept}, http.StatusInternalServerError},
		// A protocol that is no token is named by no request: never switched.
		{"a protocol with a version", geta.Upgrade{Protocol: "echo/1", Accept: accept}, http.StatusInternalServerError},
		{"a protocol of the Kelvin sign", geta.Upgrade{Protocol: "e\xe2\x84\xaacho", Serve: serve}, http.StatusInternalServerError},
		{"a protocol with a line break", geta.Upgrade{Protocol: "echo\r\nX-Injected: 1", Serve: serve}, http.StatusInternalServerError},
		{"Accept with Header", geta.Upgrade{Protocol: "echo", Accept: accept, Header: http.Header{"X": {"1"}}}, http.StatusInternalServerError},
		{"Accept answers nothing", geta.Upgrade{Protocol: "echo", Accept: accept}, http.StatusInternalServerError},
		{"the library refuses", geta.Upgrade{Protocol: "echo", Accept: func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "bad handshake", http.StatusBadRequest)
		}}, http.StatusBadRequest},
	} {
		h := func(context.Context, *struct{}) (*geta.Upgrade, error) { u := tc.u; return &u, nil }
		a, err := geta.New(geta.Table{Routes: []geta.Entry{{Path: "/ws", Route: geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, h, geta.Doc{})}}}},
			geta.WithLogger(slog.New(slog.DiscardHandler)))
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/ws", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "echo")
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: %d, want %d: %s", tc.name, rec.Code, tc.want, rec.Body)
		}
		if tc.want == http.StatusBadRequest && !strings.Contains(rec.Body.String(), "bad handshake") {
			t.Errorf("%s: %s", tc.name, rec.Body)
		}
	}
}
