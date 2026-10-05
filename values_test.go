package geta_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// A map's values take the keywords a single value takes, each after
// "additionalProperties.", as an array's elements take them after "items.":
// additionalProperties.maxLength=3 is each value's maxLength, as
// maxProperties is the map's. They hold a JSON map member's values, on the
// single pass and the reference path alike, and the document states them in
// additionalProperties.

type vlMember struct {
	Labels map[string]string           `json:"labels" schema:"maxProperties=3,additionalProperties.maxLength=3,additionalProperties.pattern=^[a-z]{1,3}$"`
	Levels *map[string]int8            `json:"levels,omitzero" schema:"additionalProperties.minimum=1,additionalProperties.maximum=5"`
	Grid   *map[string][]int           `json:"grid,omitzero" schema:"additionalProperties.maxItems=2,additionalProperties.items.maximum=9"`
	Rows   *[]map[string]string        `json:"rows,omitzero" schema:"items.additionalProperties.enum=a|b"`
	Nested *map[string]map[string]int8 `json:"nested,omitzero" schema:"additionalProperties.minProperties=1,additionalProperties.additionalProperties.minimum=0"`
}

type vlBody struct {
	Body vlMember `body:"json"`
}

type vlOut struct {
	Tags map[string]string `json:"tags" schema:"additionalProperties.enum=x|y"`
}

func TestValueKeywordsHoldEachValue(t *testing.T) {
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/b", Route: post(func(context.Context, *vlBody) (*ok, error) { return &ok{true}, nil })},
		{Path: "/o", Route: get(func(context.Context, *empty) (*vlOut, error) { return &vlOut{Tags: map[string]string{"k": "x"}}, nil })},
	}})
	// A JSON body is read in one pass, which refuses what the reference path
	// names: both hold the values.
	for _, body := range []string{
		`{"labels":{"a":"abc","b":"x"}}`,
		`{"labels":{},"levels":{"a":1,"b":5},"grid":{"g":[1,9],"h":[]},"rows":[{"x":"a"},{}],"nested":{"n":{"m":0}}}`,
	} {
		if res := c.Content(http.MethodPost, "/b", "application/json", []byte(body)); res.Status != 200 {
			t.Fatal(body, res.Status, res.Text())
		}
	}
	same(t, violations(t, c.Content(http.MethodPost, "/b", "application/json", []byte(`{"labels":{"a":"abcd","b":"B"}}`))),
		"body $.labels.a: string length 4 exceeds maxLength 3",
		`body $.labels.b: "B" does not match pattern ^[a-z]{1,3}$`)
	same(t, violations(t, c.Content(http.MethodPost, "/b", "application/json",
		[]byte(`{"labels":{},"levels":{"a":0,"b":6},"grid":{"g":[1,2,3],"h":[10]},"rows":[{"x":"c"}],"nested":{"n":{},"o":{"m":-1}}}`))),
		"body $.levels.a: 0 is less than minimum 1",
		"body $.levels.b: 6 is greater than maximum 5",
		"body $.grid.g: array length 3 exceeds maxItems 2",
		"body $.grid.h[0]: 10 is greater than maximum 9",
		`body $.rows[0].x: "c" is not one of a, b`,
		"body $.nested.n: object has 0 members, fewer than minProperties 1",
		"body $.nested.o.m: -1 is less than minimum 0")

	// The document states each value's keywords in additionalProperties,
	// beside the backstops a request's schema states.
	d := doc(t, c.App())
	props := at(t, d, "components", "schemas", "vlMember", "properties")
	for name, want := range map[string]string{
		"labels": `{"additionalProperties":{"maxLength":3,"pattern":"^[a-z]{1,3}$","type":"string"},"maxProperties":3,"propertyNames":{"maxLength":4096},"type":"object"}`,
		"levels": `{"additionalProperties":{"maximum":5,"minimum":1,"type":"integer"},"maxProperties":8192,"propertyNames":{"maxLength":4096},"type":"object"}`,
		"grid": `{"additionalProperties":{"items":{"format":"int64","maximum":9,"type":"integer"},"maxItems":2,"type":"array"},` +
			`"maxProperties":8192,"propertyNames":{"maxLength":4096},"type":"object"}`,
		"rows": `{"items":{"additionalProperties":{"enum":["a","b"],"type":"string"},"maxProperties":8192,"propertyNames":{"maxLength":4096},"type":"object"},"maxItems":8192,"type":"array"}`,
		"nested": `{"additionalProperties":{"additionalProperties":{"maximum":127,"minimum":0,"type":"integer"},"maxProperties":8192,"minProperties":1,` +
			`"propertyNames":{"maxLength":4096},"type":"object"},"maxProperties":8192,"propertyNames":{"maxLength":4096},"type":"object"}`,
	} {
		if g := compact(t, at(t, props, name)); g != want {
			t.Errorf("%s:\n got %s\nwant %s", name, g, want)
		}
	}
	// A response's schema states them, and no backstop.
	if g := compact(t, at(t, d, "components", "schemas", "vlOut", "properties", "tags")); g !=
		`{"additionalProperties":{"enum":["x","y"],"type":"string"},"type":"object"}` {
		t.Error(g)
	}
}

