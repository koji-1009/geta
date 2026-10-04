package tree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A zz_routes.go that differs from what geta sync writes is named only when
// no scope.go guards nothing: beside one, check names the scope alone.
func TestCheckNamesAnEditedTableOnlyWithoutAScopeGuardingNothing(t *testing.T) {
	root := mkTree(t, map[string]string{
		"routes/health/route.go": "",
		"routes/admin/scope.go":  "",
	})
	scan(t, root).Sync(false)
	file := filepath.Join(root, "routes", TableFile)
	b, _ := os.ReadFile(file)
	os.WriteFile(file, append(b, []byte("\n// edited\n")...), 0o644)
	p, _ := scan(t, root).Check()
	if len(p) != 1 || !strings.Contains(p[0], "scopes no route") {
		t.Fatalf("check beside a scope guarding nothing: %q", p)
	}
	os.RemoveAll(filepath.Join(root, "routes", "admin"))
	p, _ = scan(t, root).Check()
	if len(p) != 1 || !strings.Contains(p[0], "is out of date") {
		t.Fatalf("check without it: %q", p)
	}
}
