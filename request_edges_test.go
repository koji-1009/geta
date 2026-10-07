package geta_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// An absolute-form request-target with no path asks for /, which is served
// as it is, not redirected.
func TestAnAbsoluteFormTargetWithoutAPathIsTheRoot(t *testing.T) {
	a := accepts(t, one("/", get(okHandler)))
	if r := do(t, a, "GET", "http://example.com"); r.Code != 200 || r.Header().Get("Location") != "" {
		t.Fatal(r.Code, r.Header())
	}
}

// The redirect to the clean path escapes each byte of the query above 0x7F
// and leaves the ASCII bytes after one as they were.
func TestARedirectEscapesTheQuerysNonASCIIBytes(t *testing.T) {
	a := accepts(t, one("/x", get(okHandler)))
	if r := do(t, a, "GET", "/./x?q=\xc3\xa9x&r=1"); r.Code != 307 || r.Header().Get("Location") != "/x?q=%c3%a9x&r=1" {
		t.Fatal(r.Code, r.Header())
	}
}

// Content of unknown length (no Content-Length) in a media type the body
// does not take is a 415 once its first byte has arrived, for a JSON, a form,
// and a raw body alike.
func TestContentOfUnknownLengthInAnotherMediaTypeIs415(t *testing.T) {
	a := accepts(t, dlTable())
	for _, path := range []string{"/json", "/form", "/raw"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("x"))
		req.ContentLength = -1
		req.Header.Set("Content-Type", "application/xml")
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
}

// A multipart body cut off inside a file part is malformed: a 400.
func TestAMultipartBodyCutOffInAFileIsMalformed(t *testing.T) {
	c := getatest.New(t, one("/m", geta.Route{Post: geta.Op(http.StatusOK, echoUpload, geta.Doc{})}))
	cut := "--b\r\nContent-Disposition: form-data; name=\"avatar\"; filename=\"a.png\"\r\n\r\nPNG"
	r := c.With("Content-Type", "multipart/form-data; boundary=b").Post("/m", cut)
	if r.Status != 400 || !strings.Contains(r.Text(), "unexpected EOF") {
		t.Fatal(r.Status, r.Text())
	}
}

// A client holding the identity form that asks with If-None-Match: * gets
// its 304 through Compress, which leaves the tag ETag gave.
func TestCompressPassesAStarsNotModified(t *testing.T) {
	a := accepts(t, one("/x", get(okHandler), geta.Scope{geta.Gzip(), geta.ETag()}))
	tag := do(t, a, "GET", "/x", "Accept-Encoding", "gzip").Header().Get("ETag")
	r := do(t, a, "GET", "/x", "Accept-Encoding", "gzip", "If-None-Match", "*")
	if tag == "" || r.Code != 304 || r.Header().Get("ETag") != tag {
		t.Fatal(tag, r.Code, r.Header())
	}
}

// A root middleware that changes only the method of a request dispatch will
// redirect leaves it a redirect, which carries no operation's deprecation.
func TestAMethodRewriteOfARedirectStaysARedirect(t *testing.T) {
	dep := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	toPost := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Method = http.MethodPost
			next.ServeHTTP(w, r)
		})
	})
	tbl := withRoot(one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{}),
		Post: geta.Op(http.StatusOK, okHandler, geta.Doc{Deprecated: true, Deprecation: dep})}), toPost, geta.Timeout(time.Second))
	a := accepts(t, tbl)
	if r := do(t, a, "GET", "/./x"); r.Code != 307 || r.Header().Get("Location") != "/x" || r.Header().Get("Deprecation") != "" {
		t.Fatal(r.Code, r.Header())
	}
	if r := do(t, a, "GET", "/x"); r.Code != 200 || r.Header().Get("Deprecation") == "" {
		t.Fatal("the rewrite to the deprecated POST:", r.Code, r.Header())
	}
}

