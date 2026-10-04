package getavet

import (
	"strings"
	"testing"
)

// Rules of getavet that no other test asserts: a mistake reached twice from one call is reported once; an
// operation outside a route tree is not checked for its path parameters;
// the component name of a sealed type, which only geta.WithUnion declares,
// is left to geta.New.
func TestOnceOutsideTheTreeAndSealedNamesLeft(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Body struct {
	Name string `+"`json:\"name\" schema:\"maxLength=abc\"`"+`
}

type In struct {
	Body Body `+"`body:\"json\"`"+`
}

type ByID struct {
	ID string `+"`path:\"id\"`"+`
}

type 形 interface{ is形() }

type Dot struct {
	Kind string `+"`json:\"kind\"`"+`
}

func (Dot) is形() {}

type Holder struct {
	S 形 `+"`json:\"s\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/x", Route: geta.Route{Put: geta.Op(http.StatusOK, func(context.Context, *In) (*Body, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/y", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *ByID) (*Dot, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/s", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Holder, error) { return nil, nil }, geta.Doc{})}},
	}}, geta.WithUnion(geta.Sealed[形]("kind", geta.Case[Dot]("dot"))))
}
`)
	checkSame(t, diagnostics, built, []string{
		`Name: schema keyword maxLength: "abc" is not a non-negative integer`,
	})
	for _, want := range []string{
		`input binds path parameter "id" not in the URL`,
		`type lib.形: component name "形"`,
	} {
		if !strings.Contains(built, want) {
			t.Errorf("geta.New does not say %q:\n%s", want, built)
		}
	}
}
