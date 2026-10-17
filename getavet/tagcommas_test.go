package getavet

import "testing"

// A schema tag's comma followed by nothing (a trailing or doubled comma), or
// by white space and then a keyword, is reported with geta.New's text, on a
// parameter, a header, and a JSON member alike; a comma a value holds as
// written ({8,64}, an enum member, a default, \x2c) is not.
func TestTagCommasMatchGetaNew(t *testing.T) {
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
	Key   string            `+"`json:\"key\" schema:\"pattern=^\\\"[A-Za-z0-9._:-]{8,64}\\\"$,maxLength=66\"`"+`
	Kind  string            `+"`json:\"kind\" schema:\"enum=a,b|c\"`"+`
	Note  string            `+"`json:\"note\" schema:\"default=a,b\"`"+`
	Pair  string            `+"`json:\"pair\" schema:\"pattern=^a\\\\x2c b=1$\"`"+`
	Class string            `+"`json:\"class\" schema:\"pattern=^a[,]b=1$\"`"+`
	Spans map[string]string `+"`json:\"spans\" schema:\"propertyNames.pattern=^a{1,3}$,additionalProperties.pattern=^b{1,3}$\"`"+`
}

type SoundIn struct {
	geta.Conditional
	Body Sound `+"`body:\"json\"`"+`
}

type Spaced struct {
	Spaced string `+"`json:\"spaced\" schema:\"pattern=^a$, maxLength=3\"`"+`
}

type SpacedKeys struct {
	Keys map[string]int `+"`json:\"keys\" schema:\"propertyNames.pattern=^a$, propertyNames.maxLength=3\"`"+`
}

type Params struct {
	Trailing string `+"`query:\"t\" schema:\"pattern=^a$,\"`"+`
}

type Headers struct {
	Doubled *string `+"`header:\"X-D\" schema:\"minLength=1,,maxLength=2\"`"+`
}

func write[In, Result any](context.Context, *In) (*Result, error) { return nil, nil }

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Post: geta.Op(http.StatusOK, write[SoundIn, Sound], geta.Doc{})}},
		{Path: "/s", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Spaced, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/k", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*SpacedKeys, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/p", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *Params) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/h", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *Headers) (*Out, error) { return nil, nil }, geta.Doc{})}},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		`Spaced: schema tag "pattern=^a$, maxLength=3" has white space before "maxLength=3" after a comma in the value of pattern`,
		`Keys: schema tag "propertyNames.pattern=^a$, propertyNames.maxLength=3" has white space before "propertyNames.maxLength=3" after a comma in the value of propertyNames.pattern`,
		`Trailing: schema tag "pattern=^a$," has an empty entry (a trailing or doubled comma) after pattern`,
		`Doubled: schema tag "minLength=1,,maxLength=2" has an empty entry (a trailing or doubled comma) after minLength`,
	})
}