type vlOnAString struct {
	S string `json:"s" schema:"additionalProperties.maxLength=1"`
}

type vlOnASlice struct {
	S []string `json:"s" schema:"additionalProperties.maxLength=1"`
}

type vlOnAStruct struct {
	S ok `json:"s" schema:"additionalProperties.maxLength=1"`
}

type vlOnAnAnonymousStruct struct {
	S struct {
		X int `json:"x"`
	} `json:"s" schema:"additionalProperties.maxLength=1"`
}

type vlOnOwnJSON struct {
	C cents `json:"c" schema:"additionalProperties.maxLength=1"`
}

type vlOnADeclaredObject struct {
	O ownObject `json:"o" schema:"additionalProperties.maxLength=1"`
}

type vlDefault struct {
	M map[string]string `json:"m" schema:"additionalProperties.default=a"`
}

type vlDeprecated struct {
	M map[string]string `json:"m" schema:"additionalProperties.deprecated=true"`
}

type vlWrongType struct {
	M map[string]int `json:"m" schema:"additionalProperties.minLength=1"`
}

type vlStructValues struct {
	M map[string]ok `json:"m" schema:"additionalProperties.minLength=1"`
}

type vlCeiling struct {
	M map[string]string `json:"m" schema:"additionalProperties.pattern=^a+$,additionalProperties.maxLength=5000"`
}

type vlBackstop struct {
	M map[string]string `json:"m" schema:"additionalProperties.minLength=5000"`
}

type vlRowsBackstop struct {
	L []map[string]string `json:"l" schema:"items.additionalProperties.minLength=5000"`
}

type vlTextEnum struct {
	M map[string]rqLevel `json:"m" schema:"additionalProperties.enum=low|mid"`
}

