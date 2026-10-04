package tree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The generated assembly test assembles through getatest, so its package's
// test binary takes -update: go test ./... -update over an application
// whose tests are the generated one and those that call getatest.Golden
// writes every golden document. A package whose tests import no getatest
// still refuses the flag.
func TestUpdateReachesEveryGoldenOfTheModule(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the go command")
	}
	env := "package routes\n\nimport \"testing\"\n\nfunc testEnv(testing.TB) Env { return new(int) }\n"
	golden := `package main

import (
	"testing"

	"example.com/app/routes"
	"github.com/koji-1009/geta/getatest"
)

func TestDocument(t *testing.T) {
	n := 0
	c := getatest.New(t, routes.Table(&n))
	getatest.Golden(t, c.App(), "openapi.json")
}
`
	root := mkTree(t, map[string]string{
		"routes/scope.go":        "",
		"routes/health/route.go": "",
		"routes/env_test.go":     env,
		"main.go":                "package main\n\nfunc main() {}\n",
		"main_test.go":           golden,
	})
	if _, err := scan(t, root).Sync(false); err != nil {
		t.Fatal(err)
	}
	goTest := func(args ...string) (string, error) {
		cmd := exec.Command("go", append([]string{"test"}, args...)...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := goTest("./...", "-update"); err != nil {
		t.Fatalf("go test ./... -update: %v\n%s", err, out)
	}
	if b, err := os.ReadFile(filepath.Join(root, "openapi.json")); err != nil || !strings.Contains(string(b), `"/health"`) {
		t.Fatalf("openapi.json not written: %v\n%s", err, b)
	}
	if out, err := goTest("./..."); err != nil {
		t.Fatalf("go test ./... after the update: %v\n%s", err, out)
	}

	// A package with tests of its own that import no getatest.
	os.WriteFile(filepath.Join(root, "routes", "health", "health_test.go"), []byte("package health\n\nimport \"testing\"\n\nfunc TestNothing(t *testing.T) {}\n"), 0o644)
	if out, err := goTest("./...", "-update"); err == nil || !strings.Contains(out, "flag provided but not defined: -update") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// check names a generated test that is missing or edited by hand.
func TestCheckNamesTheGeneratedTest(t *testing.T) {
	root := mkTree(t, map[string]string{"routes/scope.go": "", "routes/health/route.go": ""})
	scan(t, root).Sync(false)
	file := filepath.Join(root, "routes", TestFile)
	b, _ := os.ReadFile(file)
	os.WriteFile(file, append(b, []byte("\n// edited\n")...), 0o644)
	if p, _ := scan(t, root).Check(); len(p) != 1 || !strings.Contains(p[0], TestFile+" is out of date") {
		t.Fatalf("check: %q", p)
	}
	os.Remove(file)
	if p, _ := scan(t, root).Check(); len(p) != 1 || !strings.Contains(p[0], TestFile+" does not exist") {
		t.Fatalf("check: %q", p)
	}
	if changed, _ := scan(t, root).Sync(true); len(changed) != 1 || changed[0] != TestFile {
		t.Fatalf("sync -check: %q", changed)
	}
}

// A route.go in the root directory is /, served by the root package's own
// Route.
func TestTheRootDirectoryIsSlash(t *testing.T) {
	root := mkTree(t, map[string]string{"routes/route.go": strings.Replace(defaultContent("routes/route.go"), "package routes", "package routes\n\ntype Env = *int", 1)})
	b := scan(t, root).Render()
	if !strings.Contains(string(b), `{Path: "/", Route: Route(env)},`) {
		t.Fatalf("%s", b)
	}
}

// A directory the go command ignores, holding no route.go or scope.go, is
// skipped; so is a directory with a go.mod of its own, another module, with
// the routes and scopes below it.
func TestIgnoredDirectoriesAndNestedModulesAreSkipped(t *testing.T) {
	root := mkTree(t, map[string]string{
		"routes/health/route.go":           "",
		"routes/_drafts/notes.txt":         "x",
		"routes/testdata/fixture.json":     "{}",
		"routes/.cache/notes.txt":          "x",
		"routes/plugin/go.mod":             "module example.com/plugin\n",
		"routes/plugin/route.go":           "",
		"routes/plugin/deeper/route.go":    "",
		"routes/plugin/deeper/scope.go":    "",
		"routes/health/_old/scope.go.bak":  "",
		"routes/health/testdata/route.txt": "",
	})
	tr, err := Scan(filepath.Join(root, "routes"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range tr.Routes {
		got = append(got, r.Path)
	}
	if strings.Join(got, ",") != "/health" || len(tr.Scopes) != 0 {
		t.Fatalf("routes %q, scopes %q", got, tr.Scopes)
	}
}

// The root package is named after its directory, each character that cannot
// be in an identifier replaced by _, and _ put before a leading digit.
func TestTheRootPackageIsNamedAfterItsDirectory(t *testing.T) {
	for dir, want := range map[string]string{"routes": "routes", "my-api": "my_api", "1api": "_1api", "café": "café"} {
		root := mkTree(t, map[string]string{dir + "/health/route.go": ""})
		tr, err := Scan(filepath.Join(root, dir))
		if err != nil {
			t.Fatal(err)
		}
		if tr.Package != want {
			t.Errorf("%s: package %q, want %q", dir, tr.Package, want)
		}
	}
}
