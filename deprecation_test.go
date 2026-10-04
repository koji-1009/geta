package geta_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// RFC 9745's own example (@1688169599 is 2023-06-30T23:59:59Z), and a
// Sunset after it.
var (
	deprecatedAt = time.Date(2023, 6, 30, 23, 59, 59, 0, time.UTC)
	sunsetAt     = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
)

const (
	wantDeprecation = "@1688169599"
	wantSunset      = "Fri, 01 Jan 2027 00:00:00 GMT"
)

func deprecatedDoc() geta.Doc {
	return geta.Doc{Deprecated: true, Deprecation: deprecatedAt, Sunset: sunsetAt}
}

type depQueryIn struct {
	N int `query:"n" schema:"maximum=9,default=0"`
}

type depCondIn struct {
	geta.Conditional
}

type depTick struct {
	N int `json:"n"`
}

var errDepGone = errors.New("gone")

// hasDeprecation reports a mismatch between h and what a deprecated
// operation sends, "" when h carries both headers as stated.
func hasDeprecation(h http.Header) string {
	if got := h.Values("Deprecation"); len(got) != 1 || got[0] != wantDeprecation {
		return "Deprecation " + strings.Join(got, ",")
	}
	if got := h.Values("Sunset"); len(got) != 1 || got[0] != wantSunset {
		return "Sunset " + strings.Join(got, ",")
	}
	return ""
}

func noDeprecation(h http.Header) bool { return h.Get("Deprecation") == "" && h.Get("Sunset") == "" }

// Every response of an operation whose Doc gives a Deprecation and a Sunset
// carries both headers: its success, HEAD, geta's own problems, a failure
// row's, a 304 from a Conditional or from ETag, ETag's 400, a root
// middleware's own answer, and a stream. A response of no operation (404,
// 405, the redirect to a clean path, OPTIONS) and one of an operation that
// is not deprecated carry neither.
func TestDeprecationIsSentOnEveryResponse(t *testing.T) {
	doc := deprecatedDoc()
	doc.Failures = []geta.Failure{geta.On(errDepGone, http.StatusGone, "gone")}
	shed := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Has("shed") {
				geta.WriteProblem(w, http.StatusServiceUnavailable, "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}).Answers(http.StatusServiceUnavailable, "Shed")
	tbl := geta.Table{
		Root: geta.Scope{shed, geta.ETag()},
		Routes: []geta.Entry{
			{Path: "/old", Route: geta.Route{
				Get: geta.Op(http.StatusOK, func(_ context.Context, in *depQueryIn) (*ok, error) {
					if in.N == 7 {
						return nil, errDepGone
					}
					if in.N == 8 {
						return nil, errors.New("unmatched")
					}
					return &ok{true}, nil
				}, doc),
			}},
			{Path: "/cond", Route: geta.Route{
				Put: geta.OpNoBody(http.StatusNoContent, func(_ context.Context, in *depCondIn) error {
					return in.Check(`"v1"`, time.Time{})
				}, deprecatedDoc()),
				Get: geta.Op(http.StatusOK, func(_ context.Context, in *depCondIn) (*ok, error) {
					if err := in.Check(`"v1"`, time.Time{}); err != nil {
						return nil, err
					}
					return &ok{true}, nil
				}, deprecatedDoc()),
			}},
			{Path: "/feed", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *empty) (*geta.Stream[depTick], error) {
				return &geta.Stream[depTick]{Events: func(yield func(depTick) bool) { yield(depTick{1}) }}, nil
			}, deprecatedDoc())}},
			{Path: "/new", Route: get(okHandler)},
		},
	}
	a, err := geta.New(tbl, geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	carries := []struct {
		name, method, path string
		headers            []string
		status             int
	}{
		{"success", "GET", "/old", nil, 200},
		{"head", "HEAD", "/old", nil, 200},
		{"binding 400", "GET", "/old?n=10", nil, 400},
		{"row", "GET", "/old?n=7", nil, 410},
		{"defect 500", "GET", "/old?n=8", nil, 500},
		{"root middleware", "GET", "/old?shed", nil, 503},
		{"etag 304", "GET", "/old", header("If-None-Match", "*"), 304},
		{"etag 400", "GET", "/old", header("If-None-Match", "nope"), 400},
		{"conditional 304", "GET", "/cond", header("If-None-Match", `"v1"`), 304},
		{"conditional 412", "PUT", "/cond", header("If-Match", `"v2"`), 412},
		{"stream", "GET", "/feed", nil, 200},
	}
	for _, c := range carries {
		rec := do(t, a, c.method, c.path, c.headers...)
		if rec.Code != c.status {
			t.Errorf("%s: status %d, want %d: %s", c.name, rec.Code, c.status, rec.Body)
			continue
		}
		if why := hasDeprecation(rec.Header()); why != "" {
			t.Errorf("%s: %s", c.name, why)
		}
	}
	lacks := []struct {
		name, method, path string
		status             int
	}{
		{"not deprecated", "GET", "/new", 200},
		{"404", "GET", "/missing", 404},
		{"405", "DELETE", "/old", 405},
		{"redirect", "GET", "//old", 307},
		{"options", "OPTIONS", "/old", 204},
	}
	for _, c := range lacks {
		rec := do(t, a, c.method, c.path)
		if rec.Code != c.status || !noDeprecation(rec.Header()) {
			t.Errorf("%s: %d %v", c.name, rec.Code, rec.Header())
		}
	}
}

