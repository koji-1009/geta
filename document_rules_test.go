package geta_test

import (
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/internal/fixture/a"
	"github.com/koji-1009/geta/internal/fixture/b"
)

// Rules of the document each test below states in its name.

func TestInfoDescriptionAndOperationDeprecated(t *testing.T) {
	tbl := one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Deprecated: true})})
	app, err := geta.New(tbl, geta.WithInfo(geta.Info{Title: "T", Version: "1", Description: "What it serves."}))
	if err != nil {
		t.Fatal(err)
	}
	m := doc(t, app)
	if got := compact(t, at(t, m, "info")); got != `{"description":"What it serves.","title":"T","version":"1"}` {
		t.Error(got)
	}
	if at(t, m, "paths", "/x", "get", "deprecated") != true {
		t.Error("operation not deprecated")
	}
	// Neither is written when not set.
	m = doc(t, accepts(t, one("/x", get(okHandler))))
	if _, ok := at(t, m, "info").(map[string]any)["description"]; ok {
		t.Error("info.description without one")
	}
	if _, ok := at(t, m, "paths", "/x", "get").(map[string]any)["deprecated"]; ok {
		t.Error("deprecated without Doc.Deprecated")
	}
}

func TestOpenAPIReturnsACopy(t *testing.T) {
	app := accepts(t, one("/x", get(okHandler)))
	b := app.OpenAPI()
	want := string(b)
	b[0] = 'x'
	if string(app.OpenAPI()) != want {
		t.Fatal("changing the returned document changed the next")
	}
}

// A 3.2 document is the 3.1 document but its version and each response's
// summary, where the table has no stream, cookie, or QUERY operation.
func TestDocument32IsDocument31WithSummaries(t *testing.T) {
	var d31, d32 map[string]any
	a31, err := geta.New(richTable())
	if err != nil {
		t.Fatal(err)
	}
	a32, err := geta.New(richTable(), geta.WithOpenAPI(geta.OpenAPI32))
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(a31.OpenAPI(), &d31)
	json.Unmarshal(a32.OpenAPI(), &d32)
	d32["openapi"] = d31["openapi"]
	summaries := 0
	for _, item := range d32["paths"].(map[string]any) {
		for _, op := range item.(map[string]any) {
			for status, r := range op.(map[string]any)["responses"].(map[string]any) {
				r := r.(map[string]any)
				if r["summary"] == nil {
					t.Errorf("response %s has no summary", status)
				}
				delete(r, "summary")
				summaries++
			}
		}
	}
	if summaries == 0 || !reflect.DeepEqual(d31, d32) {
		t.Fatalf("the documents differ beyond the summaries:\n%s\n%s", compact(t, d31), compact(t, d32))
	}
}

// In 3.2, a stream of a sealed type lists event and id as optional
// properties beside the required data: a variant may name or identify its
// events or not.
func TestStreamOfASealedTypeIn32(t *testing.T) {
	events := func(context.Context, *empty) (*geta.Stream[shape], error) {
		return &geta.Stream[shape]{Events: iter.Seq[shape](func(func(shape) bool) {})}, nil
	}
	app, err := geta.New(one("/s", get(events)), geta.WithUnion(shapes), geta.WithOpenAPI(geta.OpenAPI32))
	if err != nil {
		t.Fatal(err)
	}
	item := at(t, doc(t, app), "paths", "/s", "get", "responses", "200", "content", "text/event-stream", "itemSchema")
	if got := compact(t, at(t, item, "required")); got != `["data"]` {
		t.Error(got)
	}
	for _, p := range []string{"event", "id"} {
		if got := compact(t, at(t, item, "properties", p)); got != `{"type":"string"}` {
			t.Errorf("%s: %s", p, got)
		}
	}
	if got := compact(t, at(t, item, "properties", "data", "contentSchema")); got != `{"$ref":"#/components/schemas/shape"}` {
		t.Error(got)
	}
}

// A stream's events are written, so their schema is the response
// component's, the one without -Input, though a request reads the type too.
type note2 struct {
	Text string `json:"text"`
}

type note2In struct {
	Body note2 `body:"json"`
}

func TestStreamEventsReferToTheResponseComponent(t *testing.T) {
	tbl := geta.Table{Routes: []geta.Entry{
		{Path: "/n", Route: geta.Route{
			Post: geta.Op(http.StatusOK, func(_ context.Context, in *note2In) (*note2, error) { return &in.Body, nil }, geta.Doc{}),
			Get: geta.Op(http.StatusOK, func(context.Context, *empty) (*geta.Stream[note2], error) {
				return &geta.Stream[note2]{Events: iter.Seq[note2](func(func(note2) bool) {})}, nil
			}, geta.Doc{}),
		}},
	}}
	for _, v := range []geta.OpenAPIVersion{geta.OpenAPI31, geta.OpenAPI32} {
		app, err := geta.New(tbl, geta.WithOpenAPI(v))
		if err != nil {
			t.Fatal(err)
		}
		m := doc(t, app)
		at(t, m, "components", "schemas", "note2-Input")
		var ref any
		if v == geta.OpenAPI32 {
			ref = at(t, m, "paths", "/n", "get", "responses", "200", "content", "text/event-stream", "itemSchema", "properties", "data", "contentSchema")
		} else {
			ref = at(t, m, "paths", "/n", "get", "responses", "200", "content", "text/event-stream", "schema")
		}
		if got := compact(t, ref); got != `{"$ref":"#/components/schemas/note2"}` {
			t.Errorf("%s: %s", v, got)
		}
	}
}

