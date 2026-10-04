package getavet

import (
	"strings"
	"testing"
)

// A body on an operation a geta.Route literal builds in place under Get or
// Delete: getavet reports what geta.New refuses, with its text, whatever the
// body's encoding and wherever the input embeds it. The same inputs under
// Post, Put, Patch, and Query, and parameters alone under Get and Delete,
// pass.
func TestMethodBodiesMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Item struct {
	Name string `+"`json:\"name\"`"+`
}

type Fields struct {
	Name string `+"`form:\"name\"`"+`
}

type JSONIn struct {
	Body Item `+"`body:\"json\"`"+`
}

type FormIn struct {
	Body Fields `+"`body:\"form\"`"+`
}

type RawIn struct {
	Body []byte `+"`body:\"text/csv\"`"+`
}

type EmbeddedIn struct{ JSONIn }

type ParamsIn struct {
	Q string `+"`query:\"q\"`"+`
}

func h[In any](context.Context, *In) error { return nil }

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.OpNoBody(http.StatusNoContent, h[JSONIn], geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Delete: geta.OpNoBody(http.StatusNoContent, h[FormIn], geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Get: geta.OpNoBody(http.StatusNoContent, h[RawIn], geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Delete: geta.OpNoBody(http.StatusNoContent, h[EmbeddedIn], geta.Doc{})}},
		{Path: "/e", Route: geta.Route{
			Get:    geta.OpNoBody(http.StatusNoContent, h[ParamsIn], geta.Doc{}),
			Delete: geta.OpNoBody(http.StatusNoContent, h[ParamsIn], geta.Doc{}),
			Post:   geta.OpNoBody(http.StatusNoContent, h[JSONIn], geta.Doc{}),
			Put:    geta.OpNoBody(http.StatusNoContent, h[FormIn], geta.Doc{}),
			Patch:  geta.OpNoBody(http.StatusNoContent, h[RawIn], geta.Doc{}),
			Query:  geta.OpNoBody(http.StatusNoContent, h[EmbeddedIn], geta.Doc{}),
		}},
	}}, geta.WithOpenAPI(geta.OpenAPI32))
}
`)
	get := `the input has a body, which a GET request does not take; use Query or Post`
	del := `the input has a body, which a DELETE request does not take; use Post`
	checkSame(t, diagnostics, built, []string{get, del, get, del})
}

// An operation a geta.Route literal builds in place is checked whether its
// type arguments are given (geta.Op[In, Out]) and whether the literal is
// keyed or unkeyed; one built elsewhere and placed by name is left to
// geta.New.
func TestMethodBodiesInEveryLiteralForm(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Item struct {
	Name string `+"`json:\"name\"`"+`
}

type JSONIn struct {
	Body Item `+"`body:\"json\"`"+`
}

func h(context.Context, *JSONIn) (*Item, error) { return nil, nil }

var named = geta.Op(http.StatusOK, h, geta.Doc{})

var none geta.Operation

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.Op[JSONIn, Item](http.StatusOK, h, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{none, none, none, none, geta.Op(http.StatusOK, h, geta.Doc{}), none}},
		{Path: "/c", Route: geta.Route{Get: named}},
	}})
}
`)
	get := `the input has a body, which a GET request does not take`
	del := `the input has a body, which a DELETE request does not take`
	checkSame(t, diagnostics, built, []string{get, del})
	if n := strings.Count(built, get); n != 2 {
		t.Errorf("geta.New names %d GET bodies, want 2 (the one placed by name among them):\n%s", n, built)
	}
}
