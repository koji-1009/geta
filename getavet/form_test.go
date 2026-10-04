package getavet

import "testing"

// A form or multipart body's declarations are reported with geta.New's
// text: the body tag, the body field's type and schema tag, each form
// field's tag, type, and schema tag, and a geta.File outside a multipart
// body. A form getavet accepts, geta.New accepts.
func TestFormBodiesMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Inner struct {
	Note *string `+"`form:\"note\"`"+`
}

type Good struct {
	Inner
	Name  string       `+"`form:\"name\" schema:\"minLength=1\"`"+`
	Tags  *[]string    `+"`form:\"tag\" schema:\"maxItems=3\"`"+`
	Image geta.File    `+"`form:\"image\"`"+`
	More  []geta.File  `+"`form:\"more\" schema:\"maxItems=2\"`"+`
	Maybe *geta.File   `+"`form:\"maybe\"`"+`
}

type GoodIn struct {
	Body Good `+"`body:\"multipart\"`"+`
}

type XMLIn struct {
	Body Good `+"`body:\"xml\"`"+`
}

type NotStructIn struct {
	Body string `+"`body:\"form\"`"+`
}

type SchemaIn struct {
	Body Good `+"`body:\"multipart\" schema:\"minLength=1\"`"+`
}

type FileInForm struct {
	F geta.File `+"`form:\"f\"`"+`
}

type FileInFormIn struct {
	Body FileInForm `+"`body:\"form\"`"+`
}

type Untagged struct {
	F string
}

type UntaggedIn struct {
	Body Untagged `+"`body:\"form\"`"+`
}

type FileSchema struct {
	F geta.File `+"`form:\"f\" schema:\"maxLength=3\"`"+`
}

type FileSchemaIn struct {
	Body FileSchema `+"`body:\"multipart\"`"+`
}

type Unique struct {
	F []geta.File `+"`form:\"f\" schema:\"uniqueItems=true\"`"+`
}

type UniqueIn struct {
	Body Unique `+"`body:\"multipart\"`"+`
}

type MapField struct {
	M map[string]string `+"`form:\"m\"`"+`
}

type MapFieldIn struct {
	Body MapField `+"`body:\"form\"`"+`
}

type Twice struct {
	A string `+"`form:\"a\"`"+`
	B string `+"`form:\"a\"`"+`
}

type TwiceIn struct {
	Body Twice `+"`body:\"form\"`"+`
}

type FileQueryIn struct {
	F geta.File `+"`query:\"f\"`"+`
}

type FileJSON struct {
	F geta.File `+"`json:\"f\"`"+`
}

type FileJSONIn struct {
	Body FileJSON `+"`body:\"json\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/good", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *GoodIn) (*struct{}, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/xml", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *XMLIn) (*struct{}, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/notstruct", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *NotStructIn) (*struct{}, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/schema", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *SchemaIn) (*struct{}, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/fileinform", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *FileInFormIn) (*struct{}, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/untagged", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *UntaggedIn) (*struct{}, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/fileschema", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *FileSchemaIn) (*struct{}, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/unique", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *UniqueIn) (*struct{}, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/map", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *MapFieldIn) (*struct{}, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/twice", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *TwiceIn) (*struct{}, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/filequery", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *FileQueryIn) (*struct{}, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/filejson", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *FileJSONIn) (*struct{}, error) { return nil, nil }, geta.Doc{})}},
	}})
}
`)
	const file = `type geta.File outside a body:"multipart" form field`
	checkSame(t, diagnostics, built, []string{
		`Body: unknown body tag "xml"`,
		`Body: a body:"form" field has type string, not a struct`,
		`Body: a body:"multipart" field takes no schema tag`,
		`F: form field "f" has file type geta.File outside a multipart body`,
		`F has no form tag`,
		`F: schema tag "maxLength=3" on a geta.File`,
		`F: form field "f": uniqueItems on files`,
		`M: form field "m" has unsupported type map[string]string`,
		`B binds form field "a", already bound by A`,
		"F: " + file,
		"F: " + file,
	})
}
