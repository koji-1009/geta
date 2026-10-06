package geta_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// optionsTable serves one method on /x, three on /users/{id}, and POST alone
// on /post.
func optionsTable(root ...geta.Middleware) geta.Table {
	put := func(ctx context.Context, in *idIn) (*ok, error) { return &ok{true}, nil }
	del := func(ctx context.Context, in *idIn) error { return nil }
	post := func(ctx context.Context, _ *empty) (*ok, error) { return &ok{true}, nil }
	return geta.Table{Root: root, Routes: []geta.Entry{
		{Path: "/x", Route: get(okHandler)},
		{Path: "/users/{id}", Route: geta.Route{
			Get:    geta.Op(http.StatusOK, idHandler, geta.Doc{}),
			Put:    geta.Op(http.StatusOK, put, geta.Doc{}),
			Delete: geta.OpNoBody(http.StatusNoContent, del, geta.Doc{}),
		}},
		{Path: "/post", Route: geta.Route{Post: geta.Op(http.StatusCreated, post, geta.Doc{})}},
	}}
}

// OPTIONS on a path with operations answers 204 with Allow listing what the
// path serves: HEAD beside GET, and OPTIONS itself. getatest checks the 204
// against the document's options operation.
func TestOptionsListsTheMethodsServed(t *testing.T) {
	c := getatest.New(t, optionsTable())
	for path, want := range map[string]string{
		"/x":         "GET, HEAD, OPTIONS",
		"/users/7":   "DELETE, GET, HEAD, OPTIONS, PUT",
		"/users/abc": "DELETE, GET, HEAD, OPTIONS, PUT",
		"/post":      "OPTIONS, POST",
	} {
		res := c.Do(http.MethodOptions, path, nil)
		if res.Status != http.StatusNoContent || res.Header.Get("Allow") != want || len(res.Body) != 0 {
			t.Errorf("OPTIONS %s: %d Allow %q %q, want 204 Allow %q", path, res.Status, res.Header.Get("Allow"), res.Body, want)
		}
	}
}

// Where templates overlap, OPTIONS lists what every template matching the
// path serves, as a 405 does, and the document lists each value a template's
// paths can get.
func TestOptionsUnionsTheBranches(t *testing.T) {
	type xIn struct {
		X string `path:"x"`
	}
	type yIn struct {
		Y string `path:"y"`
	}
	nobody := func(context.Context, *empty) error { return nil }
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/a/b", Route: geta.Route{Put: geta.OpNoBody(http.StatusNoContent, nobody, geta.Doc{})}},
		{Path: "/a/{x}", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *xIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}},
		{Path: "/a/b/{y}", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *yIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}},
		{Path: "/a/{x}/c", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *xIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}},
	}})
	for path, want := range map[string]string{
		"/a/b":   "GET, HEAD, OPTIONS, PUT",
		"/a/z":   "GET, HEAD, OPTIONS",
		"/a/b/c": "GET, HEAD, OPTIONS, POST",
		"/a/b/d": "GET, HEAD, OPTIONS",
		"/a/z/c": "OPTIONS, POST",
	} {
		res := c.Do(http.MethodOptions, path, nil)
		if res.Status != http.StatusNoContent || res.Header.Get("Allow") != want {
			t.Errorf("OPTIONS %s: %d Allow %q, want %q", path, res.Status, res.Header.Get("Allow"), want)
		}
		if res := c.Do("TRACE", path, nil); res.Status != http.StatusMethodNotAllowed || res.Header.Get("Allow") != want {
			t.Errorf("TRACE %s: %d Allow %q, want 405 %q", path, res.Status, res.Header.Get("Allow"), want)
		}
	}
	m := doc(t, c.App())
	for template, want := range map[string]string{
		"/a/b":     `{"const":"GET, HEAD, OPTIONS, PUT","type":"string"}`,
		"/a/{x}":   `{"const":"GET, HEAD, OPTIONS","type":"string"}`,
		"/a/b/{y}": `{"enum":["GET, HEAD, OPTIONS","GET, HEAD, OPTIONS, POST"],"type":"string"}`,
		"/a/{x}/c": `{"const":"OPTIONS, POST","type":"string"}`,
	} {
		if got := compact(t, at(t, m, "paths", template, "options", "responses", "204", "headers", "Allow", "schema")); got != want {
			t.Errorf("%s: Allow %s, want %s", template, got, want)
		}
	}
}

