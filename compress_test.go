package geta_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/koji-1009/geta"
)

// fakeCoding is a reversible stand-in for a coding geta does not carry, such
// as zstd: the coded form is the name, a colon, and the body. It counts the
// encoders it makes and closes.
type fakeCoding struct {
	name            string
	opened, closed  atomic.Int64
	failNew, failCl bool
}

func (f *fakeCoding) coding() geta.Coding {
	return geta.Coding{Name: f.name, NewWriter: func(w io.Writer) (io.WriteCloser, error) {
		if f.failNew {
			return nil, errors.New("no encoder")
		}
		f.opened.Add(1)
		if _, err := io.WriteString(w, f.name+":"); err != nil {
			return nil, err
		}
		return &fakeWriter{w: w, f: f}, nil
	}}
}

type fakeWriter struct {
	w io.Writer
	f *fakeCoding
}

func (z *fakeWriter) Write(p []byte) (int, error) { return z.w.Write(p) }

func (z *fakeWriter) Close() error {
	z.f.closed.Add(1)
	if z.f.failCl {
		return errors.New("close failed")
	}
	return nil
}

// decoded reads a response body back from its Content-Encoding.
func decoded(t *testing.T, r *httptest.ResponseRecorder) string {
	t.Helper()
	switch ce := r.Header().Get("Content-Encoding"); ce {
	case "":
		return r.Body.String()
	case "gzip":
		return gunzip(t, r.Body.Bytes())
	default:
		body, ok := strings.CutPrefix(r.Body.String(), ce+":")
		if !ok {
			t.Fatalf("%s body %q", ce, r.Body)
		}
		return body
	}
}

// Of the codings the client accepts, the highest q-value wins and the
// server's order breaks a tie; identity wins only with a higher q-value.
func TestCompressNegotiation(t *testing.T) {
	zstd, br := &fakeCoding{name: "zstd"}, &fakeCoding{name: "br"}
	a := accepts(t, withRoot(one("/x", get(textHandler(big))), geta.Compress(zstd.coding(), br.coding(), geta.GzipCoding())))
	want := `{"text":"` + big + `"}`
	for ae, ce := range map[string]string{
		"gzip":                          "gzip",
		"x-gzip":                        "gzip",
		"gzip, br, zstd":                "zstd",
		"br, gzip":                      "br",
		"ZSTD":                          "zstd",
		"zstd;q=0.5, br;q=0.8":          "br",
		"zstd;q=0.5, gzip":              "gzip",
		"*":                             "zstd",
		"*, zstd;q=0":                   "br",
		"*;q=0.5, gzip":                 "gzip",
		"*;q=0.5, gzip;q=0.3":           "zstd",
		"*;q=0.5, identity;q=0.6":       "",
		"zstd;Q=0":                      "",
		"br;q=0;x=1, zstd;q=0.1":        "zstd",
		"gzip;q=0.5, identity":          "",
		"gzip, identity":                "gzip",
		"identity;q=0.5, br;q=0.6":      "br",
		"identity;q=0":                  "",
		"identity;q=0, br":              "br",
		"*;q=0":                         "",
		"deflate":                       "",
		"":                              "",
		"gzip\xc2\xa0":                  "", // trimmed of spaces and tabs alone, it names no coding
		"identity\xc2\xa0, gzip;q=0.5":  "gzip",
		"gzip;q=0, br;q=0, zstd;q=0, *": "",
	} {
		r := do(t, a, "GET", "/x", "Accept-Encoding", ae)
		if r.Code != 200 || r.Header().Get("Content-Encoding") != ce || decoded(t, r) != want ||
			!slices.Contains(r.Header().Values("Vary"), "Accept-Encoding") || r.Header().Get("Content-Length") != itoa(r.Body.Len()) {
			t.Errorf("%q: %d %v; want %q", ae, r.Code, r.Header(), ce)
		}
	}
	// Without Accept-Encoding, nothing is coded.
	if r := do(t, a, "GET", "/x"); r.Header().Get("Content-Encoding") != "" || decoded(t, r) != want {
		t.Error(r.Header())
	}
	// One encoder per coded response, each closed once.
	if zstd.opened.Load() == 0 || zstd.opened.Load() != zstd.closed.Load() || br.opened.Load() != br.closed.Load() {
		t.Errorf("zstd %d/%d, br %d/%d", zstd.opened.Load(), zstd.closed.Load(), br.opened.Load(), br.closed.Load())
	}
}

