package getavet

import (
	"strings"
	"testing"
)

// An enum member a header or a cookie parameter cannot carry, and a leap
// second as a time.Time's, are reported with geta.New's text; a DateTime
// takes a leap second, and a query parameter any string.
func TestUncarriedValues(t *testing.T) {
	out, err := vetModule(t, map[string]string{
		"lib/lib.go": `package lib

import (
	"context"
	"net/http"
	"time"

	"github.com/koji-1009/geta"
)

type Body struct {
	Stamp   time.Time     ` + "`json:\"stamp\" schema:\"enum=2016-12-31T23:59:60Z\"`" + `
	Instant geta.DateTime ` + "`json:\"instant\" schema:\"enum=2016-12-31T23:59:60Z\"`" + `
}

type In struct {
	Mode   *string        ` + "`header:\"X-Mode\" schema:\"enum=a| b\"`" + `
	Token  *string        ` + "`cookie:\"token\" schema:\"enum=a|b;c\"`" + `
	Query  *string        ` + "`query:\"q\" schema:\"enum=a| b\"`" + `
	Since  *geta.DateTime ` + "`header:\"X-Since\"`" + `
	Fine   *string        ` + "`header:\"X-Fine\" schema:\"enum=a|b c\"`" + `
	Body   Body           ` + "`body:\"json\"`" + `
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/x", Route: geta.Route{
			Post: geta.Op(http.StatusOK, func(context.Context, *In) (*struct{}, error) { return nil, nil }, geta.Doc{}),
		}},
	}})
}
`,
	})
	if err == nil {
		t.Fatalf("vet passed:\n%s", out)
	}
	wants := []string{
		`Mode: header parameter "X-Mode": enum member " b" is not a valid header field value`,
		`Token: cookie parameter "token": enum member "b;c" is not a valid cookie value`,
		`Stamp: enum member "2016-12-31T23:59:60Z" is a leap second, which a time.Time does not hold`,
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
