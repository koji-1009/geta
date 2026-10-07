package geta_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// Rules of routing.md that no other test asserts as the rule states them.

// geta.Route has one field per method it can serve, and none for HEAD or
// OPTIONS, which geta answers itself; Route.Query serves the method QUERY.
func TestRoutingRouteFieldsAreTheMethods(t *testing.T) {
	rt := reflect.TypeFor[geta.Route]()
	var names []string
	for i := range rt.NumField() {
		f := rt.Field(i)
		if f.Type != reflect.TypeFor[geta.Operation]() {
			t.Errorf("field %s has type %s, want geta.Operation", f.Name, f.Type)
		}
		names = append(names, f.Name)
	}
	if got := strings.Join(names, ","); got != "Get,Post,Put,Patch,Delete,Query" {
		t.Fatalf("geta.Route fields: %s", got)
	}
	if geta.MethodQuery != "QUERY" {
		t.Fatalf("geta.MethodQuery = %q", geta.MethodQuery)
	}
}

// A CONNECT request in authority form names no path: it matches no
// template and is a 404 problem, with no Allow.
func TestRoutingConnectAuthorityFormIs404(t *testing.T) {
	a := accepts(t, one("/x", get(okHandler)))
	rec := do(t, a, http.MethodConnect, "example.com:443")
	if rec.Code != http.StatusNotFound || rec.Header().Get("Allow") != "" || rec.Header().Get("Location") != "" ||
		rec.Header().Get("Content-Type") != geta.ProblemContentType {
		t.Fatalf("CONNECT example.com:443: %d %v", rec.Code, rec.Header())
	}
}

// A request other than CONNECT whose path does not begin with /, which only
// a request built in process can carry, is redirected to the path with /
// before it.
func TestRoutingPathWithoutLeadingSlashIsRedirected(t *testing.T) {
	a := accepts(t, one("/x", get(okHandler)))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.URL.Path, req.RequestURI = "x", ""
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "/x" {
		t.Fatalf("GET x: %d Location %q; want 307 /x", rec.Code, rec.Header().Get("Location"))
	}
}

// HEAD served by a GET operation carries that operation's pattern, GET's.
func TestRoutingHeadCarriesTheGetPattern(t *testing.T) {
	var mu sync.Mutex
	var seen string
	look := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen = r.Pattern + " id=" + r.PathValue("id")
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	})
	a := accepts(t, geta.Table{Root: geta.Scope{look}, Routes: []geta.Entry{{Path: "/users/{id}", Route: get(idHandler)}}})
	rec := do(t, a, http.MethodHead, "/users/7")
	mu.Lock()
	got := seen
	mu.Unlock()
	if rec.Code != http.StatusOK || got != "GET /users/{id} id=7" {
		t.Fatalf("HEAD /users/7: %d, root saw %q", rec.Code, got)
	}
}

// A request that reaches no operation (a 404, a 405) keeps the pattern and
// the values it arrived with: none when the App serves it directly, the
// outer ServeMux's when the App is mounted under one.
func TestRoutingUnmatchedRequestKeepsItsPattern(t *testing.T) {
	var mu sync.Mutex
	var seen string
	look := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen = r.Pattern + " tenant=" + r.PathValue("tenant") + " id=" + r.PathValue("id")
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	})
	app := accepts(t, geta.Table{Root: geta.Scope{look}, Routes: []geta.Entry{{Path: "/users/{id}", Route: get(idHandler)}}})
	mux := http.NewServeMux()
	mux.HandleFunc("/t/{tenant}/", func(w http.ResponseWriter, r *http.Request) {
		http.StripPrefix("/t/"+r.PathValue("tenant"), app).ServeHTTP(w, r)
	})
	for _, c := range []struct {
		h            http.Handler
		method, path string
		status       int
		seen         string
	}{
		{app, http.MethodGet, "/nope", http.StatusNotFound, " tenant= id="},
		{app, http.MethodDelete, "/users/7", http.StatusMethodNotAllowed, " tenant= id="},
		{mux, http.MethodGet, "/t/acme/nope", http.StatusNotFound, "/t/{tenant}/ tenant=acme id="},
		{mux, http.MethodDelete, "/t/acme/users/7", http.StatusMethodNotAllowed, "/t/{tenant}/ tenant=acme id="},
	} {
		rec := httptest.NewRecorder()
		c.h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		mu.Lock()
		got := seen
		mu.Unlock()
		if rec.Code != c.status || got != c.seen {
			t.Errorf("%s %s: %d, root saw %q; want %d, %q", c.method, c.path, rec.Code, got, c.status, c.seen)
		}
	}
}

