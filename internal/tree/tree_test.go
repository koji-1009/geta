package tree

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// mkTree writes a module with a routes tree. files maps a path under the
// module root to its content; an empty content gets a minimal package.
func mkTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	_, here, _, _ := runtime.Caller(0)
	geta := filepath.Join(filepath.Dir(here), "..", "..")
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/app\n\ngo 1.27\n\nrequire github.com/koji-1009/geta v0.0.0\n\nreplace github.com/koji-1009/geta => "+geta+"\n")
	for rel, content := range files {
		if content == "" {
			content = defaultContent(rel)
		}
		write(rel, content)
	}
	return root
}

func defaultContent(rel string) string {
	pkg := filepath.Base(filepath.Dir(rel))
	pkg = strings.NewReplacer("-", "", "_", "").Replace(pkg)
	switch filepath.Base(rel) {
	case "route.go":
		return "package " + pkg + `

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Out struct{ OK bool ` + "`json:\"ok\"`" + ` }

func Route(env *int) geta.Route {
	return geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Out, error) { return &Out{true}, nil }, geta.Doc{})}
}
`
	case "scope.go":
		if pkg == "routes" {
			return "package routes\n\nimport \"github.com/koji-1009/geta\"\n\ntype Env = *int\n\nfunc Scope(env Env) geta.Scope { return nil }\n"
		}
		return "package " + pkg + "\n\nimport \"github.com/koji-1009/geta\"\n\nfunc Scope(env *int) geta.Scope { return nil }\n"
	}
	return "package " + pkg + "\n"
}

