package getavet

import (
	"strings"
	"testing"
)

// The format types are scalars, as parameters, headers, members, slice
// elements, and map values, and take the constraints geta.New lets them
// take: getavet reports none of them. A format tag on one, which already has
// its format, and time.Duration, which encoding/json/v2 has no form for, are
// reported with geta.New's text.
func TestFormatTypes(t *testing.T) {
	out, err := vetModule(t, map[string]string{
		"lib/lib.go": `package lib

import (
	"context"
	"net/http"
	"time"

	"github.com/koji-1009/geta"
)

type Body struct {
	Date     geta.Date                     ` + "`json:\"date\"`" + `
	Time     geta.TimeOfDay                ` + "`json:\"time\"`" + `
	Duration geta.Duration                 ` + "`json:\"duration\"`" + `
	Instant  *geta.DateTime                ` + "`json:\"instant,omitzero\" schema:\"maxLength=64\"`" + `
	Addr     geta.IPv4                     ` + "`json:\"addr\" schema:\"pattern=^10\"`" + `
	V4       geta.IPv4                     ` + "`json:\"v4\"`" + `
	V6       []geta.IPv6                   ` + "`json:\"v6\" schema:\"uniqueItems=true\"`" + `
	Pointer  map[string]geta.JSONPointer   ` + "`json:\"pointer\"`" + `
	Relative geta.RelativeJSONPointer      ` + "`json:\"relative\"`" + `
	Secret   geta.Password                 ` + "`json:\"secret\" schema:\"minLength=8\"`" + `
}

type In struct {
	Day    geta.Date        ` + "`path:\"day\" schema:\"enum=2026-01-01|2026-01-02\"`" + `
	Addrs  []geta.IPv4      ` + "`query:\"addr\"`" + `
	At     *geta.TimeOfDay  ` + "`header:\"X-At\"`" + `
	Secret *geta.Password   ` + "`cookie:\"s\"`" + `
	Body   Body             ` + "`body:\"json\"`" + `
}

type Out struct {
	Next geta.Date     ` + "`header:\"X-Next\"`" + `
	Wait geta.Duration ` + "`header:\"X-Wait\"`" + `
	Body Body          ` + "`body:\"json\"`" + `
}

type Bad struct {
	Date  geta.Date     ` + "`json:\"date\" schema:\"format=date-time\"`" + `
	Pass  geta.Password ` + "`json:\"pass\" schema:\"format=ipv4\"`" + `
	Wait  time.Duration ` + "`json:\"wait\"`" + `
	Waits []time.Duration ` + "`json:\"waits\"`" + `
}

type BadIn struct {
	For  time.Duration ` + "`query:\"for\"`" + `
	Body Bad           ` + "`body:\"json\"`" + `
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/days/{day}", Route: geta.Route{
			Post: geta.Op(http.StatusOK, func(context.Context, *In) (*Out, error) { return nil, nil }, geta.Doc{}),
		}},
		{Path: "/bad", Route: geta.Route{
			Post: geta.Op(http.StatusOK, func(context.Context, *BadIn) (*Bad, error) { return nil, nil }, geta.Doc{}),
		}},
	}})
}
`,
	})
	if err == nil {
		t.Fatalf("vet passed:\n%s", out)
	}
	const noForm = "type time.Duration has no JSON form; use geta.Duration"
	wants := []string{
		`Date: schema keyword format: the type already has format "date"`,
		`Pass: schema keyword format: the type already has format "password"`,
		"Wait: " + noForm,
		"Waits: " + noForm,
		"For: " + noForm,
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