// A path no operation serves stays 404 under OPTIONS, beside one that some
// operation serves.
func TestOptionsOnAPathWithoutOperationsIs404(t *testing.T) {
	c := getatest.New(t, optionsTable())
	if res := c.Do(http.MethodOptions, "/users/7", nil); res.Status != http.StatusNoContent {
		t.Fatalf("OPTIONS /users/7: %d, want 204", res.Status)
	}
	for _, path := range []string{"/nope", "/users", "/users/7/x", "/x/y"} {
		if res := c.Do(http.MethodOptions, path, nil); res.Status != http.StatusNotFound || res.Header.Get("Allow") != "" {
			t.Errorf("OPTIONS %s: %d Allow %q, want 404", path, res.Status, res.Header.Get("Allow"))
		}
	}
	if res := c.Do(http.MethodOptions, "/x/", nil); res.Status != http.StatusNotFound {
		t.Errorf("OPTIONS /x/: %d, want 404", res.Status)
	}
}

// A 405 lists OPTIONS among the methods the path serves.
func TestMethodNotAllowedListsOptions(t *testing.T) {
	c := getatest.New(t, optionsTable())
	for path, want := range map[string]string{
		"/x":       "GET, HEAD, OPTIONS",
		"/users/7": "DELETE, GET, HEAD, OPTIONS, PUT",
		"/post":    "OPTIONS, POST",
	} {
		res := c.Do(http.MethodPatch, path, nil)
		if res.Status != http.StatusMethodNotAllowed || res.Header.Get("Allow") != want {
			t.Errorf("PATCH %s: %d Allow %q, want 405 Allow %q", path, res.Status, res.Header.Get("Allow"), want)
		}
	}
}

// The asterisk form asks about the server in general (RFC 9110 §9.3.7): 204
// with every method some path serves. It is no path, so no template matches
// it, not even one that would match /*.
func TestOptionsAsteriskListsEveryMethod(t *testing.T) {
	var matched bool
	seen := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, matched = geta.Matched(r.Context())
			next.ServeHTTP(w, r)
		})
	})
	tbl := optionsTable(seen)
	tbl.Routes = append(tbl.Routes, geta.Entry{Path: "/{name}", Route: geta.Route{Patch: geta.Op(http.StatusOK,
		func(context.Context, *struct {
			Name string `path:"name"`
		}) (*ok, error) {
			return &ok{true}, nil
		}, geta.Doc{})}})
	a := accepts(t, tbl)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "*", nil))
	if rec.Code != http.StatusNoContent || rec.Header().Get("Allow") != "DELETE, GET, HEAD, OPTIONS, PATCH, POST, PUT" || rec.Body.Len() != 0 {
		t.Fatalf("OPTIONS *: %d Allow %q %q", rec.Code, rec.Header().Get("Allow"), rec.Body)
	}
	if matched {
		t.Fatal("OPTIONS * matched an operation")
	}
	// /* itself is a path, which /{name} serves.
	if rec := do(t, a, http.MethodOptions, "/*"); rec.Code != http.StatusNoContent || rec.Header().Get("Allow") != "OPTIONS, PATCH" {
		t.Fatalf("OPTIONS /*: %d Allow %q", rec.Code, rec.Header().Get("Allow"))
	}
}

// OPTIONS * on a table with no routes still names a method the server
// serves: OPTIONS, which geta answers itself, never an empty Allow.
func TestOptionsAsteriskWithNoRoutesListsOptions(t *testing.T) {
	a := accepts(t, geta.Table{})
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "*", nil))
	if rec.Code != http.StatusNoContent || rec.Header().Get("Allow") != "OPTIONS" {
		t.Fatalf("OPTIONS *: %d Allow %q", rec.Code, rec.Header().Get("Allow"))
	}
}