// Gzip negotiates as Compress does: identity preferred by q-value is sent
// uncoded, and a client refusing identity gets even a small body coded.
func TestGzipHonoursIdentity(t *testing.T) {
	a := accepts(t, withRoot(geta.Table{Routes: []geta.Entry{
		{Path: "/big", Route: get(textHandler(big))},
		{Path: "/small", Route: get(textHandler("small"))},
	}}, geta.Gzip()))
	if r := do(t, a, "GET", "/big", "Accept-Encoding", "gzip;q=0.5, identity"); r.Header().Get("Content-Encoding") != "" {
		t.Error("identity preferred:", r.Header())
	}
	if r := do(t, a, "GET", "/big", "Accept-Encoding", "*;q=0.5, gzip;q=0.3"); r.Header().Get("Content-Encoding") != "" {
		t.Error("identity preferred through *:", r.Header())
	}
	r := do(t, a, "GET", "/small", "Accept-Encoding", "identity;q=0, gzip")
	if r.Header().Get("Content-Encoding") != "gzip" || gunzip(t, r.Body.Bytes()) != `{"text":"small"}` {
		t.Error("identity refused:", r.Header())
	}
	// Refusing identity and every coding gets the identity form, not 406.
	if r := do(t, a, "GET", "/small", "Accept-Encoding", "identity;q=0, gzip;q=0"); r.Code != 200 || r.Header().Get("Content-Encoding") != "" {
		t.Error("nothing acceptable:", r.Code, r.Header())
	}
	if r := do(t, a, "GET", "/small", "Accept-Encoding", "*;q=0"); r.Code != 200 || r.Header().Get("Content-Encoding") != "" {
		t.Error("*;q=0:", r.Code, r.Header())
	}
}

// A client refusing identity gets any body with content coded, whatever its
// media type; an empty body or a 204 stays as it is.
func TestCompressRefusedIdentityCodesAnyBody(t *testing.T) {
	br := &fakeCoding{name: "br"}
	jpeg := geta.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/jpeg")
			io.WriteString(w, "jpeg")
		})
	})
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.Compress(br.coding()), jpeg))
	if r := do(t, a, "GET", "/x", "Accept-Encoding", "identity;q=0, br"); r.Header().Get("Content-Encoding") != "br" || r.Body.String() != "br:jpeg" {
		t.Error(r.Header(), r.Body)
	}
	if r := do(t, a, "GET", "/x", "Accept-Encoding", "br"); r.Header().Get("Content-Encoding") != "" || r.Body.String() != "jpeg" {
		t.Error(r.Header(), r.Body)
	}
	a = accepts(t, withRoot(geta.Table{Routes: []geta.Entry{{Path: "/none", Route: geta.Route{
		Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error { return nil }, geta.Doc{}),
	}}}}, geta.Compress(br.coding())))
	if r := do(t, a, "DELETE", "/none", "Accept-Encoding", "identity;q=0, br"); r.Code != 204 || r.Header().Get("Content-Encoding") != "" || r.Body.Len() != 0 {
		t.Error(r.Code, r.Header())
	}
}

// compApp serves r on /r behind Compress (zstd, br, gzip) and ETag.
func compApp(t *testing.T, r *gzcResource, codings ...geta.Coding) *geta.App {
	if codings == nil {
		codings = []geta.Coding{(&fakeCoding{name: "zstd"}).coding(), (&fakeCoding{name: "br"}).coding(), geta.GzipCoding()}
	}
	return accepts(t, geta.Table{
		Root: geta.Scope{geta.Compress(codings...), geta.ETag()},
		Routes: []geta.Entry{{Path: "/r", Route: geta.Route{
			Get: geta.Op(http.StatusOK, r.get, geta.Doc{}),
			Put: geta.OpNoBody(http.StatusNoContent, r.put, geta.Doc{}),
		}}},
	})
}

