package getavet

import "testing"

// A map's propertyNames. keywords are judged as a string's, with geta.New's
// text, on a request's and a response's JSON member: one where there is no
// map, one a string does not take, an annotation, one no key meets, and one
// past the pattern ceiling where a request reads it are reported, and sound
// ones are not: nested maps' and slices' (additionalProperties.propertyNames.,
// items.propertyNames.), and the keys of a map whose values have JSON methods
// of their own, which are strings whatever the values.
func TestKeyKeywordsMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Out struct {
	OK bool `+"`json:\"ok\"`"+`
}

// Money reads itself; WithSchema declares it a string.
type Money struct{ v string }

func (m Money) MarshalJSON() ([]byte, error)  { return []byte(m.v), nil }
func (m *Money) UnmarshalJSON(b []byte) error { m.v = string(b); return nil }

type Sound struct {
	Labels map[string]string          `+"`json:\"labels\" schema:\"maxProperties=2,propertyNames.enum=a|b\"`"+`
	Rows   []map[string]string        `+"`json:\"rows\" schema:\"items.propertyNames.maxLength=3\"`"+`
	Nested map[string]map[string]int8 `+"`json:\"nested\" schema:\"additionalProperties.propertyNames.pattern=^[a-z]+$\"`"+`
	IDs    map[string]int             `+"`json:\"ids\" schema:\"propertyNames.format=uuid,propertyNames.maxLength=36\"`"+`
	Prices map[string]Money           `+"`json:\"prices\" schema:\"propertyNames.minLength=1\"`"+`
}

type SoundIn struct {
	Body Sound `+"`body:\"json\"`"+`
}

type K1 struct {
	OnAString string `+"`json:\"s\" schema:\"propertyNames.maxLength=1\"`"+`
}

type K2 struct {
	OnASlice []string `+"`json:\"l\" schema:\"propertyNames.maxLength=1\"`"+`
}

type K3 struct {
	WrongType map[string]int `+"`json:\"w\" schema:\"propertyNames.minimum=1\"`"+`
}

type K4 struct {
	Annotation map[string]string `+"`json:\"a\" schema:\"propertyNames.default=a\"`"+`
}

type K5 struct {
	Empty map[string]string `+"`json:\"e\" schema:\"propertyNames.minLength=3,propertyNames.maxLength=2\"`"+`
}

type K6 struct {
	OwnValues map[string]Money `+"`json:\"o\" schema:\"propertyNames.minimum=1\"`"+`
}

type Read struct {
	Ceiling map[string]string `+"`json:\"c\" schema:\"propertyNames.pattern=^a+$,propertyNames.maxLength=5000\"`"+`
}

type ReadIn struct {
	Body Read `+"`body:\"json\"`"+`
}

type Written struct {
	Ceiling map[string]string `+"`json:\"c\" schema:\"propertyNames.pattern=^a+$,propertyNames.maxLength=5000\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *SoundIn) (*Sound, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/k1", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*K1, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/k2", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*K2, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/k3", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*K3, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/k4", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*K4, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/k5", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*K5, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/k6", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*K6, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *ReadIn) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Written, error) { return nil, nil }, geta.Doc{})}},
	}}, geta.WithSchema[Money]("string", ""))
}
`)
	checkSame(t, diagnostics, built, []string{
		"OnAString: schema keyword propertyNames.maxLength applies to the keys of a map, not string",
		"OnASlice: schema keyword propertyNames.maxLength applies to the keys of a map, not array",
		"WrongType: propertyNames: schema keyword minimum applies to integer or number, not string",
		"Annotation: schema keyword propertyNames.default: a key takes no default",
		"Empty: propertyNames: minLength 3 exceeds maxLength 2",
		"OwnValues: propertyNames: schema keyword minimum applies to integer or number, not string",
		"Ceiling: propertyNames: maxLength 5000 with a pattern exceeds the pattern ceiling",
	})
}
