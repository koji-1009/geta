package geta_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// The request's deadline bounds reading its body (408); an operation's
// Doc.Timeout gives its deadline a length of its own; content its headers
// already refuse is refused before any of it is read.

type dlNote struct {
	Text string `json:"text"`
}

type dlJSONIn struct {
	Body dlNote `body:"json"`
}

type dlFields struct {
	A string `form:"a"`
}

type dlFormIn struct {
	Body dlFields `body:"form"`
}

type dlMultipartIn struct {
	Body dlFields `body:"multipart"`
}

type dlRawIn struct {
	Body []byte `body:"text/csv"`
}

type dlReaderIn struct {
	Body io.Reader `body:"text/csv"`
}

func dlTable(root ...geta.Middleware) geta.Table {
	okFor := func(context.Context, *dlJSONIn) (*ok, error) { return &ok{true}, nil }
	return withRoot(geta.Table{Routes: []geta.Entry{
		{Path: "/json", Route: geta.Route{Post: geta.Op(http.StatusOK, okFor, geta.Doc{})}},
		{Path: "/form", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *dlFormIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}},
		{Path: "/multipart", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *dlMultipartIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}},
		{Path: "/raw", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *dlRawIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}},
		{Path: "/reader", Route: geta.Route{Post: geta.Op(http.StatusOK, func(_ context.Context, in *dlReaderIn) (*ok, error) {
			if _, err := io.ReadAll(in.Body); err != nil {
				return nil, fmt.Errorf("reading the upload: %w", err)
			}
			return &ok{true}, nil
		}, geta.Doc{})}},
		{Path: "/get", Route: get(okHandler)},
	}}, root...)
}

// serve runs a over a real server, which takes read deadlines.
func serve(t *testing.T, a http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

// rawRequest writes head (the request line and headers, without the empty
// line) and body on a new connection and returns the connection and a
// reader of what the server sends.
func rawRequest(t *testing.T, addr, head, body string) (net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if _, err := io.WriteString(c, head+"\r\n\r\n"+body); err != nil {
		t.Fatal(err)
	}
	return c, bufio.NewReader(c)
}

// readResponse reads one response within d.
func readResponse(t *testing.T, c net.Conn, br *bufio.Reader, d time.Duration) *http.Response {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(d))
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("no response within %v: %v", d, err)
	}
	io.ReadAll(res.Body)
	res.Body.Close()
	return res
}

// A client that sends part of its body and stalls is answered 408 when the
// deadline passes, with the connection closed, rather than holding its
// concurrency slot until it resumes; the slot is free for the next request.
// So for JSON, form, multipart, and raw bodies, and for a raw io.Reader the
// handler reads and whose error it returns.
func TestASlowBodyIs408AndFreesItsSlot(t *testing.T) {
	addr := serve(t, accepts(t, dlTable(geta.ConcurrencyLimit(1), geta.Timeout(200*time.Millisecond))))
	for path, ct := range map[string]string{
		"/json":      "application/json",
		"/form":      "application/x-www-form-urlencoded",
		"/multipart": "multipart/form-data; boundary=b",
		"/raw":       "text/csv",
		"/reader":    "text/csv",
	} {
		t.Run(path, func(t *testing.T) {
			head := "POST " + path + " HTTP/1.1\r\nHost: x\r\nContent-Type: " + ct + "\r\nContent-Length: 40"
			start := time.Now()
			c, br := rawRequest(t, addr, head, "--b")
			res := readResponse(t, c, br, 2*time.Second)
			if res.StatusCode != http.StatusRequestTimeout || !res.Close {
				t.Fatalf("%d close=%v %v", res.StatusCode, res.Close, res.Header)
			}
			if el := time.Since(start); el > time.Second {
				t.Fatalf("answered after %v", el)
			}
			if res, err := http.Get("http://" + addr + "/get"); err != nil || res.StatusCode != http.StatusOK {
				t.Fatalf("the next request: %v %v", res, err)
			}
		})
	}
	// A body of unknown length (chunked) is bounded alike.
	c, br := rawRequest(t, addr, "POST /json HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked", "3\r\n{\"t")
	if res := readResponse(t, c, br, 2*time.Second); res.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("chunked: %d", res.StatusCode)
	}
}