// Deprecated alone sends neither header and declares none: the Deprecation
// header carries a date, and geta has none.
func TestDeprecatedWithoutADateSendsNoHeader(t *testing.T) {
	a := accepts(t, one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Deprecated: true})}))
	if rec := do(t, a, "GET", "/x"); !noDeprecation(rec.Header()) {
		t.Error(rec.Header())
	}
	if _, has := at(t, doc(t, a), "paths", "/x", "get", "responses", "200").(map[string]any)["headers"]; has {
		t.Error("headers declared without a date")
	}
}

// A Sunset without a Deprecation sends the Sunset alone.
func TestSunsetAlone(t *testing.T) {
	a := accepts(t, one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Deprecated: true, Sunset: sunsetAt})}))
	rec := do(t, a, "GET", "/x")
	if rec.Header().Get("Sunset") != wantSunset || rec.Header().Get("Deprecation") != "" {
		t.Error(rec.Header())
	}
	headers := at(t, doc(t, a), "paths", "/x", "get", "responses", "200", "headers").(map[string]any)
	if _, has := headers["Deprecation"]; has || headers["Sunset"] == nil {
		t.Error(headers)
	}
}

// Both headers are declared on every response of the operation, required,
// their schema the one value sent; the 101 of an Upgrade, which a library
// may write itself, declares them not required. A Serve upgrade's 101
// carries them.
func TestDeprecationIsDocumentedOnEveryResponse(t *testing.T) {
	upgrade := func(context.Context, *empty) (*geta.Upgrade, error) {
		return &geta.Upgrade{Protocol: "bye", Serve: func(_ net.Conn, rw *bufio.ReadWriter) {
			rw.WriteString("bye")
			rw.Flush()
		}}, nil
	}
	tbl := geta.Table{Routes: []geta.Entry{
		{Path: "/old", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *depQueryIn) (*ok, error) { return &ok{true}, nil }, deprecatedDoc())}},
		{Path: "/ws", Route: geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, upgrade, deprecatedDoc())}},
	}}
	c := getatest.New(t, tbl)
	m := doc(t, c.App())
	for _, path := range []string{"/old", "/ws"} {
		responses := at(t, m, "paths", path, "get", "responses").(map[string]any)
		for status := range responses {
			headers, _ := at(t, responses, status).(map[string]any)["headers"].(map[string]any)
			want := `{"description":"The operation is deprecated as of this moment, which may be in the future: a structured-field Date, seconds since the epoch (RFC 9745)","required":true,"schema":{"const":"@1688169599","type":"string"}}`
			if status == "101" {
				want = strings.Replace(want, `"required":true`, `"required":false`, 1)
			}
			if got := compact(t, headers["Deprecation"]); got != want {
				t.Errorf("%s %s Deprecation: %s", path, status, got)
			}
			sunset, _ := headers["Sunset"].(map[string]any)
			if sunset == nil || compact(t, sunset["schema"]) != `{"const":"Fri, 01 Jan 2027 00:00:00 GMT","type":"string"}` || sunset["required"] != (status != "101") {
				t.Errorf("%s %s Sunset: %v", path, status, sunset)
			}
		}
	}
	// The 101 Serve writes carries them too.
	u := c.Upgrade("/ws", "bye")
	if !u.Switched {
		t.Fatal(u.Response.Status)
	}
	if why := hasDeprecation(u.Response.Header); why != "" {
		t.Error(why)
	}
	if got, err := io.ReadAll(u.Conn); err != nil || string(got) != "bye" {
		t.Fatalf("%q %v", got, err)
	}
}

