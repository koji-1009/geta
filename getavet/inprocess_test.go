package getavet

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/packages"
)

// inProcess runs the analyzer in this test's process over the packages
// patterns name in the module at root, and returns its diagnostics as go vet
// prints them, "file:line:col: message", with root left out of every path.
// go vet runs getavet as another process, out of reach of the test's
// coverage; this runs the same analyzer where coverage sees it, and
// sameAsVet holds the two to one result.
func inProcess(t *testing.T, root string, patterns ...string) []string {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports |
			packages.NeedTypes | packages.NeedTypesSizes | packages.NeedSyntax | packages.NeedTypesInfo |
			packages.NeedDeps | packages.NeedModule,
		Dir:   root,
		Tests: true,
		Env:   append(os.Environ(), "GOWORK=off"),
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		t.Fatal(err)
	}
	g, err := checker.Analyze([]*analysis.Analyzer{Analyzer}, pkgs, nil)
	if err != nil {
		t.Fatal(err)
	}
	roots := []string{root + string(filepath.Separator)}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		roots = append(roots, real+string(filepath.Separator))
	}
	var out []string
	for _, act := range g.Roots {
		if act.Err != nil {
			t.Fatalf("%s: %v", act, act.Err)
		}
		for _, d := range act.Diagnostics {
			line := act.Package.Fset.Position(d.Pos).String() + ": " + d.Message
			for _, r := range roots {
				line = strings.ReplaceAll(line, r, "")
			}
			out = append(out, line)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// column is a position's column, after its file and line.
var column = regexp.MustCompile(`(\.go:\d+):\d+:`)

// sameAsVet fails the test unless the analyzer run in process reports, over
// the module at root, the diagnostics go vet printed (vetOut).
func sameAsVet(t *testing.T, root, vetOut string, patterns ...string) {
	t.Helper()
	got := inProcess(t, root, patterns...)
	roots := []string{root + string(filepath.Separator)}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		roots = append(roots, real+string(filepath.Separator))
	}
	var want []string
	for line := range strings.SplitSeq(vetOut, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, r := range roots {
			line = strings.ReplaceAll(line, r, "")
		}
		want = append(want, strings.TrimPrefix(line, "./"))
	}
	// A position in another package is read from its export data under go
	// vet, which keeps no column, and from source here: lines are compared.
	for _, lines := range [][]string{got, want} {
		for i, l := range lines {
			lines[i] = column.ReplaceAllString(l, "$1:")
		}
		slices.Sort(lines)
	}
	want = slices.Compact(want)
	if !slices.Equal(got, want) {
		t.Errorf("in process, getavet reports\n%s\ngo vet -vettool reports\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
