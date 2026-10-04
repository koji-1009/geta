package geta_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/koji-1009/geta"
)

// conn is a ResponseWriter whose Write fails from its failAt-th call on, or
// from the call that would bring what it took to failAtByte bytes, and whose
// Flush fails from its flushFailAt-th call on (never when zero). It records
// the statuses written, 1xx included, and what it took.
type conn struct {
	h             http.Header
	codes         []int
	body          bytes.Buffer
	writes        int
	failAt        int
	failAtByte    int
	flushes       int
	flushFailAt   int
	writeDeadline bool
}

func newConn() *conn { return &conn{h: http.Header{}} }

func (c *conn) Header() http.Header  { return c.h }
func (c *conn) WriteHeader(code int) { c.codes = append(c.codes, code) }
func (c *conn) Write(b []byte) (int, error) {
	c.writes++
	if c.failAt > 0 && c.writes >= c.failAt || c.failAtByte > 0 && c.body.Len()+len(b) >= c.failAtByte {
		return 0, errors.New("connection reset")
	}
	return c.body.Write(b)
}
func (c *conn) FlushError() error {
	c.flushes++
	if c.flushFailAt > 0 && c.flushes >= c.flushFailAt {
		return errors.New("connection reset")
	}
	return nil
}
func (c *conn) SetWriteDeadline(time.Time) error { c.writeDeadline = true; return nil }

// serveOn serves req on w, returning what ServeHTTP panicked with.
func serveOn(a http.Handler, w http.ResponseWriter, req *http.Request) (p any) {
	defer func() { p = recover() }()
	a.ServeHTTP(w, req)
	return nil
}

func get200(w http.ResponseWriter, a http.Handler) any {
	return serveOn(a, w, httptest.NewRequest(http.MethodGet, "/x", nil))
}

// A JSON body past MaxResponseBuffer whose writes fail once it is committed
// — the first, or only the last — is logged at Info and aborts, whether the
// buffer held part of it when it committed (a buffer above the encoder's
// first write) or held none (one below).
func TestALargeBodyWriteFailureAborts(t *testing.T) {
	for _, limit := range []int{16, 6 << 10} {
		build := func() (*geta.App, *syncBuffer) {
			log, buf := logger()
			return largeApp(t, largeOf(64<<10, false), limit, geta.WithLogger(log)), buf
		}
		a, _ := build()
		whole := newConn()
		if p := get200(whole, a); p != nil || whole.writes < 2 {
			t.Fatalf("limit %d: %v, %d writes", limit, p, whole.writes)
		}
		// The first write, or the one that would complete the body.
		for _, failAt := range []struct{ write, byte int }{{1, 0}, {0, whole.body.Len()}} {
			a, buf := build()
			c := newConn()
			c.failAt, c.failAtByte = failAt.write, failAt.byte
			if p := get200(c, a); p != http.ErrAbortHandler || len(c.codes) != 1 || c.codes[0] != 200 {
				t.Errorf("limit %d, fail at %d: %v %v", limit, failAt, p, c.codes)
			}
			if l := buf.String(); !strings.Contains(l, "level=INFO") || !strings.Contains(l, "connection reset") {
				t.Errorf("limit %d, fail at %d: %q", limit, failAt, l)
			}
		}
	}
}

