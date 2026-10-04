package geta_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

type change struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func (c change) EventName() string { return c.Kind }
func (c change) EventID() string   { return c.ID }

// feed yields from a channel until it closes or the context ends.
func feed[T any](ctx context.Context, ch <-chan T) iter.Seq[T] {
	return func(yield func(T) bool) {
		for {
			select {
			case v, ok := <-ch:
				if !ok || !yield(v) {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}
}

func streamRoute(s func(ctx context.Context) *geta.Stream[change], root ...geta.Middleware) geta.Table {
	h := func(ctx context.Context, _ *empty) (*geta.Stream[change], error) { return s(ctx), nil }
	return withRoot(geta.Table{Routes: []geta.Entry{
		{Path: "/events", Route: get(h)},
		{Path: "/x", Route: get(okHandler)},
	}}, root...)
}

func TestStreamDeliversEvents(t *testing.T) {
	src := func(ctx context.Context) *geta.Stream[change] {
		return &geta.Stream[change]{Events: func(yield func(change) bool) {
			_ = yield(change{"created", "1"}) && yield(change{"deleted", "2"})
		}}
	}
	c := getatest.New(t, streamRoute(src, geta.Gzip(), geta.ETag()))
	s := c.With("Accept-Encoding", "gzip").Stream("/events")
	h := s.Response.Header
	if s.Response.Status != 200 || h.Get("Content-Type") != "text/event-stream; charset=utf-8" || h.Get("Cache-Control") != "no-cache" {
		t.Fatal(s.Response.Status, h)
	}
	if h.Get("Content-Encoding") != "" || h.Get("ETag") != "" || h.Get("Vary") != "" {
		t.Fatal("a stream was rewritten:", h)
	}
	var got []string
	for e, ok := s.Next(); ok; e, ok = s.Next() {
		got = append(got, e.Name+"/"+e.ID+"/"+e.Data)
	}
	if strings.Join(got, " ") != `created/1/{"kind":"created","id":"1"} deleted/2/{"kind":"deleted","id":"2"}` {
		t.Fatal(got)
	}
}

type badName struct{}

func (badName) EventName() string { return "a\nb" }

func TestStreamRefusesASplittingEventName(t *testing.T) {
	log, buf := logger()
	h := func(ctx context.Context, _ *empty) (*geta.Stream[badName], error) {
		return &geta.Stream[badName]{Events: func(yield func(badName) bool) { yield(badName{}) }}, nil
	}
	a, err := geta.New(one("/e", get(h)), geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	s := getatest.Serve(t, a).Stream("/e")
	if _, ok := s.Next(); ok {
		t.Fatal("an event with a newline in its name was sent")
	}
	if !strings.Contains(buf.String(), "contains CR, LF, or NUL") {
		t.Fatal(buf.String())
	}
}

func TestZeroStreamIsADefect(t *testing.T) {
	h := func(ctx context.Context, _ *empty) (*geta.Stream[change], error) { return &geta.Stream[change]{}, nil }
	if res := getatest.New(t, one("/e", get(h))).Get("/e"); res.Status != 500 {
		t.Fatal(res.Status)
	}
}

// Keep-alives keep proxies open but do not count as activity: MaxIdle ends a
// silent stream on time.
func TestKeepAliveDoesNotExtendMaxIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		silent := func(ctx context.Context) *geta.Stream[change] {
			return &geta.Stream[change]{Events: feed(ctx, make(chan change)), KeepAlive: time.Second, MaxIdle: 3500 * time.Millisecond}
		}
		s := getatest.New(t, streamRoute(silent)).Stream("/events")
		if _, ok := s.Next(); ok {
			t.Fatal("an event from a silent source")
		}
		if el := time.Since(start); el != 3500*time.Millisecond {
			t.Fatalf("ended after %v", el)
		}
		if s.Comments != 3 {
			t.Fatalf("%d keep-alives", s.Comments)
		}
	})
}

func TestEventsResetMaxIdleButNotMaxLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		ticking := func(ctx context.Context) *geta.Stream[change] {
			return &geta.Stream[change]{
				Events: func(yield func(change) bool) {
					for {
						select {
						case <-time.After(time.Second):
							if !yield(change{"tick", ""}) {
								return
							}
						case <-ctx.Done():
							return
						}
					}
				},
				MaxIdle:     2 * time.Second,
				MaxLifetime: 5500 * time.Millisecond,
			}
		}
		s := getatest.New(t, streamRoute(ticking)).Stream("/events")
		n := 0
		for _, ok := s.Next(); ok; _, ok = s.Next() {
			n++
		}
		if n != 5 || time.Since(start) != 5500*time.Millisecond {
			t.Fatalf("%d events, %v", n, time.Since(start))
		}
	})
}

