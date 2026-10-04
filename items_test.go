package geta_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// A slice's elements take the keywords a single value takes, each after
// "items.": items.enum=a|b is each element's enum, as minItems is the
// array's. They hold a query's, a header's, and a form's elements and a JSON
// array member's, on the single pass and the reference path alike, and the
// document states them in items.

type itQuery struct {
	Feature *[]string `query:"feature" schema:"maxItems=3,items.enum=dark|wide"`
	Sizes   []int8    `query:"size" schema:"items.minimum=1,items.multipleOf=2"`
}

type itHeader struct {
	Feature *[]string `header:"X-Feature" schema:"items.enum=dark|wide mode"`
	Codes   *[]string `header:"X-Code" schema:"items.maxLength=3,items.pattern=^[a-z]+$"`
}

type itMember struct {
	Tags  []string  `json:"tags" schema:"minItems=1,items.enum=a|b"`
	Grid  *[][]int  `json:"grid,omitzero" schema:"items.maxItems=2,items.items.maximum=9"`
	Names *[]string `json:"names,omitzero" schema:"items.minLength=2"`
}

type itBody struct {
	Body itMember `body:"json"`
}

type itForm struct {
	Body struct {
		Days []int `form:"day" schema:"items.minimum=1,items.maximum=7"`
	} `body:"form"`
}

type itOut struct {
	Tags []string `json:"tags" schema:"items.enum=x|y"`
}

func TestElementKeywordsHoldEachElement(t *testing.T) {
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/q", Route: get(func(context.Context, *itQuery) (*ok, error) { return &ok{true}, nil })},
		{Path: "/h", Route: get(func(context.Context, *itHeader) (*ok, error) { return &ok{true}, nil })},
		{Path: "/b", Route: post(func(context.Context, *itBody) (*ok, error) { return &ok{true}, nil })},
		{Path: "/f", Route: post(func(context.Context, *itForm) (*ok, error) { return &ok{true}, nil })},
		{Path: "/o", Route: get(func(context.Context, *empty) (*itOut, error) { return &itOut{Tags: []string{"x"}}, nil })},
	}})
	if res := c.Get("/q?feature=dark&feature=wide&size=2&size=4"); res.Status != 200 {
		t.Fatal(res.Status, res.Text())
	}
	same(t, violations(t, c.Get("/q?feature=dark&feature=tall&size=0&size=3")),
		`query feature[1]: "tall" is not one of dark, wide`,
		"query size[0]: 0 is less than minimum 1",
		"query size[1]: 3 is not a multiple of 2")

	if res := c.With("X-Feature", "wide mode, dark").With("X-Code", "ab").Get("/h"); res.Status != 200 {
		t.Fatal(res.Status, res.Text())
	}
	same(t, violations(t, c.With("X-Feature", "dark, light").With("X-Code", "abcd,Ab").Get("/h")),
		`header X-Feature[1]: "light" is not one of dark, wide mode`,
		"header X-Code[0]: string length 4 exceeds maxLength 3",
		`header X-Code[1]: "Ab" does not match pattern ^[a-z]+$`)

	// A JSON body is read in one pass, which refuses what the reference path
	// names: both hold the elements.
	for _, body := range []string{`{"tags":["a","b"]}`, `{"tags":["a"],"grid":[[1,9],[]],"names":["ab"]}`} {
		if res := c.Content(http.MethodPost, "/b", "application/json", []byte(body)); res.Status != 200 {
			t.Fatal(body, res.Status, res.Text())
		}
	}
	same(t, violations(t, c.Content(http.MethodPost, "/b", "application/json", []byte(`{"tags":["a","c"]}`))),
		`body $.tags[1]: "c" is not one of a, b`)
	same(t, violations(t, c.Content(http.MethodPost, "/b", "application/json", []byte(`{"tags":["b"],"grid":[[1],[1,2,3],[10]],"names":["a"]}`))),
		"body $.grid[1]: array length 3 exceeds maxItems 2",
		"body $.grid[2][0]: 10 is greater than maximum 9",
		"body $.names[0]: string length 1 is shorter than minLength 2")

	if res := c.Form(http.MethodPost, "/f", url.Values{"day": {"1", "7"}}); res.Status != 200 {
		t.Fatal(res.Status, res.Text())
	}
	same(t, violations(t, c.Form(http.MethodPost, "/f", url.Values{"day": {"0", "8"}})),
		"body $.day[0]: 0 is less than minimum 1",
		"body $.day[1]: 8 is greater than maximum 7")

	// The document states each element's keywords, beside the backstops and
	// what a header's element carries.
	d := doc(t, c.App())
	params := map[string]any{}
	for _, path := range []string{"/q", "/h"} {
		for _, p := range at(t, d, "paths", path, "get", "parameters").([]any) {
			params[p.(map[string]any)["name"].(string)] = p.(map[string]any)["schema"]
		}
	}
	for name, want := range map[string]map[string]any{
		"feature": {"type": "array", "maxItems": 3, "items": map[string]any{"type": "string", "enum": []any{"dark", "wide"}}},
		"size": {"type": "array", "maxItems": 8192, "items": map[string]any{"type": "integer", "minimum": 1, "maximum": 127,
			"multipleOf": 2}},
		"X-Feature": {"type": "array", "maxItems": 8192, "items": map[string]any{"type": "string", "enum": []any{"dark", "wide mode"}}},
		"X-Code": {"type": "array", "maxItems": 8192, "items": map[string]any{"type": "string", "maxLength": 3,
			"pattern": "^[a-z]+$", "allOf": []any{map[string]any{"pattern": elementPattern}}}},
	} {
		if g := compact(t, params[name]); g != compact(t, want) {
			t.Errorf("%s:\n got %s\nwant %s", name, g, compact(t, want))
		}
	}
	if g := compact(t, at(t, d, "components", "schemas", "itOut", "properties", "tags")); g !=
		compact(t, map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []any{"x", "y"}}}) {
		t.Error(g)
	}
	if g := compact(t, at(t, d, "paths", "/f", "post", "requestBody", "content", "application/x-www-form-urlencoded", "schema", "properties", "day")); g !=
		compact(t, map[string]any{"type": "array", "maxItems": 8192, "items": map[string]any{"type": "integer", "format": "int64", "minimum": 1, "maximum": 7}}) {
		t.Error(g)
	}
}

