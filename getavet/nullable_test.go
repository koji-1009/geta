package getavet

import "testing"

// A geta.Nullable is refused where geta.New refuses it, with its text: a
// pointer to one, one of another, one as a parameter, and a tag keyword its
// value's type does not take. One of another is refused for that alone: its
// tag's keywords, which no kind takes, are not judged. A sound one, optional
// by omitzero, passes.
func TestNullableMatchesGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Out struct {
	OK bool `+"`json:\"ok\"`"+`
}

type Pointer struct {
	N *geta.Nullable[int] `+"`json:\"n,omitzero\"`"+`
}

type PointerIn struct {
	Body Pointer `+"`body:\"json\"`"+`
}

type Nested struct {
	N geta.Nullable[geta.Nullable[int]] `+"`json:\"n\" schema:\"maxLength=3\"`"+`
}

type ParamIn struct {
	N geta.Nullable[int] `+"`query:\"n\"`"+`
}

type Keyword struct {
	S geta.Nullable[string] `+"`json:\"s\" schema:\"minimum=1\"`"+`
}

type Sound struct {
	S geta.Nullable[string] `+"`json:\"s,omitzero\" schema:\"maxLength=3,enum=a|b\"`"+`
	R geta.Nullable[Out]    `+"`json:\"r\"`"+`
}

type SoundIn struct {
	Body Sound `+"`body:\"json\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *PointerIn) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Nested, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *ParamIn) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Keyword, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/e", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *SoundIn) (*Sound, error) { return nil, nil }, geta.Doc{})}},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		"N is a pointer to geta.Nullable; use",
		"N: type geta.Nullable[github.com/koji-1009/geta.Nullable[int]] is a geta.Nullable of a geta.Nullable; use",
		`N: query parameter "n" has unsupported type geta.Nullable[int]`,
		"S: schema keyword minimum applies to integer or number, not string",
	})
}
