package geta_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// ownObject writes its own JSON, an object, which WithSchema declares.
type ownObject struct{ m map[string]any }

func (o ownObject) MarshalJSON() ([]byte, error)  { return json.Marshal(o.m) }
func (o *ownObject) UnmarshalJSON(b []byte) error { return json.Unmarshal(b, &o.m) }

type sizedBody struct {
	Labels map[string]string `json:"labels" schema:"minProperties=1,maxProperties=3"`
	Rows   []map[string]int  `json:"rows" schema:"items.maxProperties=1"`
	Own    *ownObject        `json:"own,omitzero"`
}

type sizedIn struct {
	Body sizedBody `body:"json"`
}

var ownObjectSchema = geta.WithSchema[ownObject]("object", "minProperties=1,maxProperties=2")

// A map's minProperties and maxProperties, and a WithSchema object's, hold a
// request's member count, each violation listed as an array's minItems and
// maxItems are; an array of maps bounds each by items.; the document states
// them.
func TestMapSizeKeywordsHoldARequest(t *testing.T) {
	app, err := geta.New(one("/s", post(func(context.Context, *sizedIn) (*ok, error) { return &ok{true}, nil })), ownObjectSchema)
	if err != nil {
		t.Fatal(err)
	}
	c := getatest.Serve(t, app)
	for _, body := range []string{
		`{"labels":{"a":"x"},"rows":[]}`,
		`{"labels":{"a":"x","b":"y","c":"z"},"rows":[{"n":1},{}],"own":{"k":1,"l":2}}`,
	} {
		if res := c.Post("/s", body); res.Status != 200 {
			t.Fatal(body, res.Status, res.Text())
		}
	}
	for body, want := range map[string]string{
		`{"labels":{},"rows":[]}`:                                 "body $.labels: object has 0 members, fewer than minProperties 1",
		`{"labels":{"a":"","b":"","c":"","d":""},"rows":[]}`:      "body $.labels: object has 4 members, more than maxProperties 3",
		`{"labels":{"a":""},"rows":[{"n":1},{"n":1,"m":2}]}`:      "body $.rows[1]: object has 2 members, more than maxProperties 1",
		`{"labels":{"a":""},"rows":[],"own":{}}`:                  "body $.own: object has 0 members, fewer than minProperties 1",
		`{"labels":{"a":""},"rows":[],"own":{"a":1,"b":2,"c":3}}`: "body $.own: object has 3 members, more than maxProperties 2",
	} {
		res := c.Post("/s", body)
		if res.Status != http.StatusBadRequest {
			t.Errorf("%s: %d", body, res.Status)
		}
		if got := listed(res.Problem()); fmt.Sprint(got) != "["+want+"]" {
			t.Errorf("%s:\n got %q\nwant %q", body, got, want)
		}
	}
	m := doc(t, app)
	props := at(t, m, "components", "schemas", "sizedBody", "properties")
	if got := compact(t, at(t, props, "labels")); got != `{"additionalProperties":{"maxLength":4096,"type":"string"},"maxProperties":3,"minProperties":1,"propertyNames":{"maxLength":4096},"type":"object"}` {
		t.Errorf("labels: %s", got)
	}
	if got := compact(t, at(t, props, "rows")); got != `{"items":{"additionalProperties":{"format":"int64","type":"integer"},"maxProperties":1,"propertyNames":{"maxLength":4096},"type":"object"},"maxItems":8192,"type":"array"}` {
		t.Errorf("rows: %s", got)
	}
	if got := compact(t, at(t, props, "own")); got != `{"maxProperties":2,"minProperties":1,"propertyNames":{"maxLength":4096},"type":"object"}` {
		t.Errorf("own: %s", got)
	}
}