// A root rewrite to an operation of the same length leaves the deadline as
// it was counted; one to a longer operation keeps an earlier deadline of the
// request's own context.
func TestARootRewriteKeepsTheDeadlinesBounds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var got time.Duration
		var start time.Time
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
		for _, c := range []struct {
			a, b, parent, want time.Duration
		}{
			{0, 0, 0, time.Second},
			{time.Hour, time.Minute, 2 * time.Second, 2 * time.Second},
		} {
			tbl := geta.Table{Root: geta.Scope{geta.Timeout(time.Second), move}, Routes: []geta.Entry{
				{Path: "/a", Route: geta.Route{Get: geta.Op(http.StatusOK, wait, geta.Doc{Timeout: c.a})}},
				{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, wait, geta.Doc{Timeout: c.b})}},
			}}
			start = time.Now()
			ctx := context.Background()
			if c.parent > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, c.parent)
				defer cancel()
			}
			rec := httptest.NewRecorder()
			accepts(t, tbl).ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/a", nil))
			if rec.Code != http.StatusOK || got != c.want {
				t.Errorf("%+v: %d, deadline %v away", c, rec.Code, got)
			}
		}
	})
}

// An operation whose chain has no Timeout, in an App with one elsewhere,
// sets no read deadline after an answer that left its body unread: net/http
// waits for the rest as it would with no Timeout at all, while the operation
// with the Timeout ends its connection after the window.
func TestAnOperationWithoutATimeoutSetsNoDeadlineAfterItsAnswer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		post := geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *dlJSONIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}
		tbl := withRoot(geta.Table{Routes: []geta.Entry{
			{Path: "/t", Route: post, Scopes: []geta.Scope{{geta.Timeout(drWindow)}}},
			{Path: "/n", Route: post},
		}}, drRefuse)
		c := getatest.Serve(t, accepts(t, tbl))
		start := time.Now()
		_, br := drOpen(t, c, "POST /t HTTP/1.1\r\nHost: x\r\nX-Refuse: 1")
		if res := drRead(t, br); res.StatusCode != 429 || !res.Close || time.Since(start) != drWindow {
			t.Fatalf("/t: %d close=%v after %v", res.StatusCode, res.Close, time.Since(start))
		}
		conn, br := drOpen(t, c, "POST /n HTTP/1.1\r\nHost: x\r\nX-Refuse: 1")
		conn.SetReadDeadline(time.Now().Add(time.Hour))
		if _, err := http.ReadResponse(br, nil); !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("/n: got %v, want no response within the hour", err)
		}
	})
}

// A root middleware's flush through http.Flusher is an answer: net/http's
// reading away of the unread body before the header goes out ends after the
// window, and the connection with it.
func TestAFlushThroughHTTPFlusherBoundsTheUnreadBody(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		flush := geta.Use(func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.(http.Flusher).Flush() })
		})
		c := getatest.Serve(t, accepts(t, drTable(geta.Scope{geta.Timeout(drWindow), flush})))
		start := time.Now()
		_, br := drOpen(t, c, "POST /json HTTP/1.1\r\nHost: x")
		if res := drRead(t, br); res.StatusCode != 200 || !res.Close || time.Since(start) != drWindow {
			t.Fatalf("%d close=%v after %v", res.StatusCode, res.Close, time.Since(start))
		}
	})
}

// A connection a root middleware hijacks after an answer reads on its own
// terms: the read deadline the answer set is cleared, so a byte the client
// sends long after the window still arrives.
func TestAHijackAfterAnAnswerClearsItsReadDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var got string
		var readErr error
		hijack := geta.Use(func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				conn, rw, err := http.NewResponseController(w).Hijack()
				if err != nil {
					readErr = err
					return
				}
				defer conn.Close()
				b, err := rw.ReadByte()
				got, readErr = string(b), err
				io.WriteString(conn, "done")
			})
		})
		c := getatest.Serve(t, accepts(t, drTable(geta.Scope{geta.Timeout(drWindow), hijack})))
		conn, br := drOpen(t, c, "POST /json HTTP/1.1\r\nHost: x")
		if _, err := io.WriteString(conn, strings.Repeat("x", 38)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * drWindow)
		if _, err := io.WriteString(conn, "z"); err != nil {
			t.Fatal(err)
		}
		rest, _ := io.ReadAll(br)
		if readErr != nil || got != "z" || !strings.HasSuffix(string(rest), "done") {
			t.Fatalf("read %q, %v; the client got %q", got, readErr, rest)
		}
	})
}

