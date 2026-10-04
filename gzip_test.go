package geta_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

var big = strings.Repeat("x", 2000)

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestGzip(t *testing.T) {
	a := accepts(t, withRoot(geta.Table{Routes: []geta.Entry{
		{Path: "/big", Route: get(textHandler(big))},
		{Path: "/small", Route: get(textHandler("small"))},
		{Path: "/none", Route: geta.Route{Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error { return nil }, geta.Doc{})}},
	}}, geta.Gzip()))
	compressed := func(r *httptest.ResponseRecorder) bool { return r.Header().Get("Content-Encoding") == "gzip" }
	varies := func(r *httptest.ResponseRecorder) bool {
		return strings.Contains(r.Header().Get("Vary"), "Accept-Encoding")
	}

	r := do(t, a, "GET", "/big", "Accept-Encoding", "gzip")
	if !compressed(r) || !varies(r) || gunzip(t, r.Body.Bytes()) != `{"text":"`+big+`"}` || r.Header().Get("Content-Length") != itoa(r.Body.Len()) {
		t.Fatal(r.Header())
	}
	for _, ae := range []string{"gzip;q=0", "", "gzip;q=0, *", "br"} {
		r := do(t, a, "GET", "/big", "Accept-Encoding", ae)
		if compressed(r) || !varies(r) {
			t.Errorf("%q: %v", ae, r.Header())
		}
	}
	for _, ae := range []string{"*", "identity;q=0, gzip"} {
		if r := do(t, a, "GET", "/big", "Accept-Encoding", ae); !compressed(r) {
			t.Errorf("%q not compressed", ae)
		}
	}
	if r := do(t, a, "GET", "/small", "Accept-Encoding", "gzip"); compressed(r) || !varies(r) {
		t.Fatal("below threshold:", r.Header())
	}
	if r := do(t, a, "DELETE", "/none", "Accept-Encoding", "gzip"); compressed(r) || r.Code != 204 || !varies(r) {
		t.Fatal("204:", r.Header())
	}
}

// A response its handler writes nothing of varies on Accept-Encoding too: a
// shared cache would otherwise reuse it for a request of either coding.
func TestGzipVariesAnEmptyResponse(t *testing.T) {
	silent := geta.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("ETag", `"e"`)
		})
	})
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.Gzip(), silent))
	for _, ae := range []string{"gzip", ""} {
		r := do(t, a, "GET", "/x", "Accept-Encoding", ae)
		if r.Code != http.StatusOK || r.Body.Len() != 0 || !slices.Contains(r.Header().Values("Vary"), "Accept-Encoding") ||
			r.Header().Get("ETag") != `"e"` {
			t.Errorf("%q: %d %v", ae, r.Code, r.Header())
		}
	}
}

func TestGzipMediaTypes(t *testing.T) {
	for ct, want := range map[string]bool{
		"application/json; charset=utf-8": true,
		"image/svg+xml":                   true,
		"application/problem+json":        true,
		"text/plain":                      true,
		"image/jpeg":                      false,
		"":                                false,
	} {
		set := geta.Use(func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if ct != "" {
					w.Header().Set("Content-Type", ct)
				}
				io.WriteString(w, big)
			})
		})
		r := do(t, accepts(t, withRoot(one("/x", get(okHandler)), geta.Gzip(), set)), "GET", "/x", "Accept-Encoding", "gzip")
		if (r.Header().Get("Content-Encoding") == "gzip") != want {
			t.Errorf("%q: %v", ct, r.Header())
		}
	}
	enc := geta.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "br")
			io.WriteString(w, big)
		})
	})
	if r := do(t, accepts(t, withRoot(one("/x", get(okHandler)), geta.Gzip(), enc)), "GET", "/x", "Accept-Encoding", "gzip"); r.Header().Get("Content-Encoding") != "br" {
		t.Fatal(r.Header())
	}
}

