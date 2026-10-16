package getavet

import "testing"

// getavet refuses what geta.New refuses of a choice among a raw body's media
// types, with its text: a body listing several without a Content-Type field,
// a Content-Type field beside a body listing one or a JSON body, in a
// problem's envelope too, a Content-Type field that is no string, a listed
// type twice; and passes a body listing several beside a string
// Content-Type field.
func TestMediaTypeChoiceMatchesGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/koji-1009/geta"
)

type Item struct {
	Name string `+"`json:\"name\"`"+`
}

type Download struct {
	Type string    `+"`header:\"Content-Type\"`"+`
	Body io.Reader `+"`body:\"image/png|application/pdf\"`"+`
}

type NoChoice struct {
	Body []byte `+"`body:\"image/png|application/pdf\"`"+`
}

type OneType struct {
	Type string `+"`header:\"Content-Type\"`"+`
	Body []byte `+"`body:\"image/png\"`"+`
}

type JSONBody struct {
	Type string `+"`header:\"Content-Type\"`"+`
	Body Item   `+"`body:\"json\"`"+`
}

type Number struct {
	Type int    `+"`header:\"Content-Type\"`"+`
	Body []byte `+"`body:\"image/png\"`"+`
}

type Twice struct {
	Type string `+"`header:\"Content-Type\"`"+`
	Body []byte `+"`body:\"image/png|image/png\"`"+`
}

type Described struct {
	Type string `+"`header:\"Content-Type\"`"+`
	Body Item   `+"`body:\"json\"`"+`
}

func h[Out any](context.Context, *struct{}) (*Out, error) { return nil, nil }

func Build() (*geta.App, error) {
	conflict := geta.OnAsProblem(http.StatusConflict, "conflict", func(error) Described { return Described{} })
	_ = errors.New
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.Op(http.StatusOK, h[Download], geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, h[NoChoice], geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Get: geta.Op(http.StatusOK, h[OneType], geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Get: geta.Op(http.StatusOK, h[JSONBody], geta.Doc{})}},
		{Path: "/e", Route: geta.Route{Get: geta.Op(http.StatusOK, h[Number], geta.Doc{})}},
		{Path: "/f", Route: geta.Route{Get: geta.Op(http.StatusOK, h[Twice], geta.Doc{})}},
		{Path: "/g", Route: geta.Route{Get: geta.Op(http.StatusOK, h[Item], geta.Doc{Failures: []geta.Failure{conflict}})}},
	}})
}
`)
	reserved := `Type: header "Content-Type" is reserved for the body geta writes; it chooses among the media types a raw body field lists`
	checkSame(t, diagnostics, built, []string{
		`Body: the body lists several media types, but the envelope has no string field tagged header:"Content-Type"`,
		reserved,
		reserved,
		reserved,
		`Type: a Content-Type header field is a string, not int`,
		`Body: body tag "image/png|image/png" names "image/png" twice`,
	})
}