// The asterisk form is for OPTIONS alone (RFC 9110 §7.1): any other method in
// it is a 400 problem, as ServeMux answers, inside the root scope like a 404.
// It is neither matched as /* (which /{name} serves) nor redirected there.
func TestAsteriskFormOnOtherMethodsIs400(t *testing.T) {
	var mu sync.Mutex
	var ran, matched bool
	seen := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, m := geta.Matched(r.Context())
			mu.Lock()
			ran, matched = true, matched || m
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	})
	tbl := optionsTable(seen)
	name := func(context.Context, *struct {
		Name string `path:"name"`
	}) (*ok, error) {
		return &ok{true}, nil
	}
	tbl.Routes = append(tbl.Routes, geta.Entry{Path: "/{name}", Route: geta.Route{
		Get:   geta.Op(http.StatusOK, name, geta.Doc{}),
		Patch: geta.Op(http.StatusOK, name, geta.Doc{}),
	}})
	a := accepts(t, tbl)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPatch, http.MethodPost, "TRACE"} {
		ran, matched = false, false
		rec := httptest.NewRecorder()
		ctx, read := geta.Observe(context.Background())
		a.ServeHTTP(rec, httptest.NewRequest(method, "*", nil).WithContext(ctx))
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" || rec.Header().Get("Allow") != "" {
			t.Errorf("%s *: %d %v, want 400", method, rec.Code, rec.Header())
		}
		if method != http.MethodHead && rec.Header().Get("Content-Type") != geta.ProblemContentType {
			t.Errorf("%s *: Content-Type %q", method, rec.Header().Get("Content-Type"))
		}
		if !ran || matched {
			t.Errorf("%s *: root scope ran %v, matched %v; want it run, matching nothing", method, ran, matched)
		}
		if _, _, ok := read(); ok {
			t.Errorf("%s *: observed as served", method)
		}
	}
	// /* itself is a path, which /{name} serves.
	if rec := do(t, a, http.MethodGet, "/*"); rec.Code != http.StatusOK {
		t.Fatalf("GET /*: %d", rec.Code)
	}
}

// CORS answers a preflight as before; a plain cross-origin OPTIONS passes
// through it to the options operation, and the page may read Allow.
func TestOptionsBesideCORS(t *testing.T) {
	a := accepts(t, optionsTable(geta.CORS(geta.AllowOrigins("https://a.example"))))
	pre := do(t, a, http.MethodOptions, "/users/7", "Origin", "https://a.example", "Access-Control-Request-Method", "PUT")
	if pre.Code != http.StatusNoContent || pre.Header().Get("Access-Control-Allow-Methods") == "" || pre.Header().Get("Allow") != "" {
		t.Fatalf("preflight: %d %v", pre.Code, pre.Header())
	}
	res := do(t, a, http.MethodOptions, "/users/7", "Origin", "https://a.example")
	h := res.Header()
	if res.Code != http.StatusNoContent || h.Get("Allow") != "DELETE, GET, HEAD, OPTIONS, PUT" ||
		h.Get("Access-Control-Allow-Origin") != "https://a.example" || h.Get("Access-Control-Expose-Headers") != "Allow" ||
		h.Get("Access-Control-Allow-Methods") != "" {
		t.Fatalf("OPTIONS: %d %v", res.Code, h)
	}
}

