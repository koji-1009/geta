package geta_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/koji-1009/geta"
)

// A root middleware that answers an unmatched request itself, in place of
// geta's 404 and 405, tells them apart with geta.Allowed: a URL served under
// other methods than the request's is a 405 listing them in Allow (RFC 9110
// §15.5.6 "MUST generate an Allow header field"), as geta's own; any other
// is a 404. Matched reports neither.
func TestAllowedTellsA405FromA404(t *testing.T) {
	type seen struct {
		matched, allowed bool
		methods          []string
	}
	var got seen
	own := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, matched := geta.Matched(r.Context())
			methods, allowed := geta.Allowed(r.Context())
			got = seen{matched, allowed, methods}
			if !matched {
				if allowed {
					w.Header().Set("Allow", strings.Join(methods, ", "))
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				w.WriteHeader(http.StatusNotFound)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	tbl := geta.Table{Routes: []geta.Entry{
		{Path: "/items", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{}), Post: geta.Op(http.StatusCreated, writeHandler, geta.Doc{})}},
		{Path: "/items/{id}", Route: geta.Route{Delete: geta.OpNoBody(http.StatusNoContent, func(_ context.Context, in *struct {
			geta.Conditional
			ID string `path:"id"`
		}) error {
			return in.Check("", time.Time{})
		}, geta.Doc{})}},
	}}
	bare := accepts(t, tbl)
	tbl.Root = geta.Scope{own}
	a := accepts(t, tbl)
	for _, c := range []struct {
		method, path string
		want         int
		allow        string
	}{
		{"PUT", "/items", 405, "GET, HEAD, OPTIONS, POST"},
		{"GET", "/items/1", 405, "DELETE, OPTIONS"},
		{"GET", "/nowhere", 404, ""},
		{"GET", "/items/1/more", 404, ""},
	} {
		// The middleware answers as geta would.
		want := do(t, bare, c.method, c.path)
		rec := do(t, a, c.method, c.path)
		if rec.Code != c.want || want.Code != c.want || rec.Header().Get("Allow") != c.allow || want.Header().Get("Allow") != c.allow {
			t.Errorf("%s %s: %d %q, geta's %d %q, want %d %q", c.method, c.path, rec.Code, rec.Header().Get("Allow"),
				want.Code, want.Header().Get("Allow"), c.want, c.allow)
		}
		if got.matched || got.allowed != (c.want == 405) {
			t.Errorf("%s %s: %+v", c.method, c.path, got)
		}
	}
	// A request that matches reports no methods.
	if rec := do(t, a, "GET", "/items"); rec.Code != 200 || !got.matched || got.allowed || got.methods != nil {
		t.Errorf("a match: %d %+v", rec.Code, got)
	}
	// Outside an App there is nothing to report.
	if methods, ok := geta.Allowed(context.Background()); ok || methods != nil {
		t.Errorf("outside an App: %v %v", methods, ok)
	}
}
