package geta_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/koji-1009/geta"
)

// The routing rules a request crosses on its way through a real server, a
// root rewrite, or a mount, and the templates geta.New refuses because the
// document could not state them truthfully.

// sendRaw writes one raw HTTP/1.1 request to addr, which is listening, and
// reads its response.
func sendRaw(t *testing.T, addr, raw string) *http.Response {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if _, err := c.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res
}

// OPTIONS * reaches the App through geta.Serve (and geta.Run,
// TestRunPassesOptionsAsteriskOn), where net/http would otherwise answer it
// itself (200, no Allow) unless the server's DisableGeneralOptionsHandler is
// set.
func TestOptionsAsteriskThroughARealServer(t *testing.T) {
	var mu sync.Mutex
	var rootRan bool
	seen := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			rootRan = true
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	})
	a := accepts(t, optionsTable(seen))
	const want = "DELETE, GET, HEAD, OPTIONS, POST, PUT"
	check := func(name, addr string) {
		t.Helper()
		mu.Lock()
		rootRan = false
		mu.Unlock()
		res := sendRaw(t, addr, "OPTIONS * HTTP/1.1\r\nHost: x\r\n\r\n")
		mu.Lock()
		ran := rootRan
		mu.Unlock()
		if res.StatusCode != http.StatusNoContent || res.Header.Get("Allow") != want || !ran {
			t.Errorf("%s: OPTIONS *: %d Allow %q, root scope ran %v; want 204 Allow %q from the App", name, res.StatusCode, res.Header.Get("Allow"), ran, want)
		}
		if res := sendRaw(t, addr, "GET * HTTP/1.1\r\nHost: x\r\n\r\n"); res.StatusCode != http.StatusBadRequest ||
			res.Header.Get("Content-Type") != geta.ProblemContentType {
			t.Errorf("%s: GET *: %d %v; want the App's 400 problem", name, res.StatusCode, res.Header)
		}
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- geta.Serve(ctx, &http.Server{Handler: a}, l) }()
	check("Serve", l.Addr().String())
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// After a root rewrite, the operation's scopes and handler read
// r.Pattern and r.PathValue of the operation served, and only of it: a name
// of the template the request first matched reads "", whether the values
// were set for that template, a mount's outer pattern sits beside them, or
// only the method was rewritten.
func TestPathValuesAfterARootRewriteAreTheServedOperations(t *testing.T) {
	var mu sync.Mutex
	var seen string
	look := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen = r.Pattern + " id=" + r.PathValue("id") + " other=" + r.PathValue("other") + " x=" + r.PathValue("x") +
				" tenant=" + r.PathValue("tenant")
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	})
	rewrite := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if to := r.Header.Get("X-Move"); to != "" {
				r = r.Clone(r.Context())
				r.URL.Path = to
			}
			next.ServeHTTP(w, r)
		})
	})
	other := geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct {
		Other string `path:"other"`
	}) (*ok, error) {
		return &ok{true}, nil
	}, geta.Doc{})}
	x := func(context.Context, *struct {
		X string `path:"x"`
	}) (*ok, error) {
		return &ok{true}, nil
	}
	app, err := geta.New(geta.Table{Root: geta.Scope{rewrite, methodOverride}, Routes: []geta.Entry{
		{Path: "/alias/{other}", Route: other},
		{Path: "/users/{id}", Route: get(idHandler), Scopes: []geta.Scope{{look}}},
		{Path: "/plain", Route: get(okHandler), Scopes: []geta.Scope{{look}}},
		{Path: "/a/b", Route: geta.Route{Put: geta.Op(http.StatusOK, okHandler, geta.Doc{})}, Scopes: []geta.Scope{{look}}},
		{Path: "/a/{x}", Route: geta.Route{Post: geta.Op(http.StatusOK, x, geta.Doc{})}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/t/{tenant}/", func(w http.ResponseWriter, r *http.Request) {
		http.StripPrefix("/t/"+r.PathValue("tenant"), app).ServeHTTP(w, r)
	})
	for _, c := range []struct {
		h                    http.Handler
		method, path, move   string
		override, want, have string
	}{
		{app, http.MethodGet, "/alias/7", "/users/7", "", "GET /users/{id} id=7 other= x= tenant=", ""},
		{app, http.MethodGet, "/alias/7", "/plain", "", "GET /plain id= other= x= tenant=", ""},
		{app, http.MethodGet, "/alias/a%20b", "/users/8", "", "GET /users/{id} id=8 other= x= tenant=", ""},
		{mux, http.MethodGet, "/t/acme/alias/7", "/users/9", "", "GET /users/{id} id=9 other= x= tenant=acme", ""},
		{app, http.MethodPost, "/a/b", "", "PUT", "PUT /a/b id= other= x= tenant=", ""},
	} {
		mu.Lock()
		seen = ""
		mu.Unlock()
		req := httptest.NewRequest(c.method, c.path, nil)
		if c.move != "" {
			req.Header.Set("X-Move", c.move)
		}
		if c.override != "" {
			req.Header.Set("X-HTTP-Method-Override", c.override)
		}
		rec := httptest.NewRecorder()
		c.h.ServeHTTP(rec, req)
		mu.Lock()
		got := seen
		mu.Unlock()
		if rec.Code != http.StatusOK || got != c.want {
			t.Errorf("%s %s moved to %s%s: %d, the scope saw %q; want 200, %q", c.method, c.path, c.move, c.override, rec.Code, got, c.want)
		}
	}
}

// A dot segment in a template is refused: a client resolves it away
// before sending (RFC 3986 §5.2.4), and the router redirects a path that has
// one, so no request reaches the operation the document would list.
func TestRejectsDotSegmentsInTemplates(t *testing.T) {
	for _, p := range []string{"/.", "/..", "/a/./b", "/a/../b", "/a/.."} {
		rejects(t, one(p, get(okHandler)), "is a dot segment")
	}
	// A segment that only begins with a dot is an ordinary literal.
	for _, p := range []string{"/.well-known", "/a/...", "/.x/..y"} {
		a := accepts(t, one(p, get(okHandler)))
		if rec := do(t, a, http.MethodGet, p); rec.Code != http.StatusOK {
			t.Errorf("GET %s: %d", p, rec.Code)
		}
	}
}

// geta.New reports every mistake once, however many methods
// the entry that makes it serves.
func TestEachAssemblyMistakeIsReportedOnce(t *testing.T) {
	name := func(context.Context, *struct {
		Name string `path:"name"`
	}) (*ok, error) {
		return &ok{true}, nil
	}
	_, err := geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/u/{id}", Route: get(idHandler)},
		{Path: "/u/{name}", Route: geta.Route{
			Get:    geta.Op(http.StatusOK, name, geta.Doc{}),
			Delete: geta.Op(http.StatusOK, name, geta.Doc{}),
		}},
	}})
	if err == nil || strings.Count(err.Error(), "differ only in parameter names") != 1 {
		t.Fatalf("want the conflict named once:\n%v", err)
	}
	_, err = geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "users", Route: get(okHandler)},
		{Path: "/a/./b", Route: get(okHandler)},
		{Path: "/x", Route: geta.Route{}},
	}})
	if err == nil {
		t.Fatal("accepted")
	}
	for _, f := range []string{`path "users" does not start with /`, `"/a/./b"`, "/x: the route serves no method"} {
		if n := strings.Count(err.Error(), f); n != 1 {
			t.Errorf("%q reported %d times:\n%v", f, n, err)
		}
	}
	// A root scope's or a shared directory scope's mistake, which every
	// chain through it repeats, is reported once, where New meets it first.
	log := quietLogger()
	dir := geta.Scope{geta.Timeout(0)}
	_, err = geta.New(geta.Table{Root: geta.Scope{geta.Recover(log), geta.AccessLog(log)}, Routes: []geta.Entry{
		{Path: "/a", Route: get(okHandler), Scopes: []geta.Scope{dir}},
		{Path: "/b", Route: get(okHandler), Scopes: []geta.Scope{dir}},
	}})
	for _, f := range []string{"runs after recover(3000) but has a lower order", "geta.Timeout 0s is not positive"} {
		if n := strings.Count(fmt.Sprint(err), f); n != 1 {
			t.Errorf("%q reported %d times:\n%v", f, n, err)
		}
	}
}