// A keyword after additionalProperties. is refused where there is no map,
// where the value does not take it, as an annotation, past what a request
// can be read under (a type only a response writes is held to neither the
// ceiling nor the backstops), where the value's type refuses an enum member,
// and on a WithSchema object, whose values' schema no declaration states.
func TestValueKeywordMistakesAreRefused(t *testing.T) {
	refused := func(err error, want string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v; want %q", err, want)
		}
	}
	refused(sizedRoute[vlOnAString](false), "vlOnAString.S: schema keyword additionalProperties.maxLength applies to the values of a map, not string")
	refused(sizedRoute[vlOnASlice](false), "vlOnASlice.S: schema keyword additionalProperties.maxLength applies to the values of a map, not array")
	refused(sizedRoute[vlOnAStruct](false), "vlOnAStruct.S: schema keyword additionalProperties.maxLength applies to the values of a map, not a struct type")
	refused(sizedRoute[vlOnAnAnonymousStruct](false),
		"vlOnAnAnonymousStruct.S: schema keyword additionalProperties.maxLength applies to the values of a map, not a struct")
	refused(sizedRoute[vlOnOwnJSON](false),
		"vlOnOwnJSON.C: schema keyword additionalProperties.maxLength applies to the values of a map, not a type with JSON methods of its own")
	refused(sizedRoute[vlOnADeclaredObject](false, geta.WithSchema[ownObject]("object", "")),
		"vlOnADeclaredObject.O: schema keyword additionalProperties.maxLength: the geta.WithSchema object declares no value schema")
	refused(sizedRoute[vlDefault](false), "vlDefault.M: schema keyword additionalProperties.default: a value takes no default; put it on the map")
	refused(sizedRoute[vlDeprecated](false), "vlDeprecated.M: schema keyword additionalProperties.deprecated: a value takes no deprecated")
	refused(sizedRoute[vlWrongType](false), "vlWrongType.M: additionalProperties: schema keyword minLength applies to string, not integer")
	refused(sizedRoute[vlStructValues](false), `vlStructValues.M: additionalProperties: schema tag "minLength=1" on a struct type`)
	refused(sizedRoute[vlCeiling](true), "vlCeiling.M: additionalProperties: maxLength 5000 with a pattern exceeds the pattern ceiling")
	refused(sizedRoute[vlBackstop](true), "vlBackstop.M: additionalProperties: minLength 5000 exceeds Limits.MaxStringLength 4096")
	refused(sizedRoute[vlRowsBackstop](true), "vlRowsBackstop.L: items: additionalProperties: minLength 5000 exceeds Limits.MaxStringLength 4096")
	refused(sizedRoute[vlTextEnum](false), `vlTextEnum.M: additionalProperties: enum member "mid" is not a valid rqLevel`)
	for _, written := range []error{sizedRoute[vlCeiling](false), sizedRoute[vlBackstop](false), sizedRoute[vlRowsBackstop](false)} {
		if written != nil {
			t.Errorf("written only: %v", written)
		}
	}
	// A declaration states no schema of an object's values or an array's
	// elements, so a keyword after either prefix would narrow nothing.
	refused(sizedRoute[sizedBody](false, geta.WithSchema[ownObject]("object", "additionalProperties.maxLength=1")),
		"geta.WithSchema[github.com/koji-1009/geta_test.ownObject]: schema keyword additionalProperties.maxLength: the geta.WithSchema object declares no value schema")
	refused(sizedRoute[sizedBody](false, geta.WithSchema[ownObject]("array", "items.maxLength=1")),
		"geta.WithSchema[github.com/koji-1009/geta_test.ownObject]: schema keyword items.maxLength: the geta.WithSchema array declares no element schema")

	for kind, tag := range map[string]string{
		"string":            "additionalProperties.enum=a",
		"[]string":          "additionalProperties.enum=a",
		"object":            "additionalProperties.minLength=1",
		"map[string]int":    "additionalProperties.minLength=1",
		"map[string]string": "additionalProperties.default=a",
		"map[string]struct": "additionalProperties.minLength=1",
		"[]map[string]int":  "items.additionalProperties.minLength=1",
	} {
		if err := geta.CheckSchemaTag(tag, kind); err == nil {
			t.Errorf("CheckSchemaTag(%q, %q) accepted", tag, kind)
		}
	}
	for kind, tag := range map[string]string{
		"map[string]string":          "maxProperties=2,additionalProperties.enum=a|b,additionalProperties.maxLength=3",
		"[]map[string]int8":          "items.additionalProperties.maximum=9",
		"map[string][]int":           "additionalProperties.maxItems=1,additionalProperties.items.minimum=0",
		"map[string]map[string]?int": "additionalProperties.additionalProperties.minimum=0",
		"map[string]geta.Date":       "additionalProperties.enum=2026-01-01",
		"map":                        "additionalProperties.minLength=1", // a value of no kind getavet names: geta.New judges it
	} {
		if err := geta.CheckSchemaTag(tag, kind); err != nil {
			t.Errorf("CheckSchemaTag(%q, %q): %v", tag, kind, err)
		}
	}
	if err := geta.CheckRequestSchemaTag("additionalProperties.pattern=^a$,additionalProperties.maxLength=5000", "map[string]string"); err == nil ||
		!strings.Contains(err.Error(), "additionalProperties: maxLength 5000 with a pattern") {
		t.Error(err)
	}
	if err := geta.CheckSchemaTag("additionalProperties.enum=2026-02-30", "map[string]geta.Date"); err == nil ||
		!strings.Contains(err.Error(), `additionalProperties: enum member "2026-02-30" is not a valid date`) {
		t.Error(err)
	}
}

