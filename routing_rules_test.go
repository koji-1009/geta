package geta_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/koji-1009/geta"
)

// Rules of matching, redirects, methods, and root rewrites, each asserted as
// routing.md states it.

// echoIDRoute answers GET with the id it bound.
func echoIDRoute(name string) geta.Route {
	return geta.Route{Get: geta.Op(http.StatusOK, func(_ context.Context, in *idNameIn) (*which, error) {
		return &which{Route: name, Value: in.ID}, nil
	}, geta.Doc{})}
}

// matchRecorder is a root middleware that records what geta.Matched reports
// as the request enters the root scope.
type matchRecorder struct {
	mu   sync.Mutex
	seen string
}

func (m *matchRecorder) middleware() geta.Middleware {
	return geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mt, ok := geta.Matched(r.Context())
			m.mu.Lock()
			m.seen = "none"
			if ok {
				m.seen = mt.Method + " " + mt.Template
			}
			m.mu.Unlock()
			next.ServeHTTP(w, r)
		})
	})
}

func (m *matchRecorder) get() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seen
}

// A percent-encoded dot segment is not a dot segment: the path is
// not cleaned for it, and the decoded . or .. is matched as an ordinary
// segment.
func TestEncodedDotSegmentsAreOrdinarySegments(t *testing.T) {
	a := accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/users/{id}", Route: echoIDRoute("id")},
		{Path: "/users/{id}/name", Route: echoIDRoute("name")},
	}})
	for path, want := range map[string]string{
		"/users/%2E":           `{"route":"id","value":"."}`,
		"/users/%2E%2E":        `{"route":"id","value":".."}`,
		"/users/%2e%2e/name":   `{"route":"name","value":".."}`,
		"/users/.%2E":          `{"route":"id","value":".."}`,
		"/users/%2E%2E%2Fname": `{"route":"id","value":"../name"}`,
	} {
		rec := do(t, a, http.MethodGet, path)
		if rec.Code != http.StatusOK || rec.Body.String() != want || rec.Header().Get("Location") != "" {
			t.Errorf("%s: %d %q Location %q; want 200 %s", path, rec.Code, rec.Body, rec.Header().Get("Location"), want)
		}
	}
}

// tenIn binds ten path parameters, past the eight a match holds inline.
type tenIn struct {
	A1  string `path:"a1"`
	A2  string `path:"a2"`
	A3  string `path:"a3"`
	A4  string `path:"a4"`
	A5  string `path:"a5"`
	A6  string `path:"a6"`
	A7  string `path:"a7"`
	A8  string `path:"a8"`
	A9  string `path:"a9"`
	A10 string `path:"a10"`
}

// sevenKIn binds seven parameters, a literal k, then two more.
type sevenKIn struct {
	A1 string `path:"a1"`
	A2 string `path:"a2"`
	A3 string `path:"a3"`
	A4 string `path:"a4"`
	A5 string `path:"a5"`
	A6 string `path:"a6"`
	A7 string `path:"a7"`
	B9 string `path:"b9"`
	BX string `path:"b10"`
}

