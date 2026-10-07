package getavet

import "testing"

// A geta.Deferred is refused where geta.New refuses it, with its text: as a
// parameter, a form body, a pointer, or embedded. As a body:"json" field,
// its value type is checked as the body's; sound ones, required or
// optional, pass.
func TestDeferredMatchesGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Out struct {
	OK bool `+"`json:\"ok\"`"+`
}

type Item struct {
	Name string `+"`json:\"name\" schema:\"minLength=1\"`"+`
}

type Bad struct {
	N int `+"`json:\"n\" schema:\"minLength=1\"`"+`
}

type QueryIn struct {
	Q geta.Deferred[string] `+"`query:\"q\"`"+`
}

type FormIn struct {
	Body geta.Deferred[Item] `+"`body:\"form\"`"+`
}

type PointerIn struct {
	Body *geta.Deferred[Item] `+"`body:\"json\"`"+`
}

type EmbeddedIn struct {
	geta.Deferred[Item]
}

type BadIn struct {
	Body geta.Deferred[Bad] `+"`body:\"json\"`"+`
}

type SoundIn struct {
	geta.Conditional
	Body geta.Deferred[Item] `+"`body:\"json\" doc:\"the item\"`"+`
}

type OptionalIn struct {
	Body geta.Deferred[*Item] `+"`body:\"json\"`"+`
}

func h[In any](context.Context, *In) (*Out, error) { return nil, nil }

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Post: geta.Op(http.StatusOK, h[QueryIn], geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Post: geta.Op(http.StatusOK, h[FormIn], geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Post: geta.Op(http.StatusOK, h[PointerIn], geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Post: geta.Op(http.StatusOK, h[EmbeddedIn], geta.Doc{})}},
		{Path: "/e", Route: geta.Route{Post: geta.Op(http.StatusOK, h[BadIn], geta.Doc{})}},
		{Path: "/f", Route: geta.Route{Put: geta.Op(http.StatusOK, h[SoundIn], geta.Doc{})}},
		{Path: "/g", Route: geta.Route{Post: geta.Op(http.StatusOK, h[OptionalIn], geta.Doc{})}},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		`Q: a geta.Deferred is a body:"json" field`,
		`Body: a geta.Deferred is a body:"json" field`,
		`Body: the body is a pointer to a geta.Deferred; for an optional body, the Deferred holds a pointer`,
		`Deferred: a geta.Deferred is a body:"json" field`,
		`N: schema keyword minLength applies to`,
	})
}
