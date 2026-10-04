package geta_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
)

type largeItem struct {
	ID    int     `json:"id"`
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

type largeList struct {
	Items []largeItem `json:"items"`
}

// largeOf is a list whose encoding is at least n bytes; a NaN score in its
// last item, when poisoned, makes it fail to encode at the very end.
func largeOf(n int, poisoned bool) *largeList {
	l := &largeList{}
	for size := 0; size < n; size += 32 { // an item encodes to more than 32 bytes
		l.Items = append(l.Items, largeItem{ID: len(l.Items), Name: "item " + strconv.Itoa(len(l.Items)), Score: 1.5})
	}
	if poisoned {
		l.Items[len(l.Items)-1].Score = math.NaN()
	}
	return l
}

func largeApp(t testing.TB, body *largeList, limit int, opts ...geta.Option) *geta.App {
	t.Helper()
	limits := geta.DefaultLimits
	limits.MaxResponseBuffer = limit
	h := func(context.Context, *empty) (*largeList, error) { return body, nil }
	a, err := geta.New(one("/x", get(h)), append(opts, geta.WithLimits(limits))...)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func wantJSON(t *testing.T, got []byte, want any) {
	t.Helper()
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, w) {
		t.Fatalf("body differs: %d bytes, want %d", len(got), len(w))
	}
}

// Every success JSON body carries nosniff, as every problem does.
func TestSuccessJSONIsNosniff(t *testing.T) {
	type env struct {
		Location string `header:"Location"`
		Body     ok     `body:"json"`
	}
	envelope := func(context.Context, *empty) (*env, error) { return &env{Location: "/x", Body: ok{true}}, nil }
	a := accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/plain", Route: get(okHandler)},
		{Path: "/envelope", Route: get(envelope)},
	}})
	for _, path := range []string{"/plain", "/envelope", "/missing"} {
		rec := do(t, a, http.MethodGet, path)
		if got := rec.Header().Values("X-Content-Type-Options"); len(got) != 1 || got[0] != "nosniff" {
			t.Errorf("%s: %q", path, got)
		}
	}
	if rec := do(t, largeApp(t, largeOf(4096, false), 1024), http.MethodGet, "/x"); rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("streamed: %v", rec.Header())
	}
}

// A body within MaxResponseBuffer has its Content-Length; a larger one is
// streamed without it, whole and unchanged.
func TestLargeBodiesStream(t *testing.T) {
	small, large := largeOf(512, false), largeOf(64<<10, false)
	rec := do(t, largeApp(t, small, 1024), http.MethodGet, "/x")
	if rec.Code != 200 || rec.Header().Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
		t.Fatalf("small: %d %v", rec.Code, rec.Header())
	}
	wantJSON(t, rec.Body.Bytes(), small)
	for _, limit := range []int{0, 1, 1024, 60 << 10} {
		rec := do(t, largeApp(t, large, limit), http.MethodGet, "/x")
		if rec.Code != 200 || rec.Header().Get("Content-Length") != "" || rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("limit %d: %d %v", limit, rec.Code, rec.Header())
		}
		wantJSON(t, rec.Body.Bytes(), large)
	}
}

type badText struct{}

func (badText) MarshalText() ([]byte, error) { return nil, errors.New("no text") }
func (*badText) UnmarshalText([]byte) error  { return nil }

// A failed envelope leaves nothing of itself on the 500, as a web server's
// error response would: no header or cookie set for the success response,
// whichever field failed; headers middleware set stay.
func TestAFailedEnvelopeLeavesNoHeaders(t *testing.T) {
	type env struct {
		Total int          `header:"X-Total"`
		First *http.Cookie `cookie:"a"`
		Bad   *http.Cookie `cookie:"b"`
		Body  item         `body:"json"`
	}
	h := func(context.Context, *empty) (*env, error) {
		return &env{Total: 1, First: &http.Cookie{Value: "ok"}, Bad: &http.Cookie{Value: "x;y"}, Body: item{Name: "n", Price: 1}}, nil
	}
	mark := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Middleware", "kept")
			next.ServeHTTP(w, r)
		})
	})
	a, err := geta.New(withRoot(one("/x", get(h)), mark), geta.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, a, http.MethodGet, "/x")
	if rec.Code != 500 || rec.Header().Get("X-Total") != "" || rec.Header().Values("Set-Cookie") != nil || rec.Header().Get("X-Middleware") != "kept" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
}