// A response buffer larger than a pooled one holds and sends a body of its
// size whole, with its Content-Length.
func TestAResponseBufferPastThePoolSize(t *testing.T) {
	limits := geta.DefaultLimits
	limits.MaxResponseBuffer = 256 << 10
	body := strings.Repeat("a", 100<<10)
	a, err := geta.New(one("/x", get(textHandler(body))), geta.WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	r := do(t, a, "GET", "/x")
	if r.Code != 200 || r.Header().Get("Content-Length") != itoa(len(body)+11) || r.Body.Len() != len(body)+11 {
		t.Fatal(r.Code, r.Header())
	}
}

// ptrText writes itself by a pointer method: MarshalText.
type ptrText string

func (p *ptrText) MarshalText() ([]byte, error) { return []byte("t-" + string(*p)), nil }
func (p *ptrText) UnmarshalText(b []byte) error { *p = ptrText(b); return nil }

// ptrAppend writes itself by a pointer method: AppendText, which
// encoding/json/v2 prefers.
type ptrAppend string

func (p *ptrAppend) AppendText(b []byte) ([]byte, error) { return append(b, "a-"+string(*p)...), nil }
func (p *ptrAppend) MarshalText() ([]byte, error)        { return []byte("never"), nil }
func (p *ptrAppend) UnmarshalText(b []byte) error        { *p = ptrAppend(b); return nil }

type ptrHeaders struct {
	T    ptrText   `header:"X-T"`
	A    ptrAppend `header:"X-A"`
	Body ok        `body:"json"`
}

// A header whose type writes itself by a pointer method is written by it,
// AppendText before MarshalText, as encoding/json/v2 writes it in a body.
func TestHeadersOfPointerTextMethods(t *testing.T) {
	a := accepts(t, one("/x", get(func(context.Context, *empty) (*ptrHeaders, error) {
		return &ptrHeaders{T: "v", A: "w", Body: ok{true}}, nil
	})))
	r := do(t, a, "GET", "/x")
	if r.Code != 200 || r.Header().Get("X-T") != "t-v" || r.Header().Get("X-A") != "a-w" {
		t.Fatal(r.Code, r.Header())
	}
}

// A raw reader's body past MaxResponseBuffer whose first write, or a later
// one, fails is logged at Info and aborts.
func TestARawReaderWriteFailureAborts(t *testing.T) {
	limits := geta.DefaultLimits
	limits.MaxResponseBuffer = 16
	for _, failAt := range []int{1, 2} {
		log, buf := logger()
		a, err := geta.New(one("/x", get(func(context.Context, *empty) (*rsReaderOut, error) {
			return &rsReaderOut{Body: strings.NewReader(strings.Repeat("r", 100))}, nil
		})), geta.WithLimits(limits), geta.WithLogger(log))
		if err != nil {
			t.Fatal(err)
		}
		c := newConn()
		c.failAt = failAt
		if p := get200(c, a); p != http.ErrAbortHandler {
			t.Errorf("fail at %d: %v", failAt, p)
		}
		if l := buf.String(); !strings.Contains(l, "level=INFO") || !strings.Contains(l, "connection reset") {
			t.Errorf("fail at %d: %q", failAt, l)
		}
	}
}

func twoEvents(context.Context, *empty) (*geta.Stream[change], error) {
	return &geta.Stream[change]{Events: func(yield func(change) bool) {
		_ = yield(change{"a", "1"}) && yield(change{"b", "2"})
	}}, nil
}

// A stream whose event write, or whose flush after it, fails ends there: the
// handler returns, the source stopped.
func TestAStreamEndsWhenItsWriterFails(t *testing.T) {
	a := accepts(t, one("/x", get(twoEvents)))
	c := newConn()
	c.failAt = 1
	if p := get200(c, a); p != nil || c.writes != 1 {
		t.Fatalf("write: %v, %d writes", p, c.writes)
	}
	c = newConn()
	c.flushFailAt = 2 // the first flush sends the headers
	if p := get200(c, a); p != nil || c.writes != 1 || c.flushes != 2 {
		t.Fatalf("flush: %v, %d writes, %d flushes", p, c.writes, c.flushes)
	}
}

// A stream whose keep-alive cannot be written ends at it.
func TestAStreamEndsWhenItsKeepAliveFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		silent := func(ctx context.Context, _ *empty) (*geta.Stream[change], error) {
			return &geta.Stream[change]{Events: feed(ctx, make(chan change)), KeepAlive: time.Second}, nil
		}
		a := accepts(t, one("/x", get(silent)))
		c := newConn()
		c.failAt = 1
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		start := time.Now()
		if p := serveOn(a, c, httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ctx)); p != nil || time.Since(start) != time.Second || c.writes != 1 {
			t.Fatalf("%v after %v, %d writes", p, time.Since(start), c.writes)
		}
		cancel() // the source reads the request's context
		synctest.Wait()
	})
}

// hijacker hands out a connection whose peer has gone.
type hijacker struct {
	*conn
	server net.Conn
}

func (h *hijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.server, bufio.NewReadWriter(bufio.NewReader(h.server), bufio.NewWriter(h.server)), nil
}

func upgradeRoute(u func() *geta.Upgrade) geta.Table {
	return one("/x", geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, func(context.Context, *empty) (*geta.Upgrade, error) { return u(), nil }, geta.Doc{})})
}

// An upgrade whose 101 cannot be written serves nothing: Serve never runs.
func TestAnUpgradeWhoseSwitchCannotBeWrittenServesNothing(t *testing.T) {
	served := false
	a := accepts(t, upgradeRoute(func() *geta.Upgrade {
		return &geta.Upgrade{Protocol: "chat", Serve: func(net.Conn, *bufio.ReadWriter) { served = true }}
	}))
	server, client := net.Pipe()
	client.Close()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "chat")
	if p := serveOn(a, &hijacker{conn: newConn(), server: server}, req); p != nil || served {
		t.Fatalf("%v, served %v", p, served)
	}
}

// The writer Accept hands a library unwraps, so http.ResponseController
// reaches the server's writer through it.
func TestAcceptsWriterUnwrapsForAResponseController(t *testing.T) {
	var err error
	a := accepts(t, upgradeRoute(func() *geta.Upgrade {
		return &geta.Upgrade{Protocol: "chat", Accept: func(w http.ResponseWriter, r *http.Request) {
			err = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Second))
			w.WriteHeader(http.StatusBadRequest)
		}}
	}))
	c := newConn()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "chat")
	if p := serveOn(a, c, req); p != nil || err != nil || !c.writeDeadline {
		t.Fatalf("%v %v, the deadline reached the writer: %v", p, err, c.writeDeadline)
	}
}

