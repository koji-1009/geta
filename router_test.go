package geta_test

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

type which struct {
	Route string `json:"route"`
	Value string `json:"value"`
}

func answers(route string) geta.Route {
	return geta.Route{Get: geta.Op(http.StatusOK, func(ctx context.Context, _ *empty) (*which, error) {
		return &which{Route: route}, nil
	}, geta.Doc{})}
}

type roleIn struct {
	Role string `path:"role"`
}

type idNameIn struct {
	ID string `path:"id"`
}

// URLs that ServeMux refuses to hold together are served together: a
// literal segment is tried before a parameter, and a literal branch that
// leads nowhere gives way to the parameter branch.
func TestLiteralBeforeParameterWithBacktracking(t *testing.T) {
	byRole := geta.Route{Get: geta.Op(http.StatusOK, func(ctx context.Context, in *roleIn) (*which, error) {
		return &which{Route: "by-role", Value: in.Role}, nil
	}, geta.Doc{})}
	idName := geta.Route{Get: geta.Op(http.StatusOK, func(ctx context.Context, in *idNameIn) (*which, error) {
		return &which{Route: "id-name", Value: in.ID}, nil
	}, geta.Doc{})}
	id := geta.Route{Get: geta.Op(http.StatusOK, func(ctx context.Context, in *idNameIn) (*which, error) {
		return &which{Route: "id", Value: in.ID}, nil
	}, geta.Doc{})}
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/users/by-role/{role}", Route: byRole},
		{Path: "/users/{id}/name", Route: idName},
		{Path: "/users/{id}", Route: id},
		{Path: "/users/events", Route: answers("events")},
	}})
	for path, want := range map[string]string{
		"/users/by-role/admin": `{"route":"by-role","value":"admin"}`,
		"/users/by-role/name":  `{"route":"by-role","value":"name"}`, // both match; the literal wins
		"/users/7/name":        `{"route":"id-name","value":"7"}`,
		"/users/by-role":       `{"route":"id","value":"by-role"}`, // the literal leads nowhere: back out
		"/users/events":        `{"route":"events","value":""}`,
		"/users/a%2Fb":         `{"route":"id","value":"a/b"}`, // %2F stays in its segment
		"/users/a%2Fb/name":    `{"route":"id-name","value":"a/b"}`,
	} {
		if res := c.Get(path); res.Status != 200 || res.Text() != want {
			t.Errorf("%s: %d %s, want %s", path, res.Status, res.Body, want)
		}
	}
	for path, want := range map[string]int{"/users/7/other": 404, "/users/": 404, "/users/7/name/x": 404, "/users//name": 307} {
		if res := c.Get(path); res.Status != want {
			t.Errorf("%s: %d, want %d", path, res.Status, want)
		}
	}
}

// The 405 Allow lists every method served at the path, across the literal
// and parameter branches that reach it.
func TestMethodNotAllowedUnionsTheBranches(t *testing.T) {
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/a/b", Route: geta.Route{Put: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error { return nil }, geta.Doc{})}},
		{Path: "/a/{x}", Route: geta.Route{Get: geta.Op(http.StatusOK, func(ctx context.Context, in *struct {
			X string `path:"x"`
		}) (*ok, error) {
			return &ok{true}, nil
		}, geta.Doc{})}},
	}})
	// GET /a/b: the literal branch has only PUT, so the parameter branch serves.
	if res := c.Get("/a/b"); res.Status != 200 {
		t.Fatal(res.Status)
	}
	res := c.Delete("/a/b")
	if res.Status != 405 || res.Header.Get("Allow") != "GET, HEAD, OPTIONS, PUT" || res.Problem().Status != 405 {
		t.Fatalf("%d %q %s", res.Status, res.Header.Get("Allow"), res.Body)
	}
}

