package getavet

import (
	"strings"
	"testing"
)

// A handler whose input embeds geta.Conditional (or RequireConditional, or
// RequireAbsent) and that leaves the input unnamed, names it _, or never
// uses it calls neither Check nor CheckAbsent: geta answers each 2xx it
// returns with a 500, and getavet reports it, for a function literal and a
// function or method of the package. A handler that uses its input, even
// only to pass it on, is left to geta. RequireAbsent under Get or Query is
// refused, as geta.New refuses it.
func TestUnusedConditionalAndRequireAbsentAreReported(t *testing.T) {
	out, err := vetModule(t, map[string]string{
		"lib/lib.go": `package lib

import (
	"context"
	"net/http"
	"time"

	"github.com/koji-1009/geta"
)

type Out struct {
	OK bool ` + "`json:\"ok\"`" + `
}

type CondIn struct{ geta.Conditional }

type ReqIn struct{ geta.RequireConditional }

type NewIn struct{ geta.RequireAbsent }

type Handler struct{}

func (Handler) Unused(ctx context.Context, in *ReqIn) error { return nil }

func (Handler) Used(ctx context.Context, in *ReqIn) error { return check(in) }

func check(in *ReqIn) error { return in.Check("\"v\"", time.Time{}) }

func unnamed(context.Context, *CondIn) (*Out, error) { return &Out{}, nil }

func Build() (*geta.App, error) {
	h := Handler{}
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{
			Get: geta.Op(http.StatusOK, unnamed, geta.Doc{}),
			Put: geta.OpNoBody(http.StatusNoContent, h.Unused, geta.Doc{}),
			Delete: geta.OpNoBody(http.StatusNoContent, func(_ context.Context, _ *ReqIn) error { return nil }, geta.Doc{}),
		}},
		{Path: "/b", Route: geta.Route{
			Get: geta.Op(http.StatusOK, func(_ context.Context, in *CondIn) (*Out, error) {
				return &Out{}, in.Check("\"v\"", time.Time{})
			}, geta.Doc{}),
			Put: geta.OpNoBody(http.StatusNoContent, h.Used, geta.Doc{}),
			Post: geta.OpNoBody(http.StatusCreated, func(_ context.Context, in *NewIn) error { return in.CheckAbsent() }, geta.Doc{}),
		}},
		{Path: "/c", Route: geta.Route{
			Get: geta.Op(http.StatusOK, func(_ context.Context, in *NewIn) (*Out, error) { return &Out{}, in.CheckAbsent() }, geta.Doc{}),
		}},
	}})
}
`,
	})
	if err == nil {
		t.Fatalf("vet passed:\n%s", out)
	}
	unused := "the input embeds geta.Conditional, but the handler never uses it, so it calls neither Check nor CheckAbsent"
	if n := strings.Count(out, unused); n != 3 {
		t.Errorf("%d unused inputs reported, want 3:\n%s", n, out)
	}
	if !strings.Contains(out, "the input embeds geta.RequireAbsent, which requires If-None-Match: * of a request that creates its target, but GET is safe") {
		t.Errorf("RequireAbsent on a GET is not reported:\n%s", out)
	}
	if n := strings.Count(out, "lib/lib.go:"); n != 4 {
		t.Errorf("%d diagnostics, want 4:\n%s", n, out)
	}
}
