package tree

import (
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The rules of the tree beside the ones tree_test.go asserts: what the scan
// skips, what the render writes for the root, and what check names.

// A directory the go command ignores (_x, .x, testdata) is skipped
// when it holds neither route.go nor scope.go.
func TestIgnoredDirectoriesWithoutRoutesAreSkipped(t *testing.T) {
	root := mkTree(t, map[string]string{
		"routes/scope.go":                   "",
		"routes/health/route.go":            "",
		"routes/_helpers/helpers.go":        "package helpers\n",
		"routes/.cache/x/route.txt":         "not go",
		"routes/users/testdata/fixture.txt": "data",
	})
	tr := scan(t, root)
	if len(tr.Routes) != 1 || tr.Routes[0].Path != "/health" {
		t.Fatalf("routes %+v", tr.Routes)
	}
}

// A subdirectory with its own go.mod is another module, not part of
// the tree: its route.go and scope.go are not scanned.
func TestNestedModulesAreNotPartOfTheTree(t *testing.T) {
	root := mkTree(t, map[string]string{
		"routes/scope.go":           "",
		"routes/health/route.go":    "",
		"routes/plugin/go.mod":      "module example.com/plugin\n",
		"routes/plugin/route.go":    "",
		"routes/plugin/x_/route.go": "",
		"routes/plugin/scope.go":    "",
	})
	tr := scan(t, root)
	if len(tr.Routes) != 1 || tr.Routes[0].Path != "/health" || len(tr.Scopes) != 0 {
		t.Fatalf("routes %+v, scopes %q", tr.Routes, tr.Scopes)
	}
}

// The root's scope.go is the table's root scope, and the
// root's route.go is served at / by the root package's own Route.
func TestRenderTheRootsScopeAndRoute(t *testing.T) {
	root := mkTree(t, map[string]string{"routes/scope.go": "", "routes/route.go": "", "routes/a/route.go": ""})
	b := scan(t, root).Render()
	for _, want := range []string{"\t\tRoot: Scope(env),\n", `{Path: "/", Route: Route(env)},`, `{Path: "/a", Route: r_a.Route(env)},`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("render lacks %q:\n%s", want, b)
		}
	}
	bare := mkTree(t, map[string]string{"routes/a/route.go": ""})
	b = scan(t, bare).Render()
	if strings.Contains(string(b), "Root:") {
		t.Fatalf("a root without scope.go renders a root scope:\n%s", b)
	}
}

// The package is named after the root directory, every character
// an identifier cannot hold replaced by _, and _ put before what would still
// be no package name: a leading digit (a non-ASCII one too), a Go keyword,
// the blank identifier. The rendered table and test parse as Go.
func TestPackageNameFollowsTheDirectory(t *testing.T) {
	for dir, want := range map[string]string{
		"routes": "routes", "my-routes": "my_routes", "1routes": "_1routes", "api.v2": "api_v2", "ルート": "ルート",
		"٣routes": "_٣routes", "１api": "_１api", "type": "_type", "func": "_func", "_": "__",
	} {
		files := map[string]string{dir + "/a/route.go": ""}
		if ignored(dir) {
			// Below the module root, ./... would leave it out; a module root
			// so named is not.
			files[dir+"/go.mod"] = "module example.com/u\n"
		}
		root := mkTree(t, files)
		tr, err := Scan(filepath.Join(root, dir))
		if err != nil {
			t.Fatal(err)
		}
		b := tr.Render()
		if tr.Package != want || !strings.Contains(string(b), "package "+want+"\n") {
			t.Errorf("%s: package %q, want %q", dir, tr.Package, want)
		}
		if !token.IsIdentifier(tr.Package) || tr.Package == "_" {
			t.Errorf("%s: package %q is no package name", dir, tr.Package)
		}
		for _, src := range [][]byte{b, tr.RenderTest()} {
			if _, err := parser.ParseFile(token.NewFileSet(), "", src, parser.PackageClauseOnly); err != nil {
				t.Errorf("%s: %v", dir, err)
			}
		}
	}
}

