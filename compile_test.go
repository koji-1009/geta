package geta_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCompileErrors builds each package under testdata/compilefail and
// checks that the compiler rejects it, at the line and with the words in its
// want.txt. These are the checks geta leaves to the compiler: the
// handler's shape, one slot per method, the success status, what a route
// requires of the Env, and that a table line points at a Route.
func TestCompileErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("builds packages with the go command")
	}
	dirs, err := os.ReadDir("testdata/compilefail")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		t.Run(d.Name(), func(t *testing.T) {
			dir := filepath.Join("testdata", "compilefail", d.Name())
			want, err := os.ReadFile(filepath.Join(dir, "want.txt"))
			if err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command("go", "build", "-o", os.DevNull, "./"+dir).CombinedOutput()
			if err == nil {
				t.Fatalf("the package compiled; want a compile error")
			}
			for _, w := range strings.Split(strings.TrimSpace(string(want)), "\n") {
				if !strings.Contains(string(out), w) {
					t.Errorf("compiler output lacks %q:\n%s", w, out)
				}
			}
		})
	}
}
