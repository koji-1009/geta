package main

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The geta command: what each subcommand writes, prints, and exits with.

// bin is the geta command built once for the tests that check the process's
// exit status; "" under -short, where those tests are skipped.
var bin string

func TestMain(m *testing.M) {
	flag.Parse()
	code := func() int {
		if !testing.Short() {
			dir, err := os.MkdirTemp("", "geta-cmd")
			if err != nil {
				panic(err)
			}
			defer os.RemoveAll(dir)
			bin = filepath.Join(dir, "geta")
			if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
				panic(string(out))
			}
		}
		return m.Run()
	}()
	os.Exit(code)
}

// module writes a module whose routes tree has a root scope declaring Env
// and the given route directories, and returns its root.
func module(t *testing.T, routes ...string) string {
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
	write("routes/scope.go", "package routes\n\nimport \"github.com/koji-1009/geta\"\n\ntype Env = *int\n\nfunc Scope(env Env) geta.Scope { return nil }\n")
	for _, r := range routes {
		write(filepath.Join("routes", r, "route.go"), "package "+filepath.Base(r)+"\n")
	}
	return root
}

// geta runs the command in process.
func geta(args ...string) (code int, stderr string) {
	var e bytes.Buffer
	code = run(args, &e)
	return code, e.String()
}

// geta sync [dir], dir defaulting to routes, writes zz_routes.go and
// zz_routes_test.go in it and exits 0.
func TestSyncWritesTheTableAndItsTest(t *testing.T) {
	root := module(t, "health")
	t.Chdir(root)
	if code, errs := geta("sync"); code != 0 || errs != "" {
		t.Fatalf("geta sync: exit %d, %q", code, errs)
	}
	table, err := os.ReadFile(filepath.Join("routes", "zz_routes.go"))
	if err != nil || !bytes.Contains(table, []byte("func Table(env Env) geta.Table")) || !bytes.Contains(table, []byte(`{Path: "/health", Route: r_health.Route(env)}`)) {
		t.Fatalf("%v\n%s", err, table)
	}
	if _, err := os.Stat(filepath.Join("routes", "zz_routes_test.go")); err != nil {
		t.Fatal(err)
	}
	// An explicit directory is the same tree.
	if code, errs := geta("sync", "-check", filepath.Join(root, "routes")); code != 0 {
		t.Fatalf("geta sync -check right after sync: exit %d %q", code, errs)
	}
}

// A tree that cannot be scanned is exit 1 for sync and check, naming the
// command.
func TestScanFailures(t *testing.T) {
	root := module(t, "1d_")
	dir := filepath.Join(root, "routes")
	for _, cmd := range [][]string{{"sync", dir}, {"sync", "-check", dir}, {"check", dir}} {
		code, errs := geta(cmd...)
		if code != 1 || !strings.HasPrefix(errs, "geta "+cmd[0]+": ") || !strings.Contains(errs, "is not a Go identifier") {
			t.Errorf("geta %q: exit %d %q", cmd, code, errs)
		}
	}
	// A routes root the go command leaves out of ./... is refused, naming
	// the file: its zz_routes_test.go would never run under go test ./....
	root = module(t, "health")
	dir = filepath.Join(root, "_routes")
	if err := os.Rename(filepath.Join(root, "routes"), dir); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range [][]string{{"sync", dir}, {"sync", "-check", dir}, {"check", dir}} {
		code, errs := geta(cmd...)
		if code != 1 || !strings.HasPrefix(errs, "geta "+cmd[0]+": "+dir) || !strings.Contains(errs, `directory "_routes" is excluded from ./...`) {
			t.Errorf("geta %q: exit %d %q", cmd, code, errs)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "zz_routes.go")); err == nil {
		t.Error("geta sync wrote zz_routes.go in a refused root")
	}
}

