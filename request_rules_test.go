package geta_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// The rules of request binding the other tests leave unasserted, each
// asserted as its statement says.

// post is a POST operation of h.
func post[In, Out any](h func(context.Context, *In) (*Out, error)) geta.Route {
	return geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}
}

// An input is a struct: geta.New refuses any other, with CheckInputType's
// text.
func TestANonStructInputIsRefused(t *testing.T) {
	const want = "input type int is not a struct"
	rejects(t, one("/x", get(func(context.Context, *int) (*ok, error) { return nil, nil })), want)
	if err := geta.CheckInputType("int", false); err == nil || err.Error() != want {
		t.Fatal(err)
	}
}

type rqDupQuery struct {
	A string  `query:"a"`
	B *string `query:"a"`
}

type rqDupCookie struct {
	A string  `cookie:"c"`
	B *string `cookie:"c"`
}

type rqNullHeader struct {
	H geta.Nullable[string] `header:"X-H"`
}

type rqNullCookie struct {
	C geta.Nullable[string] `cookie:"c"`
}

type rqNullForm struct {
	Body struct {
		N geta.Nullable[int] `form:"n"`
	} `body:"form"`
}

// rqReadOnly reads itself from text but writes itself otherwise.
type rqReadOnly struct{ s string }

func (r *rqReadOnly) UnmarshalText(b []byte) error { r.s = string(b); return nil }

// rqWriteOnly writes itself as text but reads itself otherwise.
type rqWriteOnly struct{ s string }

func (r rqWriteOnly) MarshalText() ([]byte, error) { return []byte(r.s), nil }

type rqOneSidedIn struct {
	R *rqReadOnly `query:"r"`
}

type rqOneSidedHeader struct {
	W *rqWriteOnly `header:"X-W"`
}

// A parameter is bound by one field: a second field binding a query or a
// cookie name is refused. A geta.Nullable is no parameter, header, cookie,
// or form field, none of which carries null, and a type with one text
// method only is no parameter.
func TestParameterTypeMistakesAreRefused(t *testing.T) {
	for name, c := range map[string]struct {
		tbl  geta.Table
		want string
	}{
		"query twice":    {one("/x", get(func(context.Context, *rqDupQuery) (*ok, error) { return nil, nil })), `rqDupQuery.B binds query "a", already bound by A`},
		"cookie twice":   {one("/x", get(func(context.Context, *rqDupCookie) (*ok, error) { return nil, nil })), `rqDupCookie.B binds cookie "c", already bound by A`},
		"nullable head":  {one("/x", get(func(context.Context, *rqNullHeader) (*ok, error) { return nil, nil })), `rqNullHeader.H: header parameter "X-H" has unsupported type geta.Nullable[string]`},
		"nullable cook":  {one("/x", get(func(context.Context, *rqNullCookie) (*ok, error) { return nil, nil })), `rqNullCookie.C: cookie parameter "c" has unsupported type geta.Nullable[string]`},
		"nullable form":  {one("/x", post(func(context.Context, *rqNullForm) (*ok, error) { return nil, nil })), `form field "n" has unsupported type geta.Nullable[int]`},
		"read only text": {one("/x", get(func(context.Context, *rqOneSidedIn) (*ok, error) { return nil, nil })), "type geta_test.rqReadOnly implements encoding.TextUnmarshaler but not encoding.TextMarshaler"},
		"write only":     {one("/x", get(func(context.Context, *rqOneSidedHeader) (*ok, error) { return nil, nil })), "type geta_test.rqWriteOnly implements encoding.TextMarshaler but not encoding.TextUnmarshaler"},
	} {
		_, err := geta.New(c.tbl)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v\nwant %q", name, err, c.want)
		}
	}
}

type rqParamsIn struct {
	Q []string `query:"q"`
	S string   `query:"s" schema:"default=d"`
	N int      `query:"n" schema:"default=3"`
	H string   `header:"X-H" schema:"default=hv"`
	C *string  `cookie:"c"`
}

type rqParamsOut struct {
	Q []string `json:"q"`
	S string   `json:"s"`
	N int      `json:"n"`
	H string   `json:"h"`
}

// A required slice parameter with no value is missing; a header with a
// default takes it when left out; an empty value (?s=) is a value, which a
// default does not replace; a header or a cookie the input does not
// declare is ignored.
func TestParameterPresence(t *testing.T) {
	c := getatest.New(t, one("/p", get(func(_ context.Context, in *rqParamsIn) (*rqParamsOut, error) {
		return &rqParamsOut{Q: in.Q, S: in.S, N: in.N, H: in.H}, nil
	})))
	same(t, violations(t, c.Get("/p")), "query q: missing required parameter")
	res := c.With("X-Other", "1").With("Cookie", "other=2").Get("/p?q=a&q=b")
	if got := res.JSON[rqParamsOut](); res.Status != 200 || fmt.Sprint(got) != "{[a b] d 3 hv}" {
		t.Fatal(res.Status, res.Text())
	}
	res = c.With("X-H", "x").Get("/p?q=a&s=")
	if got := res.JSON[rqParamsOut](); res.Status != 200 || got.S != "" || got.H != "x" {
		t.Fatal(res.Status, res.Text())
	}
	same(t, violations(t, c.Get("/p?q=a&n=")), "query n: expected integer, got string")
}