// A map's keys, its members' names, are strings a request sends: each is held
// to the MaxStringLength backstop, which a request's schema states as
// propertyNames' maxLength, and takes the string keywords after
// "propertyNames.", the keyword the document states them under, a declared
// maxLength replacing the backstop.

type kwMember struct {
	Free  map[string]int             `json:"free"`
	Short *map[string]int            `json:"short,omitzero" schema:"propertyNames.minLength=2,propertyNames.maxLength=20,propertyNames.pattern=^[a-z]+$"`
	Pick  *map[string]int            `json:"pick,omitzero" schema:"propertyNames.enum=a|b"`
	IDs   *map[string]int            `json:"ids,omitzero" schema:"propertyNames.format=uuid,propertyNames.maxLength=36"`
	Rows  *[]map[string]int          `json:"rows,omitzero" schema:"items.propertyNames.maxLength=2"`
	Deep  *map[string]map[string]int `json:"deep,omitzero" schema:"additionalProperties.propertyNames.maxLength=2"`
	Vals  *map[string]int8           `json:"vals,omitzero" schema:"additionalProperties.minimum=0"`
}

type kwBody struct {
	Body kwMember `body:"json"`
}

type kwOut struct {
	Tags map[string]int `json:"tags" schema:"propertyNames.maxLength=3"`
	Free map[string]int `json:"free"`
}