// The root scope wraps OPTIONS as it wraps a 405: it reads the options
// operation as the match, and the access log names its template.
func TestRootScopeSeesOptions(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	record := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m, ok := geta.Matched(r.Context())
			mu.Lock()
			if ok {
				seen = append(seen, m.Method+" "+m.Template)
			} else {
				seen = append(seen, "none")
			}
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	})
	log, buf := logger()
	c := getatest.New(t, optionsTable(geta.AccessLog(log), record))
	c.Do(http.MethodOptions, "/users/7", nil)
	c.Do(http.MethodOptions, "/nope", nil)
	if got := strings.Join(seen, "|"); got != "OPTIONS /users/{id}|none" {
		t.Fatalf("root saw %s", got)
	}
	lines := buf.String()
	if !strings.Contains(lines, "method=OPTIONS route=/users/{id} status=204") || !strings.Contains(lines, "method=OPTIONS route=<unmatched> status=404") {
		t.Fatalf("log:\n%s", lines)
	}
	ctx, read := geta.Observe(context.Background())
	c.App().ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodOptions, "/x", nil))
	if method, m, ok := read(); !ok || method != http.MethodOptions || m.Method != http.MethodOptions || m.Template != "/x" {
		t.Fatalf("observed %s %+v %v", method, m, ok)
	}
}

// OPTIONS answers what a 405 answers, under the same scope: the root's, its
// gates checking their defaults as for a URL that matches no operation. A
// directory's or an operation's own scope guards the operations it wraps, and
// OPTIONS runs none of them.
func TestOptionsRunsTheRootScopeOnly(t *testing.T) {
	gate := geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer}, Verifiers: map[string]geta.Verifier{
		"bearer": func(r *http.Request) (context.Context, error) {
			if r.Header.Get("Authorization") == "Bearer good" {
				return nil, nil
			}
			return nil, geta.ErrUnauthenticated
		},
	}})
	forbid := geta.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { geta.WriteProblem(w, http.StatusForbidden, "") })
	}).Answers(http.StatusForbidden, "Forbidden")
	tbl := geta.Table{Root: geta.Scope{gate}, Routes: []geta.Entry{
		{Path: "/admin", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Scope: geta.Scope{forbid}})}, Scopes: []geta.Scope{{forbid}}},
		{Path: "/health", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{}})}},
	}}
	c := getatest.New(t, tbl)
	if res := c.Do(http.MethodOptions, "/admin", nil); res.Status != http.StatusUnauthorized || res.Header.Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("OPTIONS /admin without credentials: %d %v", res.Status, res.Header)
	}
	if res := c.Do(http.MethodOptions, "/health", nil); res.Status != http.StatusUnauthorized {
		t.Fatalf("OPTIONS /health without credentials: %d; a public GET does not make OPTIONS public", res.Status)
	}
	if res := c.Do(http.MethodPost, "/health", nil); res.Status != http.StatusUnauthorized {
		t.Fatalf("POST /health without credentials: %d; OPTIONS answers what a 405 answers, under the same gate", res.Status)
	}
	if res := c.Bearer("good").Do(http.MethodOptions, "/admin", nil); res.Status != http.StatusNoContent || res.Header.Get("Allow") != "GET, HEAD, OPTIONS" {
		t.Fatalf("OPTIONS /admin: %d %v", res.Status, res.Header)
	}
	if res := c.Bearer("good").Get("/admin"); res.Status != http.StatusForbidden {
		t.Fatalf("GET /admin: %d", res.Status)
	}
	m := doc(t, c.App())
	admin := at(t, m, "paths", "/admin", "options")
	if got := compact(t, at(t, admin, "security")); got != `[{"bearer":[]}]` {
		t.Fatalf("security %s", got)
	}
	if got := compact(t, at(t, m, "paths", "/health", "options", "security")); got != `[{"bearer":[]}]` {
		t.Fatalf("/health security %s", got)
	}
	responses := at(t, admin, "responses").(map[string]any)
	for _, status := range []string{"204", "401", "415", "500", "503"} {
		if _, ok := responses[status]; !ok {
			t.Errorf("%s not listed: %v", status, responses)
		}
	}
	if len(responses) != 5 {
		t.Errorf("responses: %v", responses)
	}
}