type rqJSONBody struct {
	A   string `json:"a"`
	Raw []byte `json:"raw"`
}

type rqJSONIn struct {
	Body rqJSONBody `body:"json"`
}

type rqOptJSONIn struct {
	Body *rqJSONBody `body:"json"`
}

// A body of at least one byte is content, checked against the schema: one
// of whitespace alone is no JSON value, and null is no object, required or
// optional. JSON is exactly one value: a byte-order mark, an escaped
// duplicate name, and invalid UTF-8 are refused; a []byte member is
// standard base64.
func TestJSONBodyContent(t *testing.T) {
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/r", Route: post(func(context.Context, *rqJSONIn) (*ok, error) { return &ok{true}, nil })},
		{Path: "/o", Route: post(func(context.Context, *rqOptJSONIn) (*ok, error) { return &ok{true}, nil })},
	}})
	for path, cases := range map[string]map[string]string{
		"/r": {
			" \n\t":                            "body $: unexpected end of JSON input",
			"null":                             "body $: expected object, got null",
			`{"a":"x","` + `\` + `u0061":"y"}`: "body $.a: duplicate object key", // a name escaped
			`{"a":"x","raw":"!!"}`:             `body $.raw: "!!" is not valid base64`,
			`{"a":"x","raw":"AA=="} `:          "",
		},
		"/o": {
			" ":    "body $: unexpected end of JSON input",
			"null": "body $: expected object, got null",
			"":     "",
		},
	} {
		for body, want := range cases {
			res := c.Post(path, body)
			if want == "" {
				if res.Status != 200 {
					t.Errorf("%s %q: %d %s", path, body, res.Status, res.Body)
				}
				continue
			}
			same(t, violations(t, res), want)
		}
	}
	for _, body := range []string{"\xef\xbb\xbf{\"a\":\"x\",\"raw\":\"\"}", "{\"a\":\"\xff\",\"raw\":\"\"}"} {
		v := violations(t, c.Post("/r", body))
		if len(v) != 1 || !strings.HasPrefix(v[0], "body $: malformed JSON at byte ") {
			t.Errorf("%q: %q", body, v)
		}
	}
	if v := violations(t, c.Post("/r", "{\"a\":\"\xff\",\"raw\":\"\"}")); !strings.Contains(v[0], "invalid UTF-8") {
		t.Error(v)
	}
}

// rqBrokenBody sends its content, then fails.
type rqBrokenBody struct{ content string }

func (b *rqBrokenBody) Read(p []byte) (int, error) {
	if b.content == "" {
		return 0, errors.New("the connection went away")
	}
	n := copy(p, b.content)
	b.content = b.content[n:]
	return n, nil
}

// A body that cannot be read, at its first byte or later, is a 400 at $,
// for every encoding.
func TestABodyThatCannotBeReadIs400(t *testing.T) {
	app := accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/j", Route: post(func(context.Context, *rqJSONIn) (*ok, error) { return &ok{true}, nil })},
		{Path: "/f", Route: post(func(context.Context, *rqLimitFormIn) (*ok, error) { return &ok{true}, nil })},
		{Path: "/r", Route: post(func(context.Context, *rqRawOptIn) (*rqRawOut, error) { return &rqRawOut{}, nil })},
	}})
	for path, ct := range map[string]string{"/j": "application/json", "/f": "application/x-www-form-urlencoded", "/r": "text/csv"} {
		for _, content := range []string{"", "{"} {
			req := httptest.NewRequest(http.MethodPost, path, &rqBrokenBody{content})
			req.Header.Set("Content-Type", ct)
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			if p := problemOf(t, rec); rec.Code != 400 || fmt.Sprint(listed(p)) != "[body $: the request body could not be read]" {
				t.Errorf("%s %q: %d %v", path, content, rec.Code, listed(p))
			}
		}
	}
}

// rqOwnNum reads any JSON number as itself.
type rqOwnNum struct{ v string }

func (n rqOwnNum) MarshalJSON() ([]byte, error) { return []byte(n.v), nil }
func (n *rqOwnNum) UnmarshalJSON(b []byte) error {
	var f json.Number
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	n.v = string(b)
	return nil
}

// rqOwnList reads a JSON array of integers as itself.
type rqOwnList struct{ v []int }

func (l rqOwnList) MarshalJSON() ([]byte, error)  { return json.Marshal(l.v) }
func (l *rqOwnList) UnmarshalJSON(b []byte) error { return json.Unmarshal(b, &l.v) }

type rqOwnBody struct {
	N rqOwnNum  `json:"n"`
	I rqOwnNum  `json:"i"`
	L rqOwnList `json:"l"`
}