// Each coded form has a strong tag of its own, and Compress reads each back
// as the handler's for Conditional.Check and ETag.
func TestCompressTagsEachCoding(t *testing.T) {
	a := compApp(t, &gzcResource{tag: `"v"`})
	for ae, tag := range map[string]string{"zstd": `"v-zstd"`, "br": `"v-br"`, "gzip": `"v-gzip"`, "": `"v"`} {
		r := do(t, a, "GET", "/r", "Accept-Encoding", ae)
		if r.Code != 200 || r.Header().Get("ETag") != tag || r.Header().Get("Content-Encoding") != ae {
			t.Errorf("%q: %d %v", ae, r.Code, r.Header())
		}
	}
	for _, c := range []struct{ ae, inm, code, tag string }{
		{"zstd", `"v-zstd"`, "304", `"v-zstd"`},
		{"br", `"v-br"`, "304", `"v-br"`},
		// The client holds the br form, though it would now get zstd.
		{"zstd, br", `"v-br"`, "304", `"v-br"`},
		{"zstd, br", `"x", "v-zstd", "v-br"`, "304", `"v-zstd"`},
		{"br", `"v-zstd", "v-br"`, "304", `"v-br"`},
		// A coding the client no longer accepts: the identity form's tag.
		{"", `"v-zstd"`, "304", `"v"`},
		{"zstd;q=0, br", `"v-zstd"`, "304", `"v"`},
		{"zstd", `"v"`, "304", `"v"`},
		{"zstd", `"v-deflate"`, "200", `"v-zstd"`},
	} {
		r := do(t, a, "GET", "/r", "Accept-Encoding", c.ae, "If-None-Match", c.inm)
		if itoa(r.Code) != c.code || r.Header().Get("ETag") != c.tag || r.Header().Get("Vary") != "Accept-Encoding" {
			t.Errorf("%q If-None-Match %s: %d %v; want %s %s", c.ae, c.inm, r.Code, r.Header(), c.code, c.tag)
		}
	}
	// Conditional writes with the tag of any coded form, each once.
	if r := do(t, a, "PUT", "/r", "If-Match", `"v-zstd"`); r.Code != 204 {
		t.Fatal(r.Code, r.Body)
	}
	if r := do(t, a, "PUT", "/r", "If-Match", `"v-br"`); r.Code != 412 {
		t.Fatal(r.Code, r.Body)
	}
	if r := do(t, a, "PUT", "/r", "If-Match", `"vi-br"`); r.Code != 204 {
		t.Fatal(r.Code, r.Body)
	}
	if r := do(t, a, "PUT", "/r", "If-Match", `"x", "vii-gzip"`, "If-None-Match", `"vii-zstd"`); r.Code != 412 {
		t.Fatal(r.Code, r.Body)
	}
}

// A weak tag stays weak, and as it is, on every coded form.
func TestCompressLeavesWeakTags(t *testing.T) {
	a := compApp(t, &gzcResource{tag: `W/"w-zstd"`})
	r := do(t, a, "GET", "/r", "Accept-Encoding", "zstd")
	if r.Header().Get("Content-Encoding") != "zstd" || r.Header().Get("ETag") != `W/"w-zstd"` {
		t.Fatal(r.Header())
	}
	if r := do(t, a, "GET", "/r", "Accept-Encoding", "br", "If-None-Match", `W/"w-zstd"`); r.Code != 304 || r.Header().Get("ETag") != `W/"w-zstd"` {
		t.Fatal(r.Code, r.Header())
	}
}