// A path that is not clean is redirected to its clean form, as ServeMux
// does, keeping the query, with no body. A byte a browser reads otherwise
// than the server (a backslash it takes for '/', a tab or line break it
// drops) goes out escaped, so Location never names another host; an escape
// the client sent stays.
func TestUncleanPathsRedirect(t *testing.T) {
	c := getatest.New(t, one("/a/b", get(okHandler)))
	for _, tc := range []struct{ target, want string }{
		{"/a//b", "/a/b"},
		{"/a/./b", "/a/b"},
		{"/x/../a/b?q", "/a/b?q"},
		{"//\\evil.example/x", "/%5Cevil.example/x"},
		{"/a/..//\\evil.example", "/%5Cevil.example"},
		{"//\\/evil.example", "/%5C/evil.example"},
		{"//%5Cevil.example", "/%5Cevil.example"},
		{"///evil.example", "/evil.example"},
		{"/.//evil.example/", "/evil.example/"},
		{"//%2F%2Fevil.example/a%2Fb", "/%2F%2Fevil.example/a%2Fb"},
		{"//café?q=café", "/caf%C3%A9?q=caf%c3%a9"},
		{"//a/\"<x>^`{|}", "/a/%22%3Cx%3E%5E%60%7B%7C%7D"},
		{"//a/!$&'()*+,;=:@[]~", "/a/!$&'()*+,;=:@[]~"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.target, nil)
		rec := httptest.NewRecorder()
		c.App().ServeHTTP(rec, req)
		h := rec.Header()
		if rec.Code != http.StatusTemporaryRedirect || h.Get("Location") != tc.want || rec.Body.Len() != 0 ||
			h.Get("Content-Length") != "0" || h.Get("Content-Type") != "" {
			t.Errorf("%s: %d %q %v %q", tc.target, rec.Code, h.Get("Location"), h, rec.Body)
		}
	}
	// Bytes net/http's server refuses in a request line, in a request made in
	// process: as its request-target and as its URL's path.
	for _, tc := range []struct{ path, want string }{
		{"//\tevil.example/x", "/%09evil.example/x"},
		{"//\nevil.example", "/%0Aevil.example"},
		{"//\r\n/evil.example", "/%0D%0A/evil.example"},
		{"// evil.example", "/%20evil.example"},
		{"//%zz/%4", "/%25zz/%254"},
	} {
		for _, sent := range []bool{true, false} {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.URL.Path, req.RequestURI = tc.path, ""
			if sent {
				req.RequestURI = tc.path
			}
			rec := httptest.NewRecorder()
			c.App().ServeHTTP(rec, req)
			if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != tc.want {
				t.Errorf("%q (sent %v): %d %q", tc.path, sent, rec.Code, rec.Header().Get("Location"))
			}
		}
	}
	// HEAD and other methods are redirected alike, with no body.
	for _, m := range []string{http.MethodHead, http.MethodPost, http.MethodDelete} {
		rec := httptest.NewRecorder()
		c.App().ServeHTTP(rec, httptest.NewRequest(m, "//\\evil.example", nil))
		if h := rec.Header(); rec.Code != http.StatusTemporaryRedirect || h.Get("Location") != "/%5Cevil.example" ||
			rec.Body.Len() != 0 || h.Get("Content-Length") != "0" || h.Get("Content-Type") != "" {
			t.Errorf("%s: %d %v %q", m, rec.Code, h, rec.Body)
		}
	}
}

// Middleware sees what ServeMux would have given it: r.Pattern and
// r.PathValue.
func TestMiddlewareSeesPatternAndPathValues(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	look := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen = append(seen, r.Pattern+" id="+r.PathValue("id"))
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	})
	c := getatest.New(t, geta.Table{Root: geta.Scope{look}, Routes: []geta.Entry{{Path: "/users/{id}", Route: get(idHandler)}}})
	c.Get("/users/a%20b")
	c.Get("/nope")
	if got := strings.Join(seen, "|"); got != "GET /users/{id} id=a b| id=" {
		t.Fatal(got)
	}
}