// A sealed type's name is a component's: one a struct takes too is refused.
func TestSealedTypeNameCollision(t *testing.T) {
	type shape struct {
		N int `json:"n"`
	}
	type both struct {
		S shape `json:"s"`
	}
	type sealedToo struct {
		S   both        `json:"both"`
		Out interface{} `json:"-"`
	}
	_, err := geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: get(func(context.Context, *empty) (*drawing, error) { return nil, nil })},
		{Path: "/b", Route: get(func(context.Context, *empty) (*sealedToo, error) { return nil, nil })},
	}}, geta.WithUnion(shapes))
	if err == nil || !strings.Contains(err.Error(), `schema name "shape" is taken by both`) {
		t.Fatalf("%v", err)
	}
}

// Two gates whose defaults define one scheme name differently are refused.
func TestGateDefaultsDefiningOneSchemeTwiceAreRefused(t *testing.T) {
	jwt := geta.Bearer
	jwt.BearerFormat = "JWT"
	root := geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer}, Verifiers: map[string]geta.Verifier{geta.Bearer.Name: admit}})
	inner := geta.Secure(geta.Policy{Default: []geta.Scheme{jwt}, Verifiers: map[string]geta.Verifier{jwt.Name: admit}})
	rejects(t, withRoot(one("/x", get(okHandler), geta.Scope{inner}), root), `security scheme "bearer" has two definitions`)
}

// Two rows of one status whose descriptions are of one type state its
// schema once, and a header both set is required.
func TestOneDescriptionTypeTwiceOnAStatus(t *testing.T) {
	rows := []geta.Failure{
		geta.OnAsProblem(http.StatusTooManyRequests, "quota", func(e *quotaError) quota { return quota{} }),
		geta.OnAsProblem(http.StatusTooManyRequests, "conflict", func(e *conflictError) quota { return quota{} }),
	}
	m := doc(t, accepts(t, one("/f", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Failures: rows})})))
	r := at(t, m, "paths", "/f", "get", "responses", "429")
	if got := compact(t, at(t, r, "content", "application/problem+json", "schema")); !strings.HasPrefix(got, `{"allOf":[{"$ref":"#/components/schemas/Problem"},`) {
		t.Error(got)
	}
	if at(t, r, "headers", "Retry-After", "required") != true {
		t.Error("a header every cause sets is not required")
	}
	if got := at(t, r, "description"); got != "quota; conflict" {
		t.Error(got)
	}
}

// A description that is an envelope of headers alone writes the problem
// with no members of its own: its status's schema is Problem's.
type retryOnly struct {
	RetryAfter int `header:"Retry-After"`
}

func TestAHeadersOnlyDescriptionIsTheProblem(t *testing.T) {
	rows := []geta.Failure{geta.OnAsProblem(http.StatusServiceUnavailable, "down", func(e *quotaError) retryOnly { return retryOnly{e.Wait} })}
	m := doc(t, accepts(t, one("/f", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Failures: rows})})))
	r := at(t, m, "paths", "/f", "get", "responses", "503")
	if got := compact(t, at(t, r, "content", "application/problem+json", "schema")); got != `{"$ref":"#/components/schemas/Problem"}` {
		t.Error(got)
	}
	if got := compact(t, at(t, r, "headers", "Retry-After")); got != `{"required":true,"schema":{"format":"int64","type":"integer"}}` {
		t.Error(got)
	}
}

// A description's type takes its component name, though the document lists
// no component of it: two types of one name anywhere in the app are refused.
func TestADescriptionTypeTakesItsName(t *testing.T) {
	rows := []geta.Failure{geta.OnAsProblem(http.StatusConflict, "taken", func(e *conflictError) b.User { return b.User{} })}
	rejects(t, geta.Table{Routes: []geta.Entry{
		{Path: "/u", Route: get(func(context.Context, *empty) (*a.User, error) { return nil, nil })},
		{Path: "/f", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Failures: rows})}},
	}}, `schema name "User"`)
}

// In 3.2 a described row's response has its summary as any other.
func TestDescribedRowsHaveSummariesIn32(t *testing.T) {
	app, err := geta.New(describedTable(), geta.WithOpenAPI(geta.OpenAPI32))
	if err != nil {
		t.Fatal(err)
	}
	m := doc(t, app)
	for status, want := range map[string]string{"429": "Too Many Requests", "409": "Conflict"} {
		if got := at(t, m, "paths", "/f", "get", "responses", status, "summary"); got != want {
			t.Errorf("%s: %v", status, got)
		}
	}
}

