package getavet

import "testing"

// A slice of a named byte type is an array, not base64, to getavet as to
// geta.New: a string keyword on it is refused, and its elements are judged.
func TestANamedByteSliceIsAnArray(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Out struct {
	OK bool `+"`json:\"ok\"`"+`
}

type B byte

type Body struct {
	Data []B    `+"`json:\"data\" schema:\"maxLength=3\"`"+`
	Raw  []byte `+"`json:\"raw\" schema:\"maxLength=3\"`"+`
}

type In struct {
	Body Body `+"`body:\"json\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{{Path: "/x", Route: geta.Route{
		Post: geta.Op(http.StatusOK, func(context.Context, *In) (*Out, error) { return nil, nil }, geta.Doc{}),
	}}}})
}
`)
	checkSame(t, diagnostics, built, []string{"Data: schema keyword maxLength applies to string, not array"})
}

// The component name Problem is geta's own, refused by getavet as by
// geta.New wherever a type takes it.
func TestTheNameProblemIsRefused(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Out struct {
	OK bool `+"`json:\"ok\"`"+`
}

type GetaProblem struct {
	X string `+"`json:\"x\"`"+`
}

type In struct {
	Body GetaProblem `+"`body:\"json\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{{Path: "/x", Route: geta.Route{
		Post: geta.Op(http.StatusOK, func(context.Context, *In) (*Out, error) { return nil, nil }, geta.Doc{}),
	}}}})
}
`)
	checkSame(t, diagnostics, built, []string{`Body: type lib.GetaProblem: component name "GetaProblem" is reserved for geta's problem schema; rename the type`})
}
