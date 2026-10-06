package getavet

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// vetModule writes a module that uses geta, runs go vet with getavet over
// it, and returns the output.
func vetModule(t *testing.T, files map[string]string) (string, error) {
	t.Helper()
	_, out, err := vetModuleAt(t, files)
	return out, err
}

// vetModuleAt is vetModule, and returns the module's directory too.
func vetModuleAt(t *testing.T, files map[string]string) (root, out string, err error) {
	t.Helper()
	if testing.Short() {
		t.Skip("runs the go command")
	}
	_, here, _, _ := runtime.Caller(0)
	geta := filepath.Join(filepath.Dir(here), "..")
	bin := filepath.Join(t.TempDir(), "getavet")
	build := exec.Command("go", "build", "-o", bin, "./cmd/getavet")
	build.Dir = filepath.Dir(here)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	root = t.TempDir()
	files["go.mod"] = "module example.com/app\n\ngo 1.27\n\nrequire github.com/koji-1009/geta v0.0.0\n\nreplace github.com/koji-1009/geta => " + geta + "\n"
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vet := exec.Command("go", "vet", "-vettool="+bin, "./...")
	vet.Dir = root
	b, err := vet.CombinedOutput()
	sameAsVet(t, root, string(b), "./...")
	return root, string(b), err
}

// vetAndNew writes a module holding package lib, whose Build calls
// geta.New, and returns getavet's diagnostics on it, each less its
// position, and the error geta.New returns when the program runs.
func vetAndNew(t *testing.T, lib string) (diagnostics []string, built string) {
	t.Helper()
	if testing.Short() {
		t.Skip("runs the go command")
	}
	_, here, _, _ := runtime.Caller(0)
	geta := filepath.Join(filepath.Dir(here), "..")
	bin := filepath.Join(t.TempDir(), "getavet")
	build := exec.Command("go", "build", "-o", bin, "./cmd/getavet")
	build.Dir = filepath.Dir(here)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.27\n\nrequire github.com/koji-1009/geta v0.0.0\n\n" +
			"replace github.com/koji-1009/geta => " + geta + "\n",
		"lib/lib.go": lib,
		"cmd/build/main.go": `package main

import (
	"fmt"

	"example.com/app/lib"
)

func main() {
	_, err := lib.Build()
	fmt.Println(err)
}
`,
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vet := exec.Command("go", "vet", "-vettool="+bin, "./lib")
	vet.Dir = root
	out, err := vet.CombinedOutput()
	if err == nil {
		t.Fatalf("vet passed:\n%s", out)
	}
	sameAsVet(t, root, string(out), "./lib")
	for line := range strings.SplitSeq(string(out), "\n") {
		if _, msg, ok := strings.Cut(line, ": "); ok && strings.HasPrefix(line, "lib/lib.go:") {
			diagnostics = append(diagnostics, msg)
		}
	}
	run := exec.Command("go", "run", "./cmd/build")
	run.Dir = root
	ran, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, ran)
	}
	return diagnostics, string(ran)
}

// checkSame asserts that getavet reports each of wants, and that each of
// its diagnostics is geta.New's text.
func checkSame(t *testing.T, diagnostics []string, built string, wants []string) {
	t.Helper()
	for _, want := range wants {
		found := false
		for _, d := range diagnostics {
			found = found || strings.Contains(d, want)
		}
		if !found {
			t.Errorf("getavet does not report %q:\n%s", want, strings.Join(diagnostics, "\n"))
		}
		if !strings.Contains(built, want) {
			t.Errorf("geta.New does not say %q:\n%s", want, built)
		}
	}
	for _, d := range diagnostics {
		if !strings.Contains(built, d) {
			t.Errorf("getavet reports %q, which geta.New does not say:\n%s", d, built)
		}
	}
	if len(diagnostics) != len(wants) {
		t.Errorf("%d diagnostics, want %d:\n%s", len(diagnostics), len(wants), strings.Join(diagnostics, "\n"))
	}
}

const header = `
import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Out struct {
	OK bool ` + "`json:\"ok\"`" + `
}
`

func route(pkg, in string) string {
	return "package " + pkg + "\n" + header + in + `
func Route(env *int) geta.Route {
	return geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *In) (*Out, error) { return &Out{}, nil }, geta.Doc{})}
}
`
}

