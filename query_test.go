package geta_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

type querySearch struct {
	Term  string `json:"term" schema:"minLength=1"`
	Limit *int   `json:"limit,omitzero" schema:"minimum=1,maximum=50"`
}

type querySearchIn struct {
	Body querySearch `body:"json"`
}

type queryHits struct {
	Hits []string `json:"hits"`
}

func querySearchHandler(_ context.Context, in *querySearchIn) (*queryHits, error) {
	return &queryHits{Hits: []string{in.Body.Term}}, nil
}

type queryFormIn struct {
	Body struct {
		Term string `form:"term"`
	} `body:"form"`
}

type queryCondIn struct {
	geta.Conditional
	Body querySearch `body:"json"`
}

func queryTable() geta.Table {
	form := func(_ context.Context, in *queryFormIn) (*queryHits, error) {
		return &queryHits{Hits: []string{in.Body.Term}}, nil
	}
	cond := func(_ context.Context, in *queryCondIn) (*queryHits, error) {
		if err := in.Check(`"v1"`, time.Time{}); err != nil {
			return nil, err
		}
		return &queryHits{Hits: []string{in.Body.Term}}, nil
	}
	return geta.Table{
		Root: geta.Scope{geta.CORS(geta.AllowOrigins("*"))},
		Routes: []geta.Entry{
			{Path: "/search", Route: geta.Route{
				Get:   geta.Op(http.StatusOK, okHandler, geta.Doc{}),
				Query: geta.Op(http.StatusOK, querySearchHandler, geta.Doc{Summary: "Search"}),
			}},
			{Path: "/search/form", Route: geta.Route{Query: geta.Op(http.StatusOK, form, geta.Doc{})}},
			{Path: "/search/cond", Route: geta.Route{Query: geta.Op(http.StatusOK, cond, geta.Doc{})}},
		},
	}
}

// A 3.1 document has no place for a QUERY operation, so New refuses one
// unless the document is 3.2, which lists it as the path's query operation.
func TestQueryNeedsOpenAPI32(t *testing.T) {
	_, err := geta.New(queryTable())
	if err == nil || !strings.Contains(err.Error(), "QUERY /search") || !strings.Contains(err.Error(), "has no QUERY operation; use geta.OpenAPI32") {
		t.Fatalf("3.1: %v", err)
	}
	if _, err := geta.New(queryTable(), geta.WithOpenAPI(geta.OpenAPI31)); err == nil {
		t.Fatal("WithOpenAPI(OpenAPI31) accepted a QUERY operation")
	}
	a, err := geta.New(queryTable(), geta.WithOpenAPI(geta.OpenAPI32))
	if err != nil {
		t.Fatal(err)
	}
	m := doc(t, a)
	op := at(t, m, "paths", "/search", "query").(map[string]any)
	if op["operationId"] != "querySearch" || op["summary"] != "Search" {
		t.Fatal(compact(t, op))
	}
	if got := compact(t, at(t, op, "requestBody")); got != `{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/querySearch"}}},"required":true}` {
		t.Fatal(got)
	}
	if got := compact(t, at(t, op, "responses", "415", "headers", "Accept-Query", "schema")); got != `{"enum":["application/json"],"type":"string"}` {
		t.Fatal(got)
	}
	if _, has := at(t, op, "responses", "415", "headers").(map[string]any)["Accept-Patch"]; has {
		t.Fatal("a QUERY's 415 documents Accept-Patch")
	}
	if got := compact(t, at(t, m, "paths", "/search", "options", "responses", "204", "headers", "Allow", "schema")); got != `{"const":"GET, HEAD, OPTIONS, QUERY","type":"string"}` {
		t.Fatal(got)
	}
	if got := at(t, m, "paths", "/search/form", "query", "responses", "415", "headers", "Accept-Query", "schema", "enum").([]any)[0]; got != "application/x-www-form-urlencoded" {
		t.Fatal(got)
	}
	getatest.Golden(t, a, "testdata/query.openapi.3.2.json")
}

