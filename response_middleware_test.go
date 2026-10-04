package geta_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// The rules and combinations of response.md on ETag, Compress, conditional
// requests, streams, and upgrades that no other test asserts.

type condChosenIn struct {
	geta.Conditional
}

type condChosen struct {
	Status int    `status:"200|201"`
	ETag   string `header:"ETag"`
	Body   ok     `body:"json"`
}

// A GET choosing among declared statuses whose input embeds Conditional
// answers Check's 304 and 412, and the document lists every one of them.
func TestDeclaredStatusesBesideConditional(t *testing.T) {
	h := func(_ context.Context, in *condChosenIn) (*condChosen, error) {
		if err := in.Check(`"v"`, time.Time{}); err != nil {
			return nil, err
		}
		return &condChosen{Status: http.StatusCreated, ETag: `"v"`, Body: ok{true}}, nil
	}
	rec := &recorder{TB: t}
	c := getatest.New(rec, one("/x", get(h)))
	if res := c.Get("/x"); res.Status != 201 || res.Header.Get("ETag") != `"v"` {
		t.Fatal(res.Status, res.Header)
	}
	if res := c.With("If-None-Match", `"v"`).Get("/x"); res.Status != 304 || res.Header.Get("ETag") != `"v"` {
		t.Fatal(res.Status, res.Header)
	}
	if res := c.With("If-Match", `"old"`).Get("/x"); res.Status != 412 {
		t.Fatal(res.Status)
	}
	if len(rec.errs) != 0 {
		t.Fatalf("%q", rec.errs)
	}
	responses := at(t, doc(t, c.App()), "paths", "/x", "get", "responses").(map[string]any)
	for _, s := range []string{"200", "201", "304", "412"} {
		if responses[s] == nil {
			t.Errorf("no %s in %v", s, responses)
		}
	}
}

type nonAuthoritative struct {
	Status int `status:"200|203"`
	Body   ok  `body:"json"`
}

// ETag tags a 200 alone: a GET that chooses another declared success status
// goes out untagged and gets no 304 from the middleware.
func TestETagLeavesAnotherSuccessStatusAlone(t *testing.T) {
	h := func(context.Context, *empty) (*nonAuthoritative, error) {
		return &nonAuthoritative{Status: http.StatusNonAuthoritativeInfo}, nil
	}
	a := accepts(t, withRoot(one("/x", get(h)), geta.ETag()))
	if r := do(t, a, "GET", "/x"); r.Code != 203 || r.Header().Get("ETag") != "" {
		t.Fatal(r.Code, r.Header())
	}
	if r := do(t, a, "GET", "/x", "If-None-Match", "*"); r.Code != 203 {
		t.Fatal(r.Code)
	}
}

// ETag tags GET and HEAD alone: a QUERY's 200 goes out untagged.
func TestETagLeavesAQueryUntagged(t *testing.T) {
	a, err := geta.New(withRoot(one("/q", geta.Route{Query: geta.Op(http.StatusOK, okHandler, geta.Doc{})}), geta.ETag()), geta.WithOpenAPI(geta.OpenAPI32))
	if err != nil {
		t.Fatal(err)
	}
	if r := do(t, a, geta.MethodQuery, "/q"); r.Code != 200 || r.Header().Get("ETag") != "" {
		t.Fatal(r.Code, r.Header())
	}
}

type csvReport struct {
	Body []byte `body:"text/csv"`
}

