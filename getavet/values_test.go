package getavet

import "testing"

// A map's additionalProperties. keywords are judged as the value's own, with
// geta.New's text, on a request's and a response's JSON member: one where
// there is no map, one the value does not take, an annotation, a value's enum
// member its type refuses, and one past the pattern ceiling where a request
// reads it are reported, and sound ones are not: nested maps' and slices'
// (additionalProperties.items., items.additionalProperties.), and a value
// with JSON methods of its own, whose declared schema geta.New alone reads.
func TestValueKeywordsMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Out struct {
	OK bool `+"`json:\"ok\"`"+`
}

// Money reads itself; WithSchema declares it a string, which takes minLength.
type Money struct{ v string }

func (m Money) MarshalJSON() ([]byte, error)  { return []byte(m.v), nil }
func (m *Money) UnmarshalJSON(b []byte) error { m.v = string(b); return nil }

type Sound struct {
	Labels map[string]string            `+"`json:\"labels\" schema:\"maxProperties=2,additionalProperties.enum=a|b\"`"+`
	Grid   map[string][]int8            `+"`json:\"grid\" schema:\"additionalProperties.maxItems=2,additionalProperties.items.minimum=0\"`"+`
	Rows   []map[string]string          `+"`json:\"rows\" schema:\"items.additionalProperties.maxLength=3\"`"+`
	Nested map[string]map[string]int8   `+"`json:\"nested\" schema:\"additionalProperties.additionalProperties.maximum=9\"`"+`
	Dates  map[string]geta.Date         `+"`json:\"dates\" schema:\"additionalProperties.enum=2026-01-01\"`"+`
	Prices map[string]Money             `+"`json:\"prices\" schema:\"additionalProperties.minLength=1\"`"+`
}

type SoundIn struct {
	Body Sound `+"`body:\"json\"`"+`
}

type M1 struct {
	OnAString string `+"`json:\"s\" schema:\"additionalProperties.enum=a\"`"+`
}

type M2 struct {
	OnASlice []string `+"`json:\"l\" schema:\"additionalProperties.enum=a\"`"+`
}

type M3 struct {
	WrongType map[string]int `+"`json:\"w\" schema:\"additionalProperties.minLength=1\"`"+`
}

type M4 struct {
	Annotation map[string]string `+"`json:\"a\" schema:\"additionalProperties.default=a\"`"+`
}

type M5 struct {
	Anon map[string]struct {
		A int `+"`json:\"a\"`"+`
	} `+"`json:\"anon\" schema:\"additionalProperties.minLength=1\"`"+`
}

type M6 struct {
	Dates map[string]geta.Date `+"`json:\"dates\" schema:\"additionalProperties.enum=2026-02-30\"`"+`
}

type Read struct {
	Ceiling map[string]string `+"`json:\"c\" schema:\"additionalProperties.pattern=^a+$,additionalProperties.maxLength=5000\"`"+`
}

type ReadIn struct {
	Body Read `+"`body:\"json\"`"+`
}

type Written struct {
	Ceiling map[string]string `+"`json:\"c\" schema:\"additionalProperties.pattern=^a+$,additionalProperties.maxLength=5000\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *SoundIn) (*Sound, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/m1", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*M1, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/m2", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*M2, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/m3", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*M3, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/m4", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*M4, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/m5", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*M5, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/m6", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*M6, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *ReadIn) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Written, error) { return nil, nil }, geta.Doc{})}},
	}}, geta.WithSchema[Money]("string", ""))
}
`)
	checkSame(t, diagnostics, built, []string{
		"OnAString: schema keyword additionalProperties.enum applies to the values of a map, not string",
		"OnASlice: schema keyword additionalProperties.enum applies to the values of a map, not array",
		"WrongType: additionalProperties: schema keyword minLength applies to string, not integer",
		"Annotation: schema keyword additionalProperties.default: a value takes no default",
		// An unnamed struct's values are an object written in place.
		"Anon: additionalProperties: schema keyword minLength applies to string, not object",
		`Dates: additionalProperties: enum member "2026-02-30" is not a valid date`,
		"Ceiling: additionalProperties: maxLength 5000 with a pattern exceeds the pattern ceiling",
	})
}