type itHeaderComma struct {
	Feature []string `header:"X-Feature" schema:"items.enum=dark|a,b"`
}

type itHeaderSpace struct {
	Feature []string `header:"X-Feature" schema:"items.enum=dark| wide"`
}

type itQueryComma struct {
	Feature []string `query:"feature" schema:"items.enum=dark|a,b"`
}

type itOnAString struct {
	S string `query:"s" schema:"items.enum=a"`
}

type itDefault struct {
	S []string `query:"s" schema:"items.default=a"`
}

type itWrongType struct {
	S []int `query:"s" schema:"items.minLength=1"`
}

type itStruct struct {
	S []itMember `json:"s" schema:"items.minLength=1"`
}

type itStructBody struct {
	Body itStruct `body:"json"`
}

type itCeiling struct {
	S []string `query:"s" schema:"items.pattern=^a+$,items.maxLength=5000"`
}

type itBackstop struct {
	S []string `query:"s" schema:"items.minLength=5000"`
}

type itOnAStruct struct {
	M itMember `json:"m" schema:"items.minLength=1"`
}

type itOnOwnJSON struct {
	C cents `json:"c" schema:"items.minLength=1"`
}

type itTextEnum struct {
	S []rqLevel `query:"s" schema:"items.enum=low|mid"`
}