// A raw text body behind Gzip and ETag is coded, carries the coded form's
// tag and nosniff, gives a HEAD the GET's Content-Length and tag, and
// revalidates to 304.
func TestARawBodyThroughGzipAndETag(t *testing.T) {
	body := []byte(strings.Repeat("id,name\n1,ada\n", 200))
	h := func(context.Context, *empty) (*csvReport, error) { return &csvReport{Body: body}, nil }
	a := accepts(t, withRoot(one("/r", get(h)), geta.Gzip(), geta.ETag()))
	r := do(t, a, "GET", "/r", "Accept-Encoding", "gzip")
	tag := r.Header().Get("ETag")
	if r.Code != 200 || r.Header().Get("Content-Encoding") != "gzip" || !strings.HasSuffix(tag, `-gzip"`) ||
		r.Header().Get("X-Content-Type-Options") != "nosniff" || r.Header().Get("Content-Type") != "text/csv" ||
		gunzip(t, r.Body.Bytes()) != string(body) {
		t.Fatal(r.Code, r.Header())
	}
	head := do(t, a, "HEAD", "/r", "Accept-Encoding", "gzip")
	if head.Code != 200 || head.Header().Get("Content-Length") != r.Header().Get("Content-Length") || head.Header().Get("ETag") != tag {
		t.Fatal(head.Code, head.Header(), r.Header())
	}
	if r := do(t, a, "GET", "/r", "Accept-Encoding", "gzip", "If-None-Match", tag); r.Code != 304 || r.Header().Get("ETag") != tag {
		t.Fatal(r.Code, r.Header())
	}
}

// An element's weight is read as RFC 9110 §12.4.2's grammar writes it, and
// one out of the grammar refuses what the element names, as q=0 does.
func TestCompressReadsMalformedQValues(t *testing.T) {
	other := &fakeCoding{name: "other"}
	a := accepts(t, withRoot(one("/x", get(textHandler(big))), geta.Compress(other.coding(), geta.GzipCoding())))
	for ae, ce := range map[string]string{
		// A weight out of RFC 9110 §12.4.2's grammar refuses what it names.
		"gzip;q=abc, identity;q=0.5":        "",
		"gzip;q=NaN":                        "",
		"gzip;q=-3":                         "",
		"gzip;q=7, other;q=0.5":             "other",
		"gzip;q=1\xc2\xa0":                  "", // a no-break space is no OWS
		"gzip;q=0\xc2\xa0, *":               "other",
		"gzip;q=1.001, other;q=0.001":       "other",
		"gzip;q=0.0001, other;q=0.001":      "other",
		"gzip;q = 1, other;q=0.001":         "other",
		"gzip;level=9, other;q=0.001":       "other",
		"gzip;q=0.5;level=9, other;q=0.001": "other",
		"gzip;, other;q=0.001":              "other",
		"gzip;q=.5, other;q=0.001":          "other",
		"gzip;q=1e0, other;q=0.001":         "other",
		"*;q=0x":                            "", // refuses identity and every coding: uncoded, never a 406
		"identity;q=1x, gzip;q=0.001":       "gzip",
		// Weights in the grammar.
		"gzip;q=1.000, other;q=1":            "other", // a tie, which the server's order breaks
		"gzip ; Q=0.5 , other;q=0.499":       "gzip",
		"gzip;\tq=0.999, other;q=0.998":      "gzip",
		"gzip;q=0., other;q=0.001":           "other",
		"gzip;q=1., other;q=0.999, identity": "gzip",
	} {
		if r := do(t, a, "GET", "/x", "Accept-Encoding", ae); r.Code != 200 || r.Header().Get("Content-Encoding") != ce {
			t.Errorf("%q: %v; want %q", ae, r.Header(), ce)
		}
	}
}

// A coding is named by a token, compared in ASCII case alone: the Kelvin
// sign, which Unicode lowercases to "k", names no coding.
func TestCompressNamesCodingsByTokens(t *testing.T) {
	pack := &fakeCoding{name: "pack"}
	a := accepts(t, withRoot(one("/x", get(textHandler(big))), geta.Compress(pack.coding())))
	for ae, ce := range map[string]string{"PACK": "pack", "pac\xe2\x84\xaa": "", "pac\xe2\x84\xaa;q=1, identity;q=0": ""} {
		if r := do(t, a, "GET", "/x", "Accept-Encoding", ae); r.Code != 200 || r.Header().Get("Content-Encoding") != ce {
			t.Errorf("%q: %v; want %q", ae, r.Header(), ce)
		}
	}
}