// geta.New reports every mistake at once: each middleware of a scope, an
// operation's own scope and BeforeGate beside its failure rows, and the
// operation's other mistakes.
func TestEveryAssemblyMistakeIsReportedAtOnce(t *testing.T) {
	_, err := geta.New(geta.Table{Routes: []geta.Entry{{Path: "/a", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{
		Scope: geta.Scope{geta.Timeout(0)}, BeforeGate: geta.Scope{geta.ConcurrencyLimit(0)}, Failures: []geta.Failure{{}},
	})}}}})
	for _, want := range []string{"geta.Timeout 0s is not positive", "geta.ConcurrencyLimit 0 is less than 1", "zero geta.Failure"} {
		if !strings.Contains(fmt.Sprint(err), want) {
			t.Errorf("missing %q:\n%v", want, err)
		}
	}
	_, err = geta.New(geta.Table{Root: geta.Scope{geta.ConcurrencyLimit(0), geta.Timeout(0)}})
	for _, want := range []string{"ConcurrencyLimit 0", "Timeout 0s"} {
		if !strings.Contains(fmt.Sprint(err), want) {
			t.Errorf("root: missing %q:\n%v", want, err)
		}
	}
}

// A nil option, a zero Union, and a nil handler are assembly mistakes, not
// panics.
func TestNilAndZeroArgumentsAreRefused(t *testing.T) {
	var h func(context.Context, *empty) (*ok, error)
	for name, c := range map[string]struct {
		tbl  geta.Table
		opts []geta.Option
		want string
	}{
		"nil option":  {geta.Table{}, []geta.Option{nil}, "option 0 is nil"},
		"zero union":  {geta.Table{}, []geta.Option{geta.WithUnion(geta.Union{})}, "a zero geta.Union"},
		"nil handler": {one("/x", geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{})}), nil, "the handler is nil"},
		"nil no-body": {one("/x", geta.Route{Delete: geta.OpNoBody[empty](http.StatusNoContent, nil, geta.Doc{})}), nil, "the handler is nil"},
	} {
		if _, err := geta.New(c.tbl, c.opts...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// An App mounted under http.StripPrefix redirects an unclean path to
// the clean form of the URL the client sent, mount prefix included, so the
// client stays inside the mount. Where the client's URL is clean and only
// the stripped path is not, there is no cleaner URL to send the client to:
// the App serves what the clean form of its path reaches.
func TestRedirectUnderAMountStaysInsideIt(t *testing.T) {
	app := accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/", Route: answers("root")},
		{Path: "/users", Route: answers("users")},
	}})
	h := http.StripPrefix("/api", app)
	for path, want := range map[string]string{
		"/api/./users":     "/api/users",
		"/api/./users?q=1": "/api/users?q=1",
		"/api/x/../users":  "/api/users",
		"/api/./":          "/api/",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != want {
			t.Errorf("%s: %d Location %q; want 308 %q", path, rec.Code, rec.Header().Get("Location"), want)
		}
	}
	for path, want := range map[string]string{
		"/api":      `{"route":"root","value":""}`,  // the App sees ""
		"/apiusers": `{"route":"users","value":""}`, // the App sees "users"
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("%s: %d %s; want 200 %s", path, rec.Code, rec.Body, want)
		}
	}
	// An absolute-form request target keeps its path.
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/./users", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != "/api/users" {
		t.Errorf("absolute form: %d Location %q", rec.Code, rec.Header().Get("Location"))
	}
}