type reShape interface{ isReShape() }

type reDot struct {
	Kind string `json:"kind"`
}

func (reDot) isReShape() {}

type reInner struct {
	S reShape `json:"s"`
}

type reOuter struct {
	In *reInner `json:"in,omitzero"`
}

// A required sealed value left nil inside an optional member a pointer
// holds is a defect, named at its path.
func TestANilSealedValueBehindAnOptionalPointerIsADefect(t *testing.T) {
	log, buf := logger()
	a, err := geta.New(one("/x", get(func(context.Context, *empty) (*reOuter, error) { return &reOuter{In: &reInner{}}, nil })),
		geta.WithUnion(geta.Sealed[reShape]("kind", geta.Case[reDot]("dot"))), geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	if r := do(t, a, "GET", "/x"); r.Code != 500 || !strings.Contains(buf.String(), "$.in.s: a required geta_test.reShape is nil") {
		t.Fatal(r.Code, buf.String())
	}
}

// inner is a scoped middleware that runs fn on the writer ETag hands on.
func inner(fn func(w http.ResponseWriter)) geta.Middleware {
	return geta.Ordered(geta.OrderAuthorize, func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fn(w) })
	})
}

// The buffer ETag and Compress hold a response in: an informational status
// goes out at once; a second final status is dropped; a flush, by
// http.Flusher too and before anything was written, sends what is held and
// passes the rest through, a later status included; a flush the connection
// does not take reports it.
func TestTheHoldingBufferPassesThroughWhatStreams(t *testing.T) {
	serve := func(fn func(w http.ResponseWriter)) *conn {
		a := accepts(t, one("/x", get(okHandler), geta.Scope{geta.ETag(), inner(fn)}))
		c := newConn()
		if p := get200(c, a); p != nil {
			t.Fatal(p)
		}
		return c
	}
	c := serve(func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusCreated)
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, "held")
	})
	if len(c.codes) != 2 || c.codes[0] != 103 || c.codes[1] != 201 || c.body.String() != "held" {
		t.Fatalf("held: %v %q", c.codes, c.body.String())
	}
	c = serve(func(w http.ResponseWriter) {
		io.WriteString(w, "a")
		w.(http.Flusher).Flush()
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, "b")
	})
	if len(c.codes) != 2 || c.codes[0] != 200 || c.codes[1] != 202 || c.body.String() != "ab" || c.h.Get("ETag") != "" {
		t.Fatalf("flushed: %v %q %v", c.codes, c.body.String(), c.h)
	}
	c = serve(func(w http.ResponseWriter) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
		}
	})
	if len(c.codes) != 1 || c.codes[0] != 200 || c.flushes != 1 {
		t.Fatalf("flushed first: %v %d", c.codes, c.flushes)
	}
	var err error
	a := accepts(t, one("/x", get(okHandler), geta.Scope{geta.ETag(), inner(func(w http.ResponseWriter) {
		io.WriteString(w, "a")
		err = http.NewResponseController(w).Flush()
	})}))
	c = newConn()
	c.failAt = 1
	get200(c, a)
	if err == nil || c.flushes != 0 {
		t.Fatalf("a held body the connection refused: %v, %d flushes", err, c.flushes)
	}
}

// The writer Recover and AccessLog observe through is an http.Flusher, and
// its flush marks the response streamed.
func TestTheObservingWriterFlushes(t *testing.T) {
	log, buf := logger()
	a, err := geta.New(withRoot(one("/x", get(okHandler)), geta.AccessLog(log), geta.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "a")
			w.(http.Flusher).Flush()
		})
	})))
	if err != nil {
		t.Fatal(err)
	}
	c := newConn()
	if p := get200(c, a); p != nil || c.flushes != 1 || c.body.String() != "a" {
		t.Fatal(p, c.flushes, c.body.String())
	}
	if !strings.Contains(buf.String(), "streaming=true") {
		t.Fatal(buf.String())
	}
}

// A body a middleware behind CORS writes without a status is completed as
// any response: Vary: Origin and the allowed origin go out with it.
func TestCORSCompletesABodyWrittenWithoutAStatus(t *testing.T) {
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.CORS(geta.AllowOrigins("https://a.example")), geta.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "a") })
	})))
	r := do(t, a, "GET", "/x", "Origin", "https://a.example")
	if r.Code != 200 || r.Body.String() != "a" || r.Header().Get("Vary") != "Origin" || r.Header().Get("Access-Control-Allow-Origin") != "https://a.example" {
		t.Fatal(r.Code, r.Header())
	}
}
