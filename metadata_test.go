package geta_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
	"github.com/koji-1009/geta/internal/vet"
)

type metaOwner struct {
	ID string `json:"id" schema:"maxLength=8"`
}

type metaItem struct {
	Name  string                `json:"name" doc:"The item's name, as shown" schema:"maxLength=20,examples=pen|ink"`
	Qty   int                   `json:"qty" doc:"How many" schema:"default=1,minimum=1"`
	When  geta.Date             `json:"when" schema:"default=2026-01-01"`
	Old   *string               `json:"old,omitzero" schema:"deprecated=true"`
	Owner metaOwner             `json:"owner" doc:"Who owns it" schema:"deprecated=true"`
	Note  geta.Nullable[string] `json:"note,omitzero" doc:"A note, or null to clear it" schema:"maxLength=8,examples=hi"`
}

type metaIn struct {
	Limit int      `query:"limit" doc:"How many to return" schema:"minimum=1,maximum=100,default=20,examples=10|50"`
	Sort  string   `query:"sort" schema:"enum=asc|desc,default=asc,deprecated=true"`
	Trace *string  `header:"X-Trace" doc:"A trace id" schema:"examples=abc"`
	Lang  string   `cookie:"lang" schema:"default=en,pattern=^[a-z]{2}$"`
	Body  metaItem `body:"json" doc:"The item to file"`
}

type metaOut struct {
	Count int      `header:"X-Count" doc:"Items in total"`
	Body  metaEcho `body:"json"`
}

type metaEcho struct {
	Limit int      `json:"limit"`
	Sort  string   `json:"sort"`
	Lang  string   `json:"lang"`
	Item  metaItem `json:"item"`
}

type metaFormIn struct {
	Body struct {
		Size int    `form:"size" doc:"The size" schema:"default=3,maximum=9"`
		Tag  string `form:"tag"`
	} `body:"form" doc:"The form"`
}

type metaFormOut struct {
	Size int    `json:"size"`
	Tag  string `json:"tag"`
}

func metaTable() geta.Table {
	h := func(_ context.Context, in *metaIn) (*metaOut, error) {
		return &metaOut{Count: 1, Body: metaEcho{Limit: in.Limit, Sort: in.Sort, Lang: in.Lang, Item: in.Body}}, nil
	}
	form := func(_ context.Context, in *metaFormIn) (*metaFormOut, error) {
		return &metaFormOut{Size: in.Body.Size, Tag: in.Body.Tag}, nil
	}
	return geta.Table{Routes: []geta.Entry{
		{Path: "/items", Route: geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}},
		{Path: "/form", Route: geta.Route{Post: geta.Op(http.StatusOK, form, geta.Doc{})}},
	}}
}