// When the path the App sees is not a suffix of the path the client
// sent, an outer handler changed it in a way the App cannot undo: no URL the
// App could name is the client's, so it does not redirect, and serves what
// the clean path reaches.
func TestNoRedirectWhereTheClientsURLIsUnknown(t *testing.T) {
	app := accepts(t, one("/users", get(okHandler)))
	outer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.URL.Path = "/v2/../users"
		app.ServeHTTP(w, r)
	})
	rec := httptest.NewRecorder()
	ctx, read := geta.Observe(context.Background())
	outer.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/users", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Location") != "" {
		t.Fatalf("%d Location %q; want 200 and no redirect", rec.Code, rec.Header().Get("Location"))
	}
	if _, m, ok := read(); !ok || m.Template != "/users" {
		t.Fatalf("observed %+v %v", m, ok)
	}
}

// The asterisk form names no operation, so the root scope's Secure
// gates check their defaults for it, as for a URL that matches nothing:
// without credentials OPTIONS * and GET * answer 401, with them 204 and 400.
func TestAsteriskFormBehindARootGate(t *testing.T) {
	a := accepts(t, optionsTable(bearerGate()))
	for _, method := range []string{http.MethodOptions, http.MethodGet} {
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, httptest.NewRequest(method, "*", nil))
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "Bearer" || rec.Header().Get("Allow") != "" {
			t.Errorf("%s * without credentials: %d %v; want 401", method, rec.Code, rec.Header())
		}
	}
	rec := do(t, a, http.MethodOptions, "*", "Authorization", "Bearer good")
	if rec.Code != http.StatusNoContent || rec.Header().Get("Allow") != "DELETE, GET, HEAD, OPTIONS, POST, PUT" {
		t.Errorf("OPTIONS * with credentials: %d %v", rec.Code, rec.Header())
	}
	if rec := do(t, a, http.MethodGet, "*", "Authorization", "Bearer good"); rec.Code != http.StatusBadRequest {
		t.Errorf("GET * with credentials: %d", rec.Code)
	}
}