func TestSoundTreePasses(t *testing.T) {
	out, err := vetModule(t, map[string]string{
		"routes/zz_routes.go":       "package routes\n",
		"routes/users/id_/route.go": route("user", "type In struct{ ID string `path:\"id\" schema:\"maxLength=64\"` }\n"),
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// A module nested under a route tree is no part of it, as geta sync leaves it
// out: a package there is not judged against the URL of its directory.
func TestANestedModuleUnderATreeIsNotPartOfIt(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the go command")
	}
	_, here, _, _ := runtime.Caller(0)
	geta := filepath.Join(filepath.Dir(here), "..")
	bin := filepath.Join(t.TempDir(), "getavet")
	build := exec.Command("go", "build", "-o", bin, "./cmd/getavet")
	build.Dir = filepath.Dir(here)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	root := t.TempDir()
	mod := func(path string) string {
		return "module " + path + "\n\ngo 1.27\n\nrequire github.com/koji-1009/geta v0.0.0\n\nreplace github.com/koji-1009/geta => " + geta + "\n"
	}
	for rel, content := range map[string]string{
		"go.mod":                 mod("example.com/app"),
		"routes/zz_routes.go":    "package routes\n",
		"routes/tool/go.mod":     mod("example.com/tool"),
		"routes/tool/lib/lib.go": route("lib", "type In struct{ ID string `path:\"id\" schema:\"maxLength=64\"` }\n"),
	} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vet := exec.Command("go", "vet", "-vettool="+bin, "./...")
	vet.Dir = filepath.Join(root, "routes", "tool")
	if out, err := vet.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// A directory in a route tree holding only an external test package, which
// go/packages loads as a package of no files beside its test, is vetted
// with no failure.
func TestAnExternalTestPackageAlonePasses(t *testing.T) {
	out, err := vetModule(t, map[string]string{
		"routes/zz_routes.go":   "package routes\n",
		"routes/only/x_test.go": "package only_test\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n",
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestEachMistakeIsNamed(t *testing.T) {
	out, err := vetModule(t, map[string]string{
		"routes/zz_routes.go": "package routes\n",
		// M2: a path field the URL lacks.
		"routes/health/route.go": route("health", "type In struct{ ID string `path:\"id\"` }\n"),
		// M3: a URL parameter nothing binds.
		"routes/users/id_/route.go": route("user", "type In struct{}\n"),
		// M4: a malformed constraint, in a body type from another package,
		// on a POST, which takes a body.
		"model/model.go": "package model\n\ntype Body struct {\n\tName string `json:\"name\" schema:\"maxLength=abc\"`\n}\n",
		"routes/things/route.go": strings.Replace(strings.Replace(route("things", "type In struct{ Body model.Body `body:\"json\"` }\n"),
			"\"github.com/koji-1009/geta\"", "\"github.com/koji-1009/geta\"\n\t\"example.com/app/model\"", 1),
			"geta.Route{Get:", "geta.Route{Post:", 1),
		// M5: a constraint on the wrong type, outside any route tree.
		"lib/lib.go": strings.Replace(route("lib", "type In struct{ Age int `query:\"age\" schema:\"maxLength=3\"` }\n"),
			"func Route", "func Build", 1),
	})
	if err == nil {
		t.Fatalf("vet passed:\n%s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, want := range [][2]string{
		{"routes/health/route.go:", `input binds path parameter "id" not in the URL`},
		{"routes/users/id_/route.go:", "input does not bind path parameter {id}"},
		// Reported on the operation that uses it, naming the field's place.
		{"routes/things/route.go:", `model/model.go:4:1: Name: schema keyword maxLength: "abc" is not a non-negative integer`},
		{"lib/lib.go:", "Age: schema keyword maxLength applies to string, not integer"},
	} {
		found := false
		for _, l := range lines {
			if strings.HasPrefix(l, want[0]) && strings.Contains(l, want[1]) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %s ... %s in:\n%s", want[0], want[1], out)
		}
	}
	if len(lines) != 4 {
		t.Errorf("%d diagnostics, want 4:\n%s", len(lines), out)
	}
}

// An untagged embedded struct, exported or not, promotes its members: it is
// no member of its own, so a schema tag on it is refused, as geta.New refuses
// it, rather than checked as a constraint; its members are checked.
func TestSchemaTagOnEmbeddedStruct(t *testing.T) {
	out, err := vetModule(t, map[string]string{
		"lib/lib.go": `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Meta struct {
	Note string ` + "`json:\"note\" schema:\"minimum=1\"`" + `
}

type base struct {
	ID string ` + "`json:\"id\"`" + `
}

type Body struct {
	Meta ` + "`schema:\"maxLength=3\"`" + `
	base ` + "`schema:\"minProperties=1\"`" + `
	Name string ` + "`json:\"name\"`" + `
}

type In struct {
	Body Body ` + "`body:\"json\"`" + `
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{{Path: "/x", Route: geta.Route{
		Post: geta.Op(http.StatusOK, func(context.Context, *In) (*In, error) { return nil, nil }, geta.Doc{}),
	}}}})
}
`,
	})
	if err == nil {
		t.Fatalf("vet passed:\n%s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, want := range []string{
		"Meta: an embedded struct takes no schema tag",
		"base: an embedded struct takes no schema tag",
		"Note: schema keyword minimum applies to",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if len(lines) != 3 {
		t.Errorf("%d diagnostics, want 3:\n%s", len(lines), out)
	}
}

// An unexported member is not part of the JSON form, so its schema tag and
// type are not read, as geta.New does not read them; a schema tag on an
// output header or cookie is refused, as geta.New refuses it.
func TestUnexportedMembersAndOutputHeaders(t *testing.T) {
	out, err := vetModule(t, map[string]string{
		"lib/lib.go": `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Deep struct {
	N int ` + "`json:\"n\" schema:\"maxLength=3\"`" + `
}

type Body struct {
	Name   string ` + "`json:\"name\"`" + `
	hidden int    ` + "`schema:\"maxLength=3\"`" + `
	deep   Deep
}

type In struct {
	Body Body ` + "`body:\"json\"`" + `
}

type Env struct {
	Count   int          ` + "`header:\"X-Count\" schema:\"minimum=1\"`" + `
	Session *http.Cookie ` + "`cookie:\"sid\" schema:\"maxLength=3\"`" + `
	Body    Body         ` + "`body:\"json\"`" + `
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{{Path: "/x", Route: geta.Route{
		Post: geta.Op(http.StatusOK, func(context.Context, *In) (*Env, error) { return &Env{}, nil }, geta.Doc{}),
	}}}})
}

var _ = Body{}.hidden
var _ = Body{}.deep
`,
	})
	if err == nil {
		t.Fatalf("vet passed:\n%s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, want := range []string{
		"Count: an output header takes no schema tag",
		"Session: a cookie field takes no schema tag",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if len(lines) != 2 {
		t.Errorf("%d diagnostics, want 2:\n%s", len(lines), out)
	}
}

// Every refusal geta.New makes of an envelope's or an input's field is
// reported, with geta.New's text.
func TestEnvelopeAndInputFieldRefusals(t *testing.T) {
	out, err := vetModule(t, map[string]string{
		"lib/lib.go": `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Body struct {
	Name string ` + "`json:\"name\"`" + `
}

type Env struct {
	CT     string       ` + "`header:\"Content-Type\"`" + `
	CL     string       ` + "`header:\"content-length\"`" + `
	XC     string       ` + "`header:\"X-Content-Type-Options\"`" + `
	Bad    string       ` + "`header:\"X Bad\"`" + `
	A      string       ` + "`header:\"X-A\"`" + `
	A2     string       ` + "`header:\"x-a\"`" + `
	List   []string     ` + "`header:\"X-List\"`" + `
	S1     *http.Cookie ` + "`cookie:\"sid\"`" + `
	S2     *http.Cookie ` + "`cookie:\"sid\"`" + `
	BadC   *http.Cookie ` + "`cookie:\"a b\"`" + `
	Str    string       ` + "`cookie:\"c\"`" + `
	Body   *Body        ` + "`body:\"json\"`" + `
	X      Body         ` + "`body:\"xml\"`" + `
	Loose  int
	hidden string ` + "`header:\"X-H\"`" + `
}

type auth struct {
	Token string ` + "`header:\"X-Token\"`" + `
}

type In struct {
	Two    string ` + "`query:\"a\" header:\"A\"`" + `
	Loose  string
	hidden string ` + "`query:\"h\"`" + `
	*auth
	Q1    string  ` + "`query:\"q\"`" + `
	Q2    string  ` + "`query:\"q\"`" + `
	Empty string  ` + "`query:\"\"`" + `
	ID    *string ` + "`path:\"id\"`" + `
	Bad   string  ` + "`path:\"a-b\"`" + `
	C     []int   ` + "`cookie:\"c\"`" + `
	BadH  string  ` + "`header:\"X Bad\"`" + `
	S     map[string]string ` + "`query:\"s\"`" + `
	B     Body    ` + "`body:\"yaml\"`" + `
	B1    Body    ` + "`body:\"json\"`" + `
	B2    Body    ` + "`body:\"json\"`" + `
}

var _ = Env{}.hidden
var _ = In{}.hidden

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{{Path: "/x", Route: geta.Route{
		Get:  geta.Op(http.StatusOK, func(context.Context, *int) (*Body, error) { return nil, nil }, geta.Doc{}),
		Post: geta.Op(http.StatusOK, func(context.Context, *In) (*Env, error) { return nil, nil }, geta.Doc{}),
	}}}})
}
`,
	})
	if err == nil {
		t.Fatalf("vet passed:\n%s", out)
	}
	wants := []string{
		`CT: header "Content-Type" is reserved for the body geta writes`,
		`CL: header "content-length" is reserved for the body geta writes`,
		`XC: header "X-Content-Type-Options" is reserved for the body geta writes`,
		`Bad: "X Bad" is not a valid header name`,
		`A2: empty or repeated header name "x-a"`,
		`List: header "X-List" has unsupported type []string`,
		`S2: empty or repeated cookie name "sid"`,
		`BadC: "a b" is not a valid cookie name`,
		`Str: a cookie field has type *http.Cookie, not string`,
		`Body: the body field is a pointer; use lib.Body`,
		`X: unknown body tag "xml"`,
		`Loose: an envelope field needs a header, cookie, or body tag`,
		`hidden is tagged but unexported`,
		`Two has both query and header tags`,
		`Loose has no path, query, header, cookie, or body tag`,
		`hidden is tagged query but unexported`,
		`auth: embedded pointer types are not supported in an input; embed lib.auth`,
		`Q2 binds query "q", already bound by Q1`,
		`Empty: empty query name`,
		`ID: path parameter "id" is a pointer`,
		`Bad: path parameter name "a-b" is not a Go identifier`,
		`C: cookie parameter "c" has unsupported type []int`,
		`BadH: "X Bad" is not a valid header name`,
		`S: query parameter "s" has unsupported type map[string]string`,
		`B: unknown body tag "yaml"`,
		`B2: a second body field`,
		`input type int is not a struct`,
	}
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "lib/lib.go:"); n != len(wants) {
		t.Errorf("%d diagnostics, want %d:\n%s", n, len(wants), out)
	}
}

// An input's embedded struct binds its fields by their own tags, so a schema
// tag on it is refused; so are a schema tag on an envelope's body and an
// envelope header named Set-Cookie, as geta.New refuses each.
func TestInputEmbeddedAndEnvelopeBody(t *testing.T) {
	out, err := vetModule(t, map[string]string{
		"lib/lib.go": `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Paging struct {
	Limit int ` + "`query:\"limit\" schema:\"maxLength=3\"`" + `
}

type auth struct {
	Token string ` + "`header:\"X-Token\"`" + `
}

type In struct {
	Paging ` + "`schema:\"minimum=1\"`" + `
	auth   ` + "`schema:\"maxLength=3\"`" + `
}

type Body struct {
	Name string ` + "`json:\"name\"`" + `
}

type Env struct {
	Raw  string ` + "`header:\"set-cookie\"`" + `
	Body Body   ` + "`body:\"json\" schema:\"minProperties=1\"`" + `
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{{Path: "/x", Route: geta.Route{
		Post: geta.Op(http.StatusOK, func(context.Context, *In) (*Env, error) { return &Env{}, nil }, geta.Doc{}),
	}}}})
}
`,
	})
	if err == nil {
		t.Fatalf("vet passed:\n%s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, want := range []string{
		"Paging: an embedded struct takes no schema tag",
		"auth: an embedded struct takes no schema tag",
		"Limit: schema keyword maxLength applies to string, not integer",
		`Raw: header "set-cookie" is reserved; use a *http.Cookie field`,
		"Body: an output body takes no schema tag",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if len(lines) != 5 {
		t.Errorf("%d diagnostics, want 5:\n%s", len(lines), out)
	}
}