// The 408 of a stalled body closes an HTTP/1.1 connection, whose unread rest
// would be read as the next request, and leaves an HTTP/2 connection open,
// resetting the stalled stream alone: Go's HTTP/2 server takes Connection:
// close as a request to shut the whole connection down (GOAWAY), which would
// retire a connection other streams share for one slow body. So the client's
// next request reuses its HTTP/2 connection and opens a new HTTP/1.1 one.
func TestALateBodyClosesOnlyAnHTTP1Connection(t *testing.T) {
	a := accepts(t, dlTable(geta.Timeout(200*time.Millisecond)))
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("h2=%v", h2), func(t *testing.T) {
			srv := httptest.NewUnstartedServer(a)
			srv.EnableHTTP2 = h2
			srv.StartTLS()
			t.Cleanup(srv.Close)
			client := srv.Client()
			pr, pw := io.Pipe()
			t.Cleanup(func() { pw.Close() })
			go io.WriteString(pw, `{"t`) // part of the body, then nothing
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/json", pr)
			req.ContentLength = 40
			req.Header.Set("Content-Type", "application/json")
			res, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
			if wantMajor := map[bool]int{false: 1, true: 2}[h2]; res.StatusCode != http.StatusRequestTimeout || res.ProtoMajor != wantMajor {
				t.Fatalf("%s %d, want HTTP/%d 408", res.Proto, res.StatusCode, wantMajor)
			}
			if res.Close == h2 {
				t.Fatalf("%s 408 close=%v, want %v", res.Proto, res.Close, !h2)
			}
			var reused bool
			trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { reused = i.Reused }}
			next, _ := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), http.MethodGet, srv.URL+"/get", nil)
			res, err = client.Do(next)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
			if res.StatusCode != http.StatusOK || reused != h2 {
				t.Fatalf("the next request over %s: %d, reused its connection %v, want %v", res.Proto, res.StatusCode, reused, h2)
			}
		})
	}
}

// A body that arrives in time is read as before, and the read deadline is
// gone once it has: a handler that runs past the deadline without watching
// its context still has its response sent, and the connection serves the
// next request.
func TestABodyInTimeLeavesTheConnectionAlone(t *testing.T) {
	slow := func(_ context.Context, in *dlJSONIn) (*dlNote, error) {
		time.Sleep(300 * time.Millisecond)
		return &in.Body, nil
	}
	tbl := withRoot(one("/slow", geta.Route{Post: geta.Op(http.StatusOK, slow, geta.Doc{})}), geta.Timeout(100*time.Millisecond))
	addr := serve(t, accepts(t, tbl))
	body := `{"text":"a"}`
	head := fmt.Sprintf("POST /slow HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: %d", len(body))
	c, br := rawRequest(t, addr, head, body)
	if res := readResponse(t, c, br, 2*time.Second); res.StatusCode != http.StatusOK || res.Close {
		t.Fatalf("%d close=%v", res.StatusCode, res.Close)
	}
	if _, err := io.WriteString(c, head+"\r\n\r\n"+body); err != nil {
		t.Fatal(err)
	}
	if res := readResponse(t, c, br, 2*time.Second); res.StatusCode != http.StatusOK {
		t.Fatalf("the next request on the connection: %d", res.StatusCode)
	}
}