// A stream answering a request whose body was read under a Timeout lifts
// the body's read deadline and the one after the answer: an event long past
// the window arrives.
func TestAStreamAfterABodyLiftsItsReadDeadlines(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		late := func(ctx context.Context, _ *dlJSONIn) (*geta.Stream[change], error) {
			return &geta.Stream[change]{Events: func(yield func(change) bool) {
				time.Sleep(3 * drWindow)
				yield(change{"late", "1"})
			}}, nil
		}
		tbl := withRoot(one("/s", geta.Route{Post: geta.Op(http.StatusOK, late, geta.Doc{})}), geta.Timeout(drWindow))
		c := getatest.Serve(t, accepts(t, tbl))
		conn, err := c.DialContext(t.Context(), "tcp", "example.com:80")
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		body := `{"text":"a"}`
		if _, err := io.WriteString(conn, "POST /s HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 12\r\n\r\n"+body); err != nil {
			t.Fatal(err)
		}
		res, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil || res.StatusCode != 200 {
			t.Fatal(res, err)
		}
		data, _ := io.ReadAll(res.Body)
		if !strings.Contains(string(data), `data: {"kind":"late","id":"1"}`) {
			t.Fatalf("%q", data)
		}
	})
}

// A root middleware that passes on a context not derived from the
// request's loses the App with it: a gate behind it that finds a verifier's
// defect answers 500 and logs it on slog's default logger.
func TestALostContextLogsAVerifierDefectOnTheDefaultLogger(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	var buf syncBuffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	drop := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.Background()))
		})
	})
	log, own := logger()
	a, err := geta.New(withRoot(one("/x", get(okHandler)), drop,
		geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer}, Verifiers: map[string]geta.Verifier{"bearer": bearerVerifier}})),
		geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	r := do(t, a, "GET", "/x", "Authorization", "Bearer bug")
	slog.SetDefault(prev)
	if r.Code != 500 || !strings.Contains(buf.String(), "verifier bug") || strings.Contains(own.String(), "verifier bug") {
		t.Fatal(r.Code, buf.String(), own.String())
	}
}

// Behind such a middleware a Timeout counts its own length, knowing no
// operation, and CORS answers a preflight without the methods the path
// serves, knowing no App; dispatch answers the request 500.
func TestALostContextLeavesTimeoutAndCORSWithoutTheApp(t *testing.T) {
	drop := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.Background()))
		})
	})
	var left time.Duration
	deadline := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			dl, _ := r.Context().Deadline()
			left = time.Until(dl)
			next.ServeHTTP(w, r)
		})
	})
	a := accepts(t, withRoot(one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Timeout: time.Hour})}),
		drop, geta.Timeout(time.Minute), deadline))
	if r := do(t, a, "GET", "/x"); r.Code != 500 || left <= 0 || left > time.Minute {
		t.Fatal(r.Code, left)
	}
	a = accepts(t, withRoot(one("/x", get(okHandler)), drop, geta.CORS(geta.AllowOrigins("https://a.example"))))
	r := do(t, a, "OPTIONS", "/x", "Origin", "https://a.example", "Access-Control-Request-Method", "GET")
	if r.Code != 204 || r.Header().Get("Access-Control-Allow-Origin") != "https://a.example" || r.Header().Get("Access-Control-Allow-Methods") != "" {
		t.Fatal(r.Code, r.Header())
	}
}
