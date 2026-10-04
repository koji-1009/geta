package getavet

import (
	"strings"
	"testing"
)

// An envelope's status field: getavet reports what geta.New refuses of one,
// with its text (a status outside 2xx and 3xx or 304, one given twice, a
// field that is no int, a second field, an operation's constant status the
// field does not list). A sound one passes.
func TestStatusFieldsMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Item struct {
	ID string `+"`json:\"id\"`"+`
}

type Upserted struct {
	Status   int     `+"`status:\"200|201\"`"+`
	Location *string `+"`header:\"Location\"`"+`
	Body     Item    `+"`body:\"json\"`"+`
}

type NotModified struct {
	Status int `+"`status:\"200|304\"`"+`
}

type Twice struct {
	Status int `+"`status:\"202|202\"`"+`
}

type NotInt struct {
	Status string `+"`status:\"200\"`"+`
}

type Two struct {
	Status int `+"`status:\"200\"`"+`
	Again  int `+"`status:\"201\"`"+`
}

func h[In, Out any](context.Context, *In) (*Out, error) { return nil, nil }

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Put: geta.Op(http.StatusOK, h[struct{}, Upserted], geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Put: geta.Op(http.StatusAccepted, h[struct{}, Upserted], geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Get: geta.Op(http.StatusOK, h[struct{}, NotModified], geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Get: geta.Op(http.StatusAccepted, h[struct{}, Twice], geta.Doc{})}},
		{Path: "/e", Route: geta.Route{Get: geta.Op(http.StatusOK, h[struct{}, NotInt], geta.Doc{})}},
		{Path: "/f", Route: geta.Route{Get: geta.Op(http.StatusOK, h[struct{}, Two], geta.Doc{})}},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		`success status 202 is not in status tag "200|201"`,
		`Status: status tag "200|304": 304 is not a 2xx or 3xx status other than 304`,
		`Status: status tag "202|202" names 202 twice`,
		`Status: a status field is an int, not string`,
		`Again: a second status field`,
	})
}

// A redirect getavet reports as geta.New refuses it: geta.OpNoBody, a plain
// output, an envelope without a Location header field (its status field
// declaring the redirect), one whose Location is a number; and 305 and 306.
// 300, a Location in any case, embedded, or of a text type pass, as do a
// stream's and an upgrade's statuses, which are left to geta.New.
func TestRedirectsMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Item struct {
	ID string `+"`json:\"id\"`"+`
}

type Maybe struct {
	Status int  `+"`status:\"200|307\"`"+`
	Body   Item `+"`body:\"json\"`"+`
}

type Numeric struct {
	Location int `+"`header:\"Location\"`"+`
}

type Target struct{ s string }

func (t Target) MarshalText() ([]byte, error) { return []byte(t.s), nil }

func (t *Target) UnmarshalText(b []byte) error { t.s = string(b); return nil }

type Text struct {
	Location *Target `+"`header:\"location\"`"+`
}

type Where struct {
	Location string `+"`header:\"Location\"`"+`
}

type Embedded struct {
	Where
	Status int `+"`status:\"201|303\"`"+`
}

func h[In, Out any](context.Context, *In) (*Out, error) { return nil, nil }

func n(context.Context, *struct{}) error { return nil }

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Post: geta.OpNoBody(http.StatusSeeOther, n, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Post: geta.Op(http.StatusFound, h[struct{}, Item], geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Get: geta.Op(http.StatusOK, h[struct{}, Maybe], geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Post: geta.Op(http.StatusMovedPermanently, h[struct{}, Numeric], geta.Doc{})}},
		{Path: "/e", Route: geta.Route{Post: geta.OpNoBody(http.StatusUseProxy, n, geta.Doc{})}},
		{Path: "/f", Route: geta.Route{Post: geta.OpNoBody(306, n, geta.Doc{})}},
		{Path: "/g", Route: geta.Route{Post: geta.OpNoBody(http.StatusMultipleChoices, n, geta.Doc{})}},
		{Path: "/h", Route: geta.Route{Post: geta.Op(http.StatusMultipleChoices, h[struct{}, Item], geta.Doc{})}},
		{Path: "/i", Route: geta.Route{Post: geta.Op(http.StatusTemporaryRedirect, h[struct{}, Text], geta.Doc{})}},
		{Path: "/j", Route: geta.Route{Post: geta.Op(http.StatusCreated, h[struct{}, Embedded], geta.Doc{})}},
		{Path: "/k", Route: geta.Route{Get: geta.Op(http.StatusOK, h[struct{}, geta.Stream[Item]], geta.Doc{})}},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		"success status 303 is a redirect, but geta.OpNoBody sets no Location",
		"success status 302 is a redirect, but the output lib.Item has no Location header field",
		"the output's status field declares 307, a redirect, but the output lib.Maybe has no Location header field",
		"success status 301 is a redirect, but the output lib.Numeric's Location header field Location has type int",
		"success status 305 is deprecated",
		"success status 306 is unused",
	})
}

// The status field of a struct the output embeds untagged is the output's;
// a success status that is no constant is left to geta.New, as is one
// given by a call returning all of geta.Op's arguments.
func TestEmbeddedStatusAndVariableStatus(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Item struct {
	ID string `+"`json:\"id\"`"+`
}

type Outcome struct {
	Status int `+"`status:\"200|201\"`"+`
}

type Upserted struct {
	Outcome
	Body Item `+"`body:\"json\"`"+`
}

var accepted = http.StatusAccepted

func h[In, Out any](context.Context, *In) (*Out, error) { return nil, nil }

func parts() (int, func(context.Context, *struct{}) (*Upserted, error), geta.Doc) {
	return http.StatusAccepted, h[struct{}, Upserted], geta.Doc{}
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Put: geta.Op(http.StatusAccepted, h[struct{}, Upserted], geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Put: geta.Op(accepted, h[struct{}, Upserted], geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Put: geta.Op(parts())}},
	}})
}
`)
	want := `success status 202 is not in status tag "200|201"`
	checkSame(t, diagnostics, built, []string{want})
	if n := strings.Count(built, want); n != 3 {
		t.Errorf("geta.New names %d, want 3 (the variable status's and the one a call gives among them):\n%s", n, built)
	}
}