// The body's window is the deadline's length counted from its first read,
// whatever the server used before reading (a root middleware here): a client
// is not given less time because the server was slow. When the server used
// 80ms of a 100ms deadline, a body whose rest arrives 50ms after the first
// read (past the deadline, within the window) is read; one that stalls past
// the window is a 408 with the connection closed and its slot freed. When the
// server used the whole deadline and more (150ms), the window is the same.
// It runs in a synctest bubble over getatest's in-memory connections, which
// take read deadlines: the times are fake, so the bounds are exact.
func TestABodyHasTheTimeoutsLengthFromItsFirstRead(t *testing.T) {
	const window = 100 * time.Millisecond
	for _, used := range []time.Duration{80 * time.Millisecond, 150 * time.Millisecond} {
		t.Run(used.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				slowRoot := geta.Use(func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						time.Sleep(used)
						next.ServeHTTP(w, r)
					})
				})
				c := getatest.Serve(t, accepts(t, dlTable(geta.ConcurrencyLimit(1), geta.Timeout(window), slowRoot)))
				body := `{"text":"a"}`
				head := fmt.Sprintf("POST /json HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", len(body))
				open := func() (net.Conn, *bufio.Reader) {
					conn, err := c.DialContext(t.Context(), "tcp", "example.com:80")
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { conn.Close() })
					if _, err := io.WriteString(conn, head+body[:4]); err != nil {
						t.Fatal(err)
					}
					return conn, bufio.NewReader(conn)
				}

				// The rest arrives 50ms after the first read: in the window.
				conn, br := open()
				time.Sleep(used + 50*time.Millisecond)
				if _, err := io.WriteString(conn, body[4:]); err != nil {
					t.Fatal(err)
				}
				res, err := http.ReadResponse(br, nil)
				if err != nil {
					t.Fatal(err)
				}
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
				if res.StatusCode != http.StatusOK || res.Close {
					t.Fatalf("a body that arrives in its window: %d close=%v", res.StatusCode, res.Close)
				}

				// A stall is a 408 once the window from the first read has passed.
				start := time.Now()
				_, br = open()
				res, err = http.ReadResponse(br, nil)
				if err != nil {
					t.Fatal(err)
				}
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
				if res.StatusCode != http.StatusRequestTimeout || !res.Close {
					t.Fatalf("a stalled body: %d close=%v", res.StatusCode, res.Close)
				}
				if el := time.Since(start); el != used+window {
					t.Fatalf("answered after %v, want the %v the server used and the %v window", el, used, window)
				}
				if res := c.Get("/get"); res.Status != http.StatusOK {
					t.Fatalf("the next request: %d %s", res.Status, res.Text())
				}
			})
		})
	}
}

// The 408 is documented on every operation that reads a body behind a
// Timeout, and on no other.
func TestTheBodyDeadlineIsDocumented(t *testing.T) {
	m := doc(t, accepts(t, dlTable(geta.Timeout(time.Second))))
	for _, path := range []string{"/json", "/form", "/multipart", "/raw", "/reader"} {
		if at(t, m, "paths", path, "post", "responses").(map[string]any)["408"] == nil {
			t.Errorf("%s documents no 408", path)
		}
	}
	if at(t, m, "paths", "/get", "get", "responses").(map[string]any)["408"] != nil {
		t.Error("an operation without a body documents a 408")
	}
	m = doc(t, accepts(t, dlTable()))
	if at(t, m, "paths", "/json", "post", "responses").(map[string]any)["408"] != nil {
		t.Error("an operation with no Timeout documents a 408")
	}
}

