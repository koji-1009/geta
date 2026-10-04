package getavet

import (
	"strings"
	"testing"
)

// A negative constant Timeout in a geta.Doc literal: getavet reports what
// geta.New refuses, with its text. A zero Timeout passes; a positive one needs
// a geta.Timeout in the operation's chain, which getavet cannot see, so it is
// left to geta.New.
func TestDocTimeoutMatchesGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"
	"time"

	"github.com/koji-1009/geta"
)

type In struct{}

func h(context.Context, *In) error { return nil }

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.OpNoBody(http.StatusNoContent, h, geta.Doc{Timeout: 0})}},
		{Path: "/b", Route: geta.Route{Get: geta.OpNoBody(http.StatusNoContent, h, geta.Doc{Timeout: -2 * time.Second})}},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{`Doc.Timeout -2s is negative`})
}

// A constant Timeout in an unkeyed geta.Doc literal is checked as a keyed
// one is; a Timeout that is no constant is left to geta.New.
func TestDocTimeoutUnkeyedAndVariable(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"
	"time"

	"github.com/koji-1009/geta"
)

type In struct{}

func h(context.Context, *In) error { return nil }

var later = -3 * time.Second

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.OpNoBody(http.StatusNoContent, h,
			geta.Doc{"", "", "", nil, false, time.Time{}, time.Time{}, nil, nil, nil, nil, nil, -time.Second})}},
		{Path: "/b", Route: geta.Route{Get: geta.OpNoBody(http.StatusNoContent, h, geta.Doc{Timeout: later})}},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{`Doc.Timeout -1s is negative`})
	if !strings.Contains(built, "Doc.Timeout -3s is negative") {
		t.Errorf("geta.New does not refuse the variable Timeout:\n%s", built)
	}
}
