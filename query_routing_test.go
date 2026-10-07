package geta_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
)

// QUERY beside the router's other rules: ETag, redirects, and root rewrites.

// geta.ETag tags GET and HEAD responses alone, not QUERY's.
func TestETagSkipsQuery(t *testing.T) {
	tbl := queryTable()
	tbl.Root = geta.Scope{geta.ETag()}
	a, err := geta.New(tbl, geta.WithOpenAPI(geta.OpenAPI32))
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if rec := do(t, a, method, "/search"); rec.Code != http.StatusOK || rec.Header().Get("ETag") == "" {
			t.Errorf("%s /search: %d ETag %q; want a tag", method, rec.Code, rec.Header().Get("ETag"))
		}
	}
	req := httptest.NewRequest(geta.MethodQuery, "/search", strings.NewReader(`{"term":"go"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != "" {
		t.Fatalf("QUERY /search: %d ETag %q; want no tag", rec.Code, rec.Header().Get("ETag"))
	}
	// A matching If-None-Match does not turn a QUERY into a 304 either.
	req = httptest.NewRequest(geta.MethodQuery, "/search", strings.NewReader(`{"term":"go"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-None-Match", "*")
	rec = httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("QUERY /search with If-None-Match: %d", rec.Code)
	}
}

// An unclean QUERY path is answered 307 to the clean path, the query
// string kept, so the client repeats the method and the body there.
func TestQueryRedirectKeepsTheQueryString(t *testing.T) {
	a, err := geta.New(queryTable(), geta.WithOpenAPI(geta.OpenAPI32))
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"/x/../search?q=1":     "/search?q=1",
		"/./search/form?a&b=c": "/search/form?a&b=c",
		"/search/./cond":       "/search/cond",
	} {
		req := httptest.NewRequest(geta.MethodQuery, path, strings.NewReader(`{"term":"go"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, req)
		if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != want || rec.Body.Len() != 0 {
			t.Errorf("QUERY %s: %d Location %q body %q; want 307 %q and no body", path, rec.Code, rec.Header().Get("Location"), rec.Body, want)
		}
	}
}

// A root middleware that rewrites a POST to QUERY has it served by
// the QUERY operation, geta.Matched and geta.Observe reporting QUERY.
func TestRewriteToQuery(t *testing.T) {
	var matched string
	h := func(ctx context.Context, in *querySearchIn) (*queryHits, error) {
		m, _ := geta.Matched(ctx)
		matched = m.Method + " " + m.Template
		return &queryHits{Hits: []string{in.Body.Term}}, nil
	}
	a, err := geta.New(geta.Table{Root: geta.Scope{methodOverride}, Routes: []geta.Entry{
		{Path: "/search", Route: geta.Route{Query: geta.Op(http.StatusOK, h, geta.Doc{})}},
	}}, geta.WithOpenAPI(geta.OpenAPI32))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(`{"term":"go"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-HTTP-Method-Override", geta.MethodQuery)
	ctx, read := geta.Observe(req.Context())
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req.WithContext(ctx))
	method, m, ok := read()
	if rec.Code != http.StatusOK || rec.Body.String() != `{"hits":["go"]}` || matched != "QUERY /search" ||
		!ok || method != geta.MethodQuery || m.Method != geta.MethodQuery {
		t.Fatalf("%d %s, handler matched %q, observed %s %+v %v", rec.Code, rec.Body, matched, method, m, ok)
	}
	// Without the rewrite, POST is not served there.
	rec = do(t, a, http.MethodPost, "/search")
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "OPTIONS, QUERY" {
		t.Fatalf("POST /search: %d Allow %q", rec.Code, rec.Header().Get("Allow"))
	}
}