// The process exits with the status run returns.
func TestExitStatus(t *testing.T) {
	if bin == "" {
		t.Skip("builds the command")
	}
	root := module(t, "health")
	for _, c := range []struct {
		args []string
		code int
	}{
		{nil, 2},
		{[]string{"check", "-check"}, 2},
		{[]string{"check", "routes", "routes"}, 2},
		{[]string{"check"}, 1},
		{[]string{"sync"}, 0},
		{[]string{"check"}, 0},
		{[]string{"sync", "-check"}, 0},
	} {
		cmd := exec.Command(bin, c.args...)
		cmd.Dir = root
		err := cmd.Run()
		code := 0
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != c.code {
			t.Errorf("geta %q: exit %d, want %d", c.args, code, c.code)
		}
	}
}

// geta exits 0 when there is nothing to report, 1 after reporting what it
// found (or a file it could not read), and 2 on a usage error.

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A usage error is exit 2 for every subcommand, on stderr, naming the usage: no or an unknown subcommand, a flag the
// subcommand does not define, more arguments than it takes. A tree is not
// read: an extra argument is no second tree, and a flag no directory.
func TestUsageErrorsExit2(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string // in stderr
	}{
		{nil, "usage: geta sync [-check] [dir] | geta check [dir]\n"},
		{[]string{"nope"}, `geta: unknown command "nope"`},
		{[]string{"frobnicate"}, "unknown command"},
		{[]string{"sync", "-nope"}, "flag provided but not defined: -nope\nusage: geta sync [-check] [dir]\n"},
		{[]string{"sync", "a", "b"}, "geta sync: 2 arguments [\"a\" \"b\"]; usage: geta sync [-check] [dir]\n"},
		{[]string{"sync", "-check", "a", "b"}, "geta sync: 2 arguments"},
		{[]string{"sync", "routes", "-check"}, "geta sync: 2 arguments [\"routes\" \"-check\"]"},
		{[]string{"check", "-check"}, "flag provided but not defined: -check\nusage: geta check [dir]\n"},
		{[]string{"check", "a", "b"}, "geta check: 2 arguments [\"a\" \"b\"]; usage: geta check [dir]\n"},
	} {
		code, errOut := geta(c.args...)
		if code != 2 || !strings.Contains(errOut, c.want) || !strings.Contains(errOut, "usage") {
			t.Errorf("geta %q: exit %d, stderr %q; want 2 and %q", c.args, code, errOut, c.want)
		}
	}
	// The trees named were never read: a check of a and b would be exit 1.
	t.Chdir(t.TempDir())
	if code, errOut := geta("check", "a", "b"); code != 2 || strings.Contains(errOut, "geta check: no go.mod") {
		t.Errorf("exit %d %q", code, errOut)
	}
}