// On a deprecated operation, an Upgrade whose Header sets a header geta
// writes on the 101 from the Doc (Deprecation, Sunset), in any case, is a
// defect, whatever the request asks: the 101 would carry two of it. A header
// the operation's Doc does not send stays the Header's.
func TestAnUpgradeHeaderCannotRepeatTheDeprecation(t *testing.T) {
	var hdr http.Header
	h := func(context.Context, *empty) (*geta.Upgrade, error) {
		return &geta.Upgrade{Protocol: "echo", Header: hdr, Serve: func(_ net.Conn, rw *bufio.ReadWriter) {
			rw.WriteString("hi")
			rw.Flush()
		}}, nil
	}
	log, buf := logger()
	a, err := geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/both", Route: geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, h, deprecatedDoc())}},
		{Path: "/sunset", Route: geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, h, geta.Doc{Deprecated: true, Sunset: sunsetAt})}},
		{Path: "/new", Route: geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, h, geta.Doc{})}},
	}}, geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	c := getatest.Serve(t, a)
	for name, hd := range map[string]http.Header{"Deprecation": {"Deprecation": {"@1"}}, "Sunset": {"sunset": {wantSunset}}} {
		hdr = hd
		u := c.Upgrade("/both", "echo")
		if u.Switched || u.Response.Status != 500 || !strings.Contains(buf.String(), "Upgrade.Header sets "+name+", which geta writes from Doc."+name) {
			t.Errorf("%v: %v %d %s", hd, u.Switched, u.Response.Status, buf)
			continue
		}
		// The defect is a response of the operation: geta's headers, once.
		if why := hasDeprecation(u.Response.Header); why != "" {
			t.Errorf("%v: %s", hd, why)
		}
		if rec := do(t, a, "GET", "/both"); rec.Code != 500 {
			t.Errorf("%v without an upgrade request: %d", hd, rec.Code)
		}
	}
	// Where the Doc does not send it, the Header's is the 101's one.
	hdr = http.Header{"Deprecation": {"@1"}}
	for path, want := range map[string]string{"/sunset": wantSunset, "/new": ""} {
		u := c.Upgrade(path, "echo")
		if !u.Switched {
			t.Errorf("%s: %d", path, u.Response.Status)
			continue
		}
		if got := u.Response.Header.Values("Deprecation"); len(got) != 1 || got[0] != "@1" || u.Response.Header.Get("Sunset") != want {
			t.Errorf("%s: %v", path, u.Response.Header)
		}
		io.ReadAll(u.Conn)
	}
}

