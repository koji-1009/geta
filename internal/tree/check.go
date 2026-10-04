package tree

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
)

var servedPath = regexp.MustCompile(`\{Path: "([^"]*)"`)

// Sync writes the table and its assembly test and returns the names of the
// files that changed. With check, it writes nothing.
func (t *Tree) Sync(check bool) (changed []string, err error) {
	for _, f := range []struct {
		name string
		want []byte
	}{{TableFile, t.Render()}, {TestFile, t.RenderTest()}} {
		file := filepath.Join(t.Dir, f.name)
		have, err := os.ReadFile(file)
		if err == nil && bytes.Equal(have, f.want) {
			continue
		}
		changed = append(changed, f.name)
		if check {
			continue
		}
		if err := os.WriteFile(file, f.want, 0o644); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// Check names what is wrong between the tree and the table on disk, one
// problem per line. It reports nothing when a sync would change nothing.
func (t *Tree) Check() ([]string, error) {
	want := t.Render()
	file := filepath.Join(t.Dir, TableFile)
	have, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return []string{fmt.Sprintf("%s does not exist; run geta sync", file)}, nil
	}
	if err != nil {
		return nil, err
	}
	var problems []string
	served := map[string]bool{}
	for _, m := range servedPath.FindAllSubmatch(have, -1) {
		served[string(m[1])] = true
	}
	denoted := map[string]bool{}
	for _, r := range t.Routes {
		denoted[r.Path] = true
		if !served[r.Path] {
			problems = append(problems, fmt.Sprintf("not served: %s answers %s, missing from %s; run geta sync",
				filepath.Join(t.Dir, filepath.FromSlash(r.Rel), "route.go"), r.Path, file))
		}
	}
	for _, p := range slices.Sorted(maps.Keys(served)) {
		if !denoted[p] {
			problems = append(problems, fmt.Sprintf("stale: %s serves %s, which no route.go denotes; run geta sync", file, p))
		}
	}
	for _, s := range t.DeadScopes() {
		problems = append(problems, fmt.Sprintf("scopes no route: no route.go at or beneath %s",
			filepath.Join(t.Dir, filepath.FromSlash(s), "scope.go")))
	}
	if len(problems) == 0 && !bytes.Equal(have, want) {
		problems = append(problems, fmt.Sprintf("%s is out of date; run geta sync", file))
	}
	test := t.RenderTest()
	testFile := filepath.Join(t.Dir, TestFile)
	switch haveTest, err := os.ReadFile(testFile); {
	case os.IsNotExist(err):
		problems = append(problems, fmt.Sprintf("%s does not exist; run geta sync", testFile))
	case err != nil:
		return nil, err
	case bytes.Contains(haveTest, []byte("Options(env)")) != t.RootOptions:
		if t.RootOptions {
			problems = append(problems, fmt.Sprintf("%s does not pass the options in %s; run geta sync",
				testFile, filepath.Join(t.Dir, OptionsFile)))
		} else {
			problems = append(problems, fmt.Sprintf("%s calls Options(env) but %s does not exist; run geta sync",
				testFile, filepath.Join(t.Dir, OptionsFile)))
		}
	case !bytes.Equal(haveTest, test):
		problems = append(problems, fmt.Sprintf("%s is out of date; run geta sync", testFile))
	}
	return problems, nil
}