// writeFails is an encoder whose Write fails.
type writeFails struct{}

func (writeFails) Write([]byte) (int, error) { return 0, errors.New("encoder broke") }
func (writeFails) Close() error              { return nil }

// An encoder whose Write fails leaves the body uncoded, with its
// Content-Length.
func TestCompressEncoderWriteFailureSendsIdentity(t *testing.T) {
	broken := geta.Coding{Name: "zz", NewWriter: func(io.Writer) (io.WriteCloser, error) { return writeFails{}, nil }}
	a := accepts(t, withRoot(one("/x", get(textHandler(big))), geta.Compress(broken)))
	r := do(t, a, "GET", "/x", "Accept-Encoding", "zz")
	if r.Code != 200 || r.Header().Get("Content-Encoding") != "" || r.Body.String() != `{"text":"`+big+`"}` ||
		r.Header().Get("Content-Length") != strconv.Itoa(r.Body.Len()) {
		t.Fatal(r.Code, r.Header())
	}
}

type idEvent struct {
	ID string `json:"id"`
}

func (e idEvent) EventID() string { return e.ID }

type numEvent struct {
	F float64 `json:"f"`
}

// A stream answers X-Accel-Buffering: no. An event whose id splits a line,
// one that does not encode (a NaN), and one leaving a required sealed value
// nil are never sent: the defect is logged and the stream ends after the
// events before it.
func TestStreamEventDefectsEndTheStream(t *testing.T) {
	ids := func(context.Context, *empty) (*geta.Stream[idEvent], error) {
		return &geta.Stream[idEvent]{Events: func(yield func(idEvent) bool) {
			_ = yield(idEvent{"1"}) && yield(idEvent{"2\n3"}) && yield(idEvent{"4"})
		}}, nil
	}
	nums := func(context.Context, *empty) (*geta.Stream[numEvent], error) {
		return &geta.Stream[numEvent]{Events: func(yield func(numEvent) bool) {
			_ = yield(numEvent{1}) && yield(numEvent{math.NaN()}) && yield(numEvent{2})
		}}, nil
	}
	drawings := func(context.Context, *empty) (*geta.Stream[drawing], error) {
		return &geta.Stream[drawing]{Events: func(yield func(drawing) bool) {
			_ = yield(drawing{Main: circle{Kind: "circle"}, Others: []shape{}}) && yield(drawing{}) && yield(drawing{Main: circle{Kind: "circle"}})
		}}, nil
	}
	log, buf := logger()
	a, err := geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/ids", Route: get(ids)},
		{Path: "/nums", Route: get(nums)},
		{Path: "/drawings", Route: get(drawings)},
	}}, geta.WithUnion(shapes), geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	c := getatest.Serve(t, a)
	for path, want := range map[string]string{
		"/ids":      "contains CR, LF, or NUL",
		"/nums":     "NaN",
		"/drawings": "a required geta_test.shape is nil",
	} {
		s := c.Stream(path)
		if s.Response.Header.Get("X-Accel-Buffering") != "no" {
			t.Errorf("%s: %v", path, s.Response.Header)
		}
		n := 0
		for _, ok := s.Next(); ok; _, ok = s.Next() {
			n++
		}
		if n != 1 {
			t.Errorf("%s: %d events; want the one before the defect", path, n)
		}
		if l := buf.String(); !strings.Contains(l, "stream event could not be written") || !strings.Contains(l, want) {
			t.Errorf("%s: %s", path, l)
		}
	}
}

