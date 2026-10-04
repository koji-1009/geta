package geta_test

import (
	"context"
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