// Doc.Timeout is the length of the operation's deadline, shorter or longer
// than the Timeout's own, counted where the root Timeout runs, so a root
// Compress, ETag, and gate after it are allowed as before. Operations
// without one keep the Timeout's.
func TestDocTimeoutIsTheOperationsDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var start time.Time
		wait := func(ctx context.Context, _ *empty) (*ok, error) {
			dl, _ := ctx.Deadline()
			if got := dl.Sub(start); got != ctx.Value(wantKey{}).(time.Duration) {
				t.Errorf("deadline %v away", got)
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		tbl := geta.Table{Root: geta.Scope{geta.Timeout(time.Second), geta.Gzip(), geta.ETag()}, Routes: []geta.Entry{
			{Path: "/default", Route: get(wait)},
			{Path: "/long", Route: geta.Route{Get: geta.Op(http.StatusOK, wait, geta.Doc{Timeout: time.Minute})}},
			{Path: "/short", Route: geta.Route{Get: geta.Op(http.StatusOK, wait, geta.Doc{Timeout: 10 * time.Millisecond})}},
		}}
		a := accepts(t, tbl)
		for path, want := range map[string]time.Duration{"/default": time.Second, "/long": time.Minute, "/short": 10 * time.Millisecond} {
			start = time.Now()
			req := httptest.NewRequestWithContext(context.WithValue(context.Background(), wantKey{}, want), http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			a.ServeHTTP(rec, req)
			if rec.Code != http.StatusGatewayTimeout || time.Since(start) != want {
				t.Errorf("%s: %d after %v", path, rec.Code, time.Since(start))
			}
		}
	})
}

type wantKey struct{}

// A root middleware that moves the request after the root Timeout ran gives
// the deadline the length of the operation served.
func TestDocTimeoutFollowsARootRewrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		var got time.Duration
		wait := func(ctx context.Context, _ *empty) (*ok, error) {
			dl, _ := ctx.Deadline()
			got = dl.Sub(start)
			return &ok{true}, nil
		}
		move := geta.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.URL.Path = "/b"
				next.ServeHTTP(w, r)
			})
		})
		tbl := geta.Table{Root: geta.Scope{geta.Timeout(time.Second), move}, Routes: []geta.Entry{
			{Path: "/a", Route: geta.Route{Get: geta.Op(http.StatusOK, wait, geta.Doc{Timeout: time.Hour})}},
			{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, wait, geta.Doc{Timeout: time.Minute})}},
		}}
		if c := do(t, accepts(t, tbl), http.MethodGet, "/a").Code; c != http.StatusOK || got != time.Minute {
			t.Fatalf("%d, deadline %v away", c, got)
		}
	})
}

// A longer Doc.Timeout lengthens every Timeout of the chain, a directory's
// beside the root's, since each counts it.
func TestDocTimeoutLengthensNestedTimeouts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		var got time.Duration
		h := func(ctx context.Context, _ *empty) (*ok, error) {
			dl, _ := ctx.Deadline()
			got = dl.Sub(start)
			return &ok{true}, nil
		}
		tbl := withRoot(one("/x", geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{Timeout: time.Hour})}, geta.Scope{geta.Timeout(time.Second)}), geta.Timeout(time.Second))
		if c := do(t, accepts(t, tbl), http.MethodGet, "/x").Code; c != http.StatusOK || got != time.Hour {
			t.Fatalf("%d, deadline %v away", c, got)
		}
	})
}

func TestDocTimeoutMistakesAreRefused(t *testing.T) {
	rejects(t, withRoot(one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Timeout: -time.Second})}), geta.Timeout(time.Second)),
		"GET /x", "Doc.Timeout -1s is negative")
	rejects(t, one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Timeout: time.Second})}),
		"GET /x", "Doc.Timeout 1s", "the chain has no geta.Timeout")
}

