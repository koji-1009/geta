package geta_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// Tests for the middleware specification's rules that no other test
// asserts in full.

// geta's order vocabulary: each stage's rank and name, outermost first,
// OrderSpacing apart, written name(rank).
func TestMWOrderVocabulary(t *testing.T) {
	if geta.OrderSpacing != 1000 {
		t.Fatal(geta.OrderSpacing)
	}
	for i, want := range []struct {
		order geta.Order
		name  string
	}{
		{geta.OrderObserve, "observe"},
		{geta.OrderCrossOrigin, "cross-origin"},
		{geta.OrderRecover, "recover"},
		{geta.OrderShed, "shed"},
		{geta.OrderDeadline, "deadline"},
		{geta.OrderNegotiate, "negotiate"},
		{geta.OrderValidate, "validate"},
		{geta.OrderAuthenticate, "authenticate"},
		{geta.OrderAuthorize, "authorize"},
	} {
		rank := (i + 1) * geta.OrderSpacing
		if want.order.Rank != rank || want.order.Name != want.name || want.order.String() != fmt.Sprintf("%s(%d)", want.name, rank) {
			t.Errorf("%+v %q; want %s(%d)", want.order, want.order.String(), want.name, rank)
		}
	}
}

// Compress carries the negotiate rank as Gzip does; Ordered carries the
// order it is given, Use none.
func TestMWMiddlewareRanks(t *testing.T) {
	if o, ok := geta.Compress(geta.GzipCoding()).Order(); !ok || o != geta.OrderNegotiate {
		t.Fatal(o, ok)
	}
	mine := geta.Order{Rank: geta.OrderAuthenticate.Rank + 500, Name: "mine"}
	if o, ok := geta.Ordered(mine, noop).Order(); !ok || o != mine {
		t.Fatal(o, ok)
	}
	if _, ok := geta.Use(noop).Order(); ok {
		t.Fatal("Use carries an order")
	}
}

// A construction mistake in one of geta's middleware is refused by geta.New,
// naming the middleware's position and name.
func TestMWConstructionMistakesNameTheMiddleware(t *testing.T) {
	for want, m := range map[string]geta.Middleware{
		"root scope: middleware 1 (concurrency-limit): geta.ConcurrencyLimit 0 is less than 1": geta.ConcurrencyLimit(0),
		"root scope: middleware 1 (timeout): geta.Timeout -1s is not positive":                 geta.Timeout(-time.Second),
		"root scope: middleware 1 (cors): geta.CORS allows no origin":                          geta.CORS(),
		`root scope: middleware 1 (cors): geta.CORS: "*" with credentials is forbidden`:        geta.CORS(geta.AllowOrigins("*"), geta.AllowCredentials()),
		"root scope: middleware 1 (cors): geta.CORS: PreflightMaxAge 500ms is not a positive whole number of seconds": geta.CORS(geta.AllowOrigins("*"),
			geta.PreflightMaxAge(500*time.Millisecond)),
	} {
		rejects(t, withRoot(one("/x", get(okHandler)), geta.Use(noop), m), want)
	}
}

type mwCtxKey struct{}

// mwVerifier refuses or fails as the X-Verdict header says.
func mwVerifier(r *http.Request) (context.Context, error) {
	switch r.Header.Get("X-Verdict") {
	case "admit":
		return nil, nil
	case "unauthenticated":
		return nil, fmt.Errorf("the token expired: %w", geta.ErrUnauthenticated)
	case "unavailable":
		return nil, fmt.Errorf("the key server is down: %w", geta.ErrUnavailable)
	}
	return nil, errors.New("verifier secret detail")
}