// A request deadline bounds the time to a response, not a stream's length.
func TestTimeoutDoesNotBoundAStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		slow := func(ctx context.Context) *geta.Stream[change] {
			return &geta.Stream[change]{Events: func(yield func(change) bool) {
				for i := range 3 {
					select {
					case <-time.After(2 * time.Second):
					case <-ctx.Done():
						return
					}
					if !yield(change{"tick", string(rune('0' + i))}) {
						return
					}
				}
			}}
		}
		s := getatest.New(t, streamRoute(slow, geta.Timeout(time.Second))).Stream("/events")
		n := 0
		for _, ok := s.Next(); ok; _, ok = s.Next() {
			n++
		}
		if n != 3 {
			t.Fatalf("%d events through a 1s timeout", n)
		}
	})
}

// A waiting stream holds no concurrency slot.
func TestWaitingStreamFreesItsSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hold := make(chan change)
		waiting := func(ctx context.Context) *geta.Stream[change] {
			return &geta.Stream[change]{Events: feed(ctx, hold)}
		}
		c := getatest.New(t, streamRoute(waiting, geta.ConcurrencyLimit(1)))
		s := c.Stream("/events")
		if s.Response.Status != 200 {
			t.Fatal(s.Response.Status)
		}
		synctest.Wait()
		if res := c.Get("/x"); res.Status != 200 {
			t.Fatalf("the open stream held the only slot: %d", res.Status)
		}
		go func() { hold <- change{"late", "9"} }()
		if e, ok := s.Next(); !ok || e.Name != "late" {
			t.Fatal(e, ok)
		}
		s.Close()
	})
}

// Upgrade: a returned value, so the gate runs before anything switches.

func echoUpgrade(ctx context.Context, _ *empty) (*geta.Upgrade, error) {
	return &geta.Upgrade{
		Protocol: "echo",
		Header:   http.Header{"X-Echo": {"1"}},
		Serve: func(conn net.Conn, rw *bufio.ReadWriter) {
			line, _ := rw.ReadString('\n')
			rw.WriteString("echo: " + line)
			rw.Flush()
		},
	}, nil
}

func upgradeTable() geta.Table {
	gate := geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer}, Verifiers: map[string]geta.Verifier{"bearer": bearerVerifier}})
	return geta.Table{
		Root:   geta.Scope{geta.AccessLog(slogDiscard()), geta.Gzip(), gate},
		Routes: []geta.Entry{{Path: "/ws", Route: geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, echoUpgrade, geta.Doc{})}}},
	}
}

func TestUnauthenticatedUpgradeIsRefusedBeforeTheSwitch(t *testing.T) {
	c := getatest.New(t, upgradeTable())
	u := c.Upgrade("/ws", "echo")
	if u.Switched || u.Response.Status != 401 {
		t.Fatal(u.Switched, u.Response.Status)
	}
	u = c.Bearer("ok").Upgrade("/ws", "echo")
	if !u.Switched || u.Response.Header.Get("X-Echo") != "1" || u.Response.Header.Get("Upgrade") != "echo" {
		t.Fatal(u.Switched, u.Response.Status, u.Response.Header)
	}
	io.WriteString(u.Conn, "hi\n")
	line, _ := bufio.NewReader(u.Conn).ReadString('\n')
	if line != "echo: hi\n" {
		t.Fatalf("%q", line)
	}
}

func TestAPlainRequestToAnUpgradeIs426(t *testing.T) {
	c := getatest.New(t, upgradeTable()).Bearer("ok")
	for _, proto := range []string{"", "other"} {
		u := c.Upgrade("/ws", proto)
		if u.Switched || u.Response.Status != 426 || u.Response.Header.Get("Upgrade") != "echo" {
			t.Fatal(proto, u.Response.Status, u.Response.Header)
		}
	}
}