// An element's enum member a header list cannot carry (a comma, or
// whitespace at either end) is refused, as a single header's is; a query
// carries it. A keyword after items. is refused where there is no array,
// where the element does not take it, as an annotation, past what a request
// can be read under, and where the element's type refuses the member.
func TestElementKeywordMistakesAreRefused(t *testing.T) {
	rejects(t, one("/h", get(func(context.Context, *itHeaderComma) (*ok, error) { return nil, nil })),
		`itHeaderComma.Feature: header parameter "X-Feature": enum member "a,b" is not a valid header list element`)
	rejects(t, one("/h", get(func(context.Context, *itHeaderSpace) (*ok, error) { return nil, nil })),
		`header parameter "X-Feature": enum member " wide" is not a valid header list element`)
	accepts(t, one("/q", get(func(context.Context, *itQueryComma) (*ok, error) { return nil, nil })))
	rejects(t, one("/q", get(func(context.Context, *itOnAString) (*ok, error) { return nil, nil })),
		"itOnAString.S: schema keyword items.enum applies to the elements of an array, not string")
	rejects(t, one("/o", get(func(context.Context, *empty) (*itOnAStruct, error) { return nil, nil })),
		"itOnAStruct.M: schema keyword items.minLength applies to the elements of an array, not a struct type")
	rejects(t, one("/o", get(func(context.Context, *empty) (*itOnOwnJSON, error) { return nil, nil })),
		"itOnOwnJSON.C: schema keyword items.minLength applies to the elements of an array, not a type with JSON methods of its own")
	rejects(t, one("/q", get(func(context.Context, *itDefault) (*ok, error) { return nil, nil })),
		"itDefault.S: schema keyword items.default: an element takes no default")
	rejects(t, one("/q", get(func(context.Context, *itWrongType) (*ok, error) { return nil, nil })),
		"itWrongType.S: items: schema keyword minLength applies to string, not integer")
	rejects(t, one("/b", post(func(context.Context, *itStructBody) (*ok, error) { return nil, nil })),
		"itStruct.S: items: schema tag \"minLength=1\" on a struct type")
	rejects(t, one("/q", get(func(context.Context, *itCeiling) (*ok, error) { return nil, nil })),
		"itCeiling.S: items: maxLength 5000 with a pattern exceeds the pattern ceiling")
	rejects(t, one("/q", get(func(context.Context, *itBackstop) (*ok, error) { return nil, nil })),
		"itBackstop.S: items: minLength 5000 exceeds Limits.MaxStringLength 4096")
	rejects(t, one("/q", get(func(context.Context, *itTextEnum) (*ok, error) { return nil, nil })),
		`itTextEnum.S: items: enum member "mid" is not a valid rqLevel`)
	for kind, tag := range map[string]string{
		"string":   "items.enum=a",
		"[]int":    "items.minLength=1",
		"[]string": "items.default=a",
		"[]struct": "items.minLength=1",
	} {
		if err := geta.CheckSchemaTag(tag, kind); err == nil {
			t.Errorf("CheckSchemaTag(%q, %q) accepted", tag, kind)
		}
	}
	for kind, tag := range map[string]string{
		"[]string":    "minItems=1,items.enum=a|b,items.maxLength=3",
		"[][]int8":    "items.minItems=1,items.items.maximum=9",
		"[]?int":      "items.minimum=0",
		"[]geta.Date": "items.enum=2026-01-01",
		"slice":       "items.minLength=1", // an element of no kind getavet names: geta.New judges it
	} {
		if err := geta.CheckSchemaTag(tag, kind); err != nil {
			t.Errorf("CheckSchemaTag(%q, %q): %v", tag, kind, err)
		}
	}
	if err := geta.CheckRequestSchemaTag("items.pattern=^a$,items.maxLength=5000", "[]string"); err == nil ||
		!strings.Contains(err.Error(), "items: maxLength 5000 with a pattern") {
		t.Error(err)
	}
	if err := geta.CheckSchemaTag("items.enum=2026-02-30", "[]geta.Date"); err == nil || !strings.Contains(err.Error(), "items: ") {
		t.Error(err)
	}
	// A slice of a kind CheckSchemaTag does not know is no kind either.
	if err := geta.CheckSchemaTag("", "[]nope"); err == nil || err.Error() != `geta.CheckSchemaTag: unknown kind "nope"` {
		t.Error(err)
	}
}
