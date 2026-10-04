package getavet

import "testing"

// A query parameter of a struct type is a deepObject, whose fields tagged
// form are its members: getavet reports what geta.New refuses of one, with
// its text (a member that is an array, a field with no form tag, a
// constraint on the object, a member's schema tag), and reads no JSON form
// of the struct. A sound one passes.
func TestDeepObjectsMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Out struct {
	OK bool `+"`json:\"ok\"`"+`
}

type WithSlice struct {
	Tags []string `+"`form:\"tags\"`"+`
}

type SliceIn struct {
	F WithSlice `+"`query:\"f\"`"+`
}

type Untagged struct {
	A string
}

type UntaggedIn struct {
	F *Untagged `+"`query:\"f\"`"+`
}

type Page struct {
	Size int `+"`form:\"size\" schema:\"default=20,maximum=100\"`"+`
}

type ConstrainedIn struct {
	F Page `+"`query:\"f\" schema:\"maxLength=3\"`"+`
}

type BadMember struct {
	Cursor *string `+"`form:\"cursor\" schema:\"maxLength=abc\"`"+`
}

type BadMemberIn struct {
	F BadMember `+"`query:\"f\"`"+`
}

type Sound struct {
	Min    *int   `+"`form:\"min\" schema:\"minimum=0\"`"+`
	Status string `+"`form:\"status\" schema:\"enum=open|closed,default=open\"`"+`
	hidden int
}

type SoundIn struct {
	F *Sound `+"`query:\"f\" doc:\"a filter\" schema:\"deprecated=true\"`"+`
}

type CollideIn struct {
	F   *Sound `+"`query:\"filter\"`"+`
	Min *int   `+"`query:\"filter[min]\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *SliceIn) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *UntaggedIn) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *ConstrainedIn) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *SoundIn) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/e", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *BadMemberIn) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/f", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *CollideIn) (*Out, error) { return nil, nil }, geta.Doc{})}},
	}})
}

var _ = Sound{}.hidden
`)
	checkSame(t, diagnostics, built, []string{
		`Tags: deepObject member "tags" has non-scalar type []string`,
		"A has no form tag",
		`F: schema tag "maxLength=3" on a struct type`,
		`Cursor: schema keyword maxLength: "abc" is not a non-negative integer`,
		`Min binds query parameter "filter[min]", which deepObject "filter" (F) also binds`,
	})
}