// Every tag Compress sends, for any handler's tag and any form, reads back as
// that handler's tag and no other's, even with coding names that hold "-".
func TestCompressTagsReadBackUnambiguously(t *testing.T) {
	codings := func() []geta.Coding {
		return []geta.Coding{
			(&fakeCoding{name: "pack200-gzip"}).coding(), (&fakeCoding{name: "br"}).coding(),
			(&fakeCoding{name: "x.y"}).coding(), (&fakeCoding{name: "pack200"}).coding(),
		}
	}
	forms := []string{"", "pack200-gzip", "br", "x.y", "pack200"}
	pieces := []string{"-br", "-identity", "-pack200-gzip", "-gzip", "-pack200", "-x.y", "-y"}
	handlers := []string{"", "a"}
	for range 3 {
		for _, h := range handlers {
			for _, p := range pieces {
				if !slices.Contains(handlers, h+p) {
					handlers = append(handlers, h+p)
				}
			}
		}
	}
	seen := map[string]string{} // sent tag → handler's
	for _, h := range handlers {
		res := &gzcResource{tag: `"` + h + `"`}
		a := compApp(t, res, codings()...)
		for _, form := range forms {
			r := do(t, a, "GET", "/r", "Accept-Encoding", form)
			sent := r.Header().Get("ETag")
			if r.Header().Get("Content-Encoding") != form {
				t.Fatalf("%q in %q: %v", h, form, r.Header())
			}
			if other, ok := seen[sent]; ok {
				t.Fatalf("%s is sent for both %q and %q", sent, other, h)
			}
			seen[sent] = h
			// The sent tag names the handler's current tag.
			if r := do(t, a, "PUT", "/r", "If-Match", sent); r.Code != 204 {
				t.Fatalf("%q in %q: If-Match %s: %d", h, form, sent, r.Code)
			}
			res.tag = `"` + h + `"`
		}
	}
	if len(seen) != len(handlers)*len(forms) {
		t.Fatal(len(seen))
	}
}

// A stream passes Compress uncoded, its encoders untouched, its tag sent as
// an identity form's.
func TestCompressStreamPassesUncoded(t *testing.T) {
	zstd := &fakeCoding{name: "zstd"}
	stream := geta.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("ETag", `"s-zstd"`)
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, "data: "+r.Header.Get("If-None-Match")+strings.Repeat(" ", 2000)+"\n\n")
		})
	})
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.Compress(zstd.coding()), stream))
	r := do(t, a, "GET", "/x", "Accept-Encoding", "zstd", "If-None-Match", `"s-zstd-identity"`)
	if r.Header().Get("ETag") != `"s-zstd-identity"` || r.Header().Get("Content-Encoding") != "" ||
		!strings.HasPrefix(r.Body.String(), "data: \"s-zstd\" ") || zstd.opened.Load() != 0 {
		t.Fatal(r.Header(), r.Body)
	}
}

// statusCounter records each status written through it.
type statusCounter struct {
	*httptest.ResponseRecorder
	codes []int
}

func (s *statusCounter) WriteHeader(code int) {
	s.codes = append(s.codes, code)
	s.ResponseRecorder.WriteHeader(code)
}

// Bytes written after a 101 pass Compress as they are, with no status after
// the 101: the tag writer beneath it writes none of its own, which net/http
// would log as a superfluous WriteHeader.
func TestCompressPassesBytesAfterA101WithoutAnotherStatus(t *testing.T) {
	switching := geta.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("ETag", `"s"`)
			w.WriteHeader(http.StatusSwitchingProtocols)
			io.WriteString(w, "switched")
		})
	})
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.Compress((&fakeCoding{name: "zstd"}).coding()), switching))
	rec := &statusCounter{ResponseRecorder: httptest.NewRecorder()}
	a.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if !slices.Equal(rec.codes, []int{http.StatusSwitchingProtocols}) || rec.Body.String() != "switched" {
		t.Fatalf("statuses %v, body %q", rec.codes, rec.Body)
	}
}

// A small body is not coded, and keeps the identity form's tag.
func TestCompressSmallBodyKeepsTheTag(t *testing.T) {
	zstd := &fakeCoding{name: "zstd"}
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.Compress(zstd.coding()), geta.ETag()))
	plain := do(t, a, "GET", "/x")
	r := do(t, a, "GET", "/x", "Accept-Encoding", "zstd")
	if r.Header().Get("Content-Encoding") != "" || r.Header().Get("ETag") != plain.Header().Get("ETag") || zstd.opened.Load() != 0 {
		t.Fatal(r.Header(), plain.Header())
	}
}