// A default is truthful: what the document says the server uses where a
// request leaves the value out, geta binds there, in a parameter, a cookie,
// a member, and a form field; a value the request sends is kept.
func TestDefaultsAreBoundWhereARequestLeavesThemOut(t *testing.T) {
	c := getatest.New(t, metaTable())
	res := c.Post("/items", `{"name":"pen","owner":{"id":"o1"}}`)
	if res.Status != http.StatusOK {
		t.Fatal(res.Status, res.Text())
	}
	got := res.JSON[metaEcho]()
	if got.Limit != 20 || got.Sort != "asc" || got.Lang != "en" || got.Item.Qty != 1 || got.Item.When.String() != "2026-01-01" {
		t.Fatalf("%+v", got)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, c.URL()+"/items?limit=5&sort=desc",
		strings.NewReader(`{"name":"pen","owner":{"id":"o1"},"qty":4,"when":"2027-02-03"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "lang", Value: "ja"})
	got = c.Send(req).JSON[metaEcho]()
	if got.Limit != 5 || got.Sort != "desc" || got.Lang != "ja" || got.Item.Qty != 4 || got.Item.When.String() != "2027-02-03" {
		t.Fatalf("%+v", got)
	}
	// A value sent is held to the schema as any is: the default is no
	// fallback for one the schema refuses.
	if res := c.Post("/items?limit=0", `{"name":"pen","owner":{"id":"o1"},"qty":0}`); res.Status != http.StatusBadRequest || len(res.Problem().Errors) != 2 {
		t.Fatal(res.Status, res.Text())
	}
	res = c.Form(http.MethodPost, "/form", url.Values{"tag": {"x"}})
	if got := res.JSON[metaFormOut](); res.Status != http.StatusOK || got.Size != 3 {
		t.Fatal(res.Status, res.Text())
	}
}

// doc describes a parameter, a member, a form field, a body, and an output
// header; a schema tag's default, examples, and deprecated go into the
// schema, deprecated onto a parameter too. A member with a default is
// required in the response's schema, which always writes it, and not in the
// request's, which may leave it out.
func TestDocAndAnnotationsAreDocumented(t *testing.T) {
	a := accepts(t, metaTable())
	m := doc(t, a)
	op := at(t, m, "paths", "/items", "post").(map[string]any)
	params := map[string]string{}
	for _, p := range op["parameters"].([]any) {
		params[p.(map[string]any)["name"].(string)] = compact(t, p)
	}
	for name, want := range map[string]string{
		"limit":   `{"description":"How many to return","in":"query","name":"limit","required":false,"schema":{"default":20,"examples":[10,50],"format":"int64","maximum":100,"minimum":1,"type":"integer"}}`,
		"sort":    `{"deprecated":true,"in":"query","name":"sort","required":false,"schema":{"default":"asc","deprecated":true,"enum":["asc","desc"],"type":"string"}}`,
		"X-Trace": `{"description":"A trace id","in":"header","name":"X-Trace","required":false,"schema":{"examples":["abc"],"maxLength":4096,"pattern":"^(?:[^\\x00-\\x20\\x7F](?:[^\\x00-\\x08\\x0A-\\x1F\\x7F]*[^\\x00-\\x20\\x7F])?)?$","type":"string"}}`,
		"lang":    `{"in":"cookie","name":"lang","required":false,"schema":{"allOf":[{"pattern":"^[\\x20\\x21\\x23-\\x3A\\x3C-\\x5B\\x5D-\\x7E]*$"}],"default":"en","maxLength":4096,"pattern":"^[a-z]{2}$","type":"string"}}`,
	} {
		if params[name] != want {
			t.Errorf("%s:\n got %s\nwant %s", name, params[name], want)
		}
	}
	if got := at(t, op, "requestBody", "description"); got != "The item to file" {
		t.Fatal(got)
	}
	props := at(t, m, "components", "schemas", "metaItem-Input", "properties").(map[string]any)
	for name, want := range map[string]string{
		"name":  `{"description":"The item's name, as shown","examples":["pen","ink"],"maxLength":20,"type":"string"}`,
		"qty":   `{"default":1,"description":"How many","format":"int64","minimum":1,"type":"integer"}`,
		"when":  `{"default":"2026-01-01","format":"date","type":"string"}`,
		"old":   `{"deprecated":true,"maxLength":4096,"type":"string"}`,
		"owner": `{"$ref":"#/components/schemas/metaOwner","deprecated":true,"description":"Who owns it"}`,
		"note":  `{"description":"A note, or null to clear it","examples":["hi"],"maxLength":8,"type":["string","null"]}`,
	} {
		if got := compact(t, props[name]); got != want {
			t.Errorf("%s:\n got %s\nwant %s", name, got, want)
		}
	}
	if got := compact(t, at(t, m, "components", "schemas", "metaItem-Input", "required")); got != `["name","owner"]` {
		t.Fatal("request:", got)
	}
	if got := compact(t, at(t, m, "components", "schemas", "metaItem", "required")); got != `["name","qty","when","owner"]` {
		t.Fatal("response:", got)
	}
	if got := compact(t, at(t, op, "responses", "200", "headers", "X-Count")); !strings.Contains(got, `"description":"Items in total"`) {
		t.Fatal(got)
	}
	form := at(t, m, "paths", "/form", "post", "requestBody").(map[string]any)
	if form["description"] != "The form" || compact(t, at(t, form, "content", "application/x-www-form-urlencoded", "schema", "required")) != `["tag"]` ||
		compact(t, at(t, form, "content", "application/x-www-form-urlencoded", "schema", "properties", "size")) != `{"default":3,"description":"The size","format":"int64","maximum":9,"type":"integer"}` {
		t.Fatal(compact(t, form))
	}
}

// The document of descriptions, defaults, examples, number enums, nullables,
// deepObjects, and map sizes together, which openapi-spec-validator checks
// against the OpenAPI 3.1 meta-schema.
func TestSchemaKeywordsGolden(t *testing.T) {
	var tbl geta.Table
	for prefix, part := range map[string]geta.Table{"/meta": metaTable(), "/null": nullTable(), "/enum": enumTable(), "/deep": deepTable(), "/size": sizeTable()} {
		for _, e := range part.Routes {
			e.Path = prefix + e.Path
			tbl.Routes = append(tbl.Routes, e)
		}
	}
	getatest.Golden(t, accepts(t, tbl), "testdata/schema.openapi.json")
}

// A default or an example the document could not state truthfully is
// refused: one the schema refuses, one the place cannot carry, one a
// pointer never takes, and one on what has none.
func TestDefaultAndExampleMistakesAreRefused(t *testing.T) {
	type pointerParam struct {
		N *int `query:"n" schema:"default=3"`
	}
	type pathParam struct {
		ID string `path:"id" schema:"default=x"`
	}
	type pointerMember struct {
		N *int `json:"n,omitzero" schema:"default=3"`
	}
	type pointerMemberIn struct {
		Body pointerMember `body:"json"`
	}
	type bodyDefault struct {
		Body string `body:"json" schema:"default=x"`
	}
	type belowMinimum struct {
		N int `query:"n" schema:"minimum=1,default=0"`
	}
	type notInEnum struct {
		S string `query:"s" schema:"enum=a|b,examples=a|c"`
	}
	type leadingZero struct {
		N int `query:"n" schema:"default=007"`
	}
	type notCarried struct {
		H string `header:"X-H" schema:"default= x"`
	}
	type badDate struct {
		D geta.Date `query:"d" schema:"examples=2026-02-30"`
	}
	type onSlice struct {
		Q []string `query:"q" schema:"default=a"`
	}
	type onNullable struct {
		N geta.Nullable[int] `json:"n" schema:"default=1"`
	}
	type embedded struct {
		VetPaging `doc:"paging"`
	}
	type cookieDoc struct {
		C    *http.Cookie `cookie:"c" doc:"a cookie"`
		Body ok           `body:"json"`
	}
	type bodyDoc struct {
		Body ok `body:"json" doc:"the body"`
	}
	type tooLong struct {
		S string `query:"s" schema:"examples=abcdefghijk"`
	}
	for name, c := range map[string]struct {
		tbl  geta.Table
		opts []geta.Option
		want string
	}{
		"pointer param":    {one("/x", get(func(context.Context, *pointerParam) (*ok, error) { return nil, nil })), nil, "N: a pointer field takes no default; use int"},
		"path param":       {one("/x/{id}", get(func(context.Context, *pathParam) (*ok, error) { return nil, nil })), nil, `path parameter "id" takes no default`},
		"pointer member":   {one("/x", geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *pointerMemberIn) (*ok, error) { return nil, nil }, geta.Doc{})}), nil, "N: a pointer field takes no default"},
		"body":             {one("/x", geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *bodyDefault) (*ok, error) { return nil, nil }, geta.Doc{})}), nil, "a body takes no default"},
		"below minimum":    {one("/x", get(func(context.Context, *belowMinimum) (*ok, error) { return nil, nil })), nil, `default "0": 0 is less than minimum 1`},
		"not in enum":      {one("/x", get(func(context.Context, *notInEnum) (*ok, error) { return nil, nil })), nil, `example "c": "c" is not one of a, b`},
		"leading zero":     {one("/x", get(func(context.Context, *leadingZero) (*ok, error) { return nil, nil })), nil, `default "007" is not a JSON number`},
		"not carried":      {one("/x", get(func(context.Context, *notCarried) (*ok, error) { return nil, nil })), nil, `default " x": " x" is not a valid header field value`},
		"bad date":         {one("/x", get(func(context.Context, *badDate) (*ok, error) { return nil, nil })), nil, `example "2026-02-30": "2026-02-30" is not a valid date`},
		"on a slice":       {one("/x", get(func(context.Context, *onSlice) (*ok, error) { return nil, nil })), nil, "default applies to string or integer or number or boolean, not array"},
		"on a nullable":    {one("/x", get(func(context.Context, *empty) (*onNullable, error) { return nil, nil })), nil, "a geta.Nullable takes no default"},
		"embedded doc":     {one("/x", get(func(context.Context, *embedded) (*ok, error) { return nil, nil })), nil, "VetPaging: an embedded struct takes no doc tag"},
		"cookie doc":       {one("/x", get(func(context.Context, *empty) (*cookieDoc, error) { return nil, nil })), nil, "C: a cookie field takes no doc tag"},
		"output body doc":  {one("/x", get(func(context.Context, *empty) (*bodyDoc, error) { return nil, nil })), nil, "Body: an output body takes no doc tag"},
		"past the ceiling": {one("/x", get(func(context.Context, *tooLong) (*ok, error) { return nil, nil })), []geta.Option{geta.WithLimits(geta.Limits{MaxBodyBytes: 1 << 20, MaxStringLength: 10, MaxItems: 10, MaxDepth: 10})}, `default or example "abcdefghijk" exceeds Limits.MaxStringLength 10`},
		"declared":         {one("/x", get(func(context.Context, *empty) (*ownDecimal, error) { return nil, nil })), []geta.Option{geta.WithSchema[decimalLike]("string", "default=1")}, "default and examples do not apply to geta_test.decimalLike"},
	} {
		_, err := geta.New(c.tbl, c.opts...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v\nwant %q", name, err, c.want)
		}
	}
	for _, c := range []struct{ tag, kind, want string }{
		{"default=1,maximum=0", "int8", `default "1": 1 is greater than maximum 0`},
		{"examples=x", "int", `example "x" is not a JSON number`},
		{"default=yes", "bool", `default "yes": expected boolean, got string`},
		{"examples=a|b", "text", ""},
		{"deprecated=true", "struct", ""},
		{"deprecated=yes", "string", `"yes" is not true or false`},
		{"default=a", "?string", "a geta.Nullable takes no default"},
	} {
		err := vet.CheckSchemaTag(c.tag, c.kind)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s on %s: %v; want %q", c.tag, c.kind, err, c.want)
		}
	}
}

// decimalLike reads and writes itself, as a decimal type does.
type decimalLike struct{ v string }

func (d decimalLike) MarshalJSON() ([]byte, error)  { return json.Marshal(d.v) }
func (d *decimalLike) UnmarshalJSON(b []byte) error { return json.Unmarshal(b, &d.v) }

type ownDecimal struct {
	D decimalLike `json:"d"`
}