// An envelope's headers and cookies go out with a streamed body; a header
// that fails is a clean 500 even when the body is past the buffer, since the
// commit fails before any byte is written.
func TestLargeEnvelopes(t *testing.T) {
	type env struct {
		Total   int          `header:"X-Total"`
		Bad     *badText     `header:"X-Bad"`
		Session *http.Cookie `cookie:"s"`
		Body    largeList    `body:"json"`
	}
	large := largeOf(8192, false)
	bad := false
	h := func(context.Context, *empty) (*env, error) {
		e := &env{Total: len(large.Items), Session: &http.Cookie{Value: "v"}, Body: *large}
		if bad {
			e.Bad = &badText{}
		}
		return e, nil
	}
	limits := geta.DefaultLimits
	limits.MaxResponseBuffer = 1024
	a, err := geta.New(one("/x", get(h)), geta.WithLimits(limits), geta.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, a, http.MethodGet, "/x")
	if rec.Code != 200 || rec.Header().Get("X-Total") != strconv.Itoa(len(large.Items)) ||
		!strings.HasPrefix(rec.Header().Get("Set-Cookie"), "s=v") || rec.Header().Get("Content-Length") != "" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	wantJSON(t, rec.Body.Bytes(), large)

	bad = true
	rec = do(t, a, http.MethodGet, "/x")
	if rec.Code != 500 || rec.Header().Get("Content-Type") != geta.ProblemContentType {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
}

// A body that fails to encode within the buffer is a clean 500.
func TestABufferedBodyThatFailsIsA500(t *testing.T) {
	log, buf := logger()
	rec := do(t, largeApp(t, largeOf(512, true), 1024, geta.WithLogger(log)), http.MethodGet, "/x")
	if rec.Code != 500 || rec.Header().Get("Content-Type") != geta.ProblemContentType {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if !strings.Contains(buf.String(), "output could not be written") {
		t.Fatal(buf.String())
	}
}

// A body that fails to encode after the response committed cannot become a
// 500: the defect is logged and the connection aborted, so a real client
// reads an error, never a complete 200. Through Recover, Gzip, and ETag too.
func TestAStreamedBodyThatFailsAbortsTheConnection(t *testing.T) {
	quiet := slog.New(slog.DiscardHandler)
	body := largeOf(256<<10, true)
	h := func(context.Context, *empty) (*largeList, error) { return body, nil }
	limits := geta.DefaultLimits
	limits.MaxResponseBuffer = 1024
	for _, root := range [][]geta.Middleware{
		nil,
		{geta.AccessLog(quiet), geta.Recover(quiet)},
		{geta.Gzip(), geta.ETag()},
	} {
		log, buf := logger()
		a, err := geta.New(withRoot(one("/x", get(h)), root...), geta.WithLimits(limits), geta.WithLogger(log))
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(a)
		res, err := srv.Client().Get(srv.URL + "/x")
		if err == nil {
			_, err = io.ReadAll(res.Body)
			res.Body.Close()
		}
		srv.Close()
		if err == nil {
			t.Fatalf("root %d: the client read a complete %d", len(root), res.StatusCode)
		}
		t.Logf("root %d: %v", len(root), err)
		if !strings.Contains(buf.String(), "output failed after the response started") || !strings.Contains(buf.String(), "NaN") {
			t.Fatalf("root %d: %s", len(root), buf.String())
		}
	}
}

// Gzip and ETag hold the whole response, streamed or not, so a large body
// still gets its validator, its Content-Length, and its compression.
func TestLargeBodiesThroughGzipAndETag(t *testing.T) {
	large := largeOf(256<<10, false)
	limits := geta.DefaultLimits
	limits.MaxResponseBuffer = 1024
	h := func(context.Context, *empty) (*largeList, error) { return large, nil }
	a, err := geta.New(withRoot(one("/x", get(h)), geta.Gzip(), geta.ETag()), geta.WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, a, http.MethodGet, "/x", "Accept-Encoding", "gzip")
	tag := rec.Header().Get("ETag")
	if rec.Code != 200 || tag == "" || rec.Header().Get("Content-Encoding") != "gzip" ||
		rec.Header().Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON(t, plain, large)
	if rec := do(t, a, http.MethodGet, "/x", "If-None-Match", tag); rec.Code != 304 {
		t.Fatalf("%d", rec.Code)
	}
}

// discardWriter is a ResponseWriter that keeps nothing of the body but its
// length and the largest single write.
type discardWriter struct {
	h        http.Header
	status   int
	n        int
	maxWrite int
}

func (d *discardWriter) Header() http.Header  { return d.h }
func (d *discardWriter) WriteHeader(code int) { d.status = code }
func (d *discardWriter) Write(p []byte) (int, error) {
	d.n += len(p)
	d.maxWrite = max(d.maxWrite, len(p))
	return len(p), nil
}

// A multi-megabyte body under the default limits is held 64 KiB at a time:
// no write is larger, and serving it allocates a small fraction of its size.
func TestLargeBodiesHoldBoundedMemory(t *testing.T) {
	large := largeOf(8<<20, false)
	a := largeApp(t, large, geta.DefaultLimits.MaxResponseBuffer)
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	serve := func() *discardWriter {
		d := &discardWriter{h: http.Header{}}
		a.ServeHTTP(d, req)
		return d
	}
	serve() // warm the pools
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	d := serve()
	runtime.ReadMemStats(&after)
	if d.status != 200 || d.n < 8<<20 || d.h.Get("Content-Length") != "" {
		t.Fatalf("%d, %d bytes, %v", d.status, d.n, d.h)
	}
	if d.maxWrite > geta.DefaultLimits.MaxResponseBuffer {
		t.Fatalf("a write of %d bytes", d.maxWrite)
	}
	alloc := after.TotalAlloc - before.TotalAlloc
	if alloc > 1<<20 {
		t.Fatalf("serving %d bytes allocated %d", d.n, alloc)
	}
	t.Logf("serving %d bytes allocated %d; the largest write was %d", d.n, alloc, d.maxWrite)
}