// geta sync -check writes nothing and exits 1 naming
// each file that differs from what a sync would write, 0 with no difference;
// geta check names, one line each, what is out of sync, and exits 1 when it
// names anything, a tree in sync exiting 0 silently; a tree that cannot be
// read is exit 1 for both, naming the command; dir defaults to routes.
func TestSyncAndCheckExitCodes(t *testing.T) {
	root := module(t, "health")
	dir := filepath.Join(root, "routes")

	// check before any sync: the table does not exist.
	if code, errOut := geta("check", dir); code != 1 || errOut != filepath.Join(dir, "zz_routes.go")+" does not exist; run geta sync\n" {
		t.Fatalf("no table: exit %d %q", code, errOut)
	}
	// sync -check before any sync names both files and writes nothing.
	code, errOut := geta("sync", "-check", dir)
	if code != 1 || strings.Count(errOut, "is out of date") != 2 {
		t.Fatalf("sync -check: %d %s", code, errOut)
	}
	for _, f := range []string{"zz_routes.go", "zz_routes_test.go"} {
		want := "geta sync -check: " + filepath.Join(dir, f) + " is out of date; run geta sync\n"
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut)
		}
		if _, err := os.Stat(filepath.Join(dir, f)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("sync -check wrote %s", f)
		}
	}
	if code, errOut := geta("sync", dir); code != 0 || errOut != "" {
		t.Fatalf("sync: %d %q", code, errOut)
	}
	if code, errOut := geta("sync", "-check", dir); code != 0 || errOut != "" {
		t.Fatalf("sync -check after sync: %d %s", code, errOut)
	}
	if code, errOut := geta("check", dir); code != 0 || errOut != "" {
		t.Fatalf("check after sync: %d %q", code, errOut)
	}
	// A route added since: sync -check names the table alone, check the route.
	write(t, filepath.Join(dir, "users", "route.go"), "package users\n")
	if code, errOut := geta("sync", "-check", dir); code != 1 || strings.Count(errOut, "\n") != 1 || !strings.Contains(errOut, "zz_routes.go is out of date") {
		t.Fatalf("a route added: exit %d %q; want 1 naming zz_routes.go alone", code, errOut)
	}
	if code, errOut := geta("check", dir); code != 1 || !strings.Contains(errOut, "not served") {
		t.Fatalf("check: %d %s", code, errOut)
	}
	// And one removed: one line each.
	if err := os.RemoveAll(filepath.Join(dir, "health")); err != nil {
		t.Fatal(err)
	}
	code, errOut = geta("check", dir)
	lines := strings.Split(strings.TrimSuffix(errOut, "\n"), "\n")
	if code != 1 || len(lines) != 2 || !strings.HasPrefix(lines[0], "not served: "+filepath.Join(dir, "users", "route.go")+" answers /users") ||
		!strings.HasPrefix(lines[1], "stale: ") || !strings.Contains(lines[1], "/health") {
		t.Fatalf("exit %d:\n%s", code, errOut)
	}
	// The default directory is routes, from the working directory.
	t.Chdir(root)
	if code, errOut := geta("check"); code != 1 || !strings.Contains(errOut, "not served: "+filepath.Join("routes", "users", "route.go")) {
		t.Fatalf("geta check in the module root (routes by default): exit %d %q", code, errOut)
	}
	// A tree that cannot be read as one: an error, exit 1.
	write(t, filepath.Join(dir, "_x", "route.go"), "package x\n")
	for _, cmd := range []string{"sync", "check"} {
		if code, errOut := geta(cmd, dir); code != 1 || !strings.Contains(errOut, "geta "+cmd+":") {
			t.Errorf("%s: %d %s", cmd, code, errOut)
		}
	}
	t.Chdir(t.TempDir())
	if code, errOut := geta("check"); code != 1 || !strings.Contains(errOut, "geta check:") {
		t.Errorf("check without a dir: %d %s", code, errOut)
	}
}

// A table or assembly test that cannot be read or written (here a directory
// where the file goes) is exit 1 for sync and check, on stderr naming the
// command and the file, and sync -check still names it as not what sync
// writes.
func TestUnwritableTableExits1(t *testing.T) {
	for _, f := range []string{"zz_routes.go", "zz_routes_test.go"} {
		root := module(t, "health")
		dir := filepath.Join(root, "routes")
		if code, errOut := geta("sync", dir); code != 0 {
			t.Fatalf("sync: %d %s", code, errOut)
		}
		if err := os.Remove(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, f), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, cmd := range []string{"sync", "check"} {
			code, errOut := geta(cmd, dir)
			if code != 1 || !strings.HasPrefix(errOut, "geta "+cmd+": ") || !strings.Contains(errOut, filepath.Join(dir, f)) {
				t.Errorf("%s with %s a directory: exit %d %q", cmd, f, code, errOut)
			}
		}
		code, errOut := geta("sync", "-check", dir)
		if code != 1 || errOut != "geta sync -check: "+filepath.Join(dir, f)+" is out of date; run geta sync\n" {
			t.Errorf("sync -check with %s a directory: exit %d %q", f, code, errOut)
		}
	}
}