// A type with its own JSON methods whose schema WithSchema declares as a
// number, an integer, or an array is held to that declaration's keywords
// before its UnmarshalJSON reads it.
func TestDeclaredOwnJSONTypesAreChecked(t *testing.T) {
	type numIn struct {
		Body rqOwnBody `body:"json"`
	}
	c := getatest.New(t, one("/o", post(func(context.Context, *numIn) (*ok, error) { return &ok{true}, nil })),
		geta.WithSchema[rqOwnNum]("number", "maximum=10"), geta.WithSchema[rqOwnList]("array", "maxItems=2"))
	if res := c.Post("/o", `{"n":1.5,"i":2,"l":[1,2]}`); res.Status != 200 {
		t.Fatal(res.Status, res.Text())
	}
	same(t, violations(t, c.Post("/o", `{"n":1e400,"i":11,"l":[1,2,3]}`)),
		"body $.n: 1e400 is out of range", "body $.i: 11 is greater than maximum 10", "body $.l: array length 3 exceeds maxItems 2")
	ci := getatest.New(t, one("/o", post(func(context.Context, *numIn) (*ok, error) { return &ok{true}, nil })),
		geta.WithSchema[rqOwnNum]("integer", "minimum=0"), geta.WithSchema[rqOwnList]("array", "minItems=1"))
	same(t, violations(t, ci.Post("/o", `{"n":1.5,"i":-1,"l":[]}`)),
		"body $.n: expected integer, got number", "body $.i: -1 is less than minimum 0", "body $.l: array length 0 is shorter than minItems 1")
}

// Each schema-tag refusal geta.New makes is CheckSchemaTag's (or, for a
// value a request reads, CheckRequestSchemaTag's), with its text.
func TestSchemaTagRefusals(t *testing.T) {
	long := strings.Repeat("a", 4097)
	for _, c := range []struct{ tag, kind, want string }{
		{"minLength", "string", `schema tag entry "minLength" is not key=value`},
		{"minLength=1,minLength=2", "string", `schema tag repeats keyword "minLength"`},
		{"pattern=", "string", "schema keyword pattern: empty pattern"},
		{"format=", "string", "schema keyword format: empty format"},
		{"enum=a||b", "string", `schema keyword enum: "a||b" has an empty member`},
		{"maxLength=2,enum=ab|abc", "string", `enum member "abc" exceeds maxLength 2`},
		{"minLength=2,enum=a|ab", "string", `enum member "a" is shorter than minLength 2`},
		{"multipleOf=0.5", "int", "multipleOf 0.5 on an integer is not an integer"},
		{"minItems=3,maxItems=2", "slice", "minItems 3 exceeds maxItems 2"},
		{"maxLength=3", "slice", "schema keyword maxLength applies to string, not array"},
		{"uniqueItems=yes", "slice", `schema keyword uniqueItems: "yes" is not true or false`},
		{"examples=", "int", `schema keyword examples: "" has an empty member`},
		{"default=x", "geta.File", "schema tag \"default=x\" on a geta.File"},
	} {
		if err := geta.CheckSchemaTag(c.tag, c.kind); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s on %s: %v\nwant %q", c.tag, c.kind, err, c.want)
		}
	}
	// Beside a pattern, a default or an example past the pattern ceiling is
	// one no request carries, whatever the Limits.
	for _, tag := range []string{"pattern=^a+$,default=" + long, "pattern=^a+$,examples=a|" + long} {
		if err := geta.CheckSchemaTag(tag, "string"); err != nil {
			t.Errorf("CheckSchemaTag: %v", err)
		}
		if err := geta.CheckRequestSchemaTag(tag, "string"); err == nil ||
			!strings.Contains(err.Error(), "with a pattern exceeds the pattern ceiling of 4096 code points") {
			t.Errorf("CheckRequestSchemaTag: %v", err)
		}
	}
}

type rqUniqueBody struct {
	B *[]bool       `json:"b,omitzero" schema:"uniqueItems=true"`
	S *[]string     `json:"s,omitzero" schema:"uniqueItems=true"`
	N *[][]float64  `json:"n,omitzero" schema:"uniqueItems=true"`
	M *[]rqJSONBody `json:"m,omitzero" schema:"uniqueItems=true"`
}

type rqUniqueIn struct {
	Body rqUniqueBody `body:"json"`
}

// uniqueItems compares booleans, strings, and nested arrays as JSON values,
// numbers by value, and reports the first repeated pair.
func TestUniqueItemsOfEveryKind(t *testing.T) {
	c := getatest.New(t, one("/u", post(func(context.Context, *rqUniqueIn) (*ok, error) { return &ok{true}, nil })))
	if res := c.Post("/u", `{"b":[true,false],"s":["a","A"],"n":[[1,2],[2,1],[1],[]],"m":[{"a":"x","raw":""},{"a":"x","raw":"AA=="}]}`); res.Status != 200 {
		t.Fatal(res.Status, res.Text())
	}
	same(t, violations(t, c.Post("/u", `{"b":[true,false,true,false],"s":["a","b","a"],"n":[[1,2],[1,2.0]],"m":[{"raw":"","a":"x"},{"a":"x","raw":""}]}`)),
		"body $.b: array items at [0] and [2] are equal (uniqueItems)",
		"body $.s: array items at [0] and [2] are equal (uniqueItems)",
		"body $.n: array items at [0] and [1] are equal (uniqueItems)",
		"body $.m: array items at [0] and [1] are equal (uniqueItems)")
}