// A verifier's errors: ErrUnauthenticated, wrapped too, is a 401 with the
// scheme's challenge; ErrUnavailable, wrapped too, a 503 problem with no
// challenge; any other a logged 500 whose instance the log carries, nothing
// of the error in the body. A nil context keeps the request's.
func TestMWVerifierErrorsMapToStatuses(t *testing.T) {
	log, buf := logger()
	mark := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), mwCtxKey{}, "kept")))
		})
	})
	read := func(ctx context.Context, _ *empty) (*text, error) {
		v, _ := ctx.Value(mwCtxKey{}).(string)
		return &text{v}, nil
	}
	a, err := geta.New(withRoot(one("/x", get(read)), mark,
		geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer}, Verifiers: map[string]geta.Verifier{"bearer": mwVerifier}})),
		geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	r := do(t, a, "GET", "/x", "X-Verdict", "admit")
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"text":"kept"`) {
		t.Fatal(r.Code, r.Body)
	}
	r = do(t, a, "GET", "/x", "X-Verdict", "unauthenticated")
	if r.Code != 401 || r.Header().Get("WWW-Authenticate") != "Bearer" || r.Header().Get("Content-Type") != geta.ProblemContentType {
		t.Fatal(r.Code, r.Header())
	}
	r = do(t, a, "GET", "/x", "X-Verdict", "unavailable")
	var p geta.Problem
	if err := json.Unmarshal(r.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if r.Code != 503 || r.Header().Get("WWW-Authenticate") != "" || p.Detail != "the credential source is unavailable" {
		t.Fatal(r.Code, r.Header(), r.Body)
	}
	r = do(t, a, "GET", "/x", "X-Verdict", "bug")
	p = geta.Problem{}
	if err := json.Unmarshal(r.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if r.Code != 500 || !strings.HasPrefix(p.Instance, "urn:uuid:") || strings.Contains(r.Body.String(), "secret") || r.Header().Get("WWW-Authenticate") != "" {
		t.Fatal(r.Code, r.Header(), r.Body)
	}
	if l := buf.String(); !strings.Contains(l, "instance="+p.Instance) || !strings.Contains(l, "verifier secret detail") {
		t.Fatal(l)
	}
}

// A gate's 401 and 503 are documented on an operation that requires a
// scheme, and neither on one that requires none.
func TestMWGateStatusesAreDocumentedWhereASchemeIsRequired(t *testing.T) {
	m := doc(t, accepts(t, geta.Table{Root: geta.Scope{gate()}, Routes: []geta.Entry{
		{Path: "/x", Route: get(okHandler)},
		{Path: "/open", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{}})}},
	}}))
	if got := at(t, m, "paths", "/x", "get", "responses", "401", "description"); got != "Unauthenticated" {
		t.Error("401:", got)
	}
	if got := at(t, m, "paths", "/x", "get", "responses", "503", "description"); got != "The credential source is unavailable" {
		t.Error("503:", got)
	}
	open := at(t, m, "paths", "/open", "get", "responses").(map[string]any)
	for _, status := range []string{"401", "503"} {
		if _, has := open[status]; has {
			t.Errorf("a public operation documents %s", status)
		}
	}
}

// The document lists, for an operation, one requirement object per way the
// gates of its chain can all admit it.
func TestMWGateCombinationsAreDocumented(t *testing.T) {
	scheme := func(name string) geta.Scheme { return geta.APIKeyHeader(name, "X-"+name) }
	a, b, c := scheme("a"), scheme("b"), scheme("c")
	pass := func(*http.Request) (context.Context, error) { return nil, nil }
	gateOf := func(def []geta.Scheme, names ...string) geta.Middleware {
		v := map[string]geta.Verifier{}
		for _, n := range names {
			v[n] = pass
		}
		return geta.Secure(geta.Policy{Default: def, Verifiers: v})
	}
	for _, tc := range []struct {
		name     string
		declared []geta.Scheme
		chain    geta.Scope
		want     string
	}{
		{"one gate", nil, geta.Scope{gateOf([]geta.Scheme{a, b}, "a", "b")}, `[{"a":[]},{"b":[]}]`},
		{"two gates", nil, geta.Scope{gateOf([]geta.Scheme{a, b}, "a", "b"), gateOf([]geta.Scheme{c}, "c")}, `[{"a":[],"c":[]},{"b":[],"c":[]}]`},
		{"overlapping defaults", nil, geta.Scope{gateOf([]geta.Scheme{a, b}, "a", "b"), gateOf([]geta.Scheme{a}, "a")}, `[{"a":[]}]`},
		{"an open gate", nil, geta.Scope{gateOf(nil), gateOf([]geta.Scheme{c}, "c")}, `[{"c":[]}]`},
		{"all open", nil, geta.Scope{gateOf(nil), gateOf([]geta.Scheme{})}, `[]`},
		{"declared", []geta.Scheme{a, c}, geta.Scope{gateOf([]geta.Scheme{b}, "a", "b", "c"), gateOf(nil, "a", "c")}, `[{"a":[]},{"c":[]}]`},
	} {
		tbl := withRoot(one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: tc.declared})}), tc.chain...)
		if got := compact(t, at(t, doc(t, accepts(t, tbl)), "paths", "/x", "get", "security")); got != tc.want {
			t.Errorf("%s: %s; want %s", tc.name, got, tc.want)
		}
	}
	// A declared scheme is checked by every gate in the chain: one that a
	// gate cannot verify is refused.
	rejects(t, withRoot(one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{a}})}),
		gateOf(nil, "a"), gateOf(nil, "c")), `requires scheme "a"`, "no verifier")
}

// Before a root gate, an operation's Doc.BeforeGate reads the match of the
// operation a root rewrite moved the request to, while the root middleware
// between the rewrite and it still read the arrival's.
func TestMWMatchedFollowsARewriteIntoTheBeforeGate(t *testing.T) {
	var tr trail
	a := accepts(t, geta.Table{Root: geta.Scope{pathMove, tr.mark("root"), gate()}, Routes: []geta.Entry{
		{Path: "/a", Route: get(okHandler)},
		{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{BeforeGate: geta.Scope{tr.mark("before")}})}},
	}})
	if r := do(t, a, "GET", "/a", "X-Move", "/b", "Authorization", "Bearer ok"); r.Code != 200 {
		t.Fatal(r.Code)
	}
	if got := tr.take(); got != "root=/a,before=/b" {
		t.Fatal(got)
	}
}

// With no gate in its chain, an operation's Doc.BeforeGate runs right after
// the root scope, before the directory scopes.
func TestMWBeforeGateWithNoGateRunsAfterTheRootScope(t *testing.T) {
	var tr trail
	a := accepts(t, geta.Table{Root: geta.Scope{tr.mark("root")}, Routes: []geta.Entry{{Path: "/b", Scopes: []geta.Scope{{tr.mark("dir")}},
		Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{BeforeGate: geta.Scope{tr.mark("before")}})}}}})
	if r := do(t, a, "GET", "/b"); r.Code != 200 {
		t.Fatal(r.Code)
	}
	if got := tr.take(); got != "root=/b,before=/b,dir=/b" {
		t.Fatal(got)
	}
}

// The access log line: message "request" at Info, the method, the route,
// the status, the body bytes written, and the duration.
func TestMWAccessLogLine(t *testing.T) {
	var buf syncBuffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	a := accepts(t, withRoot(one("/users/{id}", get(idHandler)), geta.AccessLog(log)))
	r := do(t, a, "GET", "/users/42")
	var line map[string]any
	if err := json.Unmarshal([]byte(buf.String()), &line); err != nil {
		t.Fatal(err, buf.String())
	}
	if line["msg"] != "request" || line["level"] != "INFO" || line["method"] != "GET" || line["route"] != "/users/{id}" ||
		line["status"] != float64(200) || line["bytes"] != float64(r.Body.Len()) || r.Body.Len() == 0 {
		t.Fatal(line, r.Body.Len())
	}
	if d, ok := line["duration"].(float64); !ok || d < 0 {
		t.Fatal(line["duration"])
	}
}

// A panic after a chunked response has started: Recover logs it and aborts
// the connection, so the client reads the status and what was sent, then an
// unexpected end; the same response without the panic ends cleanly.
func TestMWRecoverAbortsAChunkedResponse(t *testing.T) {
	for _, panics := range []bool{true, false} {
		log, buf := logger()
		late := geta.Use(func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				io.WriteString(w, "partial")
				http.NewResponseController(w).Flush()
				if panics {
					panic("late")
				}
			})
		})
		c := getatest.New(t, withRoot(one("/x", get(okHandler)), geta.Recover(log), late))
		res, err := c.HTTP().Get(c.URL() + "/x")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || string(body) != "partial" || len(res.TransferEncoding) == 0 || res.TransferEncoding[0] != "chunked" {
			t.Fatalf("panics %v: %d %q %v", panics, res.StatusCode, body, res.TransferEncoding)
		}
		if panics != errors.Is(err, io.ErrUnexpectedEOF) || !panics && err != nil {
			t.Fatalf("panics %v: %v", panics, err)
		}
		if !panics {
			continue
		}
		for deadline := time.Now().Add(5 * time.Second); !strings.Contains(buf.String(), "panic=late"); {
			if time.Now().After(deadline) {
				t.Fatal(buf.String())
			}
			time.Sleep(time.Millisecond)
		}
		if !strings.Contains(buf.String(), "geta: panic") || !strings.Contains(buf.String(), "stack=") {
			t.Fatal(buf.String())
		}
	}
}

// A Key's value: With and Value round-trip it, Must returns it and panics,
// naming the key, when it is absent; the zero Key panics on every use, and
// String is the key's name.
func TestMWKey(t *testing.T) {
	k := geta.NewKey[int]("count")
	ctx := k.With(context.Background(), 7)
	if k.Must(ctx) != 7 || k.String() != "count" {
		t.Fatal(k.Must(ctx), k.String())
	}
	if v, ok := k.Value(context.Background()); ok || v != 0 {
		t.Fatal(v, ok)
	}
	panicText := func(f func()) (s string) {
		defer func() { s = fmt.Sprint(recover()) }()
		f()
		return "no panic"
	}
	if got := panicText(func() { k.Must(context.Background()) }); got != `geta: no value for key "count" in the context` {
		t.Fatal(got)
	}
	var zero geta.Key[int]
	for name, f := range map[string]func(){
		"With":  func() { zero.With(ctx, 1) },
		"Value": func() { zero.Value(ctx) },
		"Must":  func() { zero.Must(ctx) },
	} {
		if got := panicText(f); got != "geta: zero Key; use geta.NewKey" {
			t.Errorf("%s: %s", name, got)
		}
	}
	if zero.String() != "<zero geta.Key>" {
		t.Fatal(zero.String())
	}
}

// Without WithLogger, geta records its defects with slog.Default as it is
// when geta.New runs.
func TestMWDefectsGoToSlogDefault(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	var buf syncBuffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	a, err := geta.New(failing(errors.New("unmapped store failure")))
	if err != nil {
		t.Fatal(err)
	}
	slog.SetDefault(prev)
	r := do(t, a, "GET", "/x")
	var p geta.Problem
	if err := json.Unmarshal(r.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if r.Code != 500 || !strings.Contains(buf.String(), "unmapped store failure") || !strings.Contains(buf.String(), "instance="+p.Instance) {
		t.Fatal(r.Code, buf.String())
	}
}

// Access-Control-Max-Age is sent only with PreflightMaxAge, and only on an allowed
// origin's preflight.
func TestMWCORSMaxAgeOnlyWhenSetAndAllowed(t *testing.T) {
	pre := func(a *geta.App, origin string) http.Header {
		return do(t, a, "OPTIONS", "/x", "Origin", origin, "Access-Control-Request-Method", "GET").Header()
	}
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.CORS(geta.AllowOrigins("https://a.example"))))
	if h := pre(a, "https://a.example"); h.Get("Access-Control-Allow-Origin") == "" || len(h.Values("Access-Control-Max-Age")) != 0 {
		t.Fatal(h)
	}
	a = accepts(t, withRoot(one("/x", get(okHandler)), geta.CORS(geta.AllowOrigins("https://a.example"), geta.PreflightMaxAge(10*time.Second))))
	if h := pre(a, "https://a.example"); h.Get("Access-Control-Max-Age") != "10" {
		t.Fatal(h)
	}
	if h := pre(a, "https://b.example"); len(h.Values("Access-Control-Max-Age")) != 0 {
		t.Fatal(h)
	}
}

// A request redirected to its clean path, and the gate's 401 before the
// redirect, expose the headers of the operation the clean path reaches.
func TestMWCORSRedirectExposesTheCleanPathsHeaders(t *testing.T) {
	a := corsApp(t)
	r := do(t, a, "GET", "/./r", "Origin", "https://a.example")
	if r.Code != 401 {
		t.Fatal(r.Code, r.Header())
	}
	sameHeaders(t, "GET /./r 401", r.Header().Get("Access-Control-Expose-Headers"), "ETag", "WWW-Authenticate")
	r = do(t, a, "GET", "/./r", "Origin", "https://a.example", "Authorization", "Bearer t")
	if r.Code != http.StatusPermanentRedirect || r.Header().Get("Location") != "/r" {
		t.Fatal(r.Code, r.Header())
	}
	sameHeaders(t, "GET /./r 308", r.Header().Get("Access-Control-Expose-Headers"), "ETag", "WWW-Authenticate")
}

// What ConcurrencyLimit and Timeout answer is documented with their reasons,
// the options operations included when they are in the root scope.
func TestMWShedAndDeadlineAnswersAreDocumented(t *testing.T) {
	m := doc(t, accepts(t, withRoot(one("/x", get(okHandler)), geta.ConcurrencyLimit(1), geta.Timeout(time.Second))))
	for _, method := range []string{"get", "options"} {
		if got := at(t, m, "paths", "/x", method, "responses", "503", "description"); got != "Server at capacity" {
			t.Errorf("%s 503: %v", method, got)
		}
	}
	if got := at(t, m, "paths", "/x", "options", "responses", "504", "description"); got != "The deadline passed before a response" {
		t.Errorf("options 504: %v", got)
	}
}

// Timeout stops nothing itself: a handler that does not watch its context
// runs past the deadline and its response is sent.
func TestMWTimeoutStopsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		slow := func(context.Context, *empty) (*ok, error) {
			time.Sleep(3 * time.Second)
			return &ok{true}, nil
		}
		r := do(t, accepts(t, withRoot(one("/x", get(slow)), geta.Timeout(time.Second))), "GET", "/x")
		if r.Code != 200 || r.Body.String() != `{"ok":true}` {
			t.Fatal(r.Code, r.Body)
		}
	})
}