// Mounted under a ServeMux pattern that names the same parameter, geta binds
// its own value and leaves the outer request's value as it was.
func TestMountedUnderTheSameParameterName(t *testing.T) {
	echo := geta.Route{Get: geta.Op(http.StatusOK, func(ctx context.Context, in *idNameIn) (*which, error) {
		return &which{Route: "id", Value: in.ID}, nil
	}, geta.Doc{})}
	app, err := geta.New(geta.Table{Routes: []geta.Entry{{Path: "/users/{id}", Route: echo}}})
	if err != nil {
		t.Fatal(err)
	}
	var after string
	mux := http.NewServeMux()
	mux.HandleFunc("/{id}/", func(w http.ResponseWriter, r *http.Request) {
		http.StripPrefix("/"+r.PathValue("id"), app).ServeHTTP(w, r)
		after = r.PathValue("id")
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/outer/users/inner", nil))
	if rec.Code != 200 || rec.Body.String() != `{"route":"id","value":"inner"}` || after != "outer" {
		t.Fatalf("%d %s; outer value after geta: %q", rec.Code, rec.Body, after)
	}
}

// A literal is written unescaped, and a request reaches it however the
// client escaped it.
func TestUnescapedLiteralsMatchEscapedRequests(t *testing.T) {
	c := getatest.New(t, one("/café", get(okHandler)))
	for _, path := range []string{"/caf%C3%A9", "/café"} {
		if res := c.Get(path); res.Status != 200 {
			t.Errorf("%s: %d", path, res.Status)
		}
	}
}

// routeHit is what an operation of FuzzRouter echoes: which method and
// template answered, and the path values it bound, in path order.
type routeHit struct {
	Method   string   `json:"method"`
	Template string   `json:"template"`
	Values   []string `json:"values"`
}

// pathVals is an input that binds a template's parameters; vals lists them
// in path order. A template of FuzzRouter has at most three segments, and the
// parameter at segment i is named xi or yi, so these 27 inputs bind every
// template it generates.
type pathVals interface{ vals() []string }

type rpNone struct{}
type rpX0 struct {
	A string `path:"x0"`
}
type rpY0 struct {
	A string `path:"y0"`
}
type rpX1 struct {
	A string `path:"x1"`
}
type rpY1 struct {
	A string `path:"y1"`
}
type rpX2 struct {
	A string `path:"x2"`
}
type rpY2 struct {
	A string `path:"y2"`
}
type rpX0X1 struct {
	A string `path:"x0"`
	B string `path:"x1"`
}
type rpX0Y1 struct {
	A string `path:"x0"`
	B string `path:"y1"`
}
type rpY0X1 struct {
	A string `path:"y0"`
	B string `path:"x1"`
}
type rpY0Y1 struct {
	A string `path:"y0"`
	B string `path:"y1"`
}
type rpX0X2 struct {
	A string `path:"x0"`
	B string `path:"x2"`
}
type rpX0Y2 struct {
	A string `path:"x0"`
	B string `path:"y2"`
}
type rpY0X2 struct {
	A string `path:"y0"`
	B string `path:"x2"`
}
type rpY0Y2 struct {
	A string `path:"y0"`
	B string `path:"y2"`
}
type rpX1X2 struct {
	A string `path:"x1"`
	B string `path:"x2"`
}
type rpX1Y2 struct {
	A string `path:"x1"`
	B string `path:"y2"`
}
type rpY1X2 struct {
	A string `path:"y1"`
	B string `path:"x2"`
}
type rpY1Y2 struct {
	A string `path:"y1"`
	B string `path:"y2"`
}
type rpX0X1X2 struct {
	A string `path:"x0"`
	B string `path:"x1"`
	C string `path:"x2"`
}
type rpX0X1Y2 struct {
	A string `path:"x0"`
	B string `path:"x1"`
	C string `path:"y2"`
}
type rpX0Y1X2 struct {
	A string `path:"x0"`
	B string `path:"y1"`
	C string `path:"x2"`
}
type rpX0Y1Y2 struct {
	A string `path:"x0"`
	B string `path:"y1"`
	C string `path:"y2"`
}
type rpY0X1X2 struct {
	A string `path:"y0"`
	B string `path:"x1"`
	C string `path:"x2"`
}
type rpY0X1Y2 struct {
	A string `path:"y0"`
	B string `path:"x1"`
	C string `path:"y2"`
}
type rpY0Y1X2 struct {
	A string `path:"y0"`
	B string `path:"y1"`
	C string `path:"x2"`
}
type rpY0Y1Y2 struct {
	A string `path:"y0"`
	B string `path:"y1"`
	C string `path:"y2"`
}

func (rpNone) vals() []string   { return []string{} }
func (p rpX0) vals() []string   { return []string{p.A} }
func (p rpY0) vals() []string   { return []string{p.A} }
func (p rpX1) vals() []string   { return []string{p.A} }
func (p rpY1) vals() []string   { return []string{p.A} }
func (p rpX2) vals() []string   { return []string{p.A} }
func (p rpY2) vals() []string   { return []string{p.A} }
func (p rpX0X1) vals() []string { return []string{p.A, p.B} }
func (p rpX0Y1) vals() []string { return []string{p.A, p.B} }
func (p rpY0X1) vals() []string { return []string{p.A, p.B} }
func (p rpY0Y1) vals() []string { return []string{p.A, p.B} }
func (p rpX0X2) vals() []string { return []string{p.A, p.B} }
func (p rpX0Y2) vals() []string { return []string{p.A, p.B} }
func (p rpY0X2) vals() []string { return []string{p.A, p.B} }
func (p rpY0Y2) vals() []string { return []string{p.A, p.B} }
func (p rpX1X2) vals() []string { return []string{p.A, p.B} }
func (p rpX1Y2) vals() []string { return []string{p.A, p.B} }
func (p rpY1X2) vals() []string { return []string{p.A, p.B} }
func (p rpY1Y2) vals() []string { return []string{p.A, p.B} }

func (p rpX0X1X2) vals() []string { return []string{p.A, p.B, p.C} }
func (p rpX0X1Y2) vals() []string { return []string{p.A, p.B, p.C} }
func (p rpX0Y1X2) vals() []string { return []string{p.A, p.B, p.C} }
func (p rpX0Y1Y2) vals() []string { return []string{p.A, p.B, p.C} }
func (p rpY0X1X2) vals() []string { return []string{p.A, p.B, p.C} }
func (p rpY0X1Y2) vals() []string { return []string{p.A, p.B, p.C} }
func (p rpY0Y1X2) vals() []string { return []string{p.A, p.B, p.C} }
func (p rpY0Y1Y2) vals() []string { return []string{p.A, p.B, p.C} }

// echoOp is an operation that records, in last, and answers which method and
// template it serves and the path values it bound.
func echoOp[In pathVals](method, tmpl, id string, last *routeHit) geta.Operation {
	return geta.Op(http.StatusOK, func(_ context.Context, in *In) (*routeHit, error) {
		*last = routeHit{Method: method, Template: tmpl, Values: (*in).vals()}
		h := *last
		return &h, nil
	}, geta.Doc{OperationID: id})
}

// echoOps builds echoOp for a template's parameter names, joined by commas.
var echoOps = map[string]func(method, tmpl, id string, last *routeHit) geta.Operation{
	"":   echoOp[rpNone],
	"x0": echoOp[rpX0], "y0": echoOp[rpY0], "x1": echoOp[rpX1], "y1": echoOp[rpY1], "x2": echoOp[rpX2], "y2": echoOp[rpY2],
	"x0,x1": echoOp[rpX0X1], "x0,y1": echoOp[rpX0Y1], "y0,x1": echoOp[rpY0X1], "y0,y1": echoOp[rpY0Y1],
	"x0,x2": echoOp[rpX0X2], "x0,y2": echoOp[rpX0Y2], "y0,x2": echoOp[rpY0X2], "y0,y2": echoOp[rpY0Y2],
	"x1,x2": echoOp[rpX1X2], "x1,y2": echoOp[rpX1Y2], "y1,x2": echoOp[rpY1X2], "y1,y2": echoOp[rpY1Y2],
	"x0,x1,x2": echoOp[rpX0X1X2], "x0,x1,y2": echoOp[rpX0X1Y2], "x0,y1,x2": echoOp[rpX0Y1X2], "x0,y1,y2": echoOp[rpX0Y1Y2],
	"y0,x1,x2": echoOp[rpY0X1X2], "y0,x1,y2": echoOp[rpY0X1Y2], "y0,y1,x2": echoOp[rpY0Y1X2], "y0,y1,y2": echoOp[rpY0Y1Y2],
}

// fuzzBytes hands out the fuzzer's bytes one at a time, then zeros.
type fuzzBytes []byte

func (b *fuzzBytes) next() int {
	if len(*b) == 0 {
		return 0
	}
	c := (*b)[0]
	*b = (*b)[1:]
	return int(c)
}

// refTemplate is one generated template, as the reference reads it.
type refTemplate struct {
	path    string
	lits    []string // the literal at each segment; "" at a parameter
	param   []bool   // whether each segment is a parameter
	names   []string // parameter names, in path order
	methods []string
}

var (
	fuzzRouteMethods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, geta.MethodQuery}
	fuzzReqMethods   = []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, geta.MethodQuery}
	// Escaped request segments: literals plain and encoded, a parameter's
	// value, an encoded slash, the segments a clean path does not have, and
	// encoded dot segments, which are ordinary ones.
	fuzzReqSegments = []string{"a", "b", "c", "x", "%61", "%62", "a%2Fb", "%2F", "", ".", "..", "%2E", "a%20b", "%7Ba%7D", "%2E%2E"}
)