// A root middleware that moves a request to another operation moves the
// headers with it: the operation served decides them.
func TestDeprecationFollowsARootRewrite(t *testing.T) {
	move := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if to := r.URL.Query().Get("to"); to != "" {
				r = r.Clone(r.Context())
				r.URL.Path = to
			}
			next.ServeHTTP(w, r)
		})
	})
	tbl := withRoot(geta.Table{Routes: []geta.Entry{
		{Path: "/old", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, deprecatedDoc())}},
		{Path: "/new", Route: get(okHandler)},
	}}, move)
	a := accepts(t, tbl)
	if rec := do(t, a, "GET", "/old?to=/new"); rec.Code != 200 || !noDeprecation(rec.Header()) {
		t.Error("to /new:", rec.Code, rec.Header())
	}
	if rec := do(t, a, "GET", "/new?to=/old"); rec.Code != 200 || hasDeprecation(rec.Header()) != "" {
		t.Error("to /old:", rec.Code, rec.Header())
	}
	if rec := do(t, a, "GET", "/old?to=/missing"); rec.Code != 404 || !noDeprecation(rec.Header()) {
		t.Error("to /missing:", rec.Code, rec.Header())
	}
}

// geta.CORS exposes both headers on the operation that sends them.
func TestCORSExposesDeprecation(t *testing.T) {
	tbl := withRoot(geta.Table{Routes: []geta.Entry{
		{Path: "/old", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, deprecatedDoc())}},
		{Path: "/new", Route: get(okHandler)},
	}}, geta.CORS(geta.AllowOrigins("*")))
	a := accepts(t, tbl)
	rec := do(t, a, "GET", "/old", "Origin", "https://page.example")
	expose := rec.Header().Get("Access-Control-Expose-Headers")
	if !strings.Contains(expose, "Deprecation") || !strings.Contains(expose, "Sunset") {
		t.Error(expose)
	}
	rec = do(t, a, "GET", "/new", "Origin", "https://page.example")
	if expose := rec.Header().Get("Access-Control-Expose-Headers"); strings.Contains(expose, "Deprecation") {
		t.Error(expose)
	}
}

type depSunsetOut struct {
	Sunset string `header:"Sunset"`
	Body   ok     `body:"json"`
}

type depRetry struct {
	Deprecation string `header:"Deprecation"`
}

// What cannot be sent as stated is refused: a date without Deprecated, a
// moment that is not a whole second or past the year 9999, a Sunset before
// the Deprecation, and a header of the same name the output, a failure row's
// description, or a middleware declares.
func TestDeprecationMistakesAreRefused(t *testing.T) {
	route := func(d geta.Doc) geta.Table {
		return one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, d)})
	}
	rejects(t, route(geta.Doc{Deprecation: deprecatedAt}), "Deprecated is false")
	rejects(t, route(geta.Doc{Sunset: sunsetAt}), "Deprecated is false")
	rejects(t, route(geta.Doc{Deprecated: true, Deprecation: deprecatedAt.Add(time.Millisecond)}), "Doc.Deprecation", "not a whole second")
	rejects(t, route(geta.Doc{Deprecated: true, Sunset: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}), "Doc.Sunset", "9999")
	rejects(t, route(geta.Doc{Deprecated: true, Deprecation: sunsetAt, Sunset: deprecatedAt}), "Doc.Sunset 2023-06-30T23:59:59Z is before Doc.Deprecation 2027-01-01T00:00:00Z")
	out := func(context.Context, *empty) (*depSunsetOut, error) { return &depSunsetOut{}, nil }
	rejects(t, one("/x", geta.Route{Get: geta.Op(http.StatusOK, out, deprecatedDoc())}), "the output declares the Sunset header")
	// The output's own Sunset is the author's where the Doc sends none.
	accepts(t, one("/x", geta.Route{Get: geta.Op(http.StatusOK, out, geta.Doc{Deprecated: true, Deprecation: deprecatedAt})}))
	d := deprecatedDoc()
	d.Failures = []geta.Failure{geta.OnAsProblem(http.StatusGone, "gone", func(error) depRetry { return depRetry{} })}
	rejects(t, route(d), "failure row 0", "declares the Deprecation header")
	mw := geta.Use(noop).Answers(http.StatusGone, "Gone").Header(http.StatusGone, "Sunset", "when")
	rejects(t, one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, deprecatedDoc())}, geta.Scope{mw}), "middleware", "declares the Sunset header")
}
