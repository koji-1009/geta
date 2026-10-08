package getavet

import (
	"strings"
	"testing"
)

// A PUT, PATCH, or DELETE a geta.Route literal builds in place, whose input
// embeds no geta.Conditional, beside a Get built in place that declares a
// validator through its types: getavet reports what geta.New refuses, with
// its text. Writes embedding Conditional or RequireConditional, POST and
// QUERY, and writes beside a GET that declares none or with no GET pass.
func TestConditionalWritesMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Item struct {
	Name string `+"`json:\"name\"`"+`
}

type ItemIn struct {
	ID string `+"`path:\"id\"`"+`
}

type WriteIn struct {
	geta.Conditional
	ItemIn
}

type RequireIn struct {
	geta.RequireConditional
	ItemIn
}

type DeepIn struct{ WriteIn }

type Tagged struct {
	ETag string `+"`header:\"ETag\"`"+`
	Body Item   `+"`body:\"json\"`"+`
}

type Headers struct {
	Modified string `+"`header:\"last-modified\"`"+`
}

type Stamped struct {
	Headers
	Body Item `+"`body:\"json\"`"+`
}

func h[In any](context.Context, *In) error { return nil }

func read[In, Out any](context.Context, *In) (*Out, error) { return new(Out), nil }

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a/{id}", Route: geta.Route{
			Get:    geta.Op(http.StatusOK, read[ItemIn, Tagged], geta.Doc{}),
			Delete: geta.OpNoBody(http.StatusNoContent, h[ItemIn], geta.Doc{}),
		}},
		{Path: "/b/{id}", Route: geta.Route{
			Get:    geta.Op(http.StatusOK, read[ItemIn, Stamped], geta.Doc{}),
			Post:   geta.OpNoBody(http.StatusNoContent, h[ItemIn], geta.Doc{}),
			Put:    geta.OpNoBody(http.StatusNoContent, h[WriteIn], geta.Doc{}),
			Patch:  geta.OpNoBody(http.StatusNoContent, h[RequireIn], geta.Doc{}),
			Delete: geta.OpNoBody(http.StatusNoContent, h[ItemIn], geta.Doc{}),
		}},
		{Path: "/c/{id}", Route: geta.Route{
			Get:    geta.Op(http.StatusOK, read[WriteIn, Item], geta.Doc{}),
			Put:    geta.OpNoBody(http.StatusNoContent, h[ItemIn], geta.Doc{}),
			Delete: geta.OpNoBody(http.StatusNoContent, h[DeepIn], geta.Doc{}),
			Query:  geta.OpNoBody(http.StatusNoContent, h[ItemIn], geta.Doc{}),
		}},
		{Path: "/d/{id}", Route: geta.Route{
			Get:    geta.Op(http.StatusOK, read[ItemIn, Item], geta.Doc{}),
			Put:    geta.Op(http.StatusOK, read[ItemIn, Tagged], geta.Doc{}),
			Delete: geta.OpNoBody(http.StatusNoContent, h[ItemIn], geta.Doc{}),
		}},
		{Path: "/e/{id}", Route: geta.Route{
			Delete: geta.OpNoBody(http.StatusNoContent, h[ItemIn], geta.Doc{}),
		}},
	}}, geta.WithOpenAPI(geta.OpenAPI32))
}
`)
	tail := "but the input embeds neither geta.Conditional nor geta.RequireConditional, " +
		"so geta would evaluate its preconditions against none; embed one and call Check with the current validators"
	checkSame(t, diagnostics, built, []string{
		"the route's GET declares a validator (its output's ETag header field), " + tail,
		"the route's GET declares a validator (its output's last-modified header field), " + tail,
		"the route's GET declares a validator (its input embeds geta.Conditional), " + tail,
	})
	for _, op := range []string{"DELETE /a/{id}", "DELETE /b/{id}", "PUT /c/{id}"} {
		if !strings.Contains(built, op+" (") {
			t.Errorf("geta.New does not name %s:\n%s", op, built)
		}
	}
	if strings.Contains(built, "POST /b/{id}") || strings.Contains(built, "QUERY /c/{id}") {
		t.Errorf("geta.New names a POST or a QUERY:\n%s", built)
	}
}

// What the source does not decide is left to geta.New: a Get or a write
// placed by name, and a GET that geta.ETag tags, which its chain decides.
func TestConditionalWritesGetaNewAloneDecides(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Item struct {
	Name string `+"`json:\"name\"`"+`
}

type ItemIn struct {
	ID string `+"`path:\"id\"`"+`
}

type Tagged struct {
	ETag string `+"`header:\"ETag\"`"+`
	Body Item   `+"`body:\"json\"`"+`
}

func h[In any](context.Context, *In) error { return nil }

func read[In, Out any](context.Context, *In) (*Out, error) { return new(Out), nil }

var tagged = geta.Op(http.StatusOK, read[ItemIn, Tagged], geta.Doc{})

var del = geta.OpNoBody(http.StatusNoContent, h[ItemIn], geta.Doc{})

// Out is the one mistake getavet names, so that vet fails.
type Out struct {
	D int `+"`json:\"d\" schema:\"minimum=x\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Root: geta.Scope{geta.ETag()}, Routes: []geta.Entry{
		{Path: "/a/{id}", Route: geta.Route{
			Get:    tagged,
			Delete: geta.OpNoBody(http.StatusNoContent, h[ItemIn], geta.Doc{}),
		}},
		{Path: "/b/{id}", Route: geta.Route{
			Get:    geta.Op(http.StatusOK, read[ItemIn, Tagged], geta.Doc{}),
			Delete: del,
		}},
		{Path: "/c/{id}", Route: geta.Route{
			Get:    geta.Op(http.StatusOK, read[ItemIn, Item], geta.Doc{}),
			Delete: geta.OpNoBody(http.StatusNoContent, h[ItemIn], geta.Doc{}),
		}},
		{Path: "/o", Route: geta.Route{Get: geta.Op(http.StatusOK, read[struct{}, Out], geta.Doc{})}},
	}})
}
`)
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0], "minimum") {
		t.Errorf("getavet reports:\n%s", strings.Join(diagnostics, "\n"))
	}
	for _, op := range []string{"DELETE /a/{id}", "DELETE /b/{id}", "DELETE /c/{id}"} {
		if !strings.Contains(built, op+" (") {
			t.Errorf("geta.New does not name %s:\n%s", op, built)
		}
	}
}