func scan(t *testing.T, root string) *Tree {
	t.Helper()
	tr, err := Scan(filepath.Join(root, "routes"))
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestURLsFollowDirectories(t *testing.T) {
	root := mkTree(t, map[string]string{
		"routes/scope.go":                     "",
		"routes/route.go":                     "",
		"routes/health/route.go":              "",
		"routes/users/route.go":               "",
		"routes/users/scope.go":               "",
		"routes/users/id_/route.go":           "",
		"routes/users/id_/posts/route.go":     "",
		"routes/users/by-role/role_/route.go": "",
		"routes/users/helpers.go":             "package users\n",
	})
	tr := scan(t, root)
	var got []string
	for _, r := range tr.Routes {
		got = append(got, r.Path+" "+strings.Join(r.Scopes, ","))
	}
	want := []string{
		"/ ",
		"/health ",
		"/users users",
		"/users/by-role/{role} users",
		"/users/{id} users",
		"/users/{id}/posts users",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
}

// The generated table compiles, and serves.
func TestSyncedTableBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("builds with the go command")
	}
	root := mkTree(t, map[string]string{
		"routes/scope.go":        "",
		"routes/health/route.go": "",
		"routes/users/scope.go":  "",
		"routes/users/id_/route.go": strings.NewReplacer("*struct{}", "*struct{ ID string `path:\"id\"` }", "Out", "User").
			Replace(defaultContent("routes/users/id_/route.go")),
		"main.go": `package main

import (
	"net/http/httptest"
	"github.com/koji-1009/geta"
	"example.com/app/routes"
)

func main() {
	n := 0
	a, err := geta.New(routes.Table(&n))
	if err != nil { panic(err) }
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest("GET", "/users/7", nil))
	if rec.Code != 200 { panic(rec.Body.String()) }
}
`,
	})
	tr := scan(t, root)
	if _, err := tr.Sync(false); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// The generated test runs geta.New under go test, so an assembly mistake
// fails go test though the application wrote no test of its own.
func TestGeneratedTestRunsTheAssemblyChecks(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the go command")
	}
	goTest := func(root string) (string, error) {
		cmd := exec.Command("go", "test", "./routes")
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	env := "package routes\n\nimport \"testing\"\n\nfunc testEnv(testing.TB) Env { return new(int) }\n"
	good := mkTree(t, map[string]string{"routes/scope.go": "", "routes/health/route.go": "", "routes/env_test.go": env})
	scan(t, good).Sync(false)
	if out, err := goTest(good); err != nil {
		t.Fatalf("a sound table fails: %v\n%s", err, out)
	}

	// M2: a path field the URL lacks.
	bad := mkTree(t, map[string]string{
		"routes/scope.go":        "",
		"routes/health/route.go": strings.Replace(defaultContent("routes/health/route.go"), "*struct{}", "*struct{ ID string `path:\"id\"` }", 1),
		"routes/env_test.go":     env,
	})
	scan(t, bad).Sync(false)
	if out, err := goTest(bad); err == nil || !strings.Contains(out, "not in the URL") {
		t.Fatalf("an assembly mistake passes go test: %v\n%s", err, out)
	}

	// Without testEnv, go test names what is missing.
	bare := mkTree(t, map[string]string{"routes/scope.go": "", "routes/health/route.go": ""})
	scan(t, bare).Sync(false)
	if out, err := goTest(bare); err == nil || !strings.Contains(out, "undefined: testEnv") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// sealedRoute is a route whose output holds a sealed type, which geta.New
// accepts only with geta.WithUnion: an app that needs an option.
const sealedRoute = `package shapes

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Shape interface{ isShape() }

type Circle struct {
	Kind string ` + "`json:\"kind\"`" + `
	R    int    ` + "`json:\"r\"`" + `
}

func (Circle) isShape() {}

var Shapes = geta.Sealed[Shape]("kind", geta.Case[Circle]("circle"))

type Out struct {
	S Shape ` + "`json:\"s\"`" + `
}

func Route(env *int) geta.Route {
	return geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Out, error) { return &Out{Circle{"circle", 1}}, nil }, geta.Doc{})}
}
`

// The root's options.go is the options main assembles the app with; the
// generated test assembles the table with them too, so an app that needs
// an option (a sealed type here) passes its own assembly test.
func TestGeneratedTestAssemblesWithTheRootOptions(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the go command")
	}
	goTest := func(root string) (string, error) {
		cmd := exec.Command("go", "test", "./routes")
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	env := "package routes\n\nimport \"testing\"\n\nfunc testEnv(testing.TB) Env { return new(int) }\n"
	options := "package routes\n\nimport (\n\t\"github.com/koji-1009/geta\"\n\t\"example.com/app/routes/shapes\"\n)\n\n" +
		"func Options(env Env) []geta.Option { return []geta.Option{geta.WithUnion(shapes.Shapes)} }\n"
	files := map[string]string{"routes/scope.go": "", "routes/shapes/route.go": sealedRoute, "routes/env_test.go": env}

	// Without options.go, the table alone does not assemble.
	bare := mkTree(t, files)
	tr := scan(t, bare)
	if tr.RootOptions {
		t.Fatal("RootOptions without options.go")
	}
	tr.Sync(false)
	if out, err := goTest(bare); err == nil {
		t.Fatalf("a table that needs WithUnion assembles without it:\n%s", out)
	}

	files["routes/options.go"] = options
	root := mkTree(t, files)
	tr = scan(t, root)
	if !tr.RootOptions {
		t.Fatal("options.go not seen")
	}
	if _, err := tr.Sync(false); err != nil {
		t.Fatal(err)
	}
	if out, err := goTest(root); err != nil {
		t.Fatalf("the generated test does not assemble with Options(env): %v\n%s", err, out)
	}
	b, _ := os.ReadFile(filepath.Join(root, "routes", TestFile))
	if !strings.Contains(string(b), "getatest.New(t, Table(env), Options(env)...)") {
		t.Fatalf("%s", b)
	}
}

// sync -check and check notice options.go added or removed since the last
// sync, and name it.
func TestCheckNamesOptionsTheTestDoesNotPass(t *testing.T) {
	root := mkTree(t, map[string]string{"routes/scope.go": "", "routes/health/route.go": ""})
	scan(t, root).Sync(false)
	opts := filepath.Join(root, "routes", OptionsFile)
	os.WriteFile(opts, []byte("package routes\n\nimport \"github.com/koji-1009/geta\"\n\nfunc Options(env Env) []geta.Option { return nil }\n"), 0o644)
	// Only the assembly test is out of date; sync -check names it, not the table.
	if changed, _ := scan(t, root).Sync(true); !slices.Equal(changed, []string{TestFile}) {
		t.Fatalf("sync -check with options.go added since the sync names %q, want %q", changed, TestFile)
	}
	p, err := scan(t, root).Check()
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 1 || !strings.Contains(p[0], "does not pass the options") || !strings.Contains(p[0], filepath.Join("routes", OptionsFile)) {
		t.Fatalf("check: %q", p)
	}
	scan(t, root).Sync(false)
	if p, _ := scan(t, root).Check(); len(p) != 0 {
		t.Fatalf("check after sync: %q", p)
	}
	os.Remove(opts)
	p, _ = scan(t, root).Check()
	if len(p) != 1 || !strings.Contains(p[0], "does not exist") || !strings.Contains(p[0], OptionsFile) {
		t.Fatalf("check: %q", p)
	}
}

func TestRenderIsAPureFunctionOfTheTree(t *testing.T) {
	files := map[string]string{
		"routes/scope.go":       "",
		"routes/b/route.go":     "",
		"routes/a/route.go":     "",
		"routes/a/x_/route.go":  "",
		"routes/a/scope.go":     "",
		"routes/c/d/e/route.go": "",
		"routes/c/scope.go":     "",
		"routes/c/d/e/scope.go": "",
		"routes/c/d/other.go":   "package d\n",
	}
	one := scan(t, mkTree(t, files)).Render()
	two := scan(t, mkTree(t, files)).Render()
	if string(one) != string(two) {
		t.Fatal("two renders of one tree differ")
	}
	if !strings.Contains(string(one), `{Path: "/c/d/e", Route: r_c_d_e.Route(env), Scopes: []geta.Scope{r_c.Scope(env), r_c_d_e.Scope(env)}}`) {
		t.Fatalf("%s", one)
	}
}

func TestSyncCheckAndCheckNameTheForgottenDirectory(t *testing.T) {
	root := mkTree(t, map[string]string{
		"routes/scope.go":        "",
		"routes/health/route.go": "",
	})
	tr := scan(t, root)
	if _, err := tr.Sync(false); err != nil {
		t.Fatal(err)
	}
	if changed, _ := scan(t, root).Sync(true); len(changed) != 0 {
		t.Fatalf("sync -check fails right after sync: %q", changed)
	}
	if p, _ := scan(t, root).Check(); len(p) != 0 {
		t.Fatalf("check: %q", p)
	}

	// A route added without a sync.
	os.MkdirAll(filepath.Join(root, "routes", "users", "id_"), 0o755)
	os.WriteFile(filepath.Join(root, "routes", "users", "id_", "route.go"), []byte(defaultContent("routes/users/id_/route.go")), 0o644)
	if changed, _ := scan(t, root).Sync(true); !slices.Contains(changed, TableFile) {
		t.Fatalf("sync -check with a route missing from the table names %q", changed)
	}
	p, err := scan(t, root).Check()
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 1 || !strings.Contains(p[0], filepath.Join("routes", "users", "id_", "route.go")) || !strings.Contains(p[0], "/users/{id}") {
		t.Fatalf("check: %q", p)
	}

	// A route removed without a sync.
	scan(t, root).Sync(false)
	os.RemoveAll(filepath.Join(root, "routes", "health"))
	p, _ = scan(t, root).Check()
	if len(p) != 1 || !strings.Contains(p[0], "stale") || !strings.Contains(p[0], "/health") {
		t.Fatalf("check: %q", p)
	}

	// A hand edit no single directory explains.
	scan(t, root).Sync(false)
	file := filepath.Join(root, "routes", TableFile)
	b, _ := os.ReadFile(file)
	os.WriteFile(file, append(b, []byte("\n// edited\n")...), 0o644)
	p, _ = scan(t, root).Check()
	if len(p) != 1 || !strings.Contains(p[0], TableFile+" is out of date") {
		t.Fatalf("check: %q", p)
	}
}

func TestCheckNamesAScopeGuardingNothing(t *testing.T) {
	root := mkTree(t, map[string]string{
		"routes/health/route.go": "",
		"routes/admin/scope.go":  "",
	})
	scan(t, root).Sync(false)
	p, _ := scan(t, root).Check()
	if len(p) != 1 || !strings.Contains(p[0], "scopes no route") || !strings.Contains(p[0], filepath.Join("admin", "scope.go")) {
		t.Fatalf("check: %q", p)
	}
}

// A route.go or a scope.go (alone too) in a directory the go
// command leaves out of ./... (_x, .x, testdata), or anywhere below one, is
// refused, naming the file: sync and check would otherwise leave it out of
// the table and report nothing. A module of its own below one is still
// another module.
func TestRejectsDirectoriesTheGoCommandIgnores(t *testing.T) {
	for _, dir := range []string{"_id", ".hidden", "testdata"} {
		for _, file := range []string{
			dir + "/route.go",
			dir + "/scope.go",
			dir + "/users/route.go",
			dir + "/a/b/scope.go",
			"x/" + dir + "/y/route.go",
		} {
			root := mkTree(t, map[string]string{"routes/users/" + file: ""})
			_, err := Scan(filepath.Join(root, "routes"))
			want := filepath.Join(root, "routes", "users", file) + ": directory " + strconv.Quote(dir) + " is excluded from ./..."
			if err == nil || !strings.HasPrefix(err.Error(), want) || strings.Contains(err.Error(), "never build") ||
				!strings.Contains(err.Error(), "rename it") {
				t.Errorf("%s: %v", file, err)
			}
		}
	}
	root := mkTree(t, map[string]string{
		"routes/health/route.go":       "",
		"routes/_x/plugin/go.mod":      "module example.com/plugin\n",
		"routes/_x/plugin/route.go":    "",
		"routes/_x/plugin/a/scope.go":  "",
		"routes/_x/notes/readme.txt":   "",
		"routes/.mod/go.mod":           "module example.com/mod\n",
		"routes/.mod/route.go":         "",
		"routes/testdata/fixture.json": "{}",
	})
	if tr, err := Scan(filepath.Join(root, "routes")); err != nil || len(tr.Routes) != 1 {
		t.Fatalf("%v %v", tr, err)
	}
}

// The root itself, or a directory between it and its module root, named so
// that the go command leaves it out of ./... is refused the same way: go test
// ./... in the module would never run the root's zz_routes_test.go. A module
// root so named is still matched by ./... in it, and is accepted.
func TestRejectsARootTheGoCommandIgnores(t *testing.T) {
	for _, dir := range []string{"_routes", ".routes", "testdata"} {
		for _, rel := range []string{dir, dir + "/routes", "x/" + dir + "/routes"} {
			for _, file := range []string{"route.go", "scope.go", "users/route.go"} {
				root := mkTree(t, map[string]string{rel + "/" + file: ""})
				_, err := Scan(filepath.Join(root, rel))
				want := filepath.Join(root, rel, file) + ": directory " + strconv.Quote(dir) + " is excluded from ./..."
				if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.Contains(err.Error(), "rename it") {
					t.Errorf("%s/%s: %v", rel, file, err)
				}
			}
		}
	}
	root := mkTree(t, map[string]string{
		"_app/go.mod":          "module example.com/app\n\ngo 1.27\n",
		"_app/health/route.go": "",
	})
	if tr, err := Scan(filepath.Join(root, "_app")); err != nil || len(tr.Routes) != 1 {
		t.Fatalf("a module root named _app: %v %v", tr, err)
	}
	// A root so named with no route.go or scope.go at or below it has nothing
	// the go command would leave out, and is read: a tree with no routes.
	root = mkTree(t, map[string]string{"_routes/notes/readme.txt": "notes"})
	if tr, err := Scan(filepath.Join(root, "_routes")); err != nil || len(tr.Routes) != 0 || tr.RootScope {
		t.Fatalf("an empty root named _routes: %v %v", tr, err)
	}
}

// A directory of the tree that cannot be read is an error naming it, not a
// part of the tree left out: in the tree, below a directory the go command
// ignores, and below a root the go command ignores.
func TestUnreadableDirectoryIsAnError(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs a directory its owner cannot read")
	}
	for _, c := range []struct{ root, locked string }{
		{"routes", "routes/locked"},
		{"routes", "routes/_x/locked"},
		{"_routes", "_routes/locked"},
	} {
		root := mkTree(t, map[string]string{"routes/health/route.go": "", "_routes/readme.txt": "notes", c.locked + "/readme.txt": "notes"})
		locked := filepath.Join(root, c.locked)
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o755) })
		_, err := Scan(filepath.Join(root, c.root))
		if !errors.Is(err, fs.ErrPermission) || !strings.Contains(err.Error(), locked) {
			t.Errorf("%s unreadable: %v", c.locked, err)
		}
	}
}