// genTemplates reads up to six templates of up to three segments, each a
// literal a, b, or c or a parameter xi or yi, serving a non-empty set of
// methods. A template whose segments repeat an earlier one's, up to
// parameter names, is skipped: the table may hold each set of paths once.
func genTemplates(b *fuzzBytes) []refTemplate {
	var out []refTemplate
	shapes := map[string]bool{}
	for range 1 + b.next()%6 {
		var t refTemplate
		var segs []string
		shape := ""
		for i := range b.next() % 4 {
			switch c := b.next() % 5; {
			case c < 3:
				lit := []string{"a", "b", "c"}[c]
				segs = append(segs, lit)
				t.lits, t.param = append(t.lits, lit), append(t.param, false)
				shape += "/" + lit
			default:
				name := []string{"x", "y"}[c-3] + fmt.Sprint(i)
				segs = append(segs, "{"+name+"}")
				t.lits, t.param = append(t.lits, ""), append(t.param, true)
				t.names = append(t.names, name)
				shape += "/{}"
			}
		}
		t.path = "/" + strings.Join(segs, "/")
		mask := b.next()%63 + 1
		for i, m := range fuzzRouteMethods {
			if mask&(1<<i) != 0 {
				t.methods = append(t.methods, m)
			}
		}
		if shapes[shape] {
			continue
		}
		shapes[shape] = true
		out = append(out, t)
	}
	return out
}