type rqSliceIn struct {
	Q *[]string `query:"q" schema:"maxItems=2"`
}

type rqSliceBad struct {
	Q *[]string `query:"q" schema:"maxLength=3"`
}

// Keywords on a slice constrain the array; its elements carry their own
// type's schema, which no tag on the slice reaches.
func TestSliceKeywordsConstrainTheArray(t *testing.T) {
	rejects(t, one("/x", get(func(context.Context, *rqSliceBad) (*ok, error) { return nil, nil })),
		"rqSliceBad.Q: schema keyword maxLength applies to string, not array")
	app := accepts(t, one("/x", get(func(context.Context, *rqSliceIn) (*ok, error) { return &ok{true}, nil })))
	if g := compact(t, at(t, doc(t, app), "paths", "/x", "get", "parameters").([]any)[0].(map[string]any)["schema"]); g !=
		`{"items":{"maxLength":4096,"type":"string"},"maxItems":2,"type":"array"}` {
		t.Fatal(g)
	}
}

type rqFormCommon struct {
	Tag  string `form:"tag" schema:"default=t"`
	Size int    `form:"size"`
}

type rqFormEmbedIn struct {
	Body struct {
		rqFormCommon
		Name string `form:"name"`
		_    string // unexported and untagged: no field of the form
	} `body:"form"`
}

type rqFormEmbedPtr struct {
	Body struct {
		*rqFormCommon
	} `body:"form"`
}

type rqFormUnexported struct {
	Body struct {
		name string `form:"name"`
	} `body:"form"`
}

type rqFormEmpty struct {
	Body struct {
		Name string `form:""`
	} `body:"form"`
}

type rqFormPtrDefault struct {
	Body struct {
		N *int `form:"n" schema:"default=3"`
	} `body:"form"`
}

type rqFormBadDefault struct {
	Body struct {
		N int `form:"n" schema:"default=0,minimum=1"`
	} `body:"form"`
}

type rqFormBadTag struct {
	Body struct {
		N int `form:"n" schema:"bogus=1"`
	} `body:"form"`
}

type rqFormNoForm struct {
	Body struct {
		W time.Duration `form:"w"`
	} `body:"form"`
}

type rqFormBadEmbedded struct {
	Body struct {
		rqFormMapped
	} `body:"form"`
}

type rqFormMapped struct {
	M map[string]string `form:"m"`
}

type rqMemberBadDefault struct {
	Body struct {
		N int `json:"n" schema:"default=0,minimum=1"`
	} `body:"json"`
}

type rqFormOut struct {
	Tag  string `json:"tag"`
	Size int    `json:"size"`
	Name string `json:"name"`
}

// An untagged embedded struct in a form has its fields bound as the form's,
// a default among them; an embedded pointer, a tagged unexported field, an
// empty name, a default on a pointer, and a default the schema refuses are
// refused.
func TestFormFieldsEmbeddedAndRefused(t *testing.T) {
	c := getatest.New(t, one("/f", post(func(_ context.Context, in *rqFormEmbedIn) (*rqFormOut, error) {
		return &rqFormOut{Tag: in.Body.Tag, Size: in.Body.Size, Name: in.Body.Name}, nil
	})))
	res := c.Form(http.MethodPost, "/f", url.Values{"size": {"2"}, "name": {"n"}})
	if got := res.JSON[rqFormOut](); res.Status != 200 || got != (rqFormOut{"t", 2, "n"}) {
		t.Fatal(res.Status, res.Text())
	}
	same(t, violations(t, c.Form(http.MethodPost, "/f", url.Values{"tag": {"x"}})),
		"body $.size: missing required field", "body $.name: missing required field")
	if s := compact(t, at(t, doc(t, c.App()), "paths", "/f", "post", "requestBody", "content", "application/x-www-form-urlencoded", "schema", "required")); s != `["size","name"]` {
		t.Fatal(s)
	}
	for name, cs := range map[string]struct {
		tbl  geta.Table
		want string
	}{
		"embedded pointer": {one("/x", post(func(context.Context, *rqFormEmbedPtr) (*ok, error) { return nil, nil })), "rqFormCommon: embedded pointer types are not supported in a form; embed geta_test.rqFormCommon"},
		"unexported":       {one("/x", post(func(context.Context, *rqFormUnexported) (*ok, error) { return nil, nil })), "name is tagged form but unexported"},
		"empty name":       {one("/x", post(func(context.Context, *rqFormEmpty) (*ok, error) { return nil, nil })), "Name: empty form field name"},
		"pointer default":  {one("/x", post(func(context.Context, *rqFormPtrDefault) (*ok, error) { return nil, nil })), "N: a pointer field takes no default; use int"},
		"refused default":  {one("/x", post(func(context.Context, *rqFormBadDefault) (*ok, error) { return nil, nil })), `N: default "0": 0 is less than minimum 1`},
		"bad schema tag":   {one("/x", post(func(context.Context, *rqFormBadTag) (*ok, error) { return nil, nil })), `N: schema tag has unknown keyword "bogus"`},
		"no form":          {one("/x", post(func(context.Context, *rqFormNoForm) (*ok, error) { return nil, nil })), "W: type time.Duration has no JSON form"},
		"inside embedded":  {one("/x", post(func(context.Context, *rqFormBadEmbedded) (*ok, error) { return nil, nil })), `rqFormMapped.M: form field "m" has unsupported type map[string]string`},
		"member default":   {one("/x", post(func(context.Context, *rqMemberBadDefault) (*ok, error) { return nil, nil })), `N: default "0": 0 is less than minimum 1`},
	} {
		_, err := geta.New(cs.tbl)
		if err == nil || !strings.Contains(err.Error(), cs.want) {
			t.Errorf("%s: %v\nwant %q", name, err, cs.want)
		}
	}
}

