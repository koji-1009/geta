package geta_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/koji-1009/geta"
)

type corsMethodsOut struct {
	OK bool `json:"ok"`
}

type corsMethodsID struct {
	ID string `path:"id"`
}

func corsMethodsApp(t *testing.T, opts ...geta.CORSOption) *geta.App {
	t.Helper()
	h := func(context.Context, *struct{}) (*corsMethodsOut, error) { return &corsMethodsOut{true}, nil }
	op := func() geta.Operation { return geta.Op(http.StatusOK, h, geta.Doc{}) }
	byID := func(context.Context, *corsMethodsID) (*corsMethodsOut, error) { return &corsMethodsOut{true}, nil }
	idOp := func() geta.Operation { return geta.Op(http.StatusOK, byID, geta.Doc{}) }
	a, err := geta.New(geta.Table{
		Root: geta.Scope{geta.CORS(append([]geta.CORSOption{geta.AllowOrigins("*")}, opts...)...)},
		Routes: []geta.Entry{
			{Path: "/users", Route: geta.Route{Get: op(), Post: op()}},
			{Path: "/users/{id}", Route: geta.Route{Get: idOp(), Put: idOp(), Delete: idOp()}},
			// /users/me overlaps /users/{id}: its path is served by both.
			{Path: "/users/me", Route: geta.Route{Patch: op()}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func corsMethodsPreflight(a *geta.App, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodOptions, path, nil)
	req.Header.Set("Origin", "https://page.example")
	req.Header.Set("Access-Control-Request-Method", method)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	return rec
}

// A preflight allows the methods the path serves, the union its Allow lists
// less HEAD and OPTIONS, so a page is never cleared to send a request that
// gets 405. AllowMethods adds to them.
func TestCORSPreflightAllowsThePathsMethods(t *testing.T) {
	a := corsMethodsApp(t)
	for _, c := range []struct{ method, path, want string }{
		{"PUT", "/users/1", "DELETE, GET, PUT"},
		{"PATCH", "/users/1", "DELETE, GET, PUT"},
		{"POST", "/users", "GET, POST"},
		{"PATCH", "/users/me", "DELETE, GET, PATCH, PUT"},
		{"GET", "/nope", ""},
	} {
		rec := corsMethodsPreflight(a, c.method, c.path)
		if got := rec.Header().Get("Access-Control-Allow-Methods"); rec.Code != http.StatusNoContent || got != c.want {
			t.Errorf("%s %s: %d %q; want %q", c.method, c.path, rec.Code, got, c.want)
		}
	}
	// The path's own methods answer as the preflight said.
	req := httptest.NewRequest(http.MethodPatch, "/users/1", nil)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PATCH /users/1: %d", rec.Code)
	}

	a = corsMethodsApp(t, geta.AllowMethods("PATCH", "GET"))
	for _, c := range []struct{ method, path, want string }{
		{"PUT", "/users/1", "DELETE, GET, PUT, PATCH"},
		{"GET", "/nope", "PATCH, GET"},
	} {
		if got := corsMethodsPreflight(a, c.method, c.path).Header().Get("Access-Control-Allow-Methods"); got != c.want {
			t.Errorf("with AllowMethods, %s %s: %q; want %q", c.method, c.path, got, c.want)
		}
	}
}