// A template with more than eight parameters is matched, and its
// values bound, like any other, backtracking included: a literal branch
// that binds values past the eighth and leads nowhere is backed out of, and
// the parameter branch binds its own.
func TestMoreThanEightParameters(t *testing.T) {
	ten := func(_ context.Context, in *tenIn) (*which, error) {
		return &which{Route: "ten", Value: strings.Join([]string{in.A1, in.A2, in.A3, in.A4, in.A5, in.A6, in.A7, in.A8, in.A9, in.A10}, ",")}, nil
	}
	sevenK := func(_ context.Context, in *sevenKIn) (*which, error) {
		return &which{Route: "k", Value: strings.Join([]string{in.A1, in.A7, in.B9, in.BX}, ",")}, nil
	}
	var seen []string
	var mu sync.Mutex
	look := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen = append(seen, r.PathValue("a9")+"/"+r.PathValue("a10")+"/"+r.PathValue("b10"))
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	})
	a := accepts(t, geta.Table{Root: geta.Scope{look}, Routes: []geta.Entry{
		{Path: "/{a1}/{a2}/{a3}/{a4}/{a5}/{a6}/{a7}/{a8}/{a9}/{a10}/end", Route: geta.Route{Get: geta.Op(http.StatusOK, ten, geta.Doc{})}},
		{Path: "/{a1}/{a2}/{a3}/{a4}/{a5}/{a6}/{a7}/k/{b9}/{b10}/z", Route: geta.Route{Get: geta.Op(http.StatusOK, sevenK, geta.Doc{})}},
	}})
	for _, c := range []struct{ path, status, body, seen string }{
		{"/1/2/3/4/5/6/7/8/9/10/end", "200", `{"route":"ten","value":"1,2,3,4,5,6,7,8,9,10"}`, "9/10/"},
		// k is tried as the literal first; its branch binds n and m, meets end
		// where it wants z, and gives way to the parameters.
		{"/1/2/3/4/5/6/7/k/n/m/end", "200", `{"route":"ten","value":"1,2,3,4,5,6,7,k,n,m"}`, "n/m/"},
		{"/1/2/3/4/5/6/7/k/n/m/z", "200", `{"route":"k","value":"1,7,n,m"}`, "//m"},
		{"/1/2/3/4/5/6/7/8/9/10/zzz", "404", "", "//"},
		{"/1/2/3/4/5/6/7/8/9/a%2Fb/end", "200", `{"route":"ten","value":"1,2,3,4,5,6,7,8,9,a/b"}`, "9/a/b/"},
	} {
		mu.Lock()
		seen = nil
		mu.Unlock()
		rec := do(t, a, http.MethodGet, c.path)
		if itoa(rec.Code) != c.status || c.body != "" && rec.Body.String() != c.body {
			t.Errorf("%s: %d %s; want %s %s", c.path, rec.Code, rec.Body, c.status, c.body)
		}
		mu.Lock()
		got := strings.Join(seen, "|")
		mu.Unlock()
		if got != c.seen {
			t.Errorf("%s: middleware read a9/a10/b10 %q, want %q", c.path, got, c.seen)
		}
	}
}

// Cleaning resolves . and .., collapses doubled slashes, and keeps a
// trailing slash.
func TestCleaningKeepsATrailingSlash(t *testing.T) {
	a := accepts(t, one("/a/b", get(okHandler)))
	for path, want := range map[string]string{
		"/a//b/":    "/a/b/",
		"/a/./b/":   "/a/b/",
		"/a/c/../":  "/a/",
		"/a/b/./":   "/a/b/",
		"/a/b/..":   "/a",
		"/a/b/../":  "/a/",
		"//":        "/",
		"/a/b/.//":  "/a/b/",
		"/x/../../": "/",
	} {
		rec := do(t, a, http.MethodGet, path)
		if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != want {
			t.Errorf("%s: %d Location %q; want 307 %q", path, rec.Code, rec.Header().Get("Location"), want)
		}
	}
}

// A request that will be redirected carries, for the root scope, the
// match of the operation its clean path reaches.
func TestARedirectedRequestCarriesTheCleanPathsMatch(t *testing.T) {
	var m matchRecorder
	a := accepts(t, geta.Table{Root: geta.Scope{m.middleware()}, Routes: []geta.Entry{{Path: "/a/b", Route: get(okHandler)}}})
	for path, want := range map[string]string{"/a//b": "GET /a/b", "/x/../a/b": "GET /a/b", "/a//c": "none"} {
		rec := do(t, a, http.MethodGet, path)
		if rec.Code != http.StatusTemporaryRedirect || m.get() != want {
			t.Errorf("%s: %d, root read %q; want 307 and %q", path, rec.Code, m.get(), want)
		}
	}
}

