// Package tree reads a route tree and writes its table. It looks at
// directory names and at whether route.go, scope.go and the root's
// options.go exist; it never reads what they contain.
package tree

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"go/format"
	"go/token"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TableFile is the name of the generated table, inside the root directory.
const TableFile = "zz_routes.go"

// TestFile is the name of the generated assembly test, beside the table.
const TestFile = "zz_routes_test.go"

// OptionsFile is the root's file defining func Options(env Env)
// []geta.Option. If it exists, the generated test passes those options to
// geta.New, as main does.
const OptionsFile = "options.go"

// Tree is a route tree.
type Tree struct {
	Dir         string // the root directory, as given
	ImportPath  string // the root directory's import path
	Package     string // the root package's name: the directory's name
	RootScope   bool   // the root has scope.go
	RootOptions bool   // the root has options.go
	Routes      []Route
	Scopes      []string // relative directories with scope.go, root excluded
}

// Route is one directory with a route.go.
type Route struct {
	Rel    string   // directory relative to the root, slash-separated; "" for the root
	Path   string   // the URL template
	Scopes []string // relative directories whose scope.go applies, outermost first
}

// Scan reads the tree under dir.
func Scan(dir string) (*Tree, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	modRoot, modPath, err := findModule(abs)
	if err != nil {
		return nil, err
	}
	// modRoot is an ancestor of abs, so Rel cannot fail.
	rel, _ := filepath.Rel(modRoot, abs)
	// Refuse a root under a directory ./... ignores, where zz_routes_test.go
	// would never run. The module root's own name does not matter.
	if rel != "." {
		for name := range strings.SplitSeq(filepath.ToSlash(rel), "/") {
			if !ignored(name) {
				continue
			}
			f, err := treeFile(abs)
			if err != nil {
				return nil, err
			}
			if f != "" {
				return nil, ignoredError(f, name)
			}
			break
		}
	}
	t := &Tree{Dir: dir, ImportPath: path.Join(modPath, filepath.ToSlash(rel)), Package: identifier(filepath.Base(abs))}
	t.RootScope = exists(filepath.Join(abs, "scope.go"))
	t.RootOptions = exists(filepath.Join(abs, OptionsFile))
	var scoped []string
	err = filepath.WalkDir(abs, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		r, _ := filepath.Rel(abs, p)
		r = filepath.ToSlash(r)
		if r == "." {
			r = ""
		}
		name := d.Name()
		if r != "" && ignored(name) {
			// A route or scope below it would silently be left out.
			if f, err := treeFile(p); err != nil || f != "" {
				if err != nil {
					return err
				}
				return ignoredError(f, name)
			}
			return filepath.SkipDir
		}
		if r != "" && exists(filepath.Join(p, "go.mod")) {
			return filepath.SkipDir
		}
		if r != "" && exists(filepath.Join(p, "scope.go")) {
			scoped = append(scoped, r)
		}
		if !exists(filepath.Join(p, "route.go")) {
			return nil
		}
		urlPath, err := urlFor(r)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		route := Route{Rel: r, Path: urlPath}
		for _, s := range scoped {
			if s == r || strings.HasPrefix(r, s+"/") {
				route.Scopes = append(route.Scopes, s)
			}
		}
		t.Routes = append(t.Routes, route)
		return nil
	})
	if err != nil {
		return nil, err
	}
	t.Scopes = scoped
	slices.SortFunc(t.Routes, func(a, b Route) int { return strings.Compare(a.Path, b.Path) })
	seen := map[string]string{}
	for _, rel := range t.imports() {
		if prev, ok := seen[alias(rel)]; ok {
			return nil, fmt.Errorf("directories %q and %q both map to identifier %q", prev, rel, alias(rel))
		}
		seen[alias(rel)] = rel
	}
	return t, nil
}

// ignored reports whether the go command excludes a directory of this name
// from patterns such as ./... (go help packages).
func ignored(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata"
}

// ignoredError reports the route.go or scope.go f found under a directory
// that ./... ignores.
func ignoredError(f, name string) error {
	return fmt.Errorf("%s: directory %q is excluded from ./...; rename it (a parameter is id_, not _id)", f, name)
}

