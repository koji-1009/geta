package tree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// check reads a path as sync writes it, escapes included, so a directory
// whose name holds a quote or a backslash is served after a sync.
func TestCheckAgreesWithSyncOnAQuotedDirectory(t *testing.T) {
	for _, dir := range []string{`a"b`, `a\b`} {
		root := mkTree(t, map[string]string{"routes/" + dir + "/route.go": "package ab\n"})
		tr, err := Scan(filepath.Join(root, "routes"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tr.Sync(false); err != nil {
			t.Fatal(err)
		}
		problems, err := tr.Check()
		if err != nil {
			t.Fatal(err)
		}
		if len(problems) != 0 {
			t.Errorf("%s: after sync, check reports %q", dir, problems)
		}
	}
}

// sync replaces a file whole, through a temporary file it leaves nowhere,
// whether the write succeeds or fails.
func TestSyncWritesWholeFilesAndLeavesNoTemporaryOne(t *testing.T) {
	root := mkTree(t, map[string]string{"routes/health/route.go": ""})
	dir := filepath.Join(root, "routes")
	tr, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, TableFile), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Sync(false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, TableFile)); string(b) != string(tr.Render()) {
		t.Fatalf("the table is not what Render writes:\n%s", b)
	}
	if fi, err := os.Stat(filepath.Join(dir, TableFile)); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("mode %v, %v; want 0644", fi.Mode().Perm(), err)
	}
	// A file that cannot be replaced (a directory in its place) fails the
	// sync, and the temporary file goes.
	if err := os.Remove(filepath.Join(dir, TestFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, TestFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, TestFile, "keep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Sync(false); err == nil {
		t.Fatal("sync replaced a directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("left behind: %s", e.Name())
		}
	}
}

// A vendor directory, which ./... skips, holds no route.
func TestAVendorDirectoryHoldsNoRoute(t *testing.T) {
	vendored := mkTree(t, map[string]string{"routes/vendor/x/route.go": ""})
	if _, err := Scan(filepath.Join(vendored, "routes")); err == nil || !strings.Contains(err.Error(), `directory "vendor" is excluded from ./...`) {
		t.Errorf("a route under vendor: %v", err)
	}
}

// A file sync -check cannot read is an error, not a file out of date.
func TestSyncCheckReportsAFileItCannotRead(t *testing.T) {
	root := mkTree(t, map[string]string{"routes/health/route.go": ""})
	dir := filepath.Join(root, "routes")
	tr, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, TableFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if changed, err := tr.Sync(true); err == nil || !strings.Contains(err.Error(), TableFile) {
		t.Fatalf("changed %q, error %v", changed, err)
	}
}

// A symbolic link to a directory holding a route or scope is refused, since
// neither sync nor ./... follows it, a root that is a link included; a link
// to anything else is left alone.
func TestSymbolicLinksInATree(t *testing.T) {
	root := mkTree(t, map[string]string{
		"routes/health/route.go":   "",
		"elsewhere/users/route.go": "",
		"assets/logo.txt":          "",
	})
	link := func(target, name string) {
		t.Helper()
		if err := os.Symlink(filepath.Join(root, target), filepath.Join(root, name)); err != nil {
			t.Skipf("no symbolic links here: %v", err)
		}
	}
	link("assets", "routes/assets")
	if _, err := Scan(filepath.Join(root, "routes")); err != nil {
		t.Fatalf("a link to a directory without routes: %v", err)
	}
	link("routes", "linked")
	if _, err := Scan(filepath.Join(root, "linked")); err == nil || !strings.Contains(err.Error(), "a symbolic link to a directory holding") {
		t.Fatalf("a root that is a link: %v", err)
	}
	link("elsewhere/users", "routes/users")
	_, err := Scan(filepath.Join(root, "routes"))
	if err == nil || !strings.Contains(err.Error(), "a symbolic link to a directory holding") ||
		!strings.Contains(err.Error(), filepath.Join("routes", "users")) {
		t.Fatalf("a link to a directory with a route: %v", err)
	}
}