func TestKeyKeywordsHoldEachKey(t *testing.T) {
	lim := geta.DefaultLimits
	lim.MaxStringLength = 10
	out := &kwOut{Tags: map[string]int{"abc": 1}, Free: map[string]int{strings.Repeat("f", 50): 1}}
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/b", Route: post(func(context.Context, *kwBody) (*ok, error) { return &ok{true}, nil })},
		{Path: "/o", Route: get(func(context.Context, *empty) (*kwOut, error) { return out, nil })},
	}}, geta.WithLimits(lim))
	send := func(body string) *getatest.Response {
		return c.Content(http.MethodPost, "/b", "application/json", []byte(body))
	}
	for _, body := range []string{
		`{"free":{"` + strings.Repeat("k", 10) + `":1}}`,
		`{"free":{"` + strings.Repeat("\u00e9", 10) + `":1}}`, // code points, not bytes
		`{"free":{},"short":{"` + strings.Repeat("s", 20) + `":1},"pick":{"a":1,"b":2},"ids":{"6ba7b810-9dad-11d1-80b4-00c04fd430c8":1},` +
			`"rows":[{"ab":1}],"deep":{"` + strings.Repeat("d", 10) + `":{"cd":1}}}`,
	} {
		if res := send(body); res.Status != 200 {
			t.Fatal(body, res.Status, res.Text())
		}
	}
	// A key past the backstop is refused, at its member, and the value under
	// it is left unchecked; on the single pass and the reference path alike.
	// Its path is cut to its last 253 bytes after "…".
	long := strings.Repeat("k", 5000)
	cut := "…" + long[:253]
	same(t, violations(t, send(`{"free":{"`+long+`":1}}`)), "body "+cut+": key length 5000 exceeds the ceiling of 10 code points")
	same(t, violations(t, send(`{"free":{},"vals":{"`+long+`":-1,"v":-1}}`)),
		"body "+cut+": key length 5000 exceeds the ceiling of 10 code points",
		"body $.vals.v: -1 is less than minimum 0")
	same(t, violations(t, send(`{"free":{},"short":{"a":1,"AB":2,"`+strings.Repeat("s", 21)+`":3},"pick":{"c":1},"ids":{"x":1},`+
		`"rows":[{"abc":1}],"deep":{"d":{"abc":1}}}`)),
		`body $.short.AB: key "AB" does not match pattern ^[a-z]+$`,
		"body $.short.a: key length 1 is shorter than minLength 2",
		"body $.short."+strings.Repeat("s", 21)+": key length 21 exceeds maxLength 20",
		`body $.pick.c: key "c" is not one of a, b`,
		`body $.ids.x: key "x" is not a valid uuid`,
		"body $.rows[0].abc: key length 3 exceeds maxLength 2",
		"body $.deep.d.abc: key length 3 exceeds maxLength 2")

	// The document states the keys' schema in propertyNames: the backstop
	// where a request's key declares no maxLength.
	d := doc(t, c.App())
	props := at(t, d, "components", "schemas", "kwMember", "properties")
	for name, want := range map[string]string{
		"free":  `{"additionalProperties":{"format":"int64","type":"integer"},"maxProperties":8192,"propertyNames":{"maxLength":10},"type":"object"}`,
		"short": `{"additionalProperties":{"format":"int64","type":"integer"},"maxProperties":8192,"propertyNames":{"maxLength":20,"minLength":2,"pattern":"^[a-z]+$"},"type":"object"}`,
		"pick":  `{"additionalProperties":{"format":"int64","type":"integer"},"maxProperties":8192,"propertyNames":{"enum":["a","b"]},"type":"object"}`,
		"ids":   `{"additionalProperties":{"format":"int64","type":"integer"},"maxProperties":8192,"propertyNames":{"format":"uuid","maxLength":36},"type":"object"}`,
		"rows": `{"items":{"additionalProperties":{"format":"int64","type":"integer"},"maxProperties":8192,"propertyNames":{"maxLength":2},"type":"object"},` +
			`"maxItems":8192,"type":"array"}`,
	} {
		if g := compact(t, at(t, props, name)); g != want {
			t.Errorf("%s:\n got %s\nwant %s", name, g, want)
		}
	}
	// A response's schema states what was declared, and no backstop; a
	// response is held to the declared keywords alone (getatest checked the
	// one /o wrote, whose free key is past the backstop).
	if g := compact(t, at(t, d, "components", "schemas", "kwOut", "properties")); g !=
		`{"free":{"additionalProperties":{"format":"int64","type":"integer"},"type":"object"},`+
			`"tags":{"additionalProperties":{"format":"int64","type":"integer"},"propertyNames":{"maxLength":3},"type":"object"}}` {
		t.Error(g)
	}
	c.Get("/o")
	if err := c.App().Conforms(geta.Match{Template: "/o", Method: "GET", Operation: true}, http.StatusOK, nil, []byte(`{"tags":{"abcd":1},"free":{}}`)); err == nil ||
		!strings.Contains(err.Error(), "key length 4 exceeds maxLength 3") {
		t.Errorf("Conforms: %v", err)
	}
}

type KeyedCounts struct {
	M map[string]int `json:"m"`
}

type KeyedCountsDeclared struct {
	M map[string]int `json:"m" schema:"maxProperties=5,propertyNames.maxLength=64"`
}