// match reports whether decoded path segments are a path of t, and its
// parameter values: a literal equals its segment, a parameter takes any
// segment but the empty one.
func (t *refTemplate) match(segs []string) ([]string, bool) {
	if len(segs) != len(t.lits) {
		return nil, false
	}
	vals := []string{}
	for i, s := range segs {
		switch {
		case !t.param[i] && s != t.lits[i]:
			return nil, false
		case t.param[i] && s == "":
			return nil, false
		case t.param[i]:
			vals = append(vals, s)
		}
	}
	return vals, true
}

// literalFirst orders two templates that match one path: at the first
// segment where they differ, the literal comes first.
func literalFirst(a, b *refTemplate) bool {
	for i := range a.param {
		if a.param[i] != b.param[i] {
			return !a.param[i]
		}
	}
	return false
}

// refClean resolves . and .. and drops empty segments of an escaped path,
// keeping a trailing slash.
func refClean(escaped string) string {
	var stack []string
	for _, s := range strings.Split(escaped[1:], "/") {
		switch s {
		case "", ".":
		case "..":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		default:
			stack = append(stack, s)
		}
	}
	out := "/" + strings.Join(stack, "/")
	if strings.HasSuffix(escaped, "/") && out != "/" {
		out += "/"
	}
	return out
}

type routeWant struct {
	status   int
	hit      routeHit
	allow    string
	location string
}

// refRoute is what a request should get, worked out from the templates
// alone.
func refRoute(tmpls []refTemplate, method, escaped string) routeWant {
	if c := refClean(escaped); c != escaped {
		return routeWant{status: http.StatusTemporaryRedirect, location: c}
	}
	var segs []string
	if escaped != "/" {
		for _, s := range strings.Split(escaped[1:], "/") {
			u, err := url.PathUnescape(s)
			if err != nil {
				return routeWant{status: http.StatusNotFound}
			}
			segs = append(segs, u)
		}
	}
	var best *refTemplate
	var want routeWant
	allowed := map[string]bool{}
	for i := range tmpls {
		t := &tmpls[i]
		vals, ok := t.match(segs)
		if !ok {
			continue
		}
		// Every template that matches serves OPTIONS, which lists them all.
		for _, m := range t.methods {
			allowed[m] = true
			if m == http.MethodGet {
				allowed[http.MethodHead] = true
			}
		}
		allowed[http.MethodOptions] = true
		serving := method
		if method == http.MethodHead {
			serving = http.MethodGet
		}
		if !slices.Contains(t.methods, serving) {
			continue
		}
		if best == nil || literalFirst(t, best) {
			best = t
			want = routeWant{status: http.StatusOK, hit: routeHit{Method: serving, Template: t.path, Values: vals}}
		}
	}
	if best != nil {
		return want
	}
	if len(allowed) == 0 {
		return routeWant{status: http.StatusNotFound}
	}
	ms := make([]string, 0, len(allowed))
	for m := range allowed {
		ms = append(ms, m)
	}
	slices.Sort(ms)
	if method == http.MethodOptions {
		return routeWant{status: http.StatusNoContent, allow: strings.Join(ms, ", ")}
	}
	return routeWant{status: http.StatusMethodNotAllowed, allow: strings.Join(ms, ", ")}
}

// documentedAllow reports whether the document's OPTIONS operation on
// template lists allow as a value of its Allow header.
func documentedAllow(document map[string]any, template, allow string) bool {
	v := any(document)
	for _, k := range []string{"paths", template, "options", "responses", "204", "headers", "Allow", "schema"} {
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		v = m[k]
	}
	schema, _ := v.(map[string]any)
	if schema["const"] == allow {
		return true
	}
	enum, _ := schema["enum"].([]any)
	return slices.Contains(enum, any(allow))
}

func sameHit(a, b routeHit) bool {
	return a.Method == b.Method && a.Template == b.Template && slices.Equal(a.Values, b.Values)
}