// A CONNECT request's path is never cleaned or redirected.
func TestConnectIsNeverRedirected(t *testing.T) {
	a := accepts(t, one("/a/b", get(okHandler)))
	for _, path := range []string{"/a//b", "/a/./b", "/x/../a/b"} {
		rec := do(t, a, http.MethodConnect, path)
		if rec.Code == http.StatusTemporaryRedirect || rec.Header().Get("Location") != "" || rec.Code != http.StatusNotFound {
			t.Errorf("CONNECT %s: %d Location %q; want 404 and no redirect", path, rec.Code, rec.Header().Get("Location"))
		}
	}
}

// Methods are matched by name, case-sensitively: a method no
// template serves at a path that some template serves is a 405.
func TestMethodsAreCaseSensitive(t *testing.T) {
	a := accepts(t, geta.Table{Routes: []geta.Entry{{Path: "/users/{id}", Route: get(idHandler)}}})
	for _, method := range []string{"get", "Get", "head", "options", "query"} {
		rec := do(t, a, method, "/users/7")
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD, OPTIONS" {
			t.Errorf("%s /users/7: %d Allow %q; want 405 GET, HEAD, OPTIONS", method, rec.Code, rec.Header().Get("Allow"))
		}
	}
}

// HEAD on a path whose template serves GET is answered by the GET
// operation: geta.Matched reports GET, geta.Observe the method HEAD.
func TestHeadIsServedByGet(t *testing.T) {
	var m matchRecorder
	var inHandler string
	h := func(ctx context.Context, _ *empty) (*ok, error) {
		mt, _ := geta.Matched(ctx)
		inHandler = mt.Method + " " + mt.Template
		return &ok{true}, nil
	}
	a := accepts(t, geta.Table{Root: geta.Scope{m.middleware()}, Routes: []geta.Entry{{Path: "/x", Route: get(h)}}})
	ctx, read := geta.Observe(context.Background())
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodHead, "/x", nil))
	method, mt, ok := read()
	if rec.Code != http.StatusOK || m.get() != "GET /x" || inHandler != "GET /x" ||
		!ok || method != http.MethodHead || mt.Method != http.MethodGet || mt.Template != "/x" {
		t.Fatalf("%d %q, root %q, handler %q, observed %s %+v %v", rec.Code, rec.Body, m.get(), inHandler, method, mt, ok)
	}
}

// HEAD is answered only where a template serves GET; on
// a path served by QUERY or POST alone it is a 405 whose Allow lacks HEAD.
func TestHeadWithoutGetIs405(t *testing.T) {
	a, err := geta.New(queryTable(), geta.WithOpenAPI(geta.OpenAPI32))
	if err != nil {
		t.Fatal(err)
	}
	if rec := do(t, a, http.MethodHead, "/search/form"); rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "OPTIONS, QUERY" {
		t.Errorf("HEAD /search/form: %d Allow %q; want 405 OPTIONS, QUERY", rec.Code, rec.Header().Get("Allow"))
	}
	b := accepts(t, optionsTable())
	if rec := do(t, b, http.MethodHead, "/post"); rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "OPTIONS, POST" {
		t.Errorf("HEAD /post: %d Allow %q; want 405 OPTIONS, POST", rec.Code, rec.Header().Get("Allow"))
	}
}

// A root rewrite to a path served under other methods is a 405 with
// that path's Allow, and so is a method rewrite to a method the path does
// not serve; a rewrite to a path nothing serves is a 404.
func TestRewriteToAMethodNotServed(t *testing.T) {
	a := accepts(t, optionsTable(pathMove, methodOverride))
	for _, c := range []struct {
		method, path, move, override string
		status                       int
		allow                        string
	}{
		{http.MethodGet, "/x", "/post", "", http.StatusMethodNotAllowed, "OPTIONS, POST"},
		{http.MethodGet, "/x", "/users/7", "", http.StatusOK, ""},
		{http.MethodPost, "/post", "", http.MethodPatch, http.StatusMethodNotAllowed, "OPTIONS, POST"},
		{http.MethodPost, "/users/7", "/x", http.MethodDelete, http.StatusMethodNotAllowed, "GET, HEAD, OPTIONS"},
		{http.MethodGet, "/x", "/nope", "", http.StatusNotFound, ""},
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		if c.move != "" {
			req.Header.Set("X-Move", c.move)
		}
		if c.override != "" {
			req.Header.Set("X-HTTP-Method-Override", c.override)
		}
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, req)
		if rec.Code != c.status || rec.Header().Get("Allow") != c.allow {
			t.Errorf("%s %s to %s %s: %d Allow %q; want %d %q", c.method, c.path, c.override, c.move, rec.Code, rec.Header().Get("Allow"), c.status, c.allow)
		}
	}
}