// treeFile returns the first route.go or scope.go at or below dir within
// this module, or "" if there is none.
func treeFile(dir string) (string, error) {
	var found string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if exists(filepath.Join(p, "go.mod")) {
			return filepath.SkipDir
		}
		for _, f := range []string{"route.go", "scope.go"} {
			if exists(filepath.Join(p, f)) {
				found = filepath.Join(p, f)
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found, err
}

// imports returns the sorted directories the table imports: every non-root
// route and every applied scope.
func (t *Tree) imports() []string {
	imports := map[string]bool{}
	for _, r := range t.Routes {
		if r.Rel != "" {
			imports[r.Rel] = true
		}
		for _, s := range r.Scopes {
			imports[s] = true
		}
	}
	return slices.Sorted(maps.Keys(imports))
}

// URLFor turns a directory relative to the root into its URL template, as
// sync does: a segment id_ becomes {id}.
func URLFor(rel string) (string, error) { return urlFor(rel) }

func urlFor(rel string) (string, error) {
	if rel == "" {
		return "/", nil
	}
	var segs []string
	for seg := range strings.SplitSeq(rel, "/") {
		if strings.HasSuffix(seg, "_") {
			name := strings.TrimSuffix(seg, "_")
			if !isIdentifier(name) {
				return "", fmt.Errorf("parameter directory %q: %q is not a Go identifier", seg, name)
			}
			seg = "{" + name + "}"
		} else if strings.ContainsAny(seg, "{}") {
			return "", fmt.Errorf("directory %q contains braces; write a parameter as name_", seg)
		}
		segs = append(segs, seg)
	}
	return "/" + strings.Join(segs, "/"), nil
}

// DeadScopes lists scope.go directories with no route at or beneath them.
func (t *Tree) DeadScopes() []string {
	var dead []string
	for _, s := range t.Scopes {
		used := false
		for _, r := range t.Routes {
			if slices.Contains(r.Scopes, s) {
				used = true
				break
			}
		}
		if !used {
			dead = append(dead, s)
		}
	}
	return dead
}

// Render returns the table source. The output is deterministic.
func (t *Tree) Render() []byte {
	var b bytes.Buffer
	b.WriteString("// Code generated by geta sync; DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "package %s\n\n", t.Package)
	b.WriteString("import (\n\t\"github.com/koji-1009/geta\"\n")
	imports := t.imports()
	if len(imports) > 0 {
		b.WriteString("\n")
	}
	for _, rel := range imports {
		fmt.Fprintf(&b, "\t%s %q\n", alias(rel), path.Join(t.ImportPath, rel))
	}
	b.WriteString(")\n\n")
	b.WriteString("// Table is the route table of this tree.\n")
	b.WriteString("func Table(env Env) geta.Table {\n\treturn geta.Table{\n")
	if t.RootScope {
		b.WriteString("\t\tRoot: Scope(env),\n")
	}
	b.WriteString("\t\tRoutes: []geta.Entry{\n")
	for _, r := range t.Routes {
		route := alias(r.Rel) + ".Route(env)"
		if r.Rel == "" {
			route = "Route(env)"
		}
		fmt.Fprintf(&b, "\t\t\t{Path: %q, Route: %s", r.Path, route)
		if len(r.Scopes) > 0 {
			var ss []string
			for _, s := range r.Scopes {
				ss = append(ss, alias(s)+".Scope(env)")
			}
			fmt.Fprintf(&b, ", Scopes: []geta.Scope{%s}", strings.Join(ss, ", "))
		}
		b.WriteString("},\n")
	}
	b.WriteString("\t\t},\n\t}\n}\n")
	return gofmt(b.Bytes())
}

// RenderTest returns the assembly test source, which runs every geta.New
// check on the table through getatest.New. The Env comes from testEnv,
// which the application defines in a _test.go file of the root package;
// until then the test does not compile. If the root has options.go, the
// table is assembled with Options(env). Using getatest also registers its
// -update flag, so go test ./... -update works across all packages.
func (t *Tree) RenderTest() []byte {
	var b bytes.Buffer
	b.WriteString("// Code generated by geta sync; DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "package %s\n\n", t.Package)
	b.WriteString("import (\n\t\"testing\"\n\n\t\"github.com/koji-1009/geta/getatest\"\n)\n\n")
	b.WriteString("// TestTableAssembles runs every geta.New check on Table, through getatest.New,\n")
	b.WriteString("// which fails the test on an assembly error. testEnv is the application's:\n")
	b.WriteString("// define func testEnv(testing.TB) Env in a _test.go file of this package,\n")
	b.WriteString("// returning an Env the table can be assembled with.\n")
	if t.RootOptions {
		b.WriteString("// The options are Options(env), from options.go, as main assembles the app.\n")
		b.WriteString("func TestTableAssembles(t *testing.T) {\n\tenv := testEnv(t)\n")
		b.WriteString("\tgetatest.New(t, Table(env), Options(env)...)\n}\n")
	} else {
		b.WriteString("func TestTableAssembles(t *testing.T) {\n")
		b.WriteString("\tgetatest.New(t, Table(testEnv(t)))\n}\n")
	}
	return gofmt(b.Bytes())
}

// gofmt formats generated source. It panics on a syntax error, which would
// be a defect in Render or RenderTest.
func gofmt(src []byte) []byte {
	out, err := format.Source(src)
	if err != nil {
		panic(fmt.Sprintf("geta sync: generated source does not parse: %v\n%s", err, src))
	}
	return out
}

// alias is a directory's import name: "r_" plus its path, with non-identifier
// characters replaced by _.
func alias(rel string) string {
	return "r_" + strings.Map(func(r rune) rune {
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return '_'
	}, rel)
}

// identifier is a directory's package name: its name with non-identifier
// characters replaced by _, prefixed with _ if it is empty, "_", a keyword,
// or starts with a digit.
func identifier(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return '_'
	}, s)
	if first, _ := utf8.DecodeRuneInString(s); s == "" || s == "_" || unicode.IsDigit(first) || token.IsKeyword(s) {
		s = "_" + s
	}
	return s
}

// isIdentifier reports whether s is a Go identifier, net/http's rule for a
// wildcard name.
func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if !unicode.IsLetter(c) && c != '_' && (i == 0 || !unicode.IsDigit(c)) {
			return false
		}
	}
	return true
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// findModule walks up from dir to the go.mod and returns its directory and
// module path.
func findModule(dir string) (string, string, error) {
	for d := dir; ; d = filepath.Dir(d) {
		f, err := os.Open(filepath.Join(d, "go.mod"))
		if err == nil {
			defer f.Close()
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				line := strings.TrimSpace(sc.Text())
				if mod, ok := strings.CutPrefix(line, "module "); ok {
					return d, strings.Trim(strings.TrimSpace(mod), `"`), nil
				}
			}
			return "", "", fmt.Errorf("%s has no module line", filepath.Join(d, "go.mod"))
		}
		if filepath.Dir(d) == d {
			return "", "", errors.New("no go.mod above " + dir)
		}
	}
}