// The q parameter's name is case-insensitive.
func TestGzipQParameterIsCaseInsensitive(t *testing.T) {
	h := func(ctx context.Context, _ *empty) (*bigBody, error) {
		return &bigBody{strings.Repeat("a", 4096)}, nil
	}
	app, err := geta.New(geta.Table{Root: geta.Scope{geta.Gzip()}, Routes: []geta.Entry{{Path: "/b", Route: get(h)}}},
		geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	for ae, want := range map[string]string{"gzip;Q=0": "", "gzip; Q=0.5": "gzip", "gzip;q=0": "", "*;Q=0": ""} {
		req := httptest.NewRequest(http.MethodGet, "/b", nil)
		req.Header.Set("Accept-Encoding", ae)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if got := rec.Header().Get("Content-Encoding"); got != want {
			t.Fatalf("Accept-Encoding %q: Content-Encoding %q, want %q", ae, got, want)
		}
	}
}

// gzip outside etag: the tag is over the identity body, and the compressed
// form carries a strong tag of its own, so the two never share a validator.
func TestGzipOverETag(t *testing.T) {
	a := accepts(t, withRoot(one("/x", get(textHandler(big))), geta.Gzip(), geta.ETag()))
	r := do(t, a, "GET", "/x", "Accept-Encoding", "gzip")
	plain := do(t, a, "GET", "/x")
	tag := plain.Header().Get("ETag")
	coded := strings.TrimSuffix(tag, `"`) + `-gzip"`
	if r.Header().Get("Content-Encoding") != "gzip" || !strings.HasPrefix(tag, `"`) || r.Header().Get("ETag") != coded {
		t.Fatal(r.Header(), plain.Header())
	}
	nm := do(t, a, "GET", "/x", "Accept-Encoding", "gzip", "If-None-Match", coded)
	if nm.Code != 304 || nm.Body.Len() != 0 || nm.Header().Get("Content-Encoding") != "" || nm.Header().Get("ETag") != coded {
		t.Fatal(nm.Code, nm.Header())
	}
}

// Over a real connection, Content-Length is the compressed size.
func TestGzipFramingOnTheWire(t *testing.T) {
	c := getatest.New(t, withRoot(one("/x", get(textHandler(big))), geta.Gzip()))
	res := c.With("Accept-Encoding", "gzip").Get("/x")
	if res.Header.Get("Content-Encoding") != "gzip" || res.Header.Get("Content-Length") != itoa(len(res.Body)) || res.Header.Get("Transfer-Encoding") != "" {
		t.Fatal(res.Header)
	}
	if gunzip(t, res.Body) != `{"text":"`+big+`"}` {
		t.Fatal("body")
	}
}

// GzipCoding's encoders are pooled, each reused one coding the same bytes
// gzip.NewWriter's does.
func TestGzipCodingReusesItsEncoders(t *testing.T) {
	c := geta.GzipCoding()
	body := []byte(`{"text":"` + big + `"}`)
	var want bytes.Buffer
	zw := gzip.NewWriter(&want)
	zw.Write(body)
	zw.Close()
	encode := func() []byte {
		var out bytes.Buffer
		w, err := c.NewWriter(&out)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	for range 3 {
		if got := encode(); !bytes.Equal(got, want.Bytes()) {
			t.Fatalf("coded %x; want %x", got, want.Bytes())
		}
	}
	if raceEnabled {
		return
	}
	var out bytes.Buffer
	out.Grow(4 * want.Len())
	allocs := testing.AllocsPerRun(100, func() {
		out.Reset()
		w, _ := c.NewWriter(&out)
		w.Write(body)
		w.Close()
	})
	if allocs > 2 {
		t.Fatalf("%v allocations per coded body", allocs)
	}
}

// A GzipCoding writer used after its Close reaches no encoder: the encoder
// it held, now another writer's, codes that writer's body alone.
func TestGzipCodingWriterUsedAfterCloseFails(t *testing.T) {
	c := geta.GzipCoding()
	body := []byte(`{"text":"` + big + `"}`)
	var want bytes.Buffer
	zw := gzip.NewWriter(&want)
	zw.Write(body)
	zw.Close()

	var first, second bytes.Buffer
	stale, _ := c.NewWriter(&first)
	stale.Write([]byte("first"))
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	fresh, _ := c.NewWriter(&second)
	fresh.Write(body[:10])
	if _, err := stale.Write([]byte("stray")); err != geta.ErrEncoderClosed {
		t.Errorf("Write after Close: %v", err)
	}
	if err := stale.(interface{ Flush() error }).Flush(); err != geta.ErrEncoderClosed {
		t.Errorf("Flush after Close: %v", err)
	}
	if err := stale.Close(); err != geta.ErrEncoderClosed {
		t.Errorf("Close after Close: %v", err)
	}
	fresh.Write(body[10:])
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(second.Bytes(), want.Bytes()) {
		t.Fatalf("the second body was changed: %x", second.Bytes())
	}
}

type bigBody struct {
	S string `json:"s"`
}

// Gzip outside ETag: the gzip form carries a strong tag of its own, derived
// from the identity form's, and either tag earns a 304.
func TestGzipTagsTheGzipForm(t *testing.T) {
	h := func(ctx context.Context, _ *empty) (*bigBody, error) {
		return &bigBody{strings.Repeat("a", 4096)}, nil
	}
	app, err := geta.New(geta.Table{Root: geta.Scope{geta.Gzip(), geta.ETag()}, Routes: []geta.Entry{{Path: "/b", Route: get(h)}}},
		geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	serve := func(gzip bool, inm string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/b", nil)
		if gzip {
			req.Header.Set("Accept-Encoding", "gzip")
		}
		if inm != "" {
			req.Header.Set("If-None-Match", inm)
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec
	}
	plain := serve(false, "")
	tag := plain.Header().Get("ETag")
	if tag == "" || strings.HasPrefix(tag, "W/") || plain.Header().Get("Content-Encoding") != "" {
		t.Fatalf("identity: ETag %q, Content-Encoding %q", tag, plain.Header().Get("Content-Encoding"))
	}
	// The gzip form has a strong tag of its own, derived from the identity
	// form's, which If-None-Match reads back.
	coded := strings.TrimSuffix(tag, `"`) + `-gzip"`
	gz := serve(true, "")
	if gz.Header().Get("Content-Encoding") != "gzip" || gz.Header().Get("ETag") != coded {
		t.Fatalf("gzip: ETag %q, Content-Encoding %q; want %s, gzip", gz.Header().Get("ETag"), gz.Header().Get("Content-Encoding"), coded)
	}
	if !strings.Contains(strings.Join(gz.Header().Values("Vary"), ","), "Accept-Encoding") {
		t.Fatalf("gzip: Vary %q", gz.Header().Values("Vary"))
	}
	if rec := serve(true, coded); rec.Code != http.StatusNotModified || rec.Header().Get("ETag") != coded {
		t.Fatalf("gzip revalidation: %d ETag %q; want 304 %s", rec.Code, rec.Header().Get("ETag"), coded)
	}
	// A weak comparison holds across codings: the weak form of either tag
	// revalidates too.
	if rec := serve(true, "W/"+tag); rec.Code != http.StatusNotModified {
		t.Fatalf("gzip revalidation with W/%s: %d", tag, rec.Code)
	}
	if rec := serve(false, tag); rec.Code != http.StatusNotModified || rec.Header().Get("ETag") != tag {
		t.Fatalf("identity revalidation: %d ETag %q; want 304 %s", rec.Code, rec.Header().Get("ETag"), tag)
	}
}

// gzcTagged is a large representation and its entity tag.
type gzcTagged struct {
	ETag string `header:"ETag"`
	Body text   `body:"json"`
}

// gzcResource is one representation whose tag a conditional PUT changes.
type gzcResource struct {
	mu  sync.Mutex
	tag string
	n   int
}

func (s *gzcResource) get(_ context.Context, in *condIn) (*gzcTagged, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := in.Check(s.tag, modifiedAt); err != nil {
		return nil, err
	}
	return &gzcTagged{ETag: s.tag, Body: text{big}}, nil
}

func (s *gzcResource) put(_ context.Context, in *condIn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := in.Check(s.tag, modifiedAt); err != nil {
		return err
	}
	s.n++
	s.tag = `"v` + strings.Repeat("i", s.n) + `"`
	return nil
}

// gzcApp serves r on /r behind Gzip and ETag, as the register example does.
func gzcApp(t *testing.T, r *gzcResource) *geta.App {
	return accepts(t, geta.Table{
		Root: geta.Scope{geta.Gzip(), geta.ETag()},
		Routes: []geta.Entry{{Path: "/r", Route: geta.Route{
			Get: geta.Op(http.StatusOK, r.get, geta.Doc{}),
			Put: geta.OpNoBody(http.StatusNoContent, r.put, geta.Doc{}),
		}}},
	})
}

// A client that fetched the gzip form holds its tag, and makes conditional
// requests with it: Gzip reads it back as the handler's before
// Conditional.Check compares it strongly.
func TestGzipFormTagMakesConditionalRequests(t *testing.T) {
	a := gzcApp(t, &gzcResource{tag: `"v"`})
	r := do(t, a, "GET", "/r", "Accept-Encoding", "gzip")
	if r.Code != 200 || r.Header().Get("Content-Encoding") != "gzip" || r.Header().Get("ETag") != `"v-gzip"` {
		t.Fatal(r.Code, r.Header())
	}
	if !slices.Contains(r.Header().Values("Vary"), "Accept-Encoding") {
		t.Fatal(r.Header())
	}
	// Revalidating with the gzip form's tag: 304 carrying that tag.
	r = do(t, a, "GET", "/r", "Accept-Encoding", "gzip", "If-None-Match", `"v-gzip"`)
	if r.Code != 304 || r.Header().Get("ETag") != `"v-gzip"` || r.Body.Len() != 0 {
		t.Fatal(r.Code, r.Header())
	}
	// A conditional write with the tag held succeeds, once.
	if r := do(t, a, "PUT", "/r", "Accept-Encoding", "gzip", "If-Match", `"v-gzip"`); r.Code != 204 {
		t.Fatalf("PUT If-Match gzip tag: %d %s", r.Code, r.Body)
	}
	if r := do(t, a, "PUT", "/r", "Accept-Encoding", "gzip", "If-Match", `"v-gzip"`); r.Code != 412 {
		t.Fatalf("PUT If-Match stale gzip tag: %d %s", r.Code, r.Body)
	}
	// In a list, beside other tags; If-None-Match on a write fails on it.
	if r := do(t, a, "PUT", "/r", "If-Match", `"x", "vi-gzip"`, "If-None-Match", `"vi-gzip"`); r.Code != 412 {
		t.Fatalf("PUT If-None-Match current gzip tag: %d %s", r.Code, r.Body)
	}
	r = do(t, a, "GET", "/r", "Accept-Encoding", "gzip", "If-None-Match", `"v-gzip"`)
	if r.Code != 200 || r.Header().Get("ETag") != `"vi-gzip"` {
		t.Fatal(r.Code, r.Header())
	}
	r = do(t, a, "GET", "/r", "Accept-Encoding", "gzip", "If-None-Match", `"x", "vi-gzip"`)
	if r.Code != 304 || r.Header().Get("ETag") != `"vi-gzip"` {
		t.Fatal(r.Code, r.Header())
	}
}

// A client that takes the identity form sees the handler's tags, unchanged.
func TestGzipLeavesIdentityTags(t *testing.T) {
	a := gzcApp(t, &gzcResource{tag: `"v"`})
	r := do(t, a, "GET", "/r")
	if r.Code != 200 || r.Header().Get("Content-Encoding") != "" || r.Header().Get("ETag") != `"v"` {
		t.Fatal(r.Code, r.Header())
	}
	if r := do(t, a, "GET", "/r", "If-None-Match", `"v"`); r.Code != 304 || r.Header().Get("ETag") != `"v"` {
		t.Fatal(r.Code, r.Header())
	}
	// A client that accepts gzip but asks with the identity tag gets it back.
	if r := do(t, a, "GET", "/r", "Accept-Encoding", "gzip", "If-None-Match", `"v"`); r.Code != 304 || r.Header().Get("ETag") != `"v"` {
		t.Fatal(r.Code, r.Header())
	}
	if r := do(t, a, "PUT", "/r", "If-Match", `"v"`); r.Code != 204 {
		t.Fatal(r.Code, r.Body)
	}
	if r := do(t, a, "PUT", "/r", "If-Match", `"v"`); r.Code != 412 {
		t.Fatal(r.Code, r.Body)
	}
	// "*" and a value that is no list pass through as they came.
	if r := do(t, a, "PUT", "/r", "If-Match", "*"); r.Code != 204 {
		t.Fatal(r.Code, r.Body)
	}
	if r := do(t, a, "PUT", "/r", "If-Match", `v-gzip`); r.Code != 400 {
		t.Fatal(r.Code, r.Body)
	}
}

// A weak tag stays weak on the gzip form, and weak comparison holds across
// codings.
func TestGzipLeavesWeakTags(t *testing.T) {
	a := gzcApp(t, &gzcResource{tag: `W/"w-gzip"`})
	r := do(t, a, "GET", "/r", "Accept-Encoding", "gzip")
	if r.Header().Get("Content-Encoding") != "gzip" || r.Header().Get("ETag") != `W/"w-gzip"` {
		t.Fatal(r.Header())
	}
	r = do(t, a, "GET", "/r", "Accept-Encoding", "gzip", "If-None-Match", `W/"w-gzip"`)
	if r.Code != 304 || r.Header().Get("ETag") != `W/"w-gzip"` {
		t.Fatal(r.Code, r.Header())
	}
	// If-Match never matches a weak tag.
	if r := do(t, a, "PUT", "/r", "If-Match", `W/"w-gzip"`); r.Code != 412 {
		t.Fatal(r.Code, r.Body)
	}
}

// A handler's strong tag that would read as a gzip form's goes out with
// "-identity" appended, so every tag Gzip sends reads back as exactly one of
// the handler's.
func TestGzipTagsReadBackUnambiguously(t *testing.T) {
	for handler, want := range map[string][2]string{ // identity form's, gzip form's
		`"x"`:                 {`"x"`, `"x-gzip"`},
		`"x-gzip"`:            {`"x-gzip-identity"`, `"x-gzip-gzip"`},
		`"x-gzip-identity"`:   {`"x-gzip-identity-identity"`, `"x-gzip-identity-gzip"`},
		`"x-identity"`:        {`"x-identity"`, `"x-identity-gzip"`},
		`"-gzip"`:             {`"-gzip-identity"`, `"-gzip-gzip"`},
		`""`:                  {`""`, `"-gzip"`},
		`"x-identity-gzip-y"`: {`"x-identity-gzip-y"`, `"x-identity-gzip-y-gzip"`},
	} {
		a := gzcApp(t, &gzcResource{tag: handler})
		plain := do(t, a, "GET", "/r")
		gz := do(t, a, "GET", "/r", "Accept-Encoding", "gzip")
		if plain.Header().Get("ETag") != want[0] || gz.Header().Get("ETag") != want[1] {
			t.Errorf("%s: identity %s, gzip %s; want %s, %s", handler, plain.Header().Get("ETag"), gz.Header().Get("ETag"), want[0], want[1])
			continue
		}
		for _, sent := range want {
			if r := do(t, a, "GET", "/r", "Accept-Encoding", "gzip", "If-None-Match", sent); r.Code != 304 || r.Header().Get("ETag") != sent {
				t.Errorf("%s: If-None-Match %s: %d %s", handler, sent, r.Code, r.Header().Get("ETag"))
			}
		}
		// Each sent tag names the handler's current one; none other does.
		a = gzcApp(t, &gzcResource{tag: handler})
		if r := do(t, a, "PUT", "/r", "If-Match", want[1]); r.Code != 204 {
			t.Errorf("%s: If-Match %s: %d", handler, want[1], r.Code)
		}
		a = gzcApp(t, &gzcResource{tag: handler})
		if r := do(t, a, "PUT", "/r", "If-Match", want[0]); r.Code != 204 {
			t.Errorf("%s: If-Match %s: %d", handler, want[0], r.Code)
		}
		if handler != want[0] {
			// The raw tag is not one Gzip sent: it names another.
			a = gzcApp(t, &gzcResource{tag: handler})
			if r := do(t, a, "PUT", "/r", "If-Match", handler); r.Code != 412 {
				t.Errorf("%s: If-Match the raw tag: %d", handler, r.Code)
			}
		}
	}
}

// A stream passes Gzip unheld, its tag sent as an identity form's.
func TestGzipStreamTagReadsBack(t *testing.T) {
	stream := geta.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("ETag", `"s-gzip"`)
			w.WriteHeader(http.StatusOK)
			http.NewResponseController(w).Flush()
			io.WriteString(w, "data: "+r.Header.Get("If-None-Match")+"\n\n")
		})
	})
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.Gzip(), stream))
	r := do(t, a, "GET", "/x", "Accept-Encoding", "gzip", "If-None-Match", `"s-gzip-identity"`)
	if r.Header().Get("ETag") != `"s-gzip-identity"` || r.Header().Get("Content-Encoding") != "" || r.Body.String() != "data: \"s-gzip\"\n\n" {
		t.Fatal(r.Header(), r.Body)
	}
}

// A small body is not compressed, and its tag is the identity form's.
func TestGzipSmallBodyKeepsTheTag(t *testing.T) {
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.Gzip(), geta.ETag()))
	plain := do(t, a, "GET", "/x")
	gz := do(t, a, "GET", "/x", "Accept-Encoding", "gzip")
	if gz.Header().Get("Content-Encoding") != "" || gz.Header().Get("ETag") != plain.Header().Get("ETag") {
		t.Fatal(gz.Header(), plain.Header())
	}
}

// A handler that writes nothing under Gzip still sends its tag as an
// identity form's, which reads back as the handler's.
func TestGzipTagsAResponseWrittenWithNothing(t *testing.T) {
	var seen string
	silent := geta.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = r.Header.Get("If-Match")
			w.Header().Set("ETag", `"s-gzip"`)
		})
	})
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.Gzip(), silent))
	r := do(t, a, "GET", "/x", "Accept-Encoding", "gzip")
	if r.Header().Get("ETag") != `"s-gzip-identity"` {
		t.Fatal(r.Code, r.Header())
	}
	do(t, a, "GET", "/x", "If-Match", `"s-gzip-identity"`)
	if seen != `"s-gzip"` {
		t.Fatal(seen)
	}
}