func TestRejectsBadParameterNames(t *testing.T) {
	for _, dir := range []string{"1d_", "{id}"} {
		root := mkTree(t, map[string]string{"routes/" + dir + "/route.go": ""})
		if _, err := Scan(filepath.Join(root, "routes")); err == nil {
			t.Errorf("%s accepted", dir)
		}
	}
}

func TestRejectsAliasCollision(t *testing.T) {
	root := mkTree(t, map[string]string{"routes/by-role/route.go": "", "routes/by_role/route.go": ""})
	if _, err := Scan(filepath.Join(root, "routes")); err == nil || !strings.Contains(err.Error(), "both map to identifier") {
		t.Fatalf("%v", err)
	}
}

// A scope package is imported too, so its alias may not collide with a
// route's either.
func TestRejectsScopeAliasCollision(t *testing.T) {
	root := mkTree(t, map[string]string{
		"routes/a-b/scope.go":   "",
		"routes/a-b/c/route.go": "",
		"routes/a_b/route.go":   "",
	})
	_, err := Scan(filepath.Join(root, "routes"))
	if err == nil || !strings.Contains(err.Error(), `"a-b" and "a_b"`) || !strings.Contains(err.Error(), "both map to identifier") {
		t.Fatalf("%v", err)
	}
}

// A parameter directory's name follows net/http's wildcard rule, Unicode
// letters and digits included, so the generator and the router agree.
func TestParameterNamesFollowServeMux(t *testing.T) {
	for _, name := range []string{"id", "_", "café", "x٣", "a1", "1a", "٣x", "a-b", "a.b", "", "日本"} {
		_, err := URLFor(name + "_")
		generator := err == nil
		mux := func() (ok bool) {
			defer func() {
				if recover() != nil {
					ok = false
				}
			}()
			http.NewServeMux().HandleFunc("/{"+name+"}", func(http.ResponseWriter, *http.Request) {})
			return true
		}()
		if generator != mux {
			t.Errorf("%q: generator accepts %v, ServeMux %v", name, generator, mux)
		}
	}
}