// A map's keys are read under the operation's limits: two operations reading
// one component under limits whose MaxStringLength differs state its keys
// differently and are refused, unless propertyNames.maxLength replaces the
// backstop.
func TestKeysAreReadUnderTheOperationsLimits(t *testing.T) {
	short := func(l geta.Limits) geta.Limits { l.MaxStringLength = 8; return l }
	read := func(context.Context, *struct {
		B KeyedCounts `body:"json"`
	}) (*ok, error) {
		return &ok{true}, nil
	}
	_, err := geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Post: geta.Op(http.StatusOK, read, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Post: geta.Op(http.StatusOK, read, geta.Doc{Limits: short})}},
	}})
	if err == nil || !strings.Contains(err.Error(), "POST /a and POST /b read geta_test.KeyedCounts under conflicting limits (MaxStringLength 4096 and 8,") {
		t.Errorf("limits: %v", err)
	}
	declared := func(context.Context, *struct {
		B KeyedCountsDeclared `body:"json"`
	}) (*ok, error) {
		return &ok{true}, nil
	}
	if _, err := geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Post: geta.Op(http.StatusOK, declared, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Post: geta.Op(http.StatusOK, declared, geta.Doc{Limits: short})}},
	}}); err != nil {
		t.Fatal(err)
	}
	// The operation's own backstop holds a key it reads, and its document
	// states it.
	app, err := geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/b", Route: geta.Route{Post: geta.Op(http.StatusOK, read, geta.Doc{Limits: short})}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	c := getatest.Serve(t, app)
	if res := c.Post("/b", `{"m":{"`+strings.Repeat("k", 8)+`":1}}`); res.Status != 200 {
		t.Error(res.Status, res.Text())
	}
	same(t, violations(t, c.Post("/b", `{"m":{"`+strings.Repeat("k", 9)+`":1}}`)),
		"body $.m."+strings.Repeat("k", 9)+": key length 9 exceeds the ceiling of 8 code points")
	if g := compact(t, at(t, doc(t, app), "components", "schemas", "KeyedCounts", "properties", "m", "propertyNames")); g != `{"maxLength":8}` {
		t.Error(g)
	}
}

type kwOnAString struct {
	S string `json:"s" schema:"propertyNames.maxLength=1"`
}

type kwOnASlice struct {
	S []string `json:"s" schema:"propertyNames.maxLength=1"`
}

type kwOnAStruct struct {
	S ok `json:"s" schema:"propertyNames.maxLength=1"`
}

type kwOnAnAnonymousStruct struct {
	S struct {
		X int `json:"x"`
	} `json:"s" schema:"propertyNames.maxLength=1"`
}

type kwOnOwnJSON struct {
	C cents `json:"c" schema:"propertyNames.maxLength=1"`
}

type kwDefault struct {
	M map[string]string `json:"m" schema:"propertyNames.default=a"`
}

type kwWrongType struct {
	M map[string]string `json:"m" schema:"propertyNames.minimum=1"`
}

type kwNested struct {
	M map[string]string `json:"m" schema:"propertyNames.items.maxLength=1"`
}

type kwUnchecked struct {
	M map[string]string `json:"m" schema:"propertyNames.format=email"`
}

type kwEmpty struct {
	M map[string]string `json:"m" schema:"propertyNames.minLength=3,propertyNames.maxLength=2"`
}

type kwEnumPattern struct {
	M map[string]string `json:"m" schema:"propertyNames.enum=A,propertyNames.pattern=^[a-z]+$"`
}

type kwCeiling struct {
	M map[string]string `json:"m" schema:"propertyNames.pattern=^a+$,propertyNames.maxLength=5000"`
}

type kwBackstop struct {
	M map[string]string `json:"m" schema:"propertyNames.minLength=5000"`
}

type kwRowsBackstop struct {
	L []map[string]string `json:"l" schema:"items.propertyNames.minLength=5000"`
}