// sizedGolden is a map bounded both ways, an array of maps bounded by
// items., a map whose values are bounded by additionalProperties., and one
// whose keys are bounded by propertyNames., read and written, for the golden
// document.
type sizedGolden struct {
	Labels map[string]string `json:"labels" schema:"minProperties=1,maxProperties=3"`
	Rows   []map[string]int  `json:"rows" schema:"items.maxProperties=1"`
	Free   map[string]bool   `json:"free"`
	Scores map[string]string `json:"scores" schema:"additionalProperties.maxLength=8,additionalProperties.enum=low|high"`
	Named  map[string]int    `json:"named" schema:"propertyNames.maxLength=63,propertyNames.pattern=^[a-z][a-z0-9-]*$"`
}

func sizeTable() geta.Table {
	return one("/s", post(func(_ context.Context, in *struct {
		B sizedGolden `body:"json"`
	}) (*sizedGolden, error) {
		return &in.B, nil
	}))
}

type sizedPastBackstop struct {
	M map[string]int `json:"m" schema:"minProperties=9000"`
}

type sizedPastBackstopCapped struct {
	M map[string]int `json:"m" schema:"minProperties=9000,maxProperties=9000"`
}

type sizedRowsPastBackstop struct {
	L []map[string]int `json:"l" schema:"items.minProperties=3"`
}

type sizedEmpty struct {
	M map[string]int `json:"m" schema:"minProperties=3,maxProperties=2"`
}

type sizedAnonymous struct {
	A struct {
		X int `json:"x"`
	} `json:"a" schema:"maxProperties=1"`
}

type sizedNamed struct {
	S ok `json:"s" schema:"minProperties=1"`
}

type sizedString struct {
	S string `json:"s" schema:"maxProperties=1"`
}

// sizedRoute reads a T, or only writes one.
func sizedRoute[T any](read bool, opts ...geta.Option) error {
	r := geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*T, error) { return nil, nil }, geta.Doc{})}
	if read {
		r = geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *struct {
			B T `body:"json"`
		}) (*ok, error) {
			return nil, nil
		}, geta.Doc{})}
	}
	_, err := geta.New(one("/x", r), opts...)
	return err
}

// geta.New refuses a map's minProperties past the backstop where no
// maxProperties replaces it, of a type a request reads, as it refuses an
// array's minItems past MaxItems; a type only a response writes is refused
// none. minProperties past maxProperties is refused, and so are the keywords
// on a struct, whose members are its fields, on a value that is not a map,
// and on a WithSchema type of another JSON type.
func TestMapSizeKeywordsAreRefused(t *testing.T) {
	refused := func(err error, want string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v; want %q", err, want)
		}
	}
	const past = "minProperties 9000 exceeds Limits.MaxItems 8192; declare a maxProperties"
	refused(sizedRoute[sizedPastBackstop](true), "sizedPastBackstop.M: "+past)
	if err := sizedRoute[sizedPastBackstop](false); err != nil {
		t.Errorf("written only: %v", err)
	}
	if err := sizedRoute[sizedPastBackstopCapped](true); err != nil {
		t.Errorf("capped: %v", err)
	}
	lim := geta.DefaultLimits
	lim.MaxItems = 2
	refused(sizedRoute[sizedRowsPastBackstop](true, geta.WithLimits(lim)), "sizedRowsPastBackstop.L: items: minProperties 3 exceeds Limits.MaxItems 2")
	refused(sizedRoute[sizedEmpty](false), "minProperties 3 exceeds maxProperties 2")
	refused(sizedRoute[sizedAnonymous](false), "schema keyword maxProperties applies to a map, not a struct")
	refused(sizedRoute[sizedNamed](false), `schema tag "minProperties=1" on a struct type`)
	refused(sizedRoute[sizedString](false), "schema keyword maxProperties applies to object, not string")
	refused(sizedRoute[sizedBody](false, geta.WithSchema[ownObject]("string", "maxProperties=1")), "schema keyword maxProperties applies to object, not string")
	refused(sizedRoute[sizedBody](false, geta.WithSchema[ownObject]("object", "minProperties=2,maxProperties=1")), "minProperties 2 exceeds maxProperties 1")
}

type DeclaredObjects struct {
	Own ownObject `json:"own"`
}