// FuzzRouter builds a table of generated templates and sends it generated
// requests. Every answer agrees with refRoute, an independent statement of
// the rules: an unclean path is redirected; among the templates that match
// the decoded segments and serve the method (GET for HEAD), the one with a
// literal at the first segment where they differ answers, with its values;
// a path matched only under other methods is a 405 listing them all, and
// OPTIONS on it a 204 listing the same; any other is a 404. The whole input
// after "//", whatever its bytes, is redirected with no body to a Location
// on the request's host.
func FuzzRouter(f *testing.F) {
	// Templates: a count, then per template a depth, its segments (0-2 a
	// literal a, b, c; 3 and 4 a parameter xi, yi), and a method mask less
	// one (bit 0 GET, 1 POST, 2 PUT, 3 PATCH, 4 DELETE, 5 QUERY). Then
	// requests: a method (fuzzReqMethods), a segment count, and segments
	// (fuzzReqSegments). More seeds are in testdata/fuzz/FuzzRouter.
	f.Add([]byte{
		3, // /a/b/{y2} GET, /a/{x1}/c GET, /a/{x1} GET POST, /a/b PUT
		3, 0, 1, 4, 0,
		3, 0, 3, 2, 0,
		2, 0, 3, 2,
		2, 0, 1, 3,
		0, 3, 0, 1, 2, // GET /a/b/c: both 3-segment templates match; the literal b wins
		0, 2, 0, 1, // GET /a/b: the literal serves only PUT, so the parameter answers
		5, 2, 0, 1, // DELETE /a/b: 405, GET, HEAD, POST, PUT
		1, 2, 4, 6, // HEAD /%61/a%2Fb
		0, 3, 0, 8, 2, // GET /a//c: redirect
		0, 2, 0, 8, // GET /a/: 404
		6, 1, 9, // OPTIONS /.: redirect
	})
	f.Add([]byte{
		1, // / GET, /{x0} GET PUT
		0, 0,
		1, 3, 4,
		0, 0, // GET /
		2, 0, // POST /: 405
		0, 1, 7, // GET /%2F
		0, 1, 13, // GET /%7Ba%7D
		3, 2, 3, 10, // PUT /x/..: redirect
	})
	f.Add([]byte{})
	f.Add([]byte("\\evil.example/x?q")) // the redirect of //\evil.example/x stays on the host
	f.Add([]byte("\t/evil.example"))
	f.Fuzz(func(t *testing.T, data []byte) {
		b := fuzzBytes(data)
		tmpls := genTemplates(&b)
		var last routeHit
		var entries []geta.Entry
		var desc []string
		for i, tm := range tmpls {
			mk := echoOps[strings.Join(tm.names, ",")]
			var r geta.Route
			for _, m := range tm.methods {
				op := mk(m, tm.path, fmt.Sprintf("op%d%s", i, m), &last)
				switch m {
				case http.MethodGet:
					r.Get = op
				case http.MethodPost:
					r.Post = op
				case http.MethodPut:
					r.Put = op
				case http.MethodPatch:
					r.Patch = op
				case http.MethodDelete:
					r.Delete = op
				case geta.MethodQuery:
					r.Query = op
				}
			}
			entries = append(entries, geta.Entry{Path: tm.path, Route: r})
			desc = append(desc, strings.Join(tm.methods, ",")+" "+tm.path)
		}
		app, err := geta.New(geta.Table{Routes: entries}, geta.WithOpenAPI(geta.OpenAPI32))
		if err != nil {
			t.Fatalf("%v: %v", desc, err)
		}
		var document map[string]any
		if err := json.Unmarshal(app.OpenAPI(), &document); err != nil {
			t.Fatal(err)
		}
		for i := 0; i == 0 || (len(b) > 0 && i < 8); i++ {
			method := fuzzReqMethods[b.next()%len(fuzzReqMethods)]
			escaped := "/"
			if n := b.next() % 5; n > 0 {
				segs := make([]string, n)
				for j := range segs {
					segs[j] = fuzzReqSegments[b.next()%len(fuzzReqSegments)]
				}
				escaped = "/" + strings.Join(segs, "/")
			}
			unescaped, err := url.PathUnescape(escaped)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(method, "/", nil)
			req.URL.Path, req.URL.RawPath, req.RequestURI = unescaped, escaped, escaped
			if req.URL.EscapedPath() != escaped {
				t.Fatalf("request path %q escapes as %q", escaped, req.URL.EscapedPath())
			}
			want := refRoute(tmpls, method, escaped)
			last = routeHit{}
			rec := httptest.NewRecorder()
			ctx, read := geta.Observe(req.Context())
			app.ServeHTTP(rec, req.WithContext(ctx))
			where := fmt.Sprintf("%s %s on %v", method, escaped, desc)
			if rec.Code != want.status {
				t.Fatalf("%s: status %d, want %d (%+v): %s", where, rec.Code, want.status, want, rec.Body)
			}
			if want.status != http.StatusOK && last.Template != "" {
				t.Fatalf("%s: %+v answered a %d", where, last, want.status)
			}
			switch want.status {
			case http.StatusOK:
				if !sameHit(last, want.hit) {
					t.Fatalf("%s: answered by %+v, want %+v", where, last, want.hit)
				}
				if method != http.MethodHead {
					var got routeHit
					if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || !sameHit(got, want.hit) {
						t.Fatalf("%s: body %s, want %+v (%v)", where, rec.Body, want.hit, err)
					}
				}
			case http.StatusMethodNotAllowed, http.StatusNoContent:
				if got := rec.Header().Get("Allow"); got != want.allow {
					t.Fatalf("%s: Allow %q, want %q", where, got, want.allow)
				}
				if want.status == http.StatusNoContent {
					_, m, ok := read()
					if !ok || m.Method != http.MethodOptions {
						t.Fatalf("%s: served as %+v %v", where, m, ok)
					}
					if !documentedAllow(document, m.Template, want.allow) {
						t.Fatalf("%s: the document of OPTIONS %s does not list Allow %q", where, m.Template, want.allow)
					}
				}
			case http.StatusTemporaryRedirect:
				if got := rec.Header().Get("Location"); got != want.location {
					t.Fatalf("%s: Location %q, want %q", where, got, want.location)
				}
			}
		}
		// The whole input as the rest of a path after "//", which is never
		// clean: as a request-target the server read, when it reads as one,
		// and as the path of a request made in process. Wherever it is
		// redirected, Location stays on the request's host.
		raw := "//" + string(data)
		if u, err := url.ParseRequestURI(raw); err == nil {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.URL, req.RequestURI = u, raw
			redirectStaysOnHost(t, app, req)
		}
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.URL.Path, req.RequestURI = raw, ""
		redirectStaysOnHost(t, app, req)
	})
}

