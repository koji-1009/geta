package geta_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// What an App reports about a request it served (Observe), and what it
// holds a response to (Documented, Conforms).

// Observe reports what an App served a request as: the operation a root
// rewrite reached, the method of a 404 or a 405, and no operation for a
// redirect or before the App has begun.
func TestObserveReportsWhatWasServed(t *testing.T) {
	app, err := geta.New(geta.Table{
		Root: geta.Scope{methodOverride},
		Routes: []geta.Entry{{Path: "/items", Route: geta.Route{
			Post:   geta.Op(http.StatusOK, okHandler, geta.Doc{}),
			Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error { return nil }, geta.Doc{}),
		}}},
	}, geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		method, path, override string
		status                 int
		served                 string
		template               string // "" for no operation
	}{
		{"POST", "/items", "", 200, "POST", "/items"},
		{"POST", "/items", "DELETE", 204, "DELETE", "/items"},
		{"POST", "/items", "PATCH", 405, "PATCH", ""},
		{"GET", "/nowhere", "", 404, "GET", ""},
		{"POST", "//items", "", 307, "POST", ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, nil)
		if c.override != "" {
			req.Header.Set("X-HTTP-Method-Override", c.override)
		}
		ctx, read := geta.Observe(req.Context())
		if m, _, ok := read(); m != "" || ok {
			t.Fatalf("before serving: %q %v", m, ok)
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req.WithContext(ctx))
		method, m, ok := read()
		if rec.Code != c.status || method != c.served || ok != (c.template != "") || ok && (m.Template != c.template || m.Method != c.served) {
			t.Errorf("%s %s as %q: %d, read %q %+v %v; want %d %s %q", c.method, c.path, c.override, rec.Code, method, m, ok, c.status, c.served, c.template)
		}
	}
}

// Inside the App, Observe reads the request the context belongs to.
func TestObserveInsideTheApp(t *testing.T) {
	var method string
	var ok bool
	inside := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			_, read := geta.Observe(r.Context())
			method, _, ok = read()
		})
	})
	app, err := geta.New(geta.Table{
		Root:   geta.Scope{inside, methodOverride},
		Routes: []geta.Entry{{Path: "/items", Route: geta.Route{Post: geta.Op(http.StatusOK, okHandler, geta.Doc{})}}},
	}, geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	if rec := do(t, app, http.MethodPost, "/items", "X-HTTP-Method-Override", "PUT"); rec.Code != http.StatusMethodNotAllowed || method != "PUT" || ok {
		t.Fatalf("%d %q %v", rec.Code, method, ok)
	}
}

// Documented and Conforms take the operation served.
func TestDocumentedTakesTheMatch(t *testing.T) {
	app, err := geta.New(geta.Table{
		Routes: []geta.Entry{{Path: "/items", Route: geta.Route{Post: geta.Op(http.StatusCreated, okHandler, geta.Doc{})}}},
	}, geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	post := geta.Match{Template: "/items", Method: "POST", Operation: true}
	if documented, matched := app.Documented(post, http.StatusCreated); !documented || !matched {
		t.Fatal(documented, matched)
	}
	if documented, matched := app.Documented(post, http.StatusTeapot); documented || !matched {
		t.Fatal(documented, matched)
	}
	if _, matched := app.Documented(geta.Match{Template: "/items", Method: "PUT", Operation: true}, http.StatusCreated); matched {
		t.Fatal("a match no operation gave")
	}
	if _, matched := app.Documented(geta.Match{}, http.StatusNotFound); matched {
		t.Fatal("no operation")
	}
	if err := app.Conforms(post, http.StatusCreated, nil, []byte(`{"ok":"yes"}`)); err == nil {
		t.Fatal("a body off the schema conforms")
	}
	if err := app.Conforms(post, http.StatusCreated, nil, []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
}

type conformsBackstop struct {
	S string `json:"s"`
}

type conformsDeclared struct {
	S string `json:"s" schema:"maxLength=11"`
}

// conformsApp serves out on GET /x under lim.
func conformsApp[Out any](t *testing.T, lim geta.Limits, out *Out) *geta.App {
	t.Helper()
	app, err := geta.New(geta.Table{Routes: []geta.Entry{{Path: "/x", Route: geta.Route{
		Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Out, error) { return out, nil }, geta.Doc{}),
	}}}}, geta.WithLimits(lim), geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	return app
}

type conformsPattern struct {
	S string `json:"s" schema:"pattern=^a+$"`
}

type conformsList struct {
	L []int `json:"l"`
}

// Conforms holds a response to its schema, which states what the author
// declared and no backstop: a string past MaxStringLength, an array past
// MaxItems, and a patterned string past the pattern ceiling conform, while
// a declared maxLength is enforced; so does getatest's conformance check.
func TestConformsHoldsResponsesToTheDeclaredSchemaOnly(t *testing.T) {
	lim := geta.DefaultLimits
	lim.MaxStringLength, lim.MaxItems = 10, 2
	const eleven, twelve = "abcdefghijk", "abcdefghijkl"
	get := geta.Match{Template: "/x", Method: "GET", Operation: true}
	conforms := func(t *testing.T, app *geta.App, body string) {
		t.Helper()
		if err := app.Conforms(get, http.StatusOK, nil, []byte(body)); err != nil {
			t.Fatalf("Conforms(%s): %v", body, err)
		}
		rec := &recorder{TB: t}
		getatest.Serve(rec, app).Get("/x")
		if len(rec.errs) != 0 {
			t.Fatalf("getatest: %q", rec.errs)
		}
	}

	app := conformsApp(t, lim, &conformsBackstop{S: eleven})
	if d := string(app.OpenAPI()); strings.Contains(d, `"maxLength"`) {
		t.Fatalf("document: %s", d)
	}
	conforms(t, app, `{"s":"`+eleven+`"}`)

	app = conformsApp(t, lim, &conformsList{L: []int{1, 2, 3}})
	if d := string(app.OpenAPI()); strings.Contains(d, `"maxItems"`) {
		t.Fatalf("document: %s", d)
	}
	conforms(t, app, `{"l":[1,2,3]}`)

	long := strings.Repeat("a", 5000)
	app = conformsApp(t, lim, &conformsPattern{S: long})
	conforms(t, app, `{"s":"`+long+`"}`)
	if err := app.Conforms(get, http.StatusOK, nil, []byte(`{"s":"`+long+`b"}`)); err == nil || !strings.Contains(err.Error(), "does not match pattern ^a+$") {
		t.Fatalf("Conforms: %v", err)
	}

	// A declared maxLength is the author's, and is enforced.
	app = conformsApp(t, lim, &conformsDeclared{S: twelve})
	if !strings.Contains(string(app.OpenAPI()), `"maxLength": 11`) {
		t.Fatalf("document: %s", app.OpenAPI())
	}
	if err := app.Conforms(get, http.StatusOK, nil, []byte(`{"s":"`+eleven+`"}`)); err != nil {
		t.Fatal(err)
	}
	if err := app.Conforms(get, http.StatusOK, nil, []byte(`{"s":"`+twelve+`"}`)); err == nil || !strings.Contains(err.Error(), "string length 12 exceeds maxLength 11") {
		t.Fatalf("Conforms: %v", err)
	}
	rec := &recorder{TB: t}
	getatest.Serve(rec, app).Get("/x")
	if len(rec.errs) != 1 || !strings.Contains(rec.errs[0], "the body does not match the documented schema") ||
		!strings.Contains(rec.errs[0], "string length 12 exceeds maxLength 11") {
		t.Fatalf("getatest: %q", rec.errs)
	}
}
