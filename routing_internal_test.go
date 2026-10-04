package geta

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A segment whose percent-encoding cannot be decoded matches
// nothing. A request's escaped path (URL.EscapedPath) is always well formed,
// so the router meets such a segment only when handed one directly.
func TestUndecodableSegmentMatchesNothing(t *testing.T) {
	a, err := New(Table{Routes: []Entry{
		{Path: "/users/{id}", Route: Route{Get: Op(http.StatusOK, func(context.Context, *struct {
			ID string `path:"id"`
		}) (*struct{}, error) {
			return &struct{}{}, nil
		}, Doc{})}},
		{Path: "/users/x", Route: Route{Put: OpNoBody(http.StatusNoContent, func(context.Context, *struct{}) error { return nil }, Doc{})}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/users/%zz", "/users/%", "/users/a%2"} {
		var m match
		a.router.lookup(http.MethodGet, p, &m)
		if m.route != nil || len(m.allowed) != 0 {
			t.Errorf("%s matched %v, allowed %v", p, m.route, m.allowed)
		}
	}
	// Through ServeHTTP, a path that holds such an escape is read as the
	// characters it holds: % is escaped again, and the value is "%zz".
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.URL.Path, req.URL.RawPath = "/users/%zz", "/users/%zz"
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// valuesMux is nil for a pattern ServeMux refuses, and setPathValues then
// sets the values one by one. New refuses every template ServeMux would
// (dot segments among them), so no route of an App has a nil mux.
func TestValuesMuxRefusingAPattern(t *testing.T) {
	if valuesMux("GET /a/../b") != nil || valuesMux("GET /{a}/{a}") != nil {
		t.Fatal("ServeMux accepted a pattern it refuses")
	}
	if valuesMux("GET /a/{b}") == nil {
		t.Fatal("ServeMux refused a pattern it accepts")
	}
}

// Secure, used outside an App, reads no match: its gate checks the
// policy's default.
func TestSecureOutsideAnApp(t *testing.T) {
	gate := Secure(Policy{Default: []Scheme{Bearer}, Verifiers: map[string]Verifier{
		"bearer": func(r *http.Request) (context.Context, error) { return nil, ErrUnauthenticated },
	}})
	h := gate.fn(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("%d, want 401", rec.Code)
	}
	if _, ok := matchedFor(httptest.NewRequest(http.MethodGet, "/x", nil)); ok {
		t.Fatal("a request outside an App matched")
	}
}
