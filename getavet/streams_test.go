package getavet

import (
	"os/exec"
	"strings"
	"testing"
)

// A stream's event type is written as a response body is: getavet reports
// what geta.New refuses of it, with its text; a sound one passes.
func TestStreamEventsMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"iter"
	"net/http"
	"time"

	"github.com/koji-1009/geta"
)

type Tick struct {
	Every time.Duration `+"`json:\"every\"`"+`
}

type Sound struct {
	N int `+"`json:\"n\"`"+`
}

type Hidden struct {
	n int
}

var _ = Hidden{}.n

func none[T any](yield func(T) bool) {}

func stream[T any](context.Context, *struct{}) (*geta.Stream[T], error) {
	return &geta.Stream[T]{Events: iter.Seq[T](none[T])}, nil
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.Op(http.StatusOK, stream[Tick], geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, stream[Hidden], geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Get: geta.Op(http.StatusOK, stream[Sound], geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Get: geta.Op(http.StatusOK, stream[map[int]string], geta.Doc{})}},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		"Every: type time.Duration has no JSON form",
		"lib.Hidden has fields but no JSON members",
		"map type map[int]string: only string keys have a JSON form",
	})
}

// A geta.OnAsProblem call that gives its first type argument and infers the
// second is checked as one that infers both or gives both.
func TestPartiallyExplicitProblemsMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Out struct {
	OK bool `+"`json:\"ok\"`"+`
}

type QuotaError struct{ Wait int }

func (*QuotaError) Error() string { return "quota" }

type OwnStatus struct {
	Status int `+"`json:\"status\"`"+`
}

type OwnTitle struct {
	Title string `+"`json:\"title\"`"+`
}

type OwnType struct {
	Type string `+"`json:\"type\"`"+`
}

func h(context.Context, *struct{}) (*Out, error) { return nil, nil }

func rows(f ...geta.Failure) geta.Route {
	return geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{Failures: f})}
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: rows(geta.OnAsProblem[*QuotaError](429, "q", func(*QuotaError) OwnStatus { return OwnStatus{} }))},
		{Path: "/b", Route: rows((geta.OnAsProblem[*QuotaError, OwnTitle])(429, "q", func(*QuotaError) OwnTitle { return OwnTitle{} }))},
		{Path: "/c", Route: rows(geta.OnAsProblem(429, "q", func(*QuotaError) OwnType { return OwnType{} }))},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		`geta.OnAsProblem: member "status" is reserved`,
		`geta.OnAsProblem: member "title" is reserved`,
		`geta.OnAsProblem: member "type" is reserved`,
	})
}

// A component's name is one OpenAPI allows, or geta.New refuses its type:
// getavet reports it, with geta.New's text, wherever the type is a body, a
// member, or a description.
func TestComponentNamesMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type 利用者 struct {
	Name string `+"`json:\"name\"`"+`
}

type 住所 struct {
	City string `+"`json:\"city\"`"+`
}

type Person struct {
	Home 住所 `+"`json:\"home\"`"+`
}

type In struct {
	Body 利用者 `+"`body:\"json\"`"+`
}

type Box[T any] struct {
	V T `+"`json:\"v\"`"+`
}

type Plain struct {
	N int `+"`json:\"n\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *In) (*Plain, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Person, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Box[struct{ N int `+"`json:\"n\"`"+` }], error) { return nil, nil }, geta.Doc{})}},
		{Path: "/e", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Box[[]Plain], error) { return nil, nil }, geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Box[Plain], error) { return nil, nil }, geta.Doc{})}},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		`type lib.利用者: component name "利用者" does not match`,
		`Home: type lib.住所: component name "住所" does not match`,
		`type lib.Box[struct { N int "json:\"n\"" }]: component name "Box_struct{Nint\"json:\\\"n\\\"\"}" does not match`,
	})
}

// What source does not decide is left to geta.New, and getavet says nothing
// of it: two types of one component name, a WithSchema declaration, a
// schema keyword on a type with its own JSON methods (valid or not by the
// WithSchema declaration of that type, which getavet does not read), a
// SchemaFormat that returns "", and a QUERY operation without the OpenAPI
// 3.2 document. geta.New refuses each.
func TestWhatGetaNewAloneDecides(t *testing.T) {
	root, out, err := vetModuleAt(t, map[string]string{
		"a/a.go": "package a\n\ntype User struct {\n\tN int `json:\"n\"`\n}\n",
		"b/b.go": "package b\n\ntype User struct {\n\tM int `json:\"m\"`\n}\n",
		"lib/lib.go": `package lib

import (
	"context"
	"net/http"

	"example.com/app/a"
	"example.com/app/b"
	"github.com/koji-1009/geta"
)

type Code struct{ s string }

func (Code) SchemaFormat() string            { return "" }
func (c Code) MarshalText() ([]byte, error)  { return []byte(c.s), nil }
func (c *Code) UnmarshalText(b []byte) error { c.s = string(b); return nil }

type Money struct{ v string }

func (m Money) MarshalJSON() ([]byte, error)  { return []byte(m.v), nil }
func (m *Money) UnmarshalJSON(b []byte) error { m.v = string(b); return nil }

type In struct {
	C Code ` + "`query:\"c\"`" + `
}

type Q struct {
	Body a.User ` + "`body:\"json\"`" + `
}

type Bare struct{ v string }

func (m Bare) MarshalJSON() ([]byte, error)  { return []byte(m.v), nil }
func (m *Bare) UnmarshalJSON(b []byte) error { m.v = string(b); return nil }

type Price struct {
	M Money ` + "`json:\"m\"`" + `
}

type Tagged struct {
	B Bare ` + "`json:\"b\" schema:\"minLength=1\"`" + `
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*a.User, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*b.User, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *In) (*Price, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/q", Route: geta.Route{Query: geta.Op(http.StatusOK, func(context.Context, *Q) (*Price, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/t", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Tagged, error) { return nil, nil }, geta.Doc{})}},
	}}, geta.WithSchema[Money]("string", "maxLength=abc"))
}
`,
		"cmd/build/main.go": `package main

import (
	"fmt"

	"example.com/app/lib"
)

func main() {
	_, err := lib.Build()
	fmt.Println(err)
}
`,
	})
	if err != nil {
		t.Fatalf("getavet reported what geta.New alone decides:\n%s", out)
	}
	run := exec.Command("go", "run", "./cmd/build")
	run.Dir = root
	ran, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, ran)
	}
	built := string(ran)
	for _, want := range []string{
		`schema name "User" is taken by both example.com/app/a.User and example.com/app/b.User`,
		"SchemaFormat",
		"maxLength",
		"use geta.OpenAPI32",
		"schema keyword minLength applies to string, not a type with its own JSON methods; use geta.WithSchema",
	} {
		if !strings.Contains(built, want) {
			t.Errorf("geta.New does not say %q:\n%s", want, built)
		}
	}
}
