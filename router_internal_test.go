package geta

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type valuesIn struct {
	ID string `path:"id"`
}

type valuesOut struct {
	ID string `json:"id"`
}

// A request's parameter values come from the route's values mux unless the
// request arrives with a pattern that names a parameter, whose values that
// mux would drop, or its path has an escape, which that mux would unescape
// again.
func TestValuesMuxIsUsedUnlessThePatternHasParameters(t *testing.T) {
	var valued bool
	look := Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			valued = requestFrom(r.Context()).valued
			next.ServeHTTP(w, r)
		})
	})
	app, err := New(Table{Root: Scope{look}, Routes: []Entry{{Path: "/users/{id}", Route: Route{
		Get: Op(http.StatusOK, func(ctx context.Context, in *valuesIn) (*valuesOut, error) {
			return &valuesOut{in.ID}, nil
		}, Doc{}),
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path, pattern, body string
		valued              bool
	}{
		{"/users/7", "", `{"id":"7"}`, true},
		{"/users/7", "/plain/", `{"id":"7"}`, true},
		{"/users/7", "/t/{tenant}/", `{"id":"7"}`, false},
		{"/users/a%20b", "", `{"id":"a b"}`, false},
	} {
		r := httptest.NewRequest(http.MethodGet, c.path, nil)
		r.Pattern = c.pattern
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, r)
		if valued != c.valued || rec.Body.String() != c.body {
			t.Errorf("%s, pattern %q: valued %v, body %s", c.path, c.pattern, valued, rec.Body)
		}
	}
}

type cafeIn struct {
	Café string `path:"café"`
	Nº2  string `path:"nº2"`
}

type cafeOut struct {
	Café string `json:"cafe"`
	Nº2  string `json:"n2"`
}

// A path parameter's name is a Go identifier in any script, as ServeMux
// takes it; it is bound, and readable with r.PathValue, through the
// route's values mux.
func TestPathParameterNamesInAnyScript(t *testing.T) {
	for name, want := range map[string]bool{"café": true, "_x1": true, "名前": true, "nº2": true, "x٣": true,
		"1x": false, "٣x": false, "a-b": false, "": false, "a b": false} {
		if got := validWildcard(name); got != want {
			t.Errorf("validWildcard(%q) = %v", name, got)
		}
	}
	if _, err := newRegistry().inPlan(reflect.TypeFor[struct {
		X string `path:"1x"`
	}]()); err == nil || !strings.Contains(err.Error(), `"1x" is not a Go identifier`) {
		t.Errorf("1x: %v", err)
	}

	const pattern = "GET /shops/{café}/items/{nº2}"
	mux := valuesMux(pattern)
	if mux == nil {
		t.Fatal("ServeMux refuses " + pattern)
	}
	rc := &requestContext{Context: context.Background()}
	r := httptest.NewRequest(http.MethodGet, "/shops/a/items/b", nil).WithContext(rc)
	mux.ServeHTTP(discard{}, r)
	if !rc.valued || r.PathValue("café") != "a" || r.PathValue("nº2") != "b" {
		t.Fatalf("values mux: valued %v, café=%q nº2=%q", rc.valued, r.PathValue("café"), r.PathValue("nº2"))
	}

	var seen string
	look := Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = r.Pattern + " café=" + r.PathValue("café") + " nº2=" + r.PathValue("nº2")
			next.ServeHTTP(w, r)
		})
	})
	app, err := New(Table{Root: Scope{look}, Routes: []Entry{{Path: "/shops/{café}/items/{nº2}",
		Route: Route{Get: Op(http.StatusOK, func(_ context.Context, in *cafeIn) (*cafeOut, error) {
			return &cafeOut{Café: in.Café, Nº2: in.Nº2}, nil
		}, Doc{})}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ path, want, seen string }{
		{"/shops/a/items/b", `{"cafe":"a","n2":"b"}`, "GET /shops/{café}/items/{nº2} café=a nº2=b"},
		{"/shops/caf%C3%A9/items/2", `{"cafe":"café","n2":"2"}`, "GET /shops/{café}/items/{nº2} café=café nº2=2"},
	} {
		seen = ""
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != c.want {
			t.Errorf("%s: %d %s, want %s", c.path, rec.Code, rec.Body, c.want)
		}
		if seen != c.seen {
			t.Errorf("%s: middleware saw %q, want %q", c.path, seen, c.seen)
		}
	}
}
