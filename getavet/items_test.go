package getavet

import "testing"

// A slice's items. keywords are judged as the element's own, with geta.New's
// text: on a query, a header (whose elements are a list's), a form field, and
// a JSON member, a request's and a response's; one where there is no array,
// one the element does not take, an annotation, and an element's enum member a
// header list cannot carry are reported, and sound ones are not: nested
// slices' (items.items.), and a type with JSON methods of its own, whose
// declared schema geta.New alone reads.
func TestElementKeywordsMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Out struct {
	OK bool `+"`json:\"ok\"`"+`
}

type Sound struct {
	Q    []string     `+"`query:\"q\" schema:\"maxItems=2,items.enum=a|b\"`"+`
	H    *[]string    `+"`header:\"X-H\" schema:\"items.enum=a|b c\"`"+`
	N    []geta.Date  `+"`query:\"n\" schema:\"items.enum=2026-01-01\"`"+`
	Body struct {
		Days []int8 `+"`form:\"day\" schema:\"items.minimum=1\"`"+`
	} `+"`body:\"form\"`"+`
}

type OnAString struct {
	S string `+"`query:\"s\" schema:\"items.enum=a\"`"+`
}

type WrongType struct {
	S []int `+"`query:\"s\" schema:\"items.minLength=1\"`"+`
}

type Annotation struct {
	S []string `+"`query:\"s\" schema:\"items.default=a\"`"+`
}

type Comma struct {
	H []string `+"`header:\"X-H\" schema:\"items.enum=a,b|c\"`"+`
}

type Member struct {
	Tags []string `+"`json:\"tags\" schema:\"items.maxLength=x\"`"+`
}

type MemberIn struct {
	Body Member `+"`body:\"json\"`"+`
}

// Money reads itself; WithSchema declares it a string, which takes minLength.
type Money struct{ v string }

func (m Money) MarshalJSON() ([]byte, error)  { return []byte(m.v), nil }
func (m *Money) UnmarshalJSON(b []byte) error { m.v = string(b); return nil }

type Shapes struct {
	Nested [][]string `+"`json:\"nested\" schema:\"items.items.enum=a|b\"`"+`
	Anon   []struct {
		A int `+"`json:\"a\"`"+`
	} `+"`json:\"anon\" schema:\"items.minLength=1\"`"+`
	Prices []Money `+"`json:\"prices\" schema:\"items.minLength=1\"`"+`
}

type Upload struct {
	Body struct {
		Files []geta.File `+"`form:\"files\" schema:\"items.maxLength=1\"`"+`
	} `+"`body:\"multipart\"`"+`
}

type Pointers struct {
	Ptrs []*string `+"`json:\"ptrs\" schema:\"items.minLength=1\"`"+`
}

type Written struct {
	Tags  []string `+"`json:\"tags\" schema:\"items.enum=x|y\"`"+`
	Grid  [][]int8 `+"`json:\"grid\" schema:\"items.maxItems=1,items.items.minimum=1\"`"+`
	Dates []geta.Date `+"`json:\"dates\" schema:\"items.enum=2026-02-30\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *Sound) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *OnAString) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *WrongType) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *Annotation) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/e", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *Comma) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/f", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *MemberIn) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/g", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Written, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/h", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Shapes, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/i", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *Upload) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/j", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Pointers, error) { return nil, nil }, geta.Doc{})}},
	}}, geta.WithSchema[Money]("string", ""))
}
`)
	checkSame(t, diagnostics, built, []string{
		"S: schema keyword items.enum applies to the elements of an array, not string",
		"S: items: schema keyword minLength applies to string, not integer",
		"S: schema keyword items.default: an element takes no default",
		`H: header parameter "X-H": enum member "a,b" is not a valid header list element`,
		`Tags: items: schema keyword maxLength: "x" is not a non-negative integer`,
		`Dates: items: enum member "2026-02-30" is not a valid date`,
		// An unnamed struct's elements are an object written in place.
		"Anon: items: schema keyword minLength applies to string, not object",
		// A file takes no keyword, as an element too.
		`Files: items: schema tag "maxLength=1" on a geta.File`,
		// A pointer element is refused for itself; its keyword is not judged.
		"Ptrs: []*string: element type *string is a pointer",
	})
}
