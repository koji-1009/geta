package getavet

import "testing"

// A default, an example, and a doc tag geta.New refuses getavet reports, with
// its text: a default on a pointer, a path parameter, or a body, one or an
// example the schema refuses or the place cannot carry, a Nullable's
// default, and a doc tag on an embedded struct or a cookie. Sound ones pass.
func TestDefaultsAndDocsMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Out struct {
	OK bool `+"`json:\"ok\"`"+`
}

type PointerParam struct {
	N *int `+"`query:\"n\" schema:\"default=3\"`"+`
}

type PathParam struct {
	ID string `+"`path:\"id\" schema:\"default=x\"`"+`
}

type PointerMember struct {
	N *int `+"`json:\"n,omitzero\" schema:\"default=3\"`"+`
}

type PointerMemberIn struct {
	Body PointerMember `+"`body:\"json\"`"+`
}

type BodyDefault struct {
	Body string `+"`body:\"json\" schema:\"default=x\"`"+`
}

type BelowMinimum struct {
	N int `+"`query:\"n\" schema:\"minimum=1,default=0\"`"+`
}

type NotCarried struct {
	H string `+"`header:\"X-H\" schema:\"examples= x\"`"+`
}

type NullDefault struct {
	N geta.Nullable[int] `+"`json:\"n\" schema:\"default=1\"`"+`
}

type Paging struct {
	Limit int `+"`query:\"limit\" schema:\"default=20,maximum=100\" doc:\"How many\"`"+`
}

type EmbeddedDoc struct {
	Paging `+"`doc:\"paging\"`"+`
}

type CookieDoc struct {
	C    *http.Cookie `+"`cookie:\"c\" doc:\"a cookie\"`"+`
	Body Out          `+"`body:\"json\"`"+`
}

type Sound struct {
	Paging
	Sort string `+"`query:\"sort\" schema:\"enum=asc|desc,default=asc,deprecated=true,examples=desc\"`"+`
	Body Out    `+"`body:\"json\" doc:\"the body\"`"+`
}

type SoundOut struct {
	N    int `+"`header:\"X-N\" doc:\"a count\"`"+`
	Body Out `+"`body:\"json\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *PointerParam) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/b/{id}", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *PathParam) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *PointerMemberIn) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *BodyDefault) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/e", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *BelowMinimum) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/f", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *NotCarried) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/g", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*NullDefault, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/h", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *EmbeddedDoc) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/i", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*CookieDoc, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/j", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *Sound) (*SoundOut, error) { return nil, nil }, geta.Doc{})}},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		"N: a pointer field takes no default; use int",
		`ID: path parameter "id" takes no default`,
		"N: a pointer field takes no default; use int", // the member's
		"Body: a body takes no default",
		`N: default "0": 0 is less than minimum 1`,
		`H: example " x": " x" is not a valid header field value`,
		"N: schema keyword default: a geta.Nullable takes no default",
		"Paging: an embedded struct takes no doc tag",
		"C: a cookie field takes no doc tag",
	})
}
