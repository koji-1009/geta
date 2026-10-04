package geta_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
)

// A row's description is written into its status's schema in place: its
// type is no component of its own, unless something else in the document
// refers to it. The named types its members hold are components, referred
// to from the description.

type quotaDetail struct {
	Window string `json:"window"`
}

type quotaInfo struct {
	Remaining int         `json:"remaining"`
	Detail    quotaDetail `json:"window"`
}

type quotaEnvelope struct {
	RetryAfter int       `header:"Retry-After"`
	Body       quotaInfo `body:"json"`
}

func schemaNames(t *testing.T, a *geta.App) []string {
	t.Helper()
	var names []string
	for name := range at(t, doc(t, a), "components", "schemas").(map[string]any) {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func TestDescriptionsAreNoComponentsOfTheirOwn(t *testing.T) {
	rows := []geta.Failure{
		geta.OnAsProblem(http.StatusTooManyRequests, "quota", func(e *quotaError) quotaEnvelope { return quotaEnvelope{} }),
		geta.OnAsProblem(http.StatusConflict, "conflict", func(e *conflictError) conflict { return conflict{} }),
	}
	tbl := one("/f", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Failures: rows})})
	if got := strings.Join(schemaNames(t, accepts(t, tbl)), ","); got != "Problem,ok,quotaDetail" {
		t.Errorf("components %s; want Problem,ok,quotaDetail: the descriptions conflict and quotaInfo are written in place", got)
	}
	// A description that a response also writes is that response's
	// component.
	tbl.Routes = append(tbl.Routes, geta.Entry{Path: "/c", Route: get(func(context.Context, *empty) (*conflict, error) { return nil, nil })})
	if got := strings.Join(schemaNames(t, accepts(t, tbl)), ","); got != "Problem,conflict,ok,quotaDetail" {
		t.Errorf("components %s; want Problem,conflict,ok,quotaDetail", got)
	}
}

// Every component the document lists is one it refers to, from an operation
// or from another component it refers to: so are those of the committed
// documents (the goldens of this package and of the examples).
func TestEveryComponentIsReferredTo(t *testing.T) {
	unreferenced(t, "describedTable", doc(t, accepts(t, describedTable())))
	files, _ := filepath.Glob("testdata/*.json")
	more, _ := filepath.Glob("examples/*/openapi*.json")
	for _, f := range append(files, more...) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if json.Unmarshal(b, &m) != nil || m["openapi"] == nil {
			continue // not a document
		}
		unreferenced(t, f, m)
	}
}

// unreferenced fails the test for each component of m nothing refers to.
func unreferenced(t *testing.T, name string, m map[string]any) {
	t.Helper()
	refd := map[string]bool{"Problem": true}
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if r, ok := v["$ref"].(string); ok {
				name := strings.TrimPrefix(r, "#/components/schemas/")
				if !refd[name] {
					refd[name] = true
					walk(at(t, m, "components", "schemas", name))
				}
			}
			for _, x := range v {
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(m["paths"])
	for c := range at(t, m, "components", "schemas").(map[string]any) {
		if !refd[c] {
			t.Errorf("%s: component %s is referred to by nothing", name, c)
		}
	}
}