// QUERY is served with its body, as any method that reads one; Allow, OPTIONS,
// OPTIONS *, a 405, a preflight, and a 415 all know it.
func TestQueryIsServedWithABody(t *testing.T) {
	c := getatest.New(t, queryTable(), geta.WithOpenAPI(geta.OpenAPI32))
	res := c.Query("/search", map[string]any{"term": "go"})
	if res.Status != http.StatusOK || res.JSON[queryHits]().Hits[0] != "go" {
		t.Fatal(res.Status, res.Text())
	}
	if res := c.Query("/search", map[string]any{"term": ""}); res.Status != http.StatusBadRequest {
		t.Fatal(res.Status, res.Text())
	}
	if res := c.Query("/search", nil); res.Status != http.StatusBadRequest {
		t.Fatal("a QUERY with no body:", res.Status, res.Text())
	}
	res = c.With("Content-Type", "text/plain").Query("/search", `{"term":"go"}`)
	if res.Status != http.StatusUnsupportedMediaType || res.Header.Get("Accept-Query") != "application/json" ||
		res.Header.Get("Accept") != "application/json" || res.Header.Get("Accept-Patch") != "" {
		t.Fatal(res.Status, res.Header)
	}
	if res := c.Form(geta.MethodQuery, "/search/form", map[string][]string{"term": {"x"}}); res.Status != http.StatusOK {
		t.Fatal(res.Status, res.Text())
	}
	// RFC 9110's rule: an If-None-Match that matches is a 304 on GET and HEAD
	// alone, so on a QUERY it is a 412.
	if res := c.With("If-None-Match", `"v1"`).Query("/search/cond", map[string]any{"term": "go"}); res.Status != http.StatusPreconditionFailed {
		t.Fatal(res.Status, res.Text())
	}
	if res := c.With("If-None-Match", `"v2"`).Query("/search/cond", map[string]any{"term": "go"}); res.Status != http.StatusOK {
		t.Fatal(res.Status, res.Text())
	}

	a := c.App()
	for _, tc := range []struct {
		method, path string
		status       int
		allow        string
	}{
		{http.MethodOptions, "/search", http.StatusNoContent, "GET, HEAD, OPTIONS, QUERY"},
		{http.MethodPost, "/search", http.StatusMethodNotAllowed, "GET, HEAD, OPTIONS, QUERY"},
		{http.MethodGet, "/search/form", http.StatusMethodNotAllowed, "OPTIONS, QUERY"},
		{http.MethodOptions, "*", http.StatusNoContent, "GET, HEAD, OPTIONS, QUERY"},
	} {
		req := httptest.NewRequest(tc.method, "/", nil)
		req.URL.Path = tc.path
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, req)
		if rec.Code != tc.status || rec.Header().Get("Allow") != tc.allow {
			t.Errorf("%s %s: %d Allow %q; want %d %q", tc.method, tc.path, rec.Code, rec.Header().Get("Allow"), tc.status, tc.allow)
		}
	}
	// An unclean path is redirected with 308, which keeps the method and the
	// body.
	if rec := do(t, a, geta.MethodQuery, "/x/../search"); rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != "/search" {
		t.Fatal(rec.Code, rec.Header())
	}

	// A preflight clears QUERY on the path that serves it, with Content-Type,
	// and the page reads Accept-Query from a 415.
	req := httptest.NewRequest(http.MethodOptions, "/search", nil)
	req.Header.Set("Origin", "https://page.example")
	req.Header.Set("Access-Control-Request-Method", geta.MethodQuery)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Methods") != "GET, QUERY" || rec.Header().Get("Access-Control-Allow-Headers") != "Content-Type" {
		t.Fatal(rec.Header())
	}
	req = httptest.NewRequest(geta.MethodQuery, "/search", strings.NewReader("x"))
	req.Header.Set("Origin", "https://page.example")
	req.Header.Set("Content-Type", "text/plain")
	rec = httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType || !strings.Contains(rec.Header().Get("Access-Control-Expose-Headers"), "Accept-Query") {
		t.Fatal(rec.Code, rec.Header())
	}
}
