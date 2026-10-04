package geta_test

import (
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// The document of raw bodies, several success statuses, described failure
// rows, an operation's own limits, and a BeforeGate, as one golden file the
// OpenAPI meta-schema validates (README, Running the checks).
func TestResponsesGolden(t *testing.T) {
	var closed bool
	tbl := geta.Table{Root: geta.Scope{gate()}}
	for _, part := range []geta.Table{
		rawTable(&closed, func() io.Reader { return strings.NewReader("") }),
		upsertTable(map[string]bool{}),
		describedTable(),
		limitedTable(nil),
	} {
		tbl.Routes = slices.Concat(tbl.Routes, part.Routes)
	}
	a, err := geta.New(tbl)
	if err != nil {
		t.Fatal(err)
	}
	getatest.Golden(t, a, "testdata/responses.openapi.json")
}
