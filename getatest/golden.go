package getatest

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/koji-1009/geta"
)

var update = flag.Bool("update", false, "rewrite golden files instead of comparing against them")

// Golden compares app's OpenAPI document with the file at path, relative to
// the test's package directory, and fails the test if they differ. Run the
// test with -update to rewrite the file.
func Golden(t testing.TB, app *geta.App, path string) {
	t.Helper()
	got := app.OpenAPI()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("getatest: reading golden %s (create with -update): %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("getatest: OpenAPI document differs from %s (accept with -update)\n%s",
			path, firstDifference(want, got))
	}
}

// firstDifference shows the first line at which two different documents
// differ.
func firstDifference(want, got []byte) string {
	wl := bytes.Split(want, []byte("\n"))
	gl := bytes.Split(got, []byte("\n"))
	line := func(lines [][]byte, i int) string {
		if i < len(lines) {
			return string(lines[i])
		}
		return "(the document ends before this line)"
	}
	for i := range max(len(wl), len(gl)) {
		if i >= len(wl) || i >= len(gl) || !bytes.Equal(wl[i], gl[i]) {
			return "line " + strconv.Itoa(i+1) + ":\n  committed: " + line(wl, i) + "\n  generated: " + line(gl, i)
		}
	}
	// Unreachable: Golden calls this only on documents that differ.
	return ""
}
