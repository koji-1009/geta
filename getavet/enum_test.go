package getavet

import (
	"strings"
	"testing"
)

// An enum member the format a string is held to refuses, or a format type
// of geta's refuses, is reported with geta.New's text: no request carries it.
// One that the format takes is not.
func TestEnumMembersTheFormatRefuses(t *testing.T) {
	out, err := vetModule(t, map[string]string{
		"lib/lib.go": `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type In struct {
	D    string     ` + "`query:\"d\" schema:\"format=date,enum=foo|2026-01-01\"`" + `
	T    *geta.Date ` + "`query:\"t\" schema:\"enum=2026-02-30\"`" + `
	A    *geta.IPv4 ` + "`header:\"X-A\" schema:\"enum=10.0.0.1\"`" + `
	Good *geta.Date ` + "`query:\"g\" schema:\"enum=2026-01-01|2026-02-28\"`" + `
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/x", Route: geta.Route{
			Get: geta.Op(http.StatusOK, func(context.Context, *In) (*struct{}, error) { return nil, nil }, geta.Doc{}),
		}},
	}})
}
`,
	})
	if err == nil {
		t.Fatalf("vet passed:\n%s", out)
	}
	wants := []string{
		`D: enum member "foo" is not a valid date`,
		`T: enum member "2026-02-30" is not a valid date`,
	}
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "lib/lib.go:"); n != len(wants) {
		t.Errorf("%d diagnostics, want %d:\n%s", n, len(wants), out)
	}
}
