package geta_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
)

// Recover: panics only.

// The panic's log line records the operation's template, never the raw path.
func TestRecoverLogsTheTemplateNeverThePath(t *testing.T) {
	log, buf := logger()
	boom := func(context.Context, *idIn) (*ok, error) { panic("kaboom") }
	tbl := geta.Table{
		Root:   geta.Scope{geta.Recover(log)},
		Routes: []geta.Entry{{Path: "/items/{id}", Route: get(boom)}},
	}
	a := accepts(t, tbl)
	if rec := do(t, a, "GET", "/items/424242"); rec.Code != 500 {
		t.Fatal(rec.Code)
	}
	if l := buf.String(); !strings.Contains(l, "route=/items/{id}") || strings.Contains(l, "path=") || strings.Contains(l, "424242") {
		t.Fatalf("log: %s", l)
	}
}

// The panic's log line pairs the method served with the route served, after
// a root middleware changed the method.
func TestRecoverLogsTheMethodServed(t *testing.T) {
	log, buf := logger()
	boom := func(context.Context, *empty) error { panic("kaboom") }
	a := accepts(t, geta.Table{
		Root:   geta.Scope{geta.Recover(log), methodOverride},
		Routes: []geta.Entry{{Path: "/items", Route: geta.Route{Delete: geta.OpNoBody(204, boom, geta.Doc{})}}},
	})
	if rec := do(t, a, "POST", "/items", "X-HTTP-Method-Override", "DELETE"); rec.Code != 500 {
		t.Fatal(rec.Code)
	}
	if l := buf.String(); !strings.Contains(l, "method=DELETE route=/items") {
		t.Fatalf("log: %s", l)
	}
}

// A response Gzip or ETag held when a panic struck is lost, and so is what
// it set: the 500 Recover writes carries none of its headers or cookies.
func TestAPanicBehindABufferLeavesNoOutputHeaders(t *testing.T) {
	type out struct {
		Session *http.Cookie `cookie:"session"`
		Note    string       `header:"X-Note"`
		Body    ok           `body:"json"`
	}
	h := func(context.Context, *empty) (*out, error) {
		return &out{Session: &http.Cookie{Value: "s1"}, Note: "n", Body: ok{true}}, nil
	}
	boom := geta.Ordered(geta.Order{Rank: geta.OrderValidate.Rank + 500, Name: "boom"}, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { next.ServeHTTP(w, r); panic("after") })
	})
	for name, buffering := range map[string]geta.Middleware{"Gzip": geta.Gzip(), "ETag": geta.ETag()} {
		a := accepts(t, withRoot(one("/x", get(h)), geta.Recover(quietLogger()), buffering, boom))
		rec := do(t, a, "GET", "/x", "Accept-Encoding", "gzip")
		if rec.Code != 500 || len(rec.Header().Values("Set-Cookie")) > 0 || rec.Header().Get("X-Note") != "" {
			t.Errorf("%s: %d %v", name, rec.Code, rec.Header())
		}
	}
}

func TestRecoverTurnsPanicsInto500(t *testing.T) {
	log, buf := logger()
	must := func(ctx context.Context, _ *empty) (*text, error) { return &text{principal.Must(ctx)}, nil }
	boom := func(ctx context.Context, _ *empty) (*ok, error) { panic("kaboom") }
	tbl := geta.Table{
		Root:   geta.Scope{geta.Recover(log)},
		Routes: []geta.Entry{{Path: "/must", Route: get(must)}, {Path: "/boom", Route: get(boom)}},
	}
	a := accepts(t, tbl)
	for _, p := range []string{"/must", "/boom"} {
		rec := do(t, a, "GET", p)
		if rec.Code != 500 || rec.Header().Get("Content-Type") != geta.ProblemContentType || strings.Contains(rec.Body.String(), "kaboom") {
			t.Fatalf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}
	if !strings.Contains(buf.String(), "kaboom") || !strings.Contains(buf.String(), `no value for key \"principal\"`) || !strings.Contains(buf.String(), "goroutine") {
		t.Fatalf("log: %s", buf.String())
	}
}