// A WithSchema object whose declaration states no maxProperties is held to
// the MaxItems backstop as a map is: a request past it is refused, the
// request's schema states it as maxProperties, and its keys' backstop as
// propertyNames (a component read and written splits, the response's stating
// none), and a declared maxProperties and propertyNames.maxLength replace
// them; a minProperties past it is refused of a request alone, and two
// operations reading it under limits that state it differently are refused.
func TestADeclaredObjectIsHeldToTheBackstop(t *testing.T) {
	lim := geta.DefaultLimits
	lim.MaxItems = 2
	echo := one("/x", post(func(_ context.Context, in *struct {
		B DeclaredObjects `body:"json"`
	}) (*DeclaredObjects, error) {
		return &in.B, nil
	}))
	app, err := geta.New(echo, geta.WithLimits(lim), geta.WithSchema[ownObject]("object", ""))
	if err != nil {
		t.Fatal(err)
	}
	c := getatest.Serve(t, app)
	if res := c.Post("/x", `{"own":{"a":1,"b":2}}`); res.Status != 200 {
		t.Fatal(res.Status, res.Text())
	}
	res := c.Post("/x", `{"own":{"a":1,"b":2,"c":3}}`)
	if got := listed(res.Problem()); res.Status != http.StatusBadRequest || fmt.Sprint(got) != "[body $.own: object has 3 members, over the ceiling of 2]" {
		t.Errorf("%d %q", res.Status, got)
	}
	schemas := at(t, doc(t, app), "components", "schemas").(map[string]any)
	if got := compact(t, at(t, schemas, "DeclaredObjects-Input", "properties", "own")); got != `{"maxProperties":2,"propertyNames":{"maxLength":4096},"type":"object"}` {
		t.Errorf("request: %s", got)
	}
	if got := compact(t, at(t, schemas, "DeclaredObjects", "properties", "own")); got != `{"type":"object"}` {
		t.Errorf("response: %s", got)
	}
	// A response is held to no backstop.
	if err := app.Conforms(geta.Match{Template: "/x", Method: "POST", Operation: true}, http.StatusOK, nil, []byte(`{"own":{"a":1,"b":2,"c":3}}`)); err != nil {
		t.Error(err)
	}

	app, err = geta.New(echo, geta.WithLimits(lim), geta.WithSchema[ownObject]("object", "maxProperties=5,propertyNames.maxLength=64"))
	if err != nil {
		t.Fatal(err)
	}
	if res := getatest.Serve(t, app).Post("/x", `{"own":{"a":1,"b":2,"c":3}}`); res.Status != 200 {
		t.Error(res.Status, res.Text())
	}
	schemas = at(t, doc(t, app), "components", "schemas").(map[string]any)
	if _, split := schemas["DeclaredObjects-Input"]; split {
		t.Errorf("split: %s", compact(t, schemas))
	}
	if got := compact(t, at(t, schemas, "DeclaredObjects", "properties", "own")); got != `{"maxProperties":5,"propertyNames":{"maxLength":64},"type":"object"}` {
		t.Errorf("declared: %s", got)
	}

	const past = "geta.WithSchema[github.com/koji-1009/geta_test.ownObject]: minProperties 3 exceeds Limits.MaxItems 2; declare a maxProperties"
	if err := sizedRoute[DeclaredObjects](true, geta.WithLimits(lim), geta.WithSchema[ownObject]("object", "minProperties=3")); err == nil ||
		!strings.Contains(err.Error(), past) {
		t.Errorf("read: %v", err)
	}
	if err := sizedRoute[DeclaredObjects](false, geta.WithLimits(lim), geta.WithSchema[ownObject]("object", "minProperties=3")); err != nil {
		t.Errorf("written only: %v", err)
	}

	few := func(l geta.Limits) geta.Limits { l.MaxItems = 3; return l }
	read := func(context.Context, *struct {
		B DeclaredObjects `body:"json"`
	}) (*ok, error) {
		return nil, nil
	}
	_, err = geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Post: geta.Op(http.StatusOK, read, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Post: geta.Op(http.StatusOK, read, geta.Doc{Limits: few})}},
	}}, geta.WithSchema[ownObject]("object", ""))
	if err == nil || !strings.Contains(err.Error(), "POST /a and POST /b read geta_test.DeclaredObjects under conflicting limits") {
		t.Errorf("limits: %v", err)
	}
}

