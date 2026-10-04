package tree

import (
	"strings"
	"testing"
)

// Rules of routing.md about what geta sync writes that no other test
// asserts as the rule states them.

// The table lists its routes sorted by URL, each directory imported under
// r_ and its path, every character an identifier cannot hold replaced by _.
func TestRoutingRenderSortsByURLAndAliases(t *testing.T) {
	root := mkTree(t, map[string]string{
		"routes/b/route.go":    "",
		"routes/a-b/route.go":  "",
		"routes/a/x_/route.go": "",
		"routes/a/route.go":    "",
		"routes/ルート/route.go":  "",
	})
	src := string(scan(t, root).Render())
	last := -1
	for _, want := range []string{
		`{Path: "/a", Route: r_a.Route(env)},`,
		`{Path: "/a-b", Route: r_a_b.Route(env)},`,
		`{Path: "/a/{x}", Route: r_a_x_.Route(env)},`,
		`{Path: "/b", Route: r_b.Route(env)},`,
		`{Path: "/ルート", Route: r_ルート.Route(env)},`,
	} {
		i := strings.Index(src, want)
		if i < 0 || i < last {
			t.Fatalf("%q missing or out of URL order:\n%s", want, src)
		}
		last = i
	}
	for _, want := range []string{`r_a_b "example.com/app/routes/a-b"`, `r_a_x_ "example.com/app/routes/a/x_"`} {
		if !strings.Contains(src, want) {
			t.Errorf("render lacks %q:\n%s", want, src)
		}
	}
}

// The generated test assembles the table through getatest.New with the Env
// testEnv returns, and with Options(env) when the root has options.go.
func TestRoutingGeneratedTestCallsGetatestNew(t *testing.T) {
	for _, c := range []struct {
		files map[string]string
		want  string
	}{
		{map[string]string{"routes/a/route.go": ""}, "func TestTableAssembles(t *testing.T) {\n\tgetatest.New(t, Table(testEnv(t)))\n}\n"},
		{map[string]string{"routes/a/route.go": "", "routes/options.go": "package routes\n"},
			"func TestTableAssembles(t *testing.T) {\n\tenv := testEnv(t)\n\tgetatest.New(t, Table(env), Options(env)...)\n}\n"},
	} {
		b := scan(t, mkTree(t, c.files)).RenderTest()
		if !strings.Contains(string(b), c.want) || !strings.Contains(string(b), `"github.com/koji-1009/geta/getatest"`) {
			t.Errorf("the generated test lacks %q:\n%s", c.want, b)
		}
	}
}