// gofmt refuses, by a panic naming the source, what does not parse: a
// defect in what Render or RenderTest wrote, not a tree to report.
func TestGofmtPanicsOnWhatDoesNotParse(t *testing.T) {
	defer func() {
		if p, _ := recover().(string); !strings.Contains(p, "does not parse") || !strings.Contains(p, "package 1") {
			t.Fatalf("recovered %q", p)
		}
	}()
	gofmt([]byte("package 1\n"))
}

// A relative directory is read against the working directory: when its
// name cannot be had (os.Getwd cannot stat "." in a directory it may not
// search), Scan fails with the error that says so.
func TestScanFailsWhereTheWorkingDirectoryCannotBeNamed(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("os.Getwd asks the system, which needs no search permission")
	}
	shut := filepath.Join(t.TempDir(), "shut")
	if err := os.Mkdir(shut, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(shut) // sets PWD, which os.Getwd checks against "."
	if err := os.Chmod(shut, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(shut, 0o755) })
	if _, err := os.Stat("."); err == nil {
		t.Skip("searching needs no permission here (root)")
	}
	_, err := Scan("routes")
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("got %v, want the working directory's permission error", err)
	}
}

// The root package must declare Env; without it the generated
// package does not compile, naming Env.
func TestTableWithoutEnvDoesNotCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("builds with the go command")
	}
	root := mkTree(t, map[string]string{"routes/health/route.go": "", "routes/doc.go": "package routes\n"})
	if _, err := scan(t, root).Sync(false); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "./routes")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "undefined: Env") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// Check names a missing table, a missing test file, and a hand edit
// of the test file; a scope with a route beneath it is not dead.
func TestCheckNamesMissingAndEditedFiles(t *testing.T) {
	root := mkTree(t, map[string]string{
		"routes/scope.go":           "",
		"routes/users/scope.go":     "",
		"routes/users/id_/route.go": "",
		"routes/admin/scope.go":     "",
	})
	dir := filepath.Join(root, "routes")
	p, err := scan(t, root).Check()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p, []string{filepath.Join(dir, TableFile) + " does not exist; run geta sync"}) {
		t.Fatalf("no table: %q", p)
	}
	tr := scan(t, root)
	if dead := tr.DeadScopes(); !slices.Equal(dead, []string{"admin"}) {
		t.Fatalf("dead scopes %q, want admin alone (users scopes /users/{id})", dead)
	}
	os.RemoveAll(filepath.Join(dir, "admin"))
	scan(t, root).Sync(false)
	test := filepath.Join(dir, TestFile)
	os.Remove(test)
	if p, _ := scan(t, root).Check(); !slices.Equal(p, []string{test + " does not exist; run geta sync"}) {
		t.Fatalf("no test file: %q", p)
	}
	scan(t, root).Sync(false)
	b, _ := os.ReadFile(test)
	os.WriteFile(test, append(b, "\n// edited\n"...), 0o644)
	if p, _ := scan(t, root).Check(); !slices.Equal(p, []string{test + " is out of date; run geta sync"}) {
		t.Fatalf("edited test file: %q", p)
	}
}

// Scan needs the module the tree is in: a go.mod above it with a module
// line.
func TestScanNeedsAModule(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "routes", "a"), 0o755)
	os.WriteFile(filepath.Join(root, "routes", "a", "route.go"), []byte("package a\n"), 0o644)
	if _, err := Scan(filepath.Join(root, "routes")); err == nil || !strings.Contains(err.Error(), "no go.mod above") {
		t.Fatalf("no go.mod: %v", err)
	}
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("go 1.27\n"), 0o644)
	if _, err := Scan(filepath.Join(root, "routes")); err == nil || !strings.Contains(err.Error(), "has no module line") {
		t.Fatalf("no module line: %v", err)
	}
}

// Sync reports a table it cannot write.
func TestSyncReportsAWriteFailure(t *testing.T) {
	root := mkTree(t, map[string]string{"routes/scope.go": "", "routes/a/route.go": ""})
	tr := scan(t, root)
	dir := filepath.Join(root, "routes")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if f, err := os.Create(filepath.Join(dir, "probe")); err == nil {
		f.Close()
		t.Skip("the directory is writable despite its mode (running as root)")
	}
	if changed, err := tr.Sync(false); err == nil || !slices.Equal(changed, []string{TableFile}) {
		t.Fatalf("changed %q, err %v; want the table named and the error", changed, err)
	}
}