// A keyword after propertyNames. is refused where there is no map (or
// WithSchema object), where a string does not take it, as an annotation,
// where no key meets it, and past what a request can be read under (a type
// only a response writes is held to neither the ceiling nor the backstops).
func TestKeyKeywordMistakesAreRefused(t *testing.T) {
	refused := func(err error, want string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v; want %q", err, want)
		}
	}
	refused(sizedRoute[kwOnAString](false), "kwOnAString.S: schema keyword propertyNames.maxLength applies to the keys of a map, not string")
	refused(sizedRoute[kwOnASlice](false), "kwOnASlice.S: schema keyword propertyNames.maxLength applies to the keys of a map, not array")
	refused(sizedRoute[kwOnAStruct](false), "kwOnAStruct.S: schema keyword propertyNames.maxLength applies to the keys of a map, not a struct type")
	refused(sizedRoute[kwOnAnAnonymousStruct](false),
		"kwOnAnAnonymousStruct.S: schema keyword propertyNames.maxLength applies to the keys of a map, not a struct")
	refused(sizedRoute[kwOnOwnJSON](false),
		"kwOnOwnJSON.C: schema keyword propertyNames.maxLength applies to the keys of a map, not a type with JSON methods of its own")
	refused(sizedRoute[kwDefault](false), "kwDefault.M: schema keyword propertyNames.default: a key takes no default; put it on the map")
	refused(sizedRoute[kwWrongType](false), "kwWrongType.M: propertyNames: schema keyword minimum applies to integer or number, not string")
	refused(sizedRoute[kwNested](false), "kwNested.M: propertyNames: schema keyword items.maxLength applies to the elements of an array, not string")
	refused(sizedRoute[kwUnchecked](false), `kwUnchecked.M: propertyNames: schema keyword format: unknown format "email"`)
	refused(sizedRoute[kwEmpty](false), "kwEmpty.M: propertyNames: minLength 3 exceeds maxLength 2")
	refused(sizedRoute[kwEnumPattern](false), `kwEnumPattern.M: propertyNames: enum member "A" does not match pattern ^[a-z]+$`)
	refused(sizedRoute[kwCeiling](true), "kwCeiling.M: propertyNames: maxLength 5000 with a pattern exceeds the pattern ceiling")
	refused(sizedRoute[kwBackstop](true), "kwBackstop.M: propertyNames: minLength 5000 exceeds Limits.MaxStringLength 4096")
	refused(sizedRoute[kwRowsBackstop](true), "kwRowsBackstop.L: items: propertyNames: minLength 5000 exceeds Limits.MaxStringLength 4096")
	for _, written := range []error{sizedRoute[kwCeiling](false), sizedRoute[kwBackstop](false), sizedRoute[kwRowsBackstop](false)} {
		if written != nil {
			t.Errorf("written only: %v", written)
		}
	}
	// A WithSchema object's keys are strings, which geta reads before the
	// type does: its declaration takes the keywords, an array's does not.
	if err := sizedRoute[sizedBody](true, geta.WithSchema[ownObject]("object", "propertyNames.maxLength=8")); err != nil {
		t.Error(err)
	}
	refused(sizedRoute[sizedBody](false, geta.WithSchema[ownObject]("array", "propertyNames.maxLength=1")),
		"geta.WithSchema[github.com/koji-1009/geta_test.ownObject]: schema keyword propertyNames.maxLength applies to the keys of a map, not array")

	for kind, tag := range map[string]string{
		"string":            "propertyNames.maxLength=1",
		"[]string":          "propertyNames.maxLength=1",
		"object":            "propertyNames.maxLength=1",
		"struct":            "propertyNames.maxLength=1",
		"map[string]int":    "propertyNames.minimum=1",
		"map[string]string": "propertyNames.examples=a",
		"map":               "propertyNames.minimum=1", // a key is a string whatever the values
		"[]map[string]int":  "items.propertyNames.uniqueItems=true",
	} {
		if err := geta.CheckSchemaTag(tag, kind); err == nil {
			t.Errorf("CheckSchemaTag(%q, %q) accepted", tag, kind)
		}
	}
	for kind, tag := range map[string]string{
		"map[string]string":          "maxProperties=2,propertyNames.enum=a|b,propertyNames.maxLength=3",
		"[]map[string]int8":          "items.propertyNames.pattern=^[a-z]+$",
		"map[string]map[string]?int": "additionalProperties.propertyNames.format=uuid",
		"?map[string]int":            "propertyNames.minLength=1",
		"map":                        "propertyNames.maxLength=9",
	} {
		if err := geta.CheckSchemaTag(tag, kind); err != nil {
			t.Errorf("CheckSchemaTag(%q, %q): %v", tag, kind, err)
		}
	}
	if err := geta.CheckRequestSchemaTag("propertyNames.pattern=^a$,propertyNames.maxLength=5000", "map[string]string"); err == nil ||
		!strings.Contains(err.Error(), "propertyNames: maxLength 5000 with a pattern") {
		t.Error(err)
	}
	if err := geta.CheckSchemaTag("propertyNames.pattern=^a$,propertyNames.maxLength=5000", "map[string]string"); err != nil {
		t.Error(err)
	}
}