// HEAD gets the header GET would.
func TestCompressHead(t *testing.T) {
	a := compApp(t, &gzcResource{tag: `"v"`})
	g := do(t, a, "GET", "/r", "Accept-Encoding", "br")
	h := do(t, a, "HEAD", "/r", "Accept-Encoding", "br")
	for _, k := range []string{"Content-Encoding", "ETag", "Vary", "Content-Length"} {
		if h.Header().Get(k) != g.Header().Get(k) {
			t.Errorf("%s: HEAD %q, GET %q", k, h.Header().Get(k), g.Header().Get(k))
		}
	}
	if h.Code != 200 || h.Header().Get("Content-Encoding") != "br" {
		t.Fatal(h.Code, h.Header())
	}
	if r := do(t, a, "HEAD", "/r", "Accept-Encoding", "br", "If-None-Match", `"v-br"`); r.Code != 304 || r.Header().Get("ETag") != `"v-br"` {
		t.Fatal(r.Code, r.Header())
	}
}

// An encoder that fails sends the identity form, with its tag.
func TestCompressEncoderFailureSendsIdentity(t *testing.T) {
	for _, f := range []*fakeCoding{{name: "zstd", failNew: true}, {name: "zstd", failCl: true}} {
		a := compApp(t, &gzcResource{tag: `"v"`}, f.coding(), geta.GzipCoding())
		r := do(t, a, "GET", "/r", "Accept-Encoding", "zstd")
		if r.Code != 200 || r.Header().Get("Content-Encoding") != "" || r.Header().Get("ETag") != `"v"` ||
			r.Header().Get("Content-Length") != itoa(r.Body.Len()) || !strings.HasPrefix(r.Body.String(), `{"text":`) {
			t.Errorf("%+v: %d %v", f, r.Code, r.Header())
		}
	}
}

func TestCompressRejectsCodings(t *testing.T) {
	c := func(name string) geta.Coding { return (&fakeCoding{name: name}).coding() }
	for name, m := range map[string]geta.Middleware{
		"none":               geta.Compress(),
		"empty name":         geta.Compress(c("")),
		"not a token":        geta.Compress(c("a b")),
		"identity":           geta.Compress(c("Identity")),
		"wildcard":           geta.Compress(c("*")),
		"x-gzip":             geta.Compress(c("x-gzip")),
		"ends in -identity":  geta.Compress(c("br-identity")),
		"no NewWriter":       geta.Compress(geta.Coding{Name: "zstd"}),
		"twice":              geta.Compress(c("br"), c("BR")),
		"suffix after":       geta.Compress(c("br"), c("x-br")),
		"suffix before":      geta.Compress(c("x-br"), c("br")),
		"suffix of gzip":     geta.Compress(c("pack200-gzip"), geta.GzipCoding()),
		"gzip twice":         geta.Compress(geta.GzipCoding(), geta.GzipCoding()),
		"suffix among three": geta.Compress(c("zstd"), c("br"), c("a-zstd")),
	} {
		t.Run(name, func(t *testing.T) {
			rejects(t, withRoot(one("/x", get(okHandler)), m), "middleware 0 (compress)", "geta.Compress")
		})
	}
	for _, m := range []geta.Middleware{
		geta.Compress(c("pack200-gzip"), c("zstd")),
		geta.Compress(c("br"), c("brx"), c("xbr")),
		geta.Compress(c("gzip")), // an application's own gzip
	} {
		accepts(t, withRoot(one("/x", get(okHandler)), m))
	}
	if o, ok := geta.Compress(c("zstd")).Order(); !ok || o != geta.OrderNegotiate {
		t.Fatal(o)
	}
}

// Compress codes only in the codings it is given: an application's own
// coding named gzip is the gzip it sends, x-gzip naming it too.
func TestCompressOwnGzip(t *testing.T) {
	own := &fakeCoding{name: "gzip"}
	a := accepts(t, withRoot(one("/x", get(textHandler(big))), geta.Compress(own.coding())))
	r := do(t, a, "GET", "/x", "Accept-Encoding", "x-gzip")
	if r.Header().Get("Content-Encoding") != "gzip" || !strings.HasPrefix(r.Body.String(), "gzip:") || own.opened.Load() != 1 {
		t.Fatal(r.Header(), fmt.Sprint(own.opened.Load()))
	}
}