// In 3.2 every status an envelope's status field declares names its cookies
// in Set-Cookie.
type cookieChoice struct {
	Status  int          `status:"200|201"`
	Session *http.Cookie `cookie:"sid"`
	Body    ok           `body:"json"`
}

func TestSetCookieOnEveryDeclaredStatusIn32(t *testing.T) {
	app, err := geta.New(one("/c", get(func(context.Context, *empty) (*cookieChoice, error) { return nil, nil })), geta.WithOpenAPI(geta.OpenAPI32))
	if err != nil {
		t.Fatal(err)
	}
	m := doc(t, app)
	for _, status := range []string{"200", "201"} {
		got := compact(t, at(t, m, "paths", "/c", "get", "responses", status, "headers", "Set-Cookie"))
		if got != `{"description":"Sets sid","explode":true,"schema":{"properties":{"sid":{"type":"string"}},"type":"object"},"style":"simple"}` {
			t.Errorf("%s: %s", status, got)
		}
	}
}

// A geta.Nullable of a sealed type is anyOf the union and null: the union's
// -Input component in a request's schema, where a variant is split.
type nullableShape struct {
	S geta.Nullable[shape] `json:"s"`
}

type nullableShapeIn struct {
	Body nullableShape `body:"json"`
}

func TestNullableSealedTypeIsDocumented(t *testing.T) {
	app, err := geta.New(one("/n", geta.Route{Put: geta.Op(http.StatusOK, func(_ context.Context, in *nullableShapeIn) (*nullableShape, error) {
		return &in.Body, nil
	}, geta.Doc{})}), geta.WithUnion(shapes))
	if err != nil {
		t.Fatal(err)
	}
	m := doc(t, app)
	if got := compact(t, at(t, m, "components", "schemas", "nullableShape", "properties", "s")); got != `{"anyOf":[{"$ref":"#/components/schemas/shape"},{"type":"null"}]}` {
		t.Error(got)
	}
	if got := compact(t, at(t, m, "components", "schemas", "nullableShape-Input", "properties", "s")); got != `{"anyOf":[{"$ref":"#/components/schemas/shape-Input"},{"type":"null"}]}` {
		t.Error(got)
	}
}

// A member default alone splits its component: the request's schema leaves
// the member out of required, the response's does not.
type tally struct {
	N int `json:"n" schema:"default=1"`
}

type tallyIn struct {
	Body tally `body:"json"`
}

func TestADefaultAloneSplitsAComponent(t *testing.T) {
	m := doc(t, accepts(t, one("/c", geta.Route{Put: geta.Op(http.StatusOK, func(_ context.Context, in *tallyIn) (*tally, error) { return &in.Body, nil }, geta.Doc{})})))
	resp, req := at(t, m, "components", "schemas", "tally"), at(t, m, "components", "schemas", "tally-Input")
	if got := compact(t, at(t, resp, "required")); got != `["n"]` {
		t.Error(got)
	}
	if _, ok := req.(map[string]any)["required"]; ok {
		t.Errorf("request schema requires: %s", compact(t, req))
	}
	delete(resp.(map[string]any), "required")
	if compact(t, resp) != compact(t, req) {
		t.Errorf("the sides differ beyond required:\n%s\n%s", compact(t, resp), compact(t, req))
	}
}

// A component an operation reads under its own limits and writes too splits:
// the request's side states those limits.
type limitedPair struct {
	Text string `json:"text"`
}

type limitedPairIn struct {
	Body limitedPair `body:"json"`
}

func TestOperationLimitsSplitAComponentWrittenToo(t *testing.T) {
	short := raise(func(l *geta.Limits) { l.MaxStringLength = 10 })
	m := doc(t, accepts(t, one("/p", geta.Route{Put: geta.Op(http.StatusOK, func(_ context.Context, in *limitedPairIn) (*limitedPair, error) {
		return &in.Body, nil
	}, geta.Doc{Limits: short})})))
	if got := at(t, m, "components", "schemas", "limitedPair-Input", "properties", "text", "maxLength"); got != 10.0 {
		t.Errorf("request side maxLength %v", got)
	}
	if _, ok := at(t, m, "components", "schemas", "limitedPair", "properties", "text").(map[string]any)["maxLength"]; ok {
		t.Error("response side states a backstop")
	}
}

// Two operations' limits that would state a component differently are
// refused where they reach it through another component too.
type limitedOuter struct {
	Inner limitedNested `json:"inner"`
}

type limitedNested struct {
	Text string `json:"text"`
}

type limitedOuterIn struct {
	Body limitedOuter `body:"json"`
}

type limitedNestedIn struct {
	Body limitedNested `body:"json"`
}

func TestOperationLimitsConflictThroughANestedComponent(t *testing.T) {
	long := raise(func(l *geta.Limits) { l.MaxStringLength = 10000 })
	rejects(t, geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *limitedNestedIn) (*ok, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *limitedOuterIn) (*ok, error) { return nil, nil }, geta.Doc{Limits: long})}},
	}}, "POST /a and POST /b read geta_test.limitedNested under conflicting limits", "MaxStringLength 4096 and 10000")
}