// A request made in process has no RequestURI: the URL the App receives is
// taken as the client's, so a path unclean in it is redirected to its clean
// form, which under http.StripPrefix lacks the prefix.
func TestRoutingRedirectWithoutRequestURIUsesTheURL(t *testing.T) {
	app := accepts(t, geta.Table{Routes: []geta.Entry{{Path: "/users", Route: answers("users")}}})
	for _, c := range []struct {
		h          http.Handler
		path, want string
	}{
		{app, "/x/../users", "/users"},
		{http.StripPrefix("/api", app), "/apiusers", "/users"},
	} {
		req, err := http.NewRequest(http.MethodGet, c.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		c.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != c.want {
			t.Errorf("%s without a RequestURI: %d Location %q; want 307 %q", c.path, rec.Code, rec.Header().Get("Location"), c.want)
		}
	}
	// With the RequestURI a server sets, the client's URL is clean.
	rec := httptest.NewRecorder()
	http.StripPrefix("/api", app).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/apiusers", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Location") != "" {
		t.Fatalf("/apiusers with its RequestURI: %d Location %q", rec.Code, rec.Header().Get("Location"))
	}
}

// A redirected request is served by no operation: the root scope runs, but
// neither the directory scopes, the operation's Doc.Scope, nor its handler.
func TestRoutingRedirectRunsNoOperation(t *testing.T) {
	var root, dir, opScope, handler atomic.Int32
	count := func(n *atomic.Int32) geta.Middleware {
		return geta.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n.Add(1)
				next.ServeHTTP(w, r)
			})
		})
	}
	h := func(context.Context, *empty) (*ok, error) {
		handler.Add(1)
		return &ok{true}, nil
	}
	a := accepts(t, geta.Table{Root: geta.Scope{count(&root)}, Routes: []geta.Entry{{
		Path:   "/a/b",
		Route:  geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{Scope: geta.Scope{count(&opScope)}})},
		Scopes: []geta.Scope{{count(&dir)}},
	}}})
	if rec := do(t, a, http.MethodGet, "/a/./b"); rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "/a/b" {
		t.Fatalf("GET /a/./b: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if root.Load() != 1 || dir.Load() != 0 || opScope.Load() != 0 || handler.Load() != 0 {
		t.Fatalf("root %d, directory scope %d, Doc.Scope %d, handler %d; want 1, 0, 0, 0", root.Load(), dir.Load(), opScope.Load(), handler.Load())
	}
}

type routingUpload struct {
	Title string    `form:"title"`
	File  geta.File `form:"file"`
}

type routingUploadIn struct {
	Body routingUpload `body:"multipart"`
}