// The document lists an options operation on every path: its 204 and the
// Allow it carries, the path's parameters, what the root scope answers, and
// the 415 of content, which it does not take; a 504 only where a root middleware answers one, since no handler runs.
func TestOptionsIsDocumented(t *testing.T) {
	m := doc(t, accepts(t, optionsTable()))
	opt := at(t, m, "paths", "/users/{id}", "options")
	if at(t, opt, "operationId") != "optionsUsersId" {
		t.Fatal(opt)
	}
	if got := compact(t, at(t, opt, "parameters")); got != `[{"in":"path","name":"id","required":true,"schema":{"minLength":1,"type":"string"}}]` {
		t.Fatal(got)
	}
	if got := compact(t, at(t, opt, "responses", "204")); got != `{"description":"No Content","headers":{"Allow":{"description":"The methods this URL serves","required":true,"schema":{"const":"DELETE, GET, HEAD, OPTIONS, PUT","type":"string"}}}}` {
		t.Fatal(got)
	}
	responses := at(t, opt, "responses").(map[string]any)
	if _, ok := responses["500"]; !ok || len(responses) != 3 || responses["415"] == nil {
		t.Fatalf("responses: %v", responses)
	}
	if got := compact(t, at(t, opt, "security")); got != `[]` {
		t.Fatal(got)
	}
	if _, has := at(t, m, "paths", "/x", "options").(map[string]any)["parameters"]; has {
		t.Fatal("/x lists parameters")
	}
	if got := at(t, m, "paths", "/post", "options", "responses", "204", "headers", "Allow", "schema", "const"); got != "OPTIONS, POST" {
		t.Fatal(got)
	}

	limited := doc(t, accepts(t, optionsTable(limit(1), geta.Timeout(time.Second), geta.ETag())))
	responses = at(t, limited, "paths", "/x", "options", "responses").(map[string]any)
	for _, status := range []string{"204", "415", "429", "500", "504"} {
		if _, ok := responses[status]; !ok {
			t.Errorf("%s not listed: %v", status, responses)
		}
	}
	if len(responses) != 5 {
		t.Errorf("an OPTIONS lists %v; ETag's 304 is a GET's", responses)
	}
}

// Documented and Types know the options operation.
func TestOptionsIsAnOperation(t *testing.T) {
	a := accepts(t, optionsTable())
	m := geta.Match{Template: "/users/{id}", Method: http.MethodOptions}
	if documented, matched := a.Documented(m, http.StatusNoContent); !documented || !matched {
		t.Fatalf("204: %v %v", documented, matched)
	}
	if documented, matched := a.Documented(m, http.StatusMethodNotAllowed); documented || !matched {
		t.Fatalf("405: %v %v", documented, matched)
	}
	if in, out, ok := a.Types(http.MethodOptions, "/users/{id}"); !ok || in.NumField() != 0 || out != nil {
		t.Fatalf("Types: %v %v %v", in, out, ok)
	}
	if got := strings.Join(a.Operations(), "|"); !strings.Contains(got, "OPTIONS /users/{id}") {
		t.Fatal(got)
	}
}

// An operationId the table gives may not take the one an options operation
// derives: the document's identifiers are unique.
func TestRejectsOperationIDOfAnOptionsOperation(t *testing.T) {
	tbl := optionsTable()
	tbl.Routes = append(tbl.Routes, geta.Entry{Path: "/other", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{OperationID: "optionsX"})}})
	rejects(t, tbl, "OPTIONS /x", `operationId "optionsX"`, "GET /other")
}

// A root middleware that loses the request context reaches dispatch without
// the match: the 500 it answers is on the document of the options operation
// the request was matched to.
func TestOptionsDefectIsDocumented(t *testing.T) {
	lose := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithoutCancel(context.Background())))
		})
	})
	app, err := geta.New(optionsTable(lose), geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	if documented, matched := app.Documented(geta.Match{Template: "/x", Method: http.MethodOptions}, http.StatusInternalServerError); !documented || !matched {
		t.Fatalf("500: %v %v", documented, matched)
	}
	c := getatest.Serve(t, app)
	if res := c.Do(http.MethodOptions, "/x", nil); res.Status != http.StatusInternalServerError {
		t.Fatalf("%d", res.Status)
	}
}