// A root rewrite of an OPTIONS request's path answers the OPTIONS
// operation of the new path.
func TestRewrittenOptionsAnswersTheNewPath(t *testing.T) {
	a := accepts(t, optionsTable(pathMove))
	req := httptest.NewRequest(http.MethodOptions, "/x", nil)
	req.Header.Set("X-Move", "/users/7")
	ctx, read := geta.Observe(req.Context())
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req.WithContext(ctx))
	_, m, ok := read()
	if rec.Code != http.StatusNoContent || rec.Header().Get("Allow") != "DELETE, GET, HEAD, OPTIONS, PUT" || !ok ||
		m.Method != http.MethodOptions || m.Template != "/users/{id}" {
		t.Fatalf("%d Allow %q, observed %+v %v", rec.Code, rec.Header().Get("Allow"), m, ok)
	}
}

// With two root gates, a rewrite between them makes dispatch refuse
// any operation either gate would check: 500, logged as "the gates checked
// other operations", and no handler runs; a move to a public operation is
// served.
func TestRewriteBetweenTwoRootGatesIsRefused(t *testing.T) {
	var ran sync.Map
	handler := func(name string) func(context.Context, *empty) (*ok, error) {
		return func(context.Context, *empty) (*ok, error) {
			ran.Store(name, true)
			return &ok{true}, nil
		}
	}
	log, buf := logger()
	a, err := geta.New(geta.Table{Root: geta.Scope{bearerGate(), pathMove, bearerGate()}, Routes: []geta.Entry{
		{Path: "/a", Route: get(handler("a"))},
		{Path: "/b", Route: get(handler("b"))},
		{Path: "/pub", Route: geta.Route{Get: geta.Op(http.StatusOK, handler("pub"), geta.Doc{Security: []geta.Scheme{}})}},
	}}, geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/a", nil)
	req.Header.Set("Authorization", "Bearer good")
	req.Header.Set("X-Move", "/b")
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if _, b := ran.Load("b"); rec.Code != http.StatusInternalServerError || b {
		t.Fatalf("GET /a moved to /b between the gates: %d, /b ran %v; want 500 and no handler", rec.Code, b)
	}
	if !strings.Contains(buf.String(), "gates checked other operations but the request reached GET /b") {
		t.Fatalf("log:\n%s", buf)
	}
	req = httptest.NewRequest(http.MethodGet, "/a", nil)
	req.Header.Set("Authorization", "Bearer good")
	req.Header.Set("X-Move", "/pub")
	rec = httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if _, pub := ran.Load("pub"); rec.Code != http.StatusOK || !pub {
		t.Fatalf("GET /a moved to the public /pub between the gates: %d; want it served", rec.Code)
	}
}

// The operations served do not depend on the order of the table's
// entries.
func TestMatchingIgnoresTableOrder(t *testing.T) {
	entries := []geta.Entry{
		{Path: "/users/by-role/{role}", Route: geta.Route{Get: geta.Op(http.StatusOK, func(_ context.Context, in *roleIn) (*which, error) {
			return &which{Route: "by-role", Value: in.Role}, nil
		}, geta.Doc{})}},
		{Path: "/users/{id}/name", Route: echoIDRoute("id-name")},
		{Path: "/users/{id}", Route: echoIDRoute("id")},
		{Path: "/users/events", Route: answers("events")},
		{Path: "/a/b", Route: geta.Route{Put: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error { return nil }, geta.Doc{})}},
		{Path: "/a/{id}", Route: echoIDRoute("a")},
	}
	reversed := slices.Clone(entries)
	slices.Reverse(reversed)
	one, two := accepts(t, geta.Table{Routes: entries}), accepts(t, geta.Table{Routes: reversed})
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		for _, path := range []string{"/users/by-role/name", "/users/by-role", "/users/7/name", "/users/events", "/a/b", "/a/c", "/users/", "/nope"} {
			r1, r2 := do(t, one, method, path), do(t, two, method, path)
			if r1.Code != r2.Code || r1.Body.String() != r2.Body.String() || r1.Header().Get("Allow") != r2.Header().Get("Allow") {
				t.Errorf("%s %s: %d %s %q in table order, %d %s %q reversed", method, path,
					r1.Code, r1.Body, r1.Header().Get("Allow"), r2.Code, r2.Body, r2.Header().Get("Allow"))
			}
		}
	}
}