// Route.Query reads a multipart body as any method that reads one.
func TestRoutingQueryReadsAMultipartBody(t *testing.T) {
	h := func(_ context.Context, in *routingUploadIn) (*text, error) {
		return &text{in.Body.Title + ":" + in.Body.File.Filename()}, nil
	}
	c := getatest.New(t, one("/q", geta.Route{Query: geta.Op(http.StatusOK, h, geta.Doc{})}), geta.WithOpenAPI(geta.OpenAPI32))
	res := c.Multipart(geta.MethodQuery, "/q", url.Values{"title": {"hi"}}, getatest.FilePart{Field: "file", Filename: "a.txt", Content: []byte("A")})
	if res.Status != http.StatusOK || res.Text() != `{"text":"hi:a.txt"}` {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
}

// Once dispatch has chosen the operation it is fixed: a rewrite in the
// operation's own Doc.Scope changes neither the operation served nor what a
// gate after it checks.
func TestRoutingRewriteInsideADocScopeIsFixed(t *testing.T) {
	var posted, deleted atomic.Bool
	app, err := geta.New(geta.Table{Routes: []geta.Entry{{Path: "/items", Route: geta.Route{
		Post:   geta.Op(http.StatusOK, reportMatched(&posted), geta.Doc{Scope: geta.Scope{methodOverride, bearerGate()}}),
		Delete: geta.Op(http.StatusOK, reportMatched(&deleted), geta.Doc{Security: []geta.Scheme{}}),
	}}}}, geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, app, http.MethodPost, "/items", "X-HTTP-Method-Override", "DELETE")
	if rec.Code != http.StatusUnauthorized || posted.Load() || deleted.Load() {
		t.Fatalf("POST overridden to DELETE without credentials: %d, post ran=%v, delete ran=%v; want 401 and no handler",
			rec.Code, posted.Load(), deleted.Load())
	}
	rec = do(t, app, http.MethodPost, "/items", "X-HTTP-Method-Override", "DELETE", "Authorization", "Bearer good")
	if rec.Code != http.StatusOK || !posted.Load() || deleted.Load() || strings.TrimSpace(rec.Body.String()) != `{"method":"POST","template":"/items"}` {
		t.Fatalf("with credentials: %d %s, post ran=%v, delete ran=%v; want 200 from the POST", rec.Code, rec.Body, posted.Load(), deleted.Load())
	}
}

// Every refusal of a table path's syntax names the path.
func TestRoutingMalformedPathRefusalsNameThePath(t *testing.T) {
	for _, p := range []string{
		"users", "/users/", "/users//x", "/a/{1d}", "/a/{id}/b/{id}", "/a/x{id}", "/files/a%20b",
		"/a/./b", "/..", "/a?b", "/a#b", "/a b", "/a\x00b", "/a\x7fb", "/a\u0085b",
	} {
		_, err := geta.New(one(p, get(okHandler)))
		if err == nil || !strings.Contains(err.Error(), strconv.Quote(p)) {
			t.Errorf("%q: %v; want an error naming the path", p, err)
		}
	}
}

// A conflict between templates (two differing only in parameter names) and
// an operationId equal to the one an options operation derives are found
// once every entry assembles: beside another mistake they are not reported;
// on their own they are reported together.
func TestRoutingTemplateConflictsWaitForEntryMistakes(t *testing.T) {
	name := func(context.Context, *struct {
		Name string `path:"name"`
	}) (*ok, error) {
		return &ok{true}, nil
	}
	routes := []geta.Entry{
		{Path: "/u/{id}", Route: get(idHandler)},
		{Path: "/u/{name}", Route: get(name)},
		{Path: "/x", Route: get(okHandler)},
		{Path: "/other", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{OperationID: "optionsX"})}},
	}
	_, err := geta.New(geta.Table{Routes: append([]geta.Entry{{Path: "users", Route: get(okHandler)}}, routes...)})
	if err == nil || !strings.Contains(err.Error(), `path "users" does not start with /`) ||
		strings.Contains(err.Error(), "differ only in parameter names") || strings.Contains(err.Error(), `"optionsX"`) {
		t.Fatalf("beside a malformed path: %v", err)
	}
	_, err = geta.New(geta.Table{Routes: routes})
	if err == nil || !strings.Contains(err.Error(), "differ only in parameter names") || !strings.Contains(err.Error(), `operationId "optionsX"`) {
		t.Fatalf("on their own: %v", err)
	}
}

// An operationId is Doc.OperationID, or the method in lower case followed by
// each run of letters and digits of the path, capitalised; / is Root.
func TestRoutingDerivedOperationIDs(t *testing.T) {
	posts := func(context.Context, *idNameIn) (*ok, error) { return &ok{true}, nil }
	m := doc(t, accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/", Route: get(okHandler)},
		{Path: "/users/{id}/posts", Route: geta.Route{
			Get:    geta.Op(http.StatusOK, posts, geta.Doc{}),
			Delete: geta.Op(http.StatusOK, posts, geta.Doc{OperationID: "removePosts"}),
		}},
		{Path: "/v2/café-menu", Route: get(okHandler)},
	}}))
	for _, c := range []struct{ path, method, want string }{
		{"/", "get", "getRoot"},
		{"/", "options", "optionsRoot"},
		{"/users/{id}/posts", "get", "getUsersByIdPosts"},
		{"/users/{id}/posts", "delete", "removePosts"},
		{"/users/{id}/posts", "options", "optionsUsersByIdPosts"},
		{"/v2/café-menu", "get", "getV2Café-menu"},
	} {
		if got := at(t, m, "paths", c.path, c.method, "operationId"); got != c.want {
			t.Errorf("%s %s: operationId %v, want %s", c.method, c.path, got, c.want)
		}
	}
}