// CheckFormField's own branches, which getavet reaches with the field's
// kind: geta.New gives the same text where it reaches them.
func TestCheckFormFieldBranches(t *testing.T) {
	for _, c := range []struct {
		f         geta.VetField
		multipart bool
		walk      bool
		want      string
	}{
		{geta.VetField{Name: "E", Embedded: true, Struct: true, Exported: true}, false, true, ""},
		{geta.VetField{Name: "T", Embedded: true, Struct: true, Text: true, Type: "lib.T"}, false, false, "T: embedded lib.T is a text type with no fields; tag it form"},
		{geta.VetField{Name: "E", Embedded: true, Struct: true, Tag: `schema:"deprecated=true"`}, false, false, "E: an embedded struct takes no schema tag"},
		{geta.VetField{Name: "E", Embedded: true, Pointer: true, Type: "*lib.E"}, false, false, "E: embedded pointer types are not supported in a form; embed lib.E"},
		{geta.VetField{Name: "x"}, false, false, ""},
		{geta.VetField{Name: "X", Exported: true}, false, false, "X has no form tag"},
		{geta.VetField{Name: "x", Tag: `form:"x"`}, false, false, "x is tagged form but unexported"},
		{geta.VetField{Name: "X", Exported: true, Tag: `form:""`}, false, false, "X: empty form field name"},
		{geta.VetField{Name: "X", Exported: true, Pointer: true, Type: "*int", Tag: `form:"x" schema:"default=1"`}, false, false, "X: a pointer field takes no default"},
		{geta.VetField{Name: "F", Exported: true, Type: "geta.File", Kind: "geta.File", Tag: `form:"f"`}, false, false, `F: form field "f" has file type geta.File outside a multipart body`},
		{geta.VetField{Name: "F", Exported: true, Type: "[]geta.File", Kind: "[]geta.File", Tag: `form:"f" schema:"uniqueItems=true"`}, true, false, `F: form field "f": uniqueItems on files`},
		{geta.VetField{Name: "F", Exported: true, Type: "[]geta.File", Kind: "[]geta.File", Tag: `form:"f" schema:"maxItems=2"`}, true, false, ""},
		{geta.VetField{Name: "M", Exported: true, Type: "map[string]string", Kind: "map", Tag: `form:"m"`}, false, false, `M: form field "m" has unsupported type map[string]string`},
		{geta.VetField{Name: "S", Exported: true, Type: "[]int", Kind: "[]int", Tag: `form:"s"`}, false, false, ""},
	} {
		walk, err := geta.CheckFormField(c.f, c.multipart, map[string]string{})
		if walk != c.walk || c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%+v: %v %v\nwant %v %q", c.f, walk, err, c.walk, c.want)
		}
	}
	seen := map[string]string{}
	if _, err := geta.CheckFormField(geta.VetField{Name: "A", Exported: true, Tag: `form:"a"`}, false, seen); err != nil {
		t.Fatal(err)
	}
	if _, err := geta.CheckFormField(geta.VetField{Name: "B", Exported: true, Tag: `form:"a"`}, false, seen); err == nil ||
		err.Error() != `B binds form field "a", already bound by A` {
		t.Fatal(err)
	}
}

type rqFilesIn struct {
	Body struct {
		Files []geta.File  `form:"files"`
		More  *[]geta.File `form:"more"`
	} `body:"multipart"`
}

type rqFilesOut struct {
	Files  int `json:"files"`
	More   int `json:"more"`
	OnDisk int `json:"onDisk"`
}

// tmpEntries counts the files in dir.
func tmpEntries(dir string) int {
	entries, _ := os.ReadDir(dir)
	return len(entries)
}