func TestSpecialOutputsFixTheirStatus(t *testing.T) {
	rejects(t, one("/ws", geta.Route{Get: geta.Op(http.StatusOK, echoUpgrade, geta.Doc{})}), "success status 200, but this output answers 101")
	h := func(ctx context.Context, _ *empty) (*geta.Stream[change], error) { return nil, nil }
	rejects(t, one("/e", geta.Route{Get: geta.Op(http.StatusCreated, h, geta.Doc{})}), "success status 201, but this output answers 200")
}

func TestSpecialOutputsAreDocumented(t *testing.T) {
	m := doc(t, accepts(t, upgradeTable()))
	ws := at(t, m, "paths", "/ws", "get", "responses")
	if at(t, ws, "101", "description") != "Switching Protocols" || at(t, ws, "426", "description") == nil || at(t, ws, "401", "description") == nil {
		t.Fatal(ws)
	}
	src := func(ctx context.Context) *geta.Stream[change] { return nil }
	m = doc(t, accepts(t, streamRoute(src)))
	if got := compact(t, at(t, m, "paths", "/events", "get", "responses", "200", "content")); got != `{"text/event-stream":{"schema":{"$ref":"#/components/schemas/change"}}}` {
		t.Fatal(got)
	}
}

func slogDiscard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func panickingStream(context.Context, *empty) (*geta.Stream[change], error) {
	return &geta.Stream[change]{Events: func(yield func(change) bool) {
		if yield(change{"created", "1"}) {
			panic("source bug")
		}
	}}, nil
}

// A panic in a stream's source is recovered on the source's goroutine and
// recorded with its stack; the response has started, so the connection is
// aborted, as Recover does for a panic after the status. The process lives.
func TestStreamSourcePanicAbortsTheConnection(t *testing.T) {
	var logs syncBuffer // written from the stream's goroutines
	log := slog.New(slog.NewTextHandler(&logs, nil))
	a, err := geta.New(withRoot(one("/s", get(panickingStream)), geta.Recover(log)), geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a)
	defer srv.Close()
	res, err := http.Get(srv.URL + "/s")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || err == nil {
		t.Fatalf("%d %q err=%v; want the stream cut short", res.StatusCode, body, err)
	}
	if !strings.Contains(string(body), `data: {"kind":"created","id":"1"}`) {
		t.Errorf("the event before the panic is missing: %q", body)
	}
	if l := logs.String(); !strings.Contains(l, "panic in a stream source") || !strings.Contains(l, "source bug") || !strings.Contains(l, "stack=") {
		t.Errorf("log: %s", l)
	}

	// Without Recover the handler aborts the same way.
	a, err = geta.New(one("/s", get(panickingStream)), geta.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if p := recover(); p != http.ErrAbortHandler {
				t.Errorf("recovered %v; want http.ErrAbortHandler", p)
			}
		}()
		a.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/s", nil))
	}()
}

// HEAD on a stream answers the stream's headers without running the source,
// so the connection serves the next request.
func TestHeadOnAStreamSendsHeadersOnly(t *testing.T) {
	ran := make(chan struct{}, 1)
	h := func(ctx context.Context, _ *empty) (*geta.Stream[change], error) {
		return &geta.Stream[change]{Events: func(yield func(change) bool) {
			ran <- struct{}{}
			<-ctx.Done()
		}}, nil
	}
	a := accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/events", Route: get(h)},
		{Path: "/x", Route: get(okHandler)},
	}})
	srv := httptest.NewServer(a)
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "HEAD /events HTTP/1.1\r\nHost: x\r\n\r\nGET /x HTTP/1.1\r\nHost: x\r\n\r\n")
	br := bufio.NewReader(conn)
	r1, err := http.ReadResponse(br, &http.Request{Method: http.MethodHead})
	if err != nil {
		t.Fatal(err)
	}
	if r1.StatusCode != 200 || r1.Header.Get("Content-Type") != "text/event-stream; charset=utf-8" {
		t.Fatalf("HEAD: %d %v", r1.StatusCode, r1.Header)
	}
	r2, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("the next request on the connection got no answer: %v", err)
	}
	b, _ := io.ReadAll(r2.Body)
	if r2.StatusCode != 200 || string(b) != `{"ok":true}` {
		t.Fatalf("GET /x: %d %s", r2.StatusCode, b)
	}
	select {
	case <-ran:
		t.Error("HEAD ran the stream's source")
	default:
	}
}
