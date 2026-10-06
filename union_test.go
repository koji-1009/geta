package geta_test

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaclient"
	"github.com/koji-1009/geta/getatest"
)

type shape interface{ isShape() }

type circle struct {
	Kind   string  `json:"kind"`
	Radius float64 `json:"radius" schema:"minimum=0"`
}

type square struct {
	Kind  string `json:"kind"`
	Side  int    `json:"side"`
	Inner shape  `json:"inner,omitzero"`
}

func (circle) isShape() {}
func (square) isShape() {}

var shapes = geta.Sealed[shape]("kind", geta.Case[circle]("circle"), geta.Case[square]("square"))

type drawing struct {
	Main   shape   `json:"main"`
	Others []shape `json:"others"`
}

type drawingIn struct {
	Body drawing `body:"json"`
}

func drawingApp(t testing.TB, h func(context.Context, *drawingIn) (*drawing, error)) *getatest.Client {
	return getatest.New(t, one("/d", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}), geta.WithUnion(shapes))
}

func echoDrawing(ctx context.Context, in *drawingIn) (*drawing, error) { return &in.Body, nil }

// A sealed type is read into the variant its discriminator selects, written
// back as that variant, and documented as oneOf with a mapping.
func TestSealedTypeRoundTrip(t *testing.T) {
	var got drawing
	c := drawingApp(t, func(ctx context.Context, in *drawingIn) (*drawing, error) {
		got = in.Body
		return &in.Body, nil
	})
	body := `{"main":{"kind":"square","side":2,"inner":{"kind":"circle","radius":1.5}},"others":[{"kind":"circle","radius":3}]}`
	res := c.Post("/d", body)
	if res.Status != 200 || res.Text() != body {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
	sq, ok := got.Main.(square)
	if !ok || sq.Side != 2 || sq.Inner.(circle).Radius != 1.5 || got.Others[0].(circle).Radius != 3 {
		t.Fatalf("%#v", got)
	}

	m := doc(t, c.App())
	if s := compact(t, at(t, m, "components", "schemas", "shape")); s !=
		`{"discriminator":{"mapping":{"circle":"#/components/schemas/circle","square":"#/components/schemas/square"},"propertyName":"kind"},"oneOf":[{"$ref":"#/components/schemas/circle"},{"$ref":"#/components/schemas/square"}]}` {
		t.Fatal(s)
	}
	if s := compact(t, at(t, m, "components", "schemas", "drawing", "properties", "main")); s != `{"$ref":"#/components/schemas/shape"}` {
		t.Fatal(s)
	}
	// The variants' strings state the backstops where a request is read, so
	// the request's schemas are components of their own, the discriminator
	// mapping among them naming the request's variants.
	if s := compact(t, at(t, m, "components", "schemas", "shape-Input")); s !=
		`{"discriminator":{"mapping":{"circle":"#/components/schemas/circle-Input","square":"#/components/schemas/square-Input"},"propertyName":"kind"},"oneOf":[{"$ref":"#/components/schemas/circle-Input"},{"$ref":"#/components/schemas/square-Input"}]}` {
		t.Fatal(s)
	}
	if s := compact(t, at(t, m, "components", "schemas", "drawing-Input", "properties", "main")); s != `{"$ref":"#/components/schemas/shape-Input"}` {
		t.Fatal(s)
	}
	if s := compact(t, at(t, m, "paths", "/d", "post", "requestBody", "content", "application/json", "schema")); s != `{"$ref":"#/components/schemas/drawing-Input"}` {
		t.Fatal(s)
	}
	if s := compact(t, at(t, m, "components", "schemas", "circle", "properties", "kind")); s != `{"type":"string"}` {
		t.Fatal(s)
	}

	// The typed client reads the same variants.
	out, err := getaclient.Call[drawingIn, drawing](t.Context(), c.Typed(), http.MethodPost, "/d",
		&drawingIn{Body: drawing{Main: circle{Kind: "circle", Radius: 2}, Others: []shape{}}})
	if err != nil || out.Main.(circle).Radius != 2 {
		t.Fatal(out, err)
	}
}

// A request is checked against the variant its discriminator selects; what
// does not select one is named.
func TestSealedTypeViolations(t *testing.T) {
	c := drawingApp(t, echoDrawing)
	for body, want := range map[string]string{
		`{"main":{"radius":1},"others":[]}`:                          `"$.main.kind","message":"missing required member"`,
		`{"main":{"kind":7},"others":[]}`:                            `"$.main.kind","message":"discriminator must be a string"`,
		`{"main":{"kind":"hexagon"},"others":[]}`:                    `"$.main.kind","message":"unknown shape \"hexagon\"; one of circle, square"`,
		`{"main":{"kind":"circle","radius":-1},"others":[]}`:         `"$.main.radius","message":"-1 is less than minimum 0"`,
		`{"main":{"kind":"circle","radius":1,"side":2},"others":[]}`: `"$.main.side","message":"unknown member"`,
		`{"main":{"kind":"square"},"others":[]}`:                     `"$.main.side","message":"missing required member"`,
		`{"main":"circle","others":[]}`:                              `"$.main","message":"expected object, got string"`,
		`{"main":null,"others":[]}`:                                  `"$.main","message":"expected object, got null"`,
		`{"main":{"kind":"circle","radius":1},"others":[null]}`:      `"$.others[0]","message":"expected object, got null"`,
	} {
		res := c.Post("/d", body)
		if res.Status != 400 || !strings.Contains(res.Text(), want) {
			t.Errorf("%s: %d %s", body, res.Status, res.Body)
		}
	}
}

type triangle struct {
	Kind string `json:"kind"`
}

func (triangle) isShape() {}

// A response that is not a declared variant, carries another variant's tag,
// or leaves a required sealed value nil is a defect, never written.
func TestSealedTypeOutputDefects(t *testing.T) {
	for name, out := range map[string]drawing{
		"undeclared variant": {Main: triangle{Kind: "triangle"}, Others: []shape{}},
		"wrong tag":          {Main: circle{Kind: "square"}, Others: []shape{}},
		"nil required":       {Others: []shape{}},
		"nil element":        {Main: circle{Kind: "circle"}, Others: []shape{nil}},
		"nested wrong tag":   {Main: square{Kind: "square", Inner: square{Kind: "circle"}}, Others: []shape{}},
	} {
		c := drawingApp(t, func(context.Context, *drawingIn) (*drawing, error) { return &out, nil })
		res := c.Post("/d", `{"main":{"kind":"circle","radius":1},"others":[]}`)
		if res.Status != 500 || strings.Contains(res.Text(), "circle") {
			t.Errorf("%s: %d %s", name, res.Status, res.Body)
		}
	}
	// An optional sealed member left nil is omitted.
	c := drawingApp(t, func(context.Context, *drawingIn) (*drawing, error) {
		return &drawing{Main: square{Kind: "square", Side: 1}, Others: []shape{}}, nil
	})
	if res := c.Post("/d", `{"main":{"kind":"circle","radius":1},"others":[]}`); res.Text() != `{"main":{"kind":"square","side":1},"others":[]}` {
		t.Fatal(res.Text())
	}
}

type sqShape interface{ isSQShape() }

type sqSquare struct {
	Kind string `json:"kind"`
	Side int    `json:"side"`
}

func (sqSquare) isSQShape() {}

type sqCircle struct {
	Kind string `json:"kind"`
	R    int    `json:"r"`
}

func (sqCircle) isSQShape() {}

type sqTri struct {
	Kind string `json:"kind"`
}

func (sqTri) isSQShape() {}

// sqPlain implements sqShape but is no variant of it.
type sqPlain struct {
	N int `json:"n"`
}

func (sqPlain) isSQShape() {}

type sqA interface{ isSQA() }
type sqB interface{ isSQB() }

// sqBoth is a variant of sqA and implements sqB, of which it is no variant.
type sqBoth struct {
	Kind string `json:"kind"`
}

func (sqBoth) isSQA() {}
func (sqBoth) isSQB() {}

type sqOnlyB struct {
	Kind string `json:"kind"`
}

func (sqOnlyB) isSQB() {}

// A type the App names in its own right is written as itself wherever it
// implements a sealed type: a struct field's type, and a variant of one
// sealed type that implements another; alike on every assembly.
func TestSealedTypesWriteWhatTheAppNames(t *testing.T) {
	type plainOut struct {
		Main  sqShape `json:"main"`
		Plain sqPlain `json:"plain"`
	}
	app := newApp(t, geta.Table{Routes: []geta.Entry{{Path: "/x", Route: get(func(context.Context, *empty) (*plainOut, error) {
		return &plainOut{Main: sqSquare{Kind: "square", Side: 2}, Plain: sqPlain{1}}, nil
	})}}}, geta.WithUnion(geta.Sealed[sqShape]("kind", geta.Case[sqSquare]("square"))))
	if rec := do(t, app, "GET", "/x"); rec.Code != 200 || rec.Body.String() != `{"main":{"kind":"square","side":2},"plain":{"n":1}}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	type twoOut struct {
		A sqA `json:"a"`
		B sqB `json:"b"`
	}
	for range 20 {
		app := newApp(t, geta.Table{Routes: []geta.Entry{{Path: "/x", Route: get(func(context.Context, *empty) (*twoOut, error) {
			return &twoOut{A: sqBoth{"a"}, B: sqOnlyB{"b"}}, nil
		})}}}, geta.WithUnion(geta.Sealed[sqA]("kind", geta.Case[sqBoth]("a")), geta.Sealed[sqB]("kind", geta.Case[sqOnlyB]("b"))))
		if rec := do(t, app, "GET", "/x"); rec.Code != 200 || rec.Body.String() != `{"a":{"kind":"a"},"b":{"kind":"b"}}` {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
}

// Sealed copies its variants: changing the slice it was given changes
// neither the document nor what a request may hold.
func TestSealedKeepsItsVariants(t *testing.T) {
	vs := []geta.Variant{geta.Case[sqSquare]("square"), geta.Case[sqCircle]("circle")}
	u := geta.Sealed[sqShape]("kind", vs...)
	vs[1] = geta.Case[sqTri]("tri")
	type in struct {
		Body sqShape `body:"json"`
	}
	app := newApp(t, geta.Table{Routes: []geta.Entry{{Path: "/x", Route: post(func(context.Context, *in) (*ok, error) { return &ok{true}, nil })}}},
		geta.WithUnion(u))
	if strings.Contains(string(app.OpenAPI()), "sqTri") {
		t.Error("the document lists a variant set after Sealed returned")
	}
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"kind":"circle","r":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// newApp assembles tbl with opts, failing the test on a mistake.
func newApp(t *testing.T, tbl geta.Table, opts ...geta.Option) *geta.App {
	t.Helper()
	a, err := geta.New(tbl, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

type noKind struct {
	Name string `json:"name"`
}

func (noKind) isShape() {}

type optKind struct {
	Kind *string `json:"kind,omitzero"`
}

func (optKind) isShape() {}

type intKind struct {
	Kind int `json:"kind"`
}

func (intKind) isShape() {}

type notAShape struct {
	Kind string `json:"kind"`
}

// Each authoring mistake in a sealed type's declaration is refused at
// assembly: a non-interface, an unnamed discriminator, no variants, an empty
// or repeated tag, a repeated variant, a variant that is no named struct or
// does not implement the interface, a discriminator member missing,
// optional, or not a string, and a type declared twice.
func TestSealedTypeDeclarationMistakes(t *testing.T) {
	tbl := one("/d", geta.Route{Post: geta.Op(http.StatusOK, echoDrawing, geta.Doc{})})
	for name, c := range map[string]struct {
		u    []geta.Union
		want string
	}{
		"undeclared interface":     {nil, "interface type geta_test.shape has no JSON form; declare it with geta.WithUnion"},
		"not an interface":         {[]geta.Union{shapes, geta.Sealed[circle]("kind", geta.Case[circle]("c"))}, "is not an interface type"},
		"empty discriminator":      {[]geta.Union{geta.Sealed[shape]("", geta.Case[circle]("circle"))}, "the discriminator member must be named"},
		"no variants":              {[]geta.Union{geta.Sealed[shape]("kind")}, "needs at least one variant"},
		"empty tag":                {[]geta.Union{geta.Sealed[shape]("kind", geta.Case[circle](""))}, "has an empty tag"},
		"duplicate tag":            {[]geta.Union{geta.Sealed[shape]("kind", geta.Case[circle]("x"), geta.Case[square]("x"))}, `tag "x" names two variants`},
		"duplicate variant":        {[]geta.Union{geta.Sealed[shape]("kind", geta.Case[circle]("a"), geta.Case[circle]("b"))}, "is a variant twice"},
		"not a struct":             {[]geta.Union{geta.Sealed[shape]("kind", geta.Case[*circle]("circle"))}, "is not a named struct type"},
		"not implementing":         {[]geta.Union{geta.Sealed[shape]("kind", geta.Case[notAShape]("x"))}, "does not implement"},
		"no discriminator":         {[]geta.Union{geta.Sealed[shape]("kind", geta.Case[noKind]("x"))}, `does not declare the discriminator member "kind"`},
		"optional discriminator":   {[]geta.Union{geta.Sealed[shape]("kind", geta.Case[optKind]("x"))}, `the discriminator member "kind" is optional`},
		"non-string discriminator": {[]geta.Union{geta.Sealed[shape]("kind", geta.Case[intKind]("x"))}, `the discriminator member "kind" must be a string`},
		"declared twice":           {[]geta.Union{shapes, shapes}, "declared twice"},
	} {
		_, err := geta.New(tbl, geta.WithUnion(c.u...))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

type fzShape interface{ isFzShape() }

type fzDot struct {
	Kind string  `json:"kind"`
	R    float64 `json:"r" schema:"minimum=0"`
}

type fzLabel struct {
	Kind string `json:"kind"`
	Text string `json:"text" schema:"maxLength=8"`
}

type fzBox struct {
	Kind  string  `json:"kind"`
	Side  int32   `json:"side"`
	Inner fzShape `json:"inner,omitzero"` // nested inside a variant, optional
}

func (fzDot) isFzShape()   {}
func (fzLabel) isFzShape() {}
func (fzBox) isFzShape()   {}

var fzShapes = geta.Sealed[fzShape]("kind", geta.Case[fzDot]("dot"), geta.Case[fzLabel]("label"), geta.Case[fzBox]("box"))

type fzPicture struct {
	Main  fzShape   `json:"main"`
	Many  []fzShape `json:"many"`
	Extra fzShape   `json:"extra,omitzero"`
}

type fzPictureIn struct {
	Body fzPicture `body:"json"`
}

// fzVariants reports where the dynamic types of v disagree with the
// discriminators of raw, the body as plain JSON read it; "" if nowhere.
func fzVariants(raw any, v fzShape, path string) string {
	obj, ok := raw.(map[string]any)
	if !ok {
		return path + ": accepted a non-object"
	}
	kind, _ := obj["kind"].(string)
	switch s := v.(type) {
	case fzDot:
		if kind != "dot" || s.Kind != "dot" {
			return path + ": " + kind + " read as fzDot"
		}
	case fzLabel:
		if kind != "label" || s.Kind != "label" {
			return path + ": " + kind + " read as fzLabel"
		}
	case fzBox:
		if kind != "box" || s.Kind != "box" {
			return path + ": " + kind + " read as fzBox"
		}
		if inner, ok := obj["inner"]; ok {
			return fzVariants(inner, s.Inner, path+".inner")
		}
		if s.Inner != nil {
			return path + ".inner: absent, but read as a value"
		}
	default:
		return path + ": " + kind + " read as another type"
	}
	return ""
}

// FuzzSealed feeds arbitrary bodies to an operation whose input holds a
// sealed type required, in a slice, optional, and nested inside a variant.
// It answers 200, 400, or 413, never 500, and never panics. On 200 every
// value is the variant its discriminator names, and the echoed response is
// the input, canonicalised.
//
// The limits are low enough to reach within a few hundred bytes, the body
// limit (a 413) and the nesting ceiling, and a body is cut to twice the body
// limit (fzCut), a 413 as one just past the limit is.
func FuzzSealed(f *testing.F) {
	var got *fzPicture
	limits := geta.DefaultLimits
	limits.MaxBodyBytes = 256
	limits.MaxDepth = 16
	app, err := geta.New(one("/s", geta.Route{Post: geta.Op(http.StatusOK, func(_ context.Context, in *fzPictureIn) (*fzPicture, error) {
		got = &in.Body
		return &in.Body, nil
	}, geta.Doc{})}), geta.WithUnion(fzShapes), geta.WithLimits(limits))
	if err != nil {
		f.Fatal(err)
	}
	for _, s := range []string{
		`{"main":{"kind":"dot","r":1.5},"many":[]}`,
		`{"main":{"kind":"box","side":2,"inner":{"kind":"box","side":-3,"inner":{"kind":"label","text":"hi"}}},"many":[{"kind":"dot","r":0},{"kind":"label","text":""}],"extra":{"kind":"dot","r":1e2}}`,
		` { "many" : [ { "r" : 1.50 , "kind" : "dot" } ] , "main" : { "text" : "A😀" , "kind" : "label" } } `,
		`{"main":{"kind":"box","side":1,"inner":null},"many":[]}`,
		`{"main":{"kind":"dot","r":1},"many":[null],"extra":null}`,
		`{"main":{"kind":"dot","kind":"box","r":1},"many":[]}`,
		`{"main":{"kind":"box","side":2147483648},"many":[]}`,
		`{"main":{"kind":"box","side":1.0},"many":[]}`,
		`{"main":{"kind":"dot","r":-0},"many":[]}`,
		`{"main":{"kind":"dot","r":1e400},"many":[]}`,
		`{"main":{"kind":"label","text":"123456789"},"many":[]}`,
		`{"main":{"kind":"circle"},"many":[]}`,
		`{"main":{"kind":"box","side":1,"inner":{"kind":"box","side":1,"inner":{"kind":"box","side":1,"inner":{"kind":"box","side":1}}}},"many":[]}`,
		`{"main":{"kind":"dot","r":1},"many":[],"other":1}`,
		`[{"kind":"dot"}]`,
		`{"main":{"kind":"dot","r":1},"many":[]}{}`,
		"{\"main\":{\"kind\":\"label\",\"text\":\"\xff\"},\"many\":[]}",
		``,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		body = fzCut(body, 2*int(limits.MaxBodyBytes))
		got = nil
		req := httptest.NewRequest(http.MethodPost, "/s", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		switch rec.Code {
		case 200:
		case 400, 413:
			return
		default:
			t.Fatalf("status %d for %q: %s", rec.Code, body, rec.Body)
		}
		var raw map[string]any
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatalf("accepted %q, which plain JSON refuses: %v", body, err)
		}
		if got == nil {
			t.Fatalf("200 for %q without calling the handler", body)
		}
		if msg := fzVariants(raw["main"], got.Main, "$.main"); msg != "" {
			t.Fatalf("%q: %s", body, msg)
		}
		many, _ := raw["many"].([]any)
		if len(many) != len(got.Many) {
			t.Fatalf("%q: many has %d values, read as %d", body, len(many), len(got.Many))
		}
		for i, el := range many {
			if msg := fzVariants(el, got.Many[i], fmt.Sprintf("$.many[%d]", i)); msg != "" {
				t.Fatalf("%q: %s", body, msg)
			}
		}
		if extra, ok := raw["extra"]; ok {
			if msg := fzVariants(extra, got.Extra, "$.extra"); msg != "" {
				t.Fatalf("%q: %s", body, msg)
			}
		} else if got.Extra != nil {
			t.Fatalf("%q: extra absent, but read as %#v", body, got.Extra)
		}
		want := jsontext.Value(bytes.Clone(body))
		if err := want.Canonicalize(); err != nil {
			t.Fatalf("accepted %q, which does not canonicalise: %v", body, err)
		}
		echoed := jsontext.Value(bytes.Clone(rec.Body.Bytes()))
		if err := echoed.Canonicalize(); err != nil {
			t.Fatalf("response %s does not canonicalise: %v", rec.Body, err)
		}
		if !bytes.Equal(want, echoed) {
			t.Fatalf("echoed %s for %q; canonically %s, want %s", rec.Body, body, echoed, want)
		}
	})
}

// A sealed type whose variant holds the sealed type again, so a nil inside
// a variant is reached through the variant.
type treeNode interface{ isTreeNode() }

type treeLeaf struct {
	Kind string `json:"kind"`
}

type treePair struct {
	Kind  string   `json:"kind"`
	Child treeNode `json:"child"`
}

func (treeLeaf) isTreeNode() {}
func (treePair) isTreeNode() {}

var treeNodes = geta.Sealed[treeNode]("kind", geta.Case[treeLeaf]("leaf"), geta.Case[treePair]("pair"))

type treeOut struct {
	Root treeNode `json:"root"`
}

func treeServe(t *testing.T, root treeNode) *httptest.ResponseRecorder {
	t.Helper()
	h := func(context.Context, *empty) (*treeOut, error) { return &treeOut{Root: root}, nil }
	a, err := geta.New(one("/n", get(h)), geta.WithUnion(treeNodes), geta.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/n", nil))
	return w
}

// A required sealed value left nil inside a variant is a 500 whether the
// variant is held by value or by pointer.
func TestNilUnionInsideAPointerVariant(t *testing.T) {
	for name, root := range map[string]treeNode{
		"value":   treePair{Kind: "pair"},
		"pointer": &treePair{Kind: "pair"},
		"nested":  &treePair{Kind: "pair", Child: &treePair{Kind: "pair"}},
	} {
		if w := treeServe(t, root); w.Code != 500 || strings.Contains(w.Body.String(), "null") {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if w := treeServe(t, &treePair{Kind: "pair", Child: &treeLeaf{Kind: "leaf"}}); w.Code != 200 ||
		strings.TrimSpace(w.Body.String()) != `{"root":{"kind":"pair","child":{"kind":"leaf"}}}` {
		t.Errorf("a complete pointer variant: %d %s", w.Code, w.Body)
	}
}

// The defect logged for a required sealed value left nil names its path.
func TestNilUnionNamesItsPath(t *testing.T) {
	for want, out := range map[string]any{
		"$.root.child.child: a required geta_test.treeNode is nil":                    &treeOut{Root: treePair{Kind: "pair", Child: &treePair{Kind: "pair"}}},
		"$.others[1]: a required geta_test.shape is nil":                              &drawing{Main: circle{Kind: "circle"}, Others: []shape{circle{Kind: "circle"}, nil}},
		"$.byName.b: a required geta_test.shape is nil":                               &shapeMap{ByName: map[string]shape{"b": nil}},
		"$.root.child: a required geta_test.treeNode holds a nil *geta_test.treePair": &treeOut{Root: treePair{Kind: "pair", Child: (*treePair)(nil)}},
	} {
		var log bytes.Buffer
		var tbl geta.Table
		switch o := out.(type) {
		case *treeOut:
			tbl = one("/n", get(func(context.Context, *empty) (*treeOut, error) { return o, nil }))
		case *drawing:
			tbl = one("/n", get(func(context.Context, *empty) (*drawing, error) { return o, nil }))
		case *shapeMap:
			tbl = one("/n", get(func(context.Context, *empty) (*shapeMap, error) { return o, nil }))
		}
		a, err := geta.New(tbl, geta.WithUnion(treeNodes), geta.WithUnion(shapes), geta.WithLogger(slog.New(slog.NewTextHandler(&log, nil))))
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/n", nil))
		if w.Code != 500 || !strings.Contains(log.String(), want) {
			t.Errorf("%d; log %s; want %q", w.Code, log.String(), want)
		}
	}
}

type shapeMap struct {
	ByName map[string]shape `json:"byName"`
}

// A response checks for nil sealed values only where one can be: a long list
// of strings beside one costs no allocation per element.
func TestNilUnionSkipsWhatHoldsNone(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts do not hold under the race detector")
	}
	a := listBesideAUnion(t)
	req := httptest.NewRequest(http.MethodGet, "/n", nil)
	d := &discardWriter{h: http.Header{}}
	allocs := testing.AllocsPerRun(20, func() {
		clear(d.h)
		a.ServeHTTP(d, req)
	})
	if d.status != http.StatusOK || allocs > 100 {
		t.Fatalf("%d, %v allocations", d.status, allocs)
	}
}

type listedDrawing struct {
	Main shape    `json:"main"`
	Tags []string `json:"tags" schema:"maxItems=2000"`
}

// listBesideAUnion answers a sealed value beside 1000 strings.
func listBesideAUnion(t testing.TB) *geta.App {
	out := &listedDrawing{Main: circle{Kind: "circle", Radius: 1}, Tags: make([]string, 1000)}
	for i := range out.Tags {
		out.Tags[i] = "t"
	}
	a, err := geta.New(one("/n", get(func(context.Context, *empty) (*listedDrawing, error) { return out, nil })), geta.WithUnion(shapes))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// BenchmarkNilUnionBesideAList answers a sealed value beside 1000 strings.
func BenchmarkNilUnionBesideAList(b *testing.B) {
	a := listBesideAUnion(b)
	req := httptest.NewRequest(http.MethodGet, "/n", nil)
	d := &discardWriter{h: http.Header{}}
	b.ReportAllocs()
	for b.Loop() {
		clear(d.h)
		a.ServeHTTP(d, req)
	}
}

// A variant pointer that is itself nil would be written as null: a 500.
func TestTypedNilPointerVariantIsADefect(t *testing.T) {
	for name, root := range map[string]treeNode{
		"root":  (*treeLeaf)(nil),
		"child": treePair{Kind: "pair", Child: (*treePair)(nil)},
	} {
		if w := treeServe(t, root); w.Code != 500 || strings.Contains(w.Body.String(), "null") {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
}