type conformsMap struct {
	M map[string]int `json:"m"`
}

type conformsMapDeclared struct {
	M map[string]int `json:"m" schema:"maxProperties=2"`
}

// A response's map is held to its declared maxProperties, by Conforms and by
// getatest's check of every response, and to no backstop.
func TestConformsHoldsAMapToItsDeclaredSize(t *testing.T) {
	lim := geta.DefaultLimits
	lim.MaxItems = 2
	get := geta.Match{Template: "/x", Method: "GET", Operation: true}
	app := conformsApp(t, lim, &conformsMap{M: map[string]int{"a": 1, "b": 2, "c": 3}})
	if d := string(app.OpenAPI()); strings.Contains(d, `"maxProperties"`) {
		t.Fatalf("document: %s", d)
	}
	if err := app.Conforms(get, http.StatusOK, nil, []byte(`{"m":{"a":1,"b":2,"c":3}}`)); err != nil {
		t.Fatal(err)
	}
	app = conformsApp(t, lim, &conformsMapDeclared{M: map[string]int{"a": 1, "b": 2, "c": 3}})
	if !strings.Contains(string(app.OpenAPI()), `"maxProperties": 2`) {
		t.Fatalf("document: %s", app.OpenAPI())
	}
	if err := app.Conforms(get, http.StatusOK, nil, []byte(`{"m":{"a":1,"b":2,"c":3}}`)); err == nil ||
		!strings.Contains(err.Error(), "object has 3 members, more than maxProperties 2") {
		t.Fatalf("Conforms: %v", err)
	}
	rec := &recorder{TB: t}
	getatest.Serve(rec, app).Get("/x")
	if len(rec.errs) != 1 || !strings.Contains(rec.errs[0], "more than maxProperties 2") {
		t.Fatalf("getatest: %q", rec.errs)
	}
}

type SplitCounts struct {
	M map[string]int `json:"m"`
}

type SameCounts struct {
	M map[string]int `json:"m" schema:"maxProperties=5,propertyNames.maxLength=64"`
}

// A type a request reads and a response writes whose map declares no
// maxProperties is two components, the request's stating the backstops (the
// member count's, and its keys'); a declared maxProperties and
// propertyNames.maxLength, which both state, leave it one.
func TestADeclaredMaxPropertiesLeavesOneComponent(t *testing.T) {
	echo := func(tbl geta.Table) map[string]any {
		t.Helper()
		app, err := geta.New(tbl)
		if err != nil {
			t.Fatal(err)
		}
		return at(t, doc(t, app), "components", "schemas").(map[string]any)
	}
	schemas := echo(one("/x", post(func(_ context.Context, in *struct {
		B SplitCounts `body:"json"`
	}) (*SplitCounts, error) {
		return &in.B, nil
	})))
	if got := compact(t, at(t, schemas, "SplitCounts-Input", "properties", "m")); got != `{"additionalProperties":{"format":"int64","type":"integer"},"maxProperties":8192,"propertyNames":{"maxLength":4096},"type":"object"}` {
		t.Errorf("request: %s", got)
	}
	if got := compact(t, at(t, schemas, "SplitCounts", "properties", "m")); got != `{"additionalProperties":{"format":"int64","type":"integer"},"type":"object"}` {
		t.Errorf("response: %s", got)
	}
	schemas = echo(one("/x", post(func(_ context.Context, in *struct {
		B SameCounts `body:"json"`
	}) (*SameCounts, error) {
		return &in.B, nil
	})))
	if _, split := schemas["SameCounts-Input"]; split {
		t.Errorf("split: %s", compact(t, schemas))
	}
	if got := compact(t, at(t, schemas, "SameCounts", "properties", "m")); got != `{"additionalProperties":{"format":"int64","type":"integer"},"maxProperties":5,"propertyNames":{"maxLength":64},"type":"object"}` {
		t.Errorf("one component: %s", got)
	}
}