// A required []geta.File takes at least one part; one with no maxItems is
// bounded by MaxItems. With the default limits, every file of an accepted
// body is held in memory; MaxMultipartMemory of zero or less writes every
// non-empty file to disk, removed once the handler has returned; a file
// geta cannot store is a 500, not a 400.
func TestMultipartFilesAndTheirStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	files := func(n int, size int) []getatest.FilePart {
		var fs []getatest.FilePart
		for i := range n {
			fs = append(fs, getatest.FilePart{Field: "files", Filename: strconv.Itoa(i), Content: bytes.Repeat([]byte("x"), size)})
		}
		return fs
	}
	serve := func(limits geta.Limits) *getatest.Client {
		app, err := geta.New(one("/m", post(func(_ context.Context, in *rqFilesIn) (*rqFilesOut, error) {
			out := &rqFilesOut{Files: len(in.Body.Files), OnDisk: tmpEntries(dir)}
			if in.Body.More != nil {
				out.More = len(*in.Body.More)
			}
			return out, nil
		})), geta.WithLimits(limits))
		if err != nil {
			t.Fatal(err)
		}
		return getatest.Serve(t, app)
	}
	c := serve(geta.DefaultLimits)
	same(t, violations(t, c.Multipart(http.MethodPost, "/m", url.Values{"x": nil}, getatest.FilePart{Field: "more", Filename: "m"})),
		"body $.files: missing required field")
	res := c.Multipart(http.MethodPost, "/m", nil, files(1, int(geta.DefaultLimits.MaxBodyBytes)-400)...)
	if got := res.JSON[rqFilesOut](); res.Status != 200 || got.Files != 1 || got.OnDisk != 0 {
		t.Fatal(res.Status, res.Text())
	}
	tight := geta.DefaultLimits
	tight.MaxItems = 2
	same(t, violations(t, serve(tight).Multipart(http.MethodPost, "/m", nil, files(3, 1)...)),
		"body $.files: array length 3 exceeds the ceiling of 2 items")
	for _, mem := range []int64{0, -1} {
		l := geta.DefaultLimits
		l.MaxMultipartMemory = mem
		cd := serve(l)
		res := cd.Multipart(http.MethodPost, "/m", nil, append(files(2, 3), getatest.FilePart{Field: "more", Filename: "empty"})...)
		if got := res.JSON[rqFilesOut](); res.Status != 200 || got.Files != 2 || got.More != 1 || got.OnDisk != 2 {
			t.Fatalf("MaxMultipartMemory %d: %d %s", mem, res.Status, res.Body)
		}
		if n := tmpEntries(dir); n != 0 {
			t.Fatalf("MaxMultipartMemory %d: %d files left", mem, n)
		}
	}
	// No temporary file can be made: the server's failure.
	l := geta.DefaultLimits
	l.MaxMultipartMemory = 0
	cd := serve(l)
	t.Setenv("TMPDIR", filepath.Join(dir, "missing"))
	if res := cd.Multipart(http.MethodPost, "/m", nil, files(1, 3)...); res.Status != 500 || len(res.Problem().Errors) != 0 {
		t.Fatal(res.Status, res.Text())
	}
}

type rqRawOptIn struct {
	Body *[]byte `body:"text/csv"`
}

type rqRawReaderIn struct {
	Body io.Reader `body:"text/csv"`
}

type rqRawMultipart struct {
	Body []byte `body:"multipart/form-data"`
}

type rqRawProblemJSON struct {
	Body []byte `body:"application/problem+json"`
}

type rqRawOut struct {
	Got string `json:"got"`
}

// A raw body: an optional one with content holds the bytes; an io.Reader
// one past the body limit returns the *http.MaxBytesError, which a handler
// may wrap and still have answered 413; a multipart or +json media type is
// refused; a media type geta does not take is a 415 whatever its size, as a
// multipart body's is.
func TestRawBodyRules(t *testing.T) {
	limits := geta.DefaultLimits
	limits.MaxBodyBytes = 16
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/opt", Route: post(func(_ context.Context, in *rqRawOptIn) (*rqRawOut, error) {
			if in.Body == nil {
				return &rqRawOut{Got: "<nil>"}, nil
			}
			return &rqRawOut{Got: string(*in.Body)}, nil
		})},
		{Path: "/read", Route: post(func(_ context.Context, in *rqRawReaderIn) (*rqRawOut, error) {
			b, err := io.ReadAll(in.Body)
			if err != nil {
				return nil, fmt.Errorf("reading the rows: %w", err)
			}
			return &rqRawOut{Got: string(b)}, nil
		})},
		{Path: "/multi", Route: post(func(context.Context, *rqFilesIn) (*rqFilesOut, error) { return &rqFilesOut{}, nil })},
	}}, geta.WithLimits(limits))
	if res := c.Content(http.MethodPost, "/opt", "text/csv", []byte("a,b")); res.Status != 200 || res.JSON[rqRawOut]().Got != "a,b" {
		t.Fatal(res.Status, res.Text())
	}
	if res := c.Content(http.MethodPost, "/opt", "text/csv", nil); res.Status != 200 || res.JSON[rqRawOut]().Got != "<nil>" {
		t.Fatal(res.Status, res.Text())
	}
	if p := c.Content(http.MethodPost, "/read", "text/csv", bytes.Repeat([]byte("x"), 17)).Problem(); p.Status != 413 || p.Detail != "the request body exceeds 16 bytes" {
		t.Fatal(p)
	}
	for _, path := range []string{"/opt", "/read", "/multi"} {
		res := c.Content(http.MethodPost, path, "application/json", bytes.Repeat([]byte("x"), 100))
		if res.Status != 415 {
			t.Errorf("%s: %d %s", path, res.Status, res.Body)
		}
	}
	rejects(t, one("/x", post(func(context.Context, *rqRawMultipart) (*ok, error) { return nil, nil })),
		`Body: body tag "multipart/form-data" is a multipart form; use body:"multipart"`)
	rejects(t, one("/x", post(func(context.Context, *rqRawProblemJSON) (*ok, error) { return nil, nil })),
		`Body: body tag "application/problem+json" is JSON; use body:"json"`)
}