// redirectStaysOnHost serves req, whose path is not clean, and fails unless
// the answer is a 307 with no body whose Location, resolved against the
// request's URL, names the request's host.
func redirectStaysOnHost(t *testing.T, app http.Handler, req *http.Request) {
	t.Helper()
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusTemporaryRedirect || rec.Body.Len() != 0 {
		t.Fatalf("%q (path %q): status %d, body %q, want a 307 with no body", req.RequestURI, req.URL.Path, rec.Code, rec.Body)
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("%q (path %q): Location %q does not parse: %v", req.RequestURI, req.URL.Path, loc, err)
	}
	base := &url.URL{Scheme: "http", Host: "geta.test", Path: "/"}
	if got := base.ResolveReference(u); got.Host != base.Host || !strings.HasPrefix(loc, "/") ||
		strings.HasPrefix(loc, "//") || strings.ContainsAny(strings.SplitN(loc, "?", 2)[0], "\\\t\r\n") {
		t.Fatalf("%q (path %q): Location %q leaves the host (resolves to %q)", req.RequestURI, req.URL.Path, loc, got)
	}
}

// A root scope that rewrites the path has the request matched again, and
// the handler binds the values of the new path.
func TestRootRewriteIsMatchedAgain(t *testing.T) {
	rewrite := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rest, ok := strings.CutPrefix(r.URL.Path, "/alias/"); ok {
				r = r.Clone(r.Context())
				r.URL.Path = "/users/" + rest + "x"
			}
			next.ServeHTTP(w, r)
		})
	})
	echo := geta.Route{Get: geta.Op(http.StatusOK, func(ctx context.Context, in *idNameIn) (*which, error) {
		return &which{Route: "id", Value: in.ID}, nil
	}, geta.Doc{})}
	c := getatest.New(t, geta.Table{Root: geta.Scope{rewrite}, Routes: []geta.Entry{
		{Path: "/users/{id}", Route: echo},
		{Path: "/alias/{id}", Route: echo},
	}})
	for path, want := range map[string]string{
		"/users/7": `{"route":"id","value":"7"}`,
		"/alias/7": `{"route":"id","value":"7x"}`,
	} {
		if res := c.Get(path); res.Status != 200 || res.Text() != want {
			t.Errorf("%s: %d %s, want %s", path, res.Status, res.Body, want)
		}
	}
}