// A root rewrite that leaves the path unclean is not the client's
// doing, so the client is not redirected to the internal path: the request
// is served by the operation the clean form reaches, which is what the gates
// read.
func TestRewriteToAnUncleanPathIsServedNotRedirected(t *testing.T) {
	var mu sync.Mutex
	var matched string
	look := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			m, _ := geta.Matched(r.Context())
			mu.Lock()
			matched = m.Method + " " + m.Template
			mu.Unlock()
		})
	})
	rewrite := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rest, ok := strings.CutPrefix(r.URL.Path, "/alias/"); ok {
				r = r.Clone(r.Context())
				r.URL.Path = "/internal/./users/" + rest
			}
			next.ServeHTTP(w, r)
		})
	})
	echo := geta.Route{Get: geta.Op(http.StatusOK, func(_ context.Context, in *idNameIn) (*which, error) {
		return &which{Route: "internal", Value: in.ID}, nil
	}, geta.Doc{})}
	a := accepts(t, geta.Table{Root: geta.Scope{look, rewrite}, Routes: []geta.Entry{
		{Path: "/internal/users/{id}", Route: echo},
		{Path: "/alias/{id}", Route: echo},
	}})
	// Built in process, the request has no RequestURI: the rewrite alone
	// tells that the path is not the client's.
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/alias/2", nil),
		func() *http.Request {
			r, _ := http.NewRequest(http.MethodGet, "/alias/2", nil)
			return r
		}(),
	} {
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, req)
		mu.Lock()
		got := matched
		mu.Unlock()
		if rec.Code != http.StatusOK || rec.Header().Get("Location") != "" || rec.Body.String() != `{"route":"internal","value":"2"}` ||
			got != "GET /internal/users/{id}" {
			t.Errorf("RequestURI %q: %d Location %q %s, matched %q", req.RequestURI, rec.Code, rec.Header().Get("Location"), rec.Body, got)
		}
	}
}

// A literal segment holding what a URL path cannot hold as written is
// refused: ? and # end the path, and whitespace and control characters are
// no URI characters, so the document's path would name a URL no client
// sends.
func TestRejectsLiteralsAPathCannotHold(t *testing.T) {
	for _, p := range []string{"/a?b", "/a#b", "/a b", "/a\tb", "/a\x00b", "/a\x7fb", "/a\u0085b", "/x/?"} {
		rejects(t, one(p, get(okHandler)), "segment ")
	}
	// Unicode letters, and the characters RFC 3986 allows in a segment, stay.
	for _, p := range []string{"/café", "/a:b", "/a@b", "/a!$&'()*+,;=b", "/a~b", "/日本"} {
		accepts(t, one(p, get(okHandler)))
	}
}