// DefaultLimits are the documented backstops.
func TestDefaultLimitsAreTheDocumentedOnes(t *testing.T) {
	want := geta.Limits{MaxBodyBytes: 1 << 20, MaxMultipartMemory: 1 << 20, MaxStringLength: 4096, MaxItems: 8192, MaxDepth: 512, MaxResponseBuffer: 64 << 10}
	if geta.DefaultLimits != want {
		t.Fatalf("%+v", geta.DefaultLimits)
	}
}

type rqNested struct {
	Next *rqNested `json:"next,omitzero"`
}

type rqDeepIn struct {
	Body rqNested `body:"json"`
}

type rqLimitFormIn struct {
	Body struct {
		Name string `form:"name"`
	} `body:"form"`
}

type rqLimitUploadIn struct {
	Body struct {
		F geta.File `form:"f"`
	} `body:"multipart"`
}

// An operation's Doc.Limits hold its form body (its size and its fields'
// backstops), its JSON body's nesting, and its files' memory, and its
// document states the form's backstops.
func TestOperationLimitsHoldEveryBody(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	own := func(l geta.Limits) geta.Limits {
		l.MaxBodyBytes, l.MaxStringLength, l.MaxDepth, l.MaxMultipartMemory = 300, 3, 2, 0
		return l
	}
	onDisk := -1
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/f", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *rqLimitFormIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{Limits: own})}},
		{Path: "/d", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *rqDeepIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{Limits: own})}},
		{Path: "/m", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *rqLimitUploadIn) (*ok, error) {
			onDisk = tmpEntries(dir)
			return &ok{true}, nil
		}, geta.Doc{Limits: own})}},
		{Path: "/app", Route: post(func(context.Context, *rqDeepIn) (*ok, error) { return &ok{true}, nil })},
	}})
	same(t, violations(t, c.Form(http.MethodPost, "/f", url.Values{"name": {"abcd"}})),
		"body $.name: string length 4 exceeds the ceiling of 3 code points")
	if p := c.Form(http.MethodPost, "/f", url.Values{"name": {strings.Repeat("a", 301)}}).Problem(); p.Status != 413 || p.Detail != "the request body exceeds 300 bytes" {
		t.Fatal(p)
	}
	if g := at(t, doc(t, c.App()), "paths", "/f", "post", "requestBody", "content", "application/x-www-form-urlencoded", "schema", "properties", "name", "maxLength"); g != 3.0 {
		t.Fatal(g)
	}
	deep := `{"next":{"next":{}}}`
	if v := violations(t, c.Post("/d", deep)); len(v) != 1 || !strings.Contains(v[0], "nesting exceeds the ceiling of 2") {
		t.Fatal(v)
	}
	if res := c.Post("/app", deep); res.Status != 200 {
		t.Fatal(res.Status, res.Text())
	}
	if res := c.Multipart(http.MethodPost, "/m", nil, getatest.FilePart{Field: "f", Filename: "f", Content: []byte("abc")}); res.Status != 200 || onDisk != 1 {
		t.Fatal(res.Status, res.Text(), onDisk)
	}
}

type rqVariant interface{ isRqVariant() }

type rqDotV struct {
	Kind string `json:"kind"`
	Fill string `json:"fill" schema:"default=none"`
}

type rqBoxV struct {
	Kind  string    `json:"kind"`
	Inner rqVariant `json:"inner,omitzero"`
}

func (rqDotV) isRqVariant() {}
func (rqBoxV) isRqVariant() {}

var rqVariants = geta.Sealed[rqVariant]("kind", geta.Case[rqDotV]("dot"), geta.Case[rqBoxV]("box"))

type rqDefEl struct {
	A string `json:"a" schema:"default=za"`
	B *int   `json:"b,omitzero"`
}

type rqDefaultsBody struct {
	List  []rqDefEl                           `json:"list"`
	Map   map[string]rqDefEl                  `json:"map"`
	Null  geta.Nullable[rqDefEl]              `json:"null"`
	Opt   geta.Nullable[rqDefEl]              `json:"opt,omitzero"`
	Ptr   *rqDefEl                            `json:"ptr,omitzero"`
	Shape rqVariant                           `json:"shape"`
	Nest  map[string][]geta.Nullable[rqDefEl] `json:"nest"`
}

type rqDefaultsIn struct {
	Body rqDefaultsBody `body:"json"`
}

