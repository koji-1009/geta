package getavet

import (
	"os/exec"
	"strings"
	"testing"
)

// An alias of geta.Stream is a stream, to getavet as to geta.New.
func TestAStreamAliasIsAStream(t *testing.T) {
	out, err := vetModule(t, map[string]string{"lib/lib.go": `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Event struct {
	N int ` + "`json:\"n\"`" + `
}

type Feed = geta.Stream[Event]

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/f", Route: geta.Route{Get: geta.Op[struct{}, Feed](http.StatusOK, func(context.Context, *struct{}) (*Feed, error) { return nil, nil }, geta.Doc{})}},
	}})
}
`})
	if err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// An operation whose types hold a type parameter is left to geta.New, which
// judges the instantiation: getavet reports nothing of it, in a route
// directory or not.
func TestWhatHoldsATypeParameterIsLeftToNew(t *testing.T) {
	out, err := vetModule(t, map[string]string{"lib/lib.go": `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Out struct {
	OK bool ` + "`json:\"ok\"`" + `
}

type Moved struct {
	Location string ` + "`header:\"Location\"`" + `
}

type Page[T any] struct {
	Cursor T ` + "`query:\"cursor\"`" + `
}

func moved[O any]() geta.Operation {
	return geta.Op(http.StatusFound, func(context.Context, *struct{}) (*O, error) { return nil, nil }, geta.Doc{})
}

func list[T any]() geta.Operation {
	return geta.Op(http.StatusOK, func(context.Context, *Page[T]) (*Out, error) { return nil, nil }, geta.Doc{})
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{{Path: "/x", Route: geta.Route{Get: moved[Moved](), Post: list[string]()}}}})
}
`,
		"routes/zz_routes.go": "package routes\n",
		"routes/users/id_/route.go": "package id_\n" + header + "\nfunc op[I any]() geta.Operation {\n\treturn geta.Op(http.StatusOK, func(context.Context, *I) (*Out, error) { return nil, nil }, geta.Doc{})\n}\n\n" +
			"type In struct{ ID string `path:\"id\"` }\n\nfunc Route(env *int) geta.Route { return geta.Route{Get: op[In]()} }\n"})
	if err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// A 204 or 205 for an output with a body, and a stream's or an upgrade's
// status other than its own, are reported as geta.New refuses them.
func TestStatusesGetaNewRefuses(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Out struct {
	OK bool `+"`json:\"ok\"`"+`
}

type Status struct {
	Status int `+"`status:\"200|205\"`"+`
	Body   Out `+"`body:\"json\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.Op(http.StatusNoContent, func(context.Context, *struct{}) (*Out, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Status, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/s", Route: geta.Route{Get: geta.Op(http.StatusCreated, func(context.Context, *struct{}) (*geta.Stream[Out], error) { return nil, nil }, geta.Doc{})}},
		{Path: "/u", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*geta.Upgrade, error) { return nil, nil }, geta.Doc{})}},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		"success status 204 takes no body, but the output has one",
		"success status 205 takes no body, but the output has one",
		"success status 201, but this output answers 200",
		"success status 200, but this output answers 101",
	})
}

// A route using cgo is read by its source's directory, though cgo writes the
// files vet reads elsewhere: its path parameters are judged.
func TestACgoRouteIsJudged(t *testing.T) {
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("no C compiler")
	}
	src := "package id_\n\n// int one(void) { return 1; }\nimport \"C\"\n\nimport (\n\t\"context\"\n\t\"net/http\"\n\n\t\"github.com/koji-1009/geta\"\n)\n\n" +
		"type Out struct{ OK bool `json:\"ok\"` }\n\ntype In struct{ Name string `path:\"name\"` }\n\n" +
		"func Route(env *int) geta.Route {\n\t_ = C.one()\n\treturn geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *In) (*Out, error) { return nil, nil }, geta.Doc{})}\n}\n"
	out, err := vetModule(t, map[string]string{"routes/zz_routes.go": "package routes\n", "routes/users/id_/route.go": src})
	if err == nil || !strings.Contains(out, `binds path parameter "name" not in the URL`) || !strings.Contains(out, "does not bind path parameter {id}") {
		t.Errorf("%v\n%s", err, out)
	}
}

// Of geta's own types, only a stream and an upgrade are special: a
// *geta.Problem output is judged as any other, by getavet as by geta.New,
// which refuse its omitempty members.
func TestAProblemOutputIsJudged(t *testing.T) {
	out, err := vetModule(t, map[string]string{"lib/lib.go": `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/p", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*geta.Problem, error) { return nil, nil }, geta.Doc{})}},
	}})
}
`})
	if err == nil || !strings.Contains(out, `Detail: json tag option "omitempty" is not supported`) {
		t.Errorf("%v\n%s", err, out)
	}
}

// A helper package in a route tree, without route.go, is no route; nor is an
// operation built in a test: neither has its path judged against a URL.
func TestOnlyARoutesOperationsAreJudgedByItsURL(t *testing.T) {
	r := route("id_", "type In struct{ ID string `path:\"id\"` }\n")
	tst := "package id_\n\nimport (\n\t\"context\"\n\t\"net/http\"\n\t\"testing\"\n\n\t\"github.com/koji-1009/geta\"\n)\n\nfunc TestX(t *testing.T) {\n\t_ = geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Out, error) { return nil, nil }, geta.Doc{})\n}\n"
	if out, err := vetModule(t, map[string]string{
		"routes/zz_routes.go":            "package routes\n",
		"routes/shared/shared.go":        route("shared", "type In struct{ ID string `path:\"id\"` }\n"),
		"routes/users/id_/route.go":      r,
		"routes/users/id_/route_test.go": tst,
	}); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}