// A request that will be redirected passes the root scope with the
// match of its clean path, so without credentials an unclean path to a
// protected operation, or to none, answers 401, and one to a public
// operation 307.
func TestRedirectBehindARootGate(t *testing.T) {
	a := accepts(t, geta.Table{Root: geta.Scope{bearerGate()}, Routes: []geta.Entry{
		{Path: "/secret", Route: get(okHandler)},
		{Path: "/open", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{}})}},
	}})
	for _, c := range []struct {
		path, auth string
		status     int
	}{
		{"//secret", "", http.StatusUnauthorized},
		{"//open", "", http.StatusTemporaryRedirect},
		{"//nowhere", "", http.StatusUnauthorized},
		{"//secret", "Bearer good", http.StatusTemporaryRedirect},
	} {
		var rec *httptest.ResponseRecorder
		if c.auth != "" {
			rec = do(t, a, http.MethodGet, c.path, "Authorization", c.auth)
		} else {
			rec = do(t, a, http.MethodGet, c.path)
		}
		if rec.Code != c.status {
			t.Errorf("%s (%q): %d, want %d", c.path, c.auth, rec.Code, c.status)
		}
	}
}

// The OPTIONS operation geta serves runs the root scope's gates with their
// defaults, so a default scheme with no verifier fails geta.New, though
// every operation of the table declares its own security: the root scope's
// default is judged on its own, whatever the operations.
func TestOptionsNeedsTheRootGatesDefaultVerifier(t *testing.T) {
	gate := geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer}})
	pub := geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{}})}
	rejects(t, geta.Table{Root: geta.Scope{gate}, Routes: []geta.Entry{{Path: "/x", Route: pub}}},
		`root scope: requires scheme "bearer", but the gate's policy has no verifier for it`)
}

// A refused rewrite is refused whatever
// method reaches the protected operation; the root gate checks OPTIONS on
// any path with its defaults.
func TestRefusedRewriteAfterTheGateForEachMethod(t *testing.T) {
	log, _ := logger()
	a, err := geta.New(geta.Table{Root: geta.Scope{bearerGate(), pathMove}, Routes: []geta.Entry{
		{Path: "/pub", Route: geta.Route{
			Get:    geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{}}),
			Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error { return nil }, geta.Doc{Security: []geta.Scheme{}}),
		}},
		{Path: "/admin", Route: geta.Route{
			Get:    geta.Op(http.StatusOK, okHandler, geta.Doc{}),
			Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error { return nil }, geta.Doc{}),
		}},
	}}, geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodDelete} {
		req := httptest.NewRequest(method, "/pub", nil)
		req.Header.Set("X-Move", "/admin")
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s /pub moved to /admin after the gate: %d, want 500", method, rec.Code)
		}
	}
	// OPTIONS on /pub is gated by the defaults, so it is no way past the gate.
	req := httptest.NewRequest(http.MethodOptions, "/pub", nil)
	req.Header.Set("X-Move", "/admin")
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("OPTIONS /pub moved to /admin: %d, want 401", rec.Code)
	}
}