// Mounted under a ServeMux, with or without a wildcard in the outer
// pattern, middleware sees the outer pattern's values beside geta's, the
// handler binds geta's values, and a value middleware sets is the one bound.
func TestPathValuesWhenMounted(t *testing.T) {
	var seen string
	look := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = r.Pattern + " tenant=" + r.PathValue("tenant") + " id=" + r.PathValue("id")
			if r.URL.Query().Has("override") {
				r.SetPathValue("id", "set")
			}
			next.ServeHTTP(w, r)
		})
	})
	echo := geta.Route{Get: geta.Op(http.StatusOK, func(ctx context.Context, in *idNameIn) (*which, error) {
		return &which{Route: "id", Value: in.ID}, nil
	}, geta.Doc{})}
	app, err := geta.New(geta.Table{Root: geta.Scope{look}, Routes: []geta.Entry{{Path: "/users/{id}", Route: echo}}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/plain/", http.StripPrefix("/plain", app))
	mux.HandleFunc("/t/{tenant}/", func(w http.ResponseWriter, r *http.Request) {
		http.StripPrefix("/t/"+r.PathValue("tenant"), app).ServeHTTP(w, r)
	})
	for _, c := range []struct {
		h                http.Handler
		path, seen, body string
	}{
		{app, "/users/7", "GET /users/{id} tenant= id=7", `{"route":"id","value":"7"}`},
		{app, "/users/a%2Fb", "GET /users/{id} tenant= id=a/b", `{"route":"id","value":"a/b"}`},
		{app, "/users/7?override", "GET /users/{id} tenant= id=7", `{"route":"id","value":"set"}`},
		{mux, "/plain/users/7", "GET /users/{id} tenant= id=7", `{"route":"id","value":"7"}`},
		{mux, "/plain/users/7?override", "GET /users/{id} tenant= id=7", `{"route":"id","value":"set"}`},
		{mux, "/t/acme/users/7", "GET /users/{id} tenant=acme id=7", `{"route":"id","value":"7"}`},
		{mux, "/t/acme/users/7?override", "GET /users/{id} tenant=acme id=7", `{"route":"id","value":"set"}`},
	} {
		rec := httptest.NewRecorder()
		c.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != 200 || rec.Body.String() != c.body || seen != c.seen {
			t.Errorf("%s: %d %s, saw %q; want %s, %q", c.path, rec.Code, rec.Body, seen, c.body, c.seen)
		}
	}
}

// A %2F stays in its segment beside a byte net/http takes raw in a
// request-target but url.URL.EscapedPath would escape ('"', '{', '|', past
// ASCII): EscapedPath then drops the client's escaping and the %2F, read as
// a '/', would split the segment, binding another value (or none, with a
// leading %2F read as an unclean // the client never sent).
func TestAnEscapedSlashBesideARawByteStaysInItsSegment(t *testing.T) {
	echo := geta.Route{Get: geta.Op(http.StatusOK, func(ctx context.Context, in *idNameIn) (*which, error) {
		return &which{Route: "id", Value: in.ID}, nil
	}, geta.Doc{})}
	app := accepts(t, geta.Table{Routes: []geta.Entry{{Path: "/users/{id}", Route: echo}}})
	for target, want := range map[string]string{
		`/users/a%2Fb"`:        `a/b\"`,
		`/users/%2F"`:          `/\"`,
		`/users/a%2Fb{x}`:      `a/b{x}`,
		`/users/a%2Fb|`:        `a/b|`,
		"/users/a%2Fb\xc3\xa9": "a/b\xc3\xa9",
		`/users/%2F%2F{`:       `//{`,
	} {
		req, err := http.ReadRequest(bufio.NewReader(strings.NewReader("GET " + target + " HTTP/1.1\r\nHost: geta.test\r\n\r\n")))
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if body := `{"route":"id","value":"` + want + `"}`; rec.Code != 200 || rec.Body.String() != body {
			t.Errorf("%s: %d %s, want %s", target, rec.Code, rec.Body, body)
		}
	}
}

// A CONNECT request in authority form has an empty path. It matches nothing,
// and geta answers as ServeMux does, without panicking.
func TestConnectAuthorityFormMatchesNothing(t *testing.T) {
	app, err := geta.New(one("/x", get(okHandler)), geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /x", func(http.ResponseWriter, *http.Request) {})
	for _, target := range []string{"example.com:443", "/x"} {
		req := httptest.NewRequest(http.MethodConnect, target, nil)
		rec := httptest.NewRecorder()
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("%s: ServeHTTP panicked: %v", target, p)
				}
			}()
			app.ServeHTTP(rec, req)
		}()
		mrec := httptest.NewRecorder()
		mux.ServeHTTP(mrec, httptest.NewRequest(http.MethodConnect, target, nil))
		// geta answers OPTIONS on every path it serves, and its Allow says so.
		want := mrec.Header().Get("Allow")
		if want != "" {
			ms := append(strings.Split(want, ", "), http.MethodOptions)
			slices.Sort(ms)
			want = strings.Join(ms, ", ")
		}
		if rec.Code != mrec.Code || rec.Header().Get("Allow") != want {
			t.Fatalf("%s: geta %d Allow %q, ServeMux %d Allow %q, want %q", target, rec.Code, rec.Header().Get("Allow"),
				mrec.Code, mrec.Header().Get("Allow"), want)
		}
	}
}