// A member with a default that a request leaves out takes it at any depth:
// in a slice's elements, a map's values, a Nullable's value (not when it is
// null or absent), a pointer's value (not when it is absent), and a sealed
// variant, the discriminator first or late.
func TestDefaultsAtAnyDepth(t *testing.T) {
	var got rqDefaultsBody
	c := getatest.New(t, one("/d", post(func(_ context.Context, in *rqDefaultsIn) (*ok, error) {
		got = in.Body
		return &ok{true}, nil
	})), geta.WithUnion(rqVariants))
	describe := func(e rqDefEl) string {
		if e.B != nil {
			return e.A + ":" + strconv.Itoa(*e.B)
		}
		return e.A
	}
	nullable := func(n geta.Nullable[rqDefEl]) string {
		if v, ok := n.Get(); ok {
			return describe(v)
		}
		if n.IsNull() {
			return "null"
		}
		return "absent"
	}
	for _, tc := range []struct{ body, want string }{
		{`{"list":[{},{"a":"q","b":1}],"map":{"x":{},"y":{"a":"q"}},"null":{},"shape":{"kind":"dot"},"nest":{"k":[null,{}]}}`,
			"list [za q:1] map za q null za opt absent ptr <nil> shape none nest [null za]"},
		{`{"nest":{},"shape":{"fill":"red","kind":"dot"},"null":null,"opt":{"b":2},"ptr":{},"map":{},"list":[]}`,
			"list [] map  null null opt za:2 ptr za shape red nest []"},
		{`{"list":[],"map":{},"null":null,"opt":null,"shape":{"inner":{"kind":"dot"},"kind":"box"},"nest":{}}`,
			"list [] map  null null opt null ptr <nil> shape box(none) nest []"},
		{`{"list":[],"map":{},"null":null,"shape":{"kind":"box","inner":{"kind":"box","inner":{"fill":"x","kind":"dot"}}},"nest":{}}`,
			"list [] map  null null opt absent ptr <nil> shape box(box(x)) nest []"},
	} {
		got = rqDefaultsBody{}
		if res := c.Post("/d", tc.body); res.Status != 200 {
			t.Fatal(res.Status, res.Text())
		}
		var list, nest []string
		for _, e := range got.List {
			list = append(list, describe(e))
		}
		for _, n := range got.Nest["k"] {
			nest = append(nest, nullable(n))
		}
		ptr := "<nil>"
		if got.Ptr != nil {
			ptr = describe(*got.Ptr)
		}
		var shape func(v rqVariant) string
		shape = func(v rqVariant) string {
			switch v := v.(type) {
			case rqDotV:
				return v.Fill
			case rqBoxV:
				return "box(" + shape(v.Inner) + ")"
			}
			return fmt.Sprint(v)
		}
		s := fmt.Sprintf("list [%s] map %s null %s opt %s ptr %s shape %s nest [%s]", strings.Join(list, " "),
			strings.TrimSpace(describe(got.Map["x"])+" "+describe(got.Map["y"])), nullable(got.Null), nullable(got.Opt), ptr, shape(got.Shape), strings.Join(nest, " "))
		if s != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.body, s, tc.want)
		}
	}
}

type rqNullMapBody struct {
	M map[string]geta.Nullable[int] `json:"m"`
}

type rqNullMapIn struct {
	Body rqNullMapBody `body:"json"`
}

// A Nullable may be a map value: null, or a value, each held as sent.
func TestNullableMapValues(t *testing.T) {
	var got rqNullMapBody
	c := getatest.New(t, one("/n", post(func(_ context.Context, in *rqNullMapIn) (*ok, error) {
		got = in.Body
		return &ok{true}, nil
	})))
	if res := c.Post("/n", `{"m":{"a":null,"b":1}}`); res.Status != 200 {
		t.Fatal(res.Status, res.Text())
	}
	if v, ok := got.M["b"].Get(); !got.M["a"].IsNull() || !ok || v != 1 || len(got.M) != 2 {
		t.Fatalf("%+v", got.M)
	}
	same(t, violations(t, c.Post("/n", `{"m":{"a":"x"}}`)), "body $.m.a: expected integer, got string")
}

type rqF32Enum struct {
	F float32 `query:"f" schema:"enum=0.5|1.5,multipleOf=0.5"`
}

type rqF32EnumBad struct {
	F float32 `query:"f" schema:"enum=0.5|0.7,multipleOf=0.5"`
}

// A float32's enum members meet its multipleOf at assembly, and a request's
// number is held to both, as written.
func TestFloat32EnumWithMultipleOf(t *testing.T) {
	rejects(t, one("/x", get(func(context.Context, *rqF32EnumBad) (*ok, error) { return nil, nil })),
		"enum member 0.7 does not meet the schema's bounds or multipleOf")
	c := getatest.New(t, one("/x", get(func(context.Context, *rqF32Enum) (*ok, error) { return &ok{true}, nil })))
	for q, status := range map[string]int{"f=1.50": 200, "f=0.5": 200, "f=15e-1": 200, "f=1": 400, "f=2.5": 400} {
		if res := c.Get("/x?" + q); res.Status != status {
			t.Errorf("%s: %d %s", q, res.Status, res.Body)
		}
	}
}
