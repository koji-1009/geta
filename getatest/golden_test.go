package getatest

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
)

// failures is a testing.TB that records what Golden reports, a Fatal ending
// the goroutine as it ends a test's.
type failures struct {
	testing.TB
	errs  []string
	fatal bool
}

func (f *failures) Helper() {}

func (f *failures) Errorf(format string, args ...any) {
	f.errs = append(f.errs, fmt.Sprintf(format, args...))
}

func (f *failures) Fatalf(format string, args ...any) {
	f.errs = append(f.errs, fmt.Sprintf(format, args...))
	f.fatal = true
	runtime.Goexit()
}

func (f *failures) Fatal(args ...any) {
	f.errs = append(f.errs, fmt.Sprint(args...))
	f.fatal = true
	runtime.Goexit()
}

// golden runs Golden with f, on a goroutine a Fatal may end.
func golden(t *testing.T, app *geta.App, path string) *failures {
	f := &failures{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Golden(f, app, path)
	}()
	<-done
	return f
}

type greeting struct {
	Text string `json:"text"`
}

func greetApp(t *testing.T, summary string) *geta.App {
	t.Helper()
	app, err := geta.New(geta.Table{Routes: []geta.Entry{{Path: "/g", Route: geta.Route{
		Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*greeting, error) { return &greeting{"hi"}, nil }, geta.Doc{Summary: summary}),
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

// With -update, Golden writes the document, creating the directories it
// needs, and reports nothing; without it, the same document passes, a
// changed one fails naming the first line that differs, and a missing file
// fails naming -update.
func TestGoldenUpdatesAndCompares(t *testing.T) {
	path := filepath.Join(t.TempDir(), "docs", "v1", "openapi.json")
	app := greetApp(t, "Greet")

	if f := golden(t, app, path); len(f.errs) != 1 || !f.fatal || !strings.Contains(f.errs[0], "create with -update") {
		t.Fatalf("missing file: %q", f.errs)
	}

	*update = true
	f := golden(t, app, path)
	*update = false
	if len(f.errs) != 0 {
		t.Fatalf("update: %q", f.errs)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != string(app.OpenAPI()) {
		t.Fatalf("update wrote %q, %v", b, err)
	}

	if f := golden(t, app, path); len(f.errs) != 0 {
		t.Fatalf("equal: %q", f.errs)
	}

	f = golden(t, greetApp(t, "Say hello"), path)
	if len(f.errs) != 1 || f.fatal {
		t.Fatalf("changed: %q", f.errs)
	}
	for _, want := range []string{"OpenAPI document differs from " + path, "accept with -update",
		"\n  committed:         \"summary\": \"Greet\"\n  generated:         \"summary\": \"Say hello\""} {
		if !strings.Contains(f.errs[0], want) {
			t.Errorf("lacks %q:\n%s", want, f.errs[0])
		}
	}

	// A document longer than the commit differs on the first line the
	// commit lacks.
	if err := os.WriteFile(path, []byte("{\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if f := golden(t, app, path); len(f.errs) != 1 || !strings.Contains(f.errs[0], "line 2:\n  committed: \n  generated: ") {
		t.Fatalf("shorter commit: %q", f.errs)
	}

	// A commit that differs only in lacking the final newline differs on
	// the line after its last, which it shows as ended, not as empty.
	doc := app.OpenAPI()
	if err := os.WriteFile(path, doc[:len(doc)-1], 0o644); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("line %d:\n  committed: (the document ends before this line)\n  generated: ", strings.Count(string(doc), "\n")+1)
	if f := golden(t, app, path); len(f.errs) != 1 || !strings.HasSuffix(f.errs[0], want) {
		t.Fatalf("no final newline: %q", f.errs)
	}

	// An update that cannot write fails the test.
	*update = true
	f = golden(t, app, filepath.Join(path, "below-a-file.json"))
	*update = false
	if len(f.errs) != 1 || !f.fatal {
		t.Fatalf("unwritable: %q", f.errs)
	}
}