// Each event restarts the KeepAlive timer as it restarts MaxIdle's: a source
// that is never silent for KeepAlive sends no keep-alive.
func TestEventsRestartTheKeepAlive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		busy := func(ctx context.Context) *geta.Stream[change] {
			return &geta.Stream[change]{
				Events: func(yield func(change) bool) {
					for {
						select {
						case <-time.After(900 * time.Millisecond):
							if !yield(change{"tick", ""}) {
								return
							}
						case <-ctx.Done():
							return
						}
					}
				},
				KeepAlive:   time.Second,
				MaxLifetime: 3500 * time.Millisecond,
			}
		}
		s := getatest.New(t, streamRoute(busy)).Stream("/events")
		n := 0
		for _, ok := s.Next(); ok; _, ok = s.Next() {
			n++
		}
		if n != 3 || s.Comments != 0 || time.Since(start) != 3500*time.Millisecond {
			t.Fatalf("%d events, %d keep-alives, %v", n, s.Comments, time.Since(start))
		}
	})
}

// The connection Serve is handed is closed when Serve returns.
func TestServeClosesTheConnectionWhenItReturns(t *testing.T) {
	h := func(context.Context, *empty) (*geta.Upgrade, error) {
		return &geta.Upgrade{Protocol: "bye", Serve: func(_ net.Conn, rw *bufio.ReadWriter) {
			rw.WriteString("bye")
			rw.Flush()
		}}, nil
	}
	u := getatest.New(t, one("/ws", geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, h, geta.Doc{})})).Upgrade("/ws", "bye")
	if !u.Switched {
		t.Fatal(u.Response.Status)
	}
	got, err := io.ReadAll(u.Conn)
	if err != nil || string(got) != "bye" {
		t.Fatalf("%q %v", got, err)
	}
}

// A connection that cannot be hijacked cannot switch for Serve: a 500.
func TestServeOnAConnectionThatCannotSwitchIsADefect(t *testing.T) {
	log, buf := logger()
	a, err := geta.New(one("/ws", geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, echoUpgrade, geta.Doc{})}), geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "echo")
	rec := httptest.NewRecorder() // no http.Hijacker
	a.ServeHTTP(rec, req)
	if rec.Code != 500 || !strings.Contains(buf.String(), "the connection cannot be switched") {
		t.Fatal(rec.Code, buf.String())
	}
}

// The writers AccessLog, Recover, ETag, and Compress hand on unwrap, so
// http.ResponseController reaches the server's writer through them: a
// deadline set, and full duplex enabled, by a handler behind each takes
// effect on the connection, where a writer that hid it would report
// http.ErrNotSupported.
func TestMiddlewareWritersUnwrapForAResponseController(t *testing.T) {
	for _, tc := range []struct {
		name string
		mw   geta.Middleware
		hdr  string // Accept-Encoding
	}{
		{"AccessLog", geta.AccessLog(slog.New(slog.DiscardHandler)), ""},
		{"Recover", geta.Recover(slog.New(slog.DiscardHandler)), ""},
		{"ETag", geta.ETag(), ""},
		{"Compress", geta.Compress(geta.GzipCoding()), "gzip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var errs []error
			probe := geta.Ordered(geta.OrderAuthorize, func(http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					rc := http.NewResponseController(w)
					errs = append(errs,
						rc.SetWriteDeadline(time.Now().Add(time.Minute)),
						rc.SetReadDeadline(time.Now().Add(time.Minute)),
						rc.EnableFullDuplex())
					io.WriteString(w, "ok")
				})
			})
			srv := httptest.NewServer(accepts(t, withRoot(one("/x", get(okHandler)), tc.mw, probe)))
			defer srv.Close()
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/x", nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.hdr != "" {
				req.Header.Set("Accept-Encoding", tc.hdr)
			}
			res, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil || res.StatusCode != http.StatusOK || string(body) != "ok" {
				t.Fatal(res.StatusCode, string(body), err)
			}
			if len(errs) != 3 {
				t.Fatalf("the handler ran %d times", len(errs)/3)
			}
			for i, err := range errs {
				if err != nil {
					t.Errorf("call %d: %v", i, err)
				}
			}
		})
	}
}