// Content its headers declare (a Content-Length) is refused before any of it
// is read when they decide the answer: a client that waits for 100 Continue
// gets the 415 or the 413 in its place and sends nothing. The 415 still comes
// before the 413, and the coding's before the media type's.
func TestDeclaredContentIsRefusedBeforeItIsRead(t *testing.T) {
	addr := serve(t, accepts(t, dlTable()))
	cases := []struct {
		path, headers string
		want          int
		accept        string
	}{
		{"/json", "Content-Type: application/json\r\nContent-Length: 2000000", 413, ""},
		{"/json", "Content-Type: text/plain\r\nContent-Length: 2000000", 415, "application/json"},
		{"/json", "Content-Type: text/plain\r\nContent-Encoding: gzip\r\nContent-Length: 10", 415, ""},
		{"/form", "Content-Type: application/x-www-form-urlencoded\r\nContent-Length: 2000000", 413, ""},
		{"/form", "Content-Type: application/json\r\nContent-Length: 10", 415, "application/x-www-form-urlencoded"},
		{"/multipart", "Content-Type: multipart/form-data; boundary=b\r\nContent-Length: 2000000", 413, ""},
		{"/multipart", "Content-Type: text/plain\r\nContent-Length: 10", 415, "multipart/form-data"},
		{"/raw", "Content-Type: text/csv\r\nContent-Length: 2000000", 413, ""},
		{"/raw", "Content-Type: text/plain\r\nContent-Length: 10", 415, "text/csv"},
		{"/reader", "Content-Type: text/plain\r\nContent-Length: 10", 415, "text/csv"},
	}
	for _, tc := range cases {
		head := "POST " + tc.path + " HTTP/1.1\r\nHost: x\r\nExpect: 100-continue\r\n" + tc.headers
		c, br := rawRequest(t, addr, head, "")
		res := readResponse(t, c, br, 2*time.Second)
		if res.StatusCode != tc.want || res.Header.Get("Accept") != tc.accept {
			t.Errorf("%s %q: %d Accept %q", tc.path, tc.headers, res.StatusCode, res.Header.Get("Accept"))
		}
	}
	// Content it takes is asked for (100 Continue) and read.
	head := "POST /json HTTP/1.1\r\nHost: x\r\nExpect: 100-continue\r\nContent-Type: application/json\r\nContent-Length: 12"
	c, br := rawRequest(t, addr, head, "")
	if res := readResponse(t, c, br, 2*time.Second); res.StatusCode != http.StatusContinue {
		t.Fatalf("got %d, want 100 Continue", res.StatusCode)
	}
	io.WriteString(c, `{"text":"a"}`)
	if res := readResponse(t, c, br, 2*time.Second); res.StatusCode != http.StatusOK {
		t.Fatalf("after 100 Continue: %d", res.StatusCode)
	}
}

// A raw io.Reader's size is the handler's to read: a declared length past
// MaxBodyBytes is a 413 only when it reads past it, as before.
func TestADeclaredLengthLeavesAReaderToItsHandler(t *testing.T) {
	prefix := func(_ context.Context, in *dlReaderIn) (*ok, error) {
		b := make([]byte, 3)
		_, err := io.ReadFull(in.Body, b)
		return &ok{string(b) == "a,b"}, err
	}
	small := func(l geta.Limits) geta.Limits { l.MaxBodyBytes = 8; return l }
	a := accepts(t, one("/r", geta.Route{Post: geta.Op(http.StatusOK, prefix, geta.Doc{Limits: small})}))
	req := httptest.NewRequest(http.MethodPost, "/r", strings.NewReader("a,b,c,d,e,f"))
	req.Header.Set("Content-Type", "text/csv")
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "true") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// A root middleware that replaces the body leaves the headers' length
// describing another body: geta reads it rather than trust them.
func TestAReplacedBodyIsReadNotJudgedByItsHeaders(t *testing.T) {
	replace := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(""))
			next.ServeHTTP(w, r)
		})
	})
	a := accepts(t, withRoot(geta.Table{Routes: []geta.Entry{{Path: "/json", Route: geta.Route{Post: geta.Op(http.StatusOK,
		func(_ context.Context, in *struct {
			Body *dlNote `body:"json"`
		}) (*ok, error) {
			return &ok{in.Body == nil}, nil
		}, geta.Doc{})}}}}, replace))
	req := httptest.NewRequest(http.MethodPost, "/json", strings.NewReader("xxxxxxxxxx"))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "true") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
