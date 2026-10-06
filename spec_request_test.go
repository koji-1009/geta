package geta_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
	"github.com/koji-1009/geta/internal/vet"
)

// Request rules that no other test asserts clause by clause.

// srHidden is an unexported struct an input embeds: its fields are bound by
// their own tags.
type srHidden struct {
	Token string `header:"X-Token"`
}

// SrShown is an exported struct an input embeds.
type SrShown struct {
	Limit int `query:"limit"`
}

type srEmbedIn struct {
	srHidden
	SrShown
	ID string `path:"id"`
}

type srEmbedOut struct {
	Token string `json:"token"`
	Limit int    `json:"limit"`
	ID    string `json:"id"`
}

type srTwicePath struct {
	A string `path:"id"`
	B string `path:"id"`
}

// srCond is geta.Conditional under an unexported name.
type srCond = geta.Conditional

type srCondAlias struct {
	srCond
}

// An operation that reads nothing takes *struct{}; an input's untagged
// embedded struct, exported or not, has its fields bound by their own tags;
// one path parameter bound by two fields is refused, as is geta.Conditional
// embedded under an unexported alias.
func TestInputFieldBindingRules(t *testing.T) {
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/none", Route: get(func(context.Context, *struct{}) (*ok, error) { return &ok{true}, nil })},
		{Path: "/e/{id}", Route: get(func(_ context.Context, in *srEmbedIn) (*srEmbedOut, error) {
			return &srEmbedOut{Token: in.Token, Limit: in.Limit, ID: in.ID}, nil
		})},
	}})
	if res := c.Get("/none"); res.Status != 200 {
		t.Fatal(res.Status, res.Text())
	}
	res := c.With("X-Token", "tk").Get("/e/7?limit=3")
	if got := res.JSON[srEmbedOut](); res.Status != 200 || got != (srEmbedOut{"tk", 3, "7"}) {
		t.Fatal(res.Status, res.Text())
	}
	same(t, violations(t, c.Get("/e/7")), "header X-Token: missing required parameter", "query limit: missing required parameter")
	rejects(t, one("/x/{id}", get(func(context.Context, *srTwicePath) (*ok, error) { return nil, nil })),
		`srTwicePath.B binds path "id", already bound by A`)
	rejects(t, one("/x", get(func(context.Context, *srCondAlias) (*ok, error) { return nil, nil })),
		"geta.Conditional is embedded under an unexported alias")
	rejects(t, one("/x", get(func(context.Context, *struct {
		geta.Conditional
		Again *string `header:"if-none-match"`
	}) (*ok, error) {
		return nil, nil
	})), `Again binds header "if-none-match", already bound by IfNoneMatch`)
	rejects(t, one("/x", get(func(context.Context, *struct {
		H string `header:""`
	}) (*ok, error) {
		return nil, nil
	})), `H: empty header name`)
	rejects(t, one("/x", get(func(context.Context, *struct {
		C string `cookie:""`
	}) (*ok, error) {
		return nil, nil
	})), `C: empty cookie name`)
	rejects(t, one("/x", get(func(context.Context, *struct {
		P string `path:""`
	}) (*ok, error) {
		return nil, nil
	})), `P: empty path name`)
}

type srCondIn struct {
	geta.Conditional
}

type srCondOut struct {
	Seen []string `json:"seen"`
}

// An input embedding geta.Conditional binds its four precondition headers,
// each optional.
func TestConditionalBindsItsFourHeaders(t *testing.T) {
	app := accepts(t, one("/x", get(func(_ context.Context, in *srCondIn) (*srCondOut, error) {
		var out srCondOut
		for _, p := range []*string{in.IfMatch, in.IfNoneMatch, in.IfModifiedSince, in.IfUnmodifiedSince} {
			if p == nil {
				out.Seen = append(out.Seen, "absent")
				continue
			}
			out.Seen = append(out.Seen, *p)
		}
		return &out, nil
	})))
	for _, c := range []struct {
		headers []string
		want    string
	}{
		{nil, `{"seen":["absent","absent","absent","absent"]}`},
		{[]string{"If-Match", `"a"`, "If-None-Match", `"b"`, "If-Modified-Since", "Mon, 02 Jan 2006 15:04:05 GMT",
			"If-Unmodified-Since", "Tue, 03 Jan 2006 15:04:05 GMT"},
			`{"seen":["\"a\"","\"b\"","Mon, 02 Jan 2006 15:04:05 GMT","Tue, 03 Jan 2006 15:04:05 GMT"]}`},
	} {
		rec := do(t, app, "GET", "/x", c.headers...)
		if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != c.want {
			t.Errorf("%q: %d %s", c.headers, rec.Code, rec.Body)
		}
	}
}

// srBoth reads and writes itself both as text and as JSON.
type srBoth struct{ s string }

func (b srBoth) MarshalText() ([]byte, error)  { return []byte(b.s), nil }
func (b *srBoth) UnmarshalText(p []byte) error { b.s = string(p); return nil }
func (b srBoth) MarshalJSON() ([]byte, error)  { return json.Marshal(b.s) }
func (b *srBoth) UnmarshalJSON(p []byte) error { return json.Unmarshal(p, &b.s) }

type srPlain struct {
	N int `form:"n"`
}

// A parameter is a string, a boolean, a number, or a text type, a slice of
// one in a query or a header, and in a query a struct (a deepObject); any
// other type is refused, a type with JSON methods of its own (text methods
// too) and []byte among them.
func TestParameterTypesRefused(t *testing.T) {
	for name, c := range map[string]struct {
		tbl  geta.Table
		want string
	}{
		"text and JSON": {one("/x", get(func(context.Context, *struct {
			B srBoth `query:"b"`
		}) (*ok, error) {
			return nil, nil
		})), `query parameter "b" has unsupported type geta_test.srBoth`},
		"bytes in a query": {one("/x", get(func(context.Context, *struct {
			B []byte `query:"b"`
		}) (*ok, error) {
			return nil, nil
		})), `query parameter "b" has unsupported type []uint8`},
		"bytes in a cookie": {one("/x", get(func(context.Context, *struct {
			B []byte `cookie:"b"`
		}) (*ok, error) {
			return nil, nil
		})), `cookie parameter "b" has unsupported type []uint8`},
		"map in a query": {one("/x", get(func(context.Context, *struct {
			M map[string]string `query:"m"`
		}) (*ok, error) {
			return nil, nil
		})), `query parameter "m" has unsupported type map[string]string`},
		"slice in a path": {one("/x/{s}", get(func(context.Context, *struct {
			S []string `path:"s"`
		}) (*ok, error) {
			return nil, nil
		})), `path parameter "s" has unsupported type []string`},
		"struct in a path": {one("/x/{s}", get(func(context.Context, *struct {
			S srPlain `path:"s"`
		}) (*ok, error) {
			return nil, nil
		})), `path parameter "s" has unsupported type geta_test.srPlain`},
		"struct in a cookie": {one("/x", get(func(context.Context, *struct {
			S srPlain `cookie:"s"`
		}) (*ok, error) {
			return nil, nil
		})), `cookie parameter "s" has unsupported type geta_test.srPlain`},
		"slice of structs in a query": {one("/x", get(func(context.Context, *struct {
			S []srPlain `query:"s"`
		}) (*ok, error) {
			return nil, nil
		})), `query parameter "s" has unsupported type []geta_test.srPlain`},
	} {
		_, err := geta.New(c.tbl)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v\nwant %q", name, err, c.want)
		}
	}
}

type srPresenceIn struct {
	C   string  `cookie:"c"`
	Opt *string `cookie:"opt"`
	H   *string `header:"X-H"`
	B   *bool   `query:"b"`
	N   *int    `cookie:"n"`
}

type srPresenceOut struct {
	C   string `json:"c"`
	Opt string `json:"opt"`
}

type srPathInt struct {
	N int `path:"n"`
}

// A required cookie left out is missing; an optional one left out is nil; a
// scalar header sent on two lines is given twice; a boolean is exactly true
// or false; a path parameter's integer is a JSON integer.
func TestParameterPresenceAndRepetition(t *testing.T) {
	app := accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/p", Route: get(func(_ context.Context, in *srPresenceIn) (*srPresenceOut, error) {
			out := &srPresenceOut{C: in.C, Opt: "<nil>"}
			if in.Opt != nil {
				out.Opt = *in.Opt
			}
			return out, nil
		})},
		{Path: "/n/{n}", Route: get(func(context.Context, *srPathInt) (*ok, error) { return &ok{true}, nil })},
	}})
	if p := problemOf(t, do(t, app, "GET", "/p")); fmt.Sprint(listed(p)) != "[cookie c: missing required parameter]" {
		t.Error(listed(p))
	}
	rec := do(t, app, "GET", "/p", "Cookie", "c=v")
	var out srPresenceOut
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 || out != (srPresenceOut{"v", "<nil>"}) {
		t.Fatal(rec.Code, rec.Body)
	}
	if p := problemOf(t, do(t, app, "GET", "/p", "Cookie", "c=v", "X-H", "a", "X-H", "b")); fmt.Sprint(listed(p)) != "[header X-H: given 2 times, but takes one value]" {
		t.Error(listed(p))
	}
	for _, q := range []string{"True", "1", "TRUE"} {
		if p := problemOf(t, do(t, app, "GET", "/p?b="+q, "Cookie", "c=v")); fmt.Sprint(listed(p)) != "[query b: expected boolean, got string]" {
			t.Errorf("b=%s: %v", q, listed(p))
		}
	}
	if p := problemOf(t, do(t, app, "GET", "/p", "Cookie", "c=v; n=07")); fmt.Sprint(listed(p)) != "[cookie n: expected integer, got string]" {
		t.Error(listed(p))
	}
	if p := problemOf(t, do(t, app, "GET", "/n/007")); fmt.Sprint(listed(p)) != "[path n: expected integer, got string]" {
		t.Error(listed(p))
	}
	if rec := do(t, app, "GET", "/n/7"); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
}

type srQueryBodyIn struct {
	Q    *string `query:"q"`
	Body srBody  `body:"json"`
}

type srBody struct {
	A string `json:"a"`
}

type srNoQueryIn struct {
	Body srBody `body:"json"`
}

// A malformed query string is a 400 of its own, answered before the body is
// read, where the operation declares a query parameter; where it declares
// none, the query string is not read.
func TestMalformedQueryString(t *testing.T) {
	app := accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/q", Route: post(func(context.Context, *srQueryBodyIn) (*ok, error) { return &ok{true}, nil })},
		{Path: "/n", Route: post(func(context.Context, *srNoQueryIn) (*ok, error) { return &ok{true}, nil })},
	}})
	send := func(target, ct, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec
	}
	for _, target := range []string{"/q?q=%zz", "/q?a;b", "/q?q=a;q=b"} {
		for _, ct := range []string{"application/json", "text/plain"} {
			rec := send(target, ct, strings.Repeat(" ", 2<<20))
			if p := problemOf(t, rec); rec.Code != 400 || p.Detail != "the query string is malformed" || len(p.Errors) != 0 {
				t.Errorf("%s %s: %d %+v", target, ct, rec.Code, p)
			}
		}
	}
	if rec := send("/n?%zz;", "application/json", `{"a":"x"}`); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
}

// A deepObject whose name stands in the query only as a bare key, with no
// name[...] key, is absent.
func TestDeepObjectBareKeyIsAbsent(t *testing.T) {
	c := getatest.New(t, deepTable())
	res := c.Get("/items?page=5")
	if p := res.Problem(); res.Status != 400 || fmt.Sprint(listed(p)) != "[query page: missing required parameter]" {
		t.Fatal(res.Status, listed(p))
	}
}

type srRawReaderIn struct {
	Body io.Reader `body:"text/csv"`
}

type srRawCSVIn struct {
	Body []byte `body:"text/csv"`
}

type srRawStreamIn struct {
	Body []byte `body:"text/event-stream"`
}

type srRawGot struct {
	Got string `json:"got"`
}

// identity is taken in any case; a JSON body's media type takes any
// parameters; a malformed Content-Type is a 415; a raw body's media type is
// compared in any case; an empty body for an io.Reader is missing; an
// input's raw body may be an event stream's bytes.
func TestContentTypeAndCodingDetails(t *testing.T) {
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/j", Route: post(func(context.Context, *srNoQueryIn) (*ok, error) { return &ok{true}, nil })},
		{Path: "/r", Route: post(func(_ context.Context, in *srRawReaderIn) (*srRawGot, error) {
			b, err := io.ReadAll(in.Body)
			return &srRawGot{string(b)}, err
		})},
		{Path: "/c", Route: post(func(_ context.Context, in *srRawCSVIn) (*srRawGot, error) { return &srRawGot{string(in.Body)}, nil })},
		{Path: "/s", Route: post(func(_ context.Context, in *srRawStreamIn) (*srRawGot, error) { return &srRawGot{string(in.Body)}, nil })},
	}})
	if res := c.With("Content-Encoding", "IDENTITY").Post("/j", `{"a":"x"}`); res.Status != 200 {
		t.Fatal(res.Status, res.Text())
	}
	res := c.With("Content-Encoding", "Identity, GZIP").Post("/j", `{"a":"x"}`)
	if res.Status != 415 || res.Problem().Detail != `Content-Encoding "GZIP" is not supported` ||
		res.Header.Get("Accept-Encoding") != "identity" || res.Header.Get("Accept") != "" {
		t.Fatal(res.Status, res.Header, res.Text())
	}
	if res := c.Content(http.MethodPost, "/j", "application/json; charset=utf-8", []byte(`{"a":"x"}`)); res.Status != 200 {
		t.Fatal(res.Status, res.Text())
	}
	if res := c.Content(http.MethodPost, "/j", "application/vnd.x+json; v=2", []byte(`{"a":"x"}`)); res.Status != 200 {
		t.Fatal(res.Status, res.Text())
	}
	res = c.Content(http.MethodPost, "/j", "application/json; =x", []byte(`{"a":"x"}`))
	if res.Status != 415 || res.Problem().Detail != `Content-Type "application/json; =x" is not application/json` ||
		res.Header.Get("Accept") != "application/json" || res.Header.Values("Accept-Encoding") != nil {
		t.Fatal(res.Status, res.Header, res.Text())
	}
	if res := c.Content(http.MethodPost, "/c", "TEXT/CSV", []byte("a,b")); res.Status != 200 || res.JSON[srRawGot]().Got != "a,b" {
		t.Fatal(res.Status, res.Text())
	}
	if res := c.Content(http.MethodPost, "/r", "text/csv", nil); res.Status != 400 ||
		fmt.Sprint(listed(res.Problem())) != "[body $: missing required request body]" {
		t.Fatal(res.Status, res.Text())
	}
	if res := c.Content(http.MethodPost, "/r", "text/csv", []byte("x,y")); res.Status != 200 || res.JSON[srRawGot]().Got != "x,y" {
		t.Fatal(res.Status, res.Text())
	}
	if res := c.Content(http.MethodPost, "/s", "text/event-stream", []byte("data: 1\n\n")); res.Status != 200 || res.JSON[srRawGot]().Got != "data: 1\n\n" {
		t.Fatal(res.Status, res.Text())
	}
	rejects(t, one("/x", post(func(context.Context, *struct {
		Body []byte `body:"text/"`
	}) (*ok, error) {
		return nil, nil
	})), `Body: body tag "text/" is not a media type`)
}

type srUploadIn struct {
	N    int `query:"n"`
	Body struct {
		F geta.File `form:"f"`
	} `body:"multipart"`
}

// A file geta cannot store is a 500 answered alone: the parameters'
// violations found before it are not listed.
func TestAFileGetaCannotStoreIsAnsweredAlone(t *testing.T) {
	dir := t.TempDir()
	l := geta.DefaultLimits
	l.MaxMultipartMemory = 0
	app, err := geta.New(one("/m", post(func(context.Context, *srUploadIn) (*ok, error) { return &ok{true}, nil })),
		geta.WithLimits(l), geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", filepath.Join(dir, "missing"))
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	w, _ := mw.CreateFormFile("f", "f.txt")
	w.Write([]byte("content"))
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/m?n=x", &b)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if p := problemOf(t, rec); rec.Code != 500 || len(p.Errors) != 0 {
		t.Fatal(rec.Code, rec.Body)
	}
}

type srItem struct {
	Name  string            `json:"name"`
	Plain string            // untagged: the member Plain
	Ptr   *int              `json:"ptr,omitzero"`
	List  []int             `json:"list"`
	Small int8              `json:"small"`
	F32   float32           `json:"f32"`
	F64   float64           `json:"f64"`
	Map   map[string]int    `json:"map"`
	Deep  *srDeep           `json:"deep,omitzero"`
	Notes map[string]string `json:"notes"`
}

type srDeep struct {
	Next *srDeep `json:"next,omitzero"`
}

type srItemIn struct {
	Body srItem `body:"json"`
}

const srGood = `{"name":"a","Plain":"p","list":[1],"small":1,"f32":1,"f64":1,"map":{},"notes":{}}`

// srWith is srGood with the member name set to value.
func srWith(name, value string) string {
	var m map[string]json.RawMessage
	json.Unmarshal([]byte(srGood), &m)
	m[name] = json.RawMessage(value)
	b, _ := json.Marshal(m)
	return string(b)
}

// A JSON body is exactly one value; an untagged exported field is the member
// its Go name names; null, a wrong type, a fraction on an integer, and a
// number past its Go type are each one violation; a map over MaxItems and
// nesting past MaxDepth are refused.
func TestJSONBodyRulesOverHTTP(t *testing.T) {
	l := geta.DefaultLimits
	l.MaxItems, l.MaxDepth = 2, 3
	app, err := geta.New(one("/i", post(func(context.Context, *srItemIn) (*ok, error) { return &ok{true}, nil })), geta.WithLimits(l))
	if err != nil {
		t.Fatal(err)
	}
	c := getatest.Serve(t, app)
	if res := c.Post("/i", srGood); res.Status != 200 {
		t.Fatal(res.Status, res.Text())
	}
	for body, want := range map[string]string{
		srGood + ` {}`:                                  "body $: trailing data after the JSON value",
		srGood[:len(srGood)-1]:                          "body $: unexpected end of JSON input",
		srWith("Name", `"x"`):                           "body $.Name: unknown member",
		srWith("plain", `"x"`):                          "body $.plain: unknown member",
		srWith("ptr", "null"):                           "body $.ptr: expected integer, got null",
		srWith("list", "[null]"):                        "body $.list[0]: expected integer, got null",
		srWith("name", "null"):                          "body $.name: expected string, got null",
		srWith("name", "7"):                             "body $.name: expected string, got integer",
		srWith("small", "1.0"):                          "body $.small: expected integer, got number",
		srWith("small", "1e0"):                          "body $.small: expected integer, got number",
		srWith("small", "128"):                          "body $.small: 128 is out of range for int8",
		srWith("f32", "1e40"):                           "body $.f32: 1e40 is out of range for float32",
		srWith("f64", "1e400"):                          "body $.f64: 1e400 is out of range for float64",
		srWith("map", `{"a":1,"b":2,"c":3}`):            "body $.map: object has 3 members, over the ceiling of 2",
		srWith("deep", `{"next":{"next":{"next":{}}}}`): "body $: JSON nesting exceeds the ceiling of 3",
	} {
		if got := listed(c.Post("/i", body).Problem()); fmt.Sprint(got) != "["+want+"]" {
			t.Errorf("%s:\n got %q\nwant %q", body, got, want)
		}
	}
	// Unknown members are listed sorted, after the declared members'
	// violations.
	same(t, violations(t, c.Post("/i", `{"zz":1,"name":7,"b":2,"Plain":"p","list":[1],"small":1,"f32":1,"f64":1,"map":{},"notes":{}}`)),
		"body $.name: expected string, got integer", "body $.b: unknown member", "body $.zz: unknown member")
}

// The discriminator may stand at any position among the variant's members,
// its name written escaped or not.
func TestSealedDiscriminatorAtAnyPosition(t *testing.T) {
	c := drawingApp(t, echoDrawing)
	for _, body := range []string{
		`{"main":{"radius":1,"kind":"circle"},"others":[{"side":2,"\u006bind":"square"}]}`,
		`{"others":[],"main":{"\u006b\u0069nd":"circle","radius":1}}`,
	} {
		res := c.Post("/d", body)
		got := res.JSON[drawing]()
		if res.Status != 200 {
			t.Fatal(body, res.Status, res.Text())
		}
		if _, isCircle := got.Main.(circle); !isCircle {
			t.Fatalf("%s: %#v", body, got)
		}
	}
}

type srPromoted struct {
	A string `json:"a"`
}

type srDocEmbedded struct {
	srPromoted `doc:"promoted"`
}

type srComplex struct {
	C complex128 `json:"c"`
}

// A promoted embedded struct takes no doc tag; a complex number has no JSON
// form.
func TestJSONMemberRulesBeyondTheSharedOnes(t *testing.T) {
	_ = srDocEmbedded{}.srPromoted
	rejects(t, one("/x", get(func(context.Context, *empty) (*srDocEmbedded, error) { return nil, nil })),
		"srDocEmbedded.srPromoted: an embedded struct takes no doc tag")
	rejects(t, one("/x", get(func(context.Context, *empty) (*srComplex, error) { return nil, nil })),
		"type complex128 has no JSON form geta can derive (complex128)")
}

// A comma followed by letters and = begins a new entry, so a value holding
// one cannot be written; each keyword's value has its own syntax.
func TestSchemaTagGrammarAndValues(t *testing.T) {
	for _, c := range []struct{ tag, kind, want string }{
		{`pattern=^a,b=c$`, "string", `schema tag has unknown keyword "b"`},
		{`enum=x,y=1`, "string", `schema tag has unknown keyword "y"`},
		{"maxLength=-1", "string", `schema keyword maxLength: "-1" is not a non-negative integer`},
		{"maxItems=1.5", "slice", `schema keyword maxItems: "1.5" is not a non-negative integer`},
		{"minItems=x", "slice", `schema keyword minItems: "x" is not a non-negative integer`},
		{"minimum=Inf", "float64", `schema keyword minimum: "Inf" is not a number`},
		{"maximum=NaN", "float64", `schema keyword maximum: "NaN" is not a number`},
		{"multipleOf=-2", "int", `schema keyword multipleOf: "-2" is not greater than zero`},
		{"deprecated=1", "string", `schema keyword deprecated: "1" is not true or false`},
		{"uniqueItems=TRUE", "slice", `schema keyword uniqueItems: "TRUE" is not true or false`},
		{"enum=a||b", "string", `"a||b" has an empty member`},
		{"examples=a||b", "int", `"a||b" has an empty member`},
		{"pattern=(", "string", `schema keyword pattern: "(" does not compile`},
	} {
		if err := vet.CheckSchemaTag(c.tag, c.kind); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s on %s: %v\nwant %q", c.tag, c.kind, err, c.want)
		}
	}
	for _, c := range []struct{ tag, kind string }{
		{"pattern=^a{1,3}$", "string"},
		{"uniqueItems=false", "slice"},
		{"deprecated=false", "string"},
		{"examples=|a", "string"},
		{"enum=a|b c", "string"},
		{"maxItems=2,items.maxItems=3", "[][]int8"},
	} {
		if err := vet.CheckSchemaTag(c.tag, c.kind); err != nil {
			t.Errorf("%s on %s: %v", c.tag, c.kind, err)
		}
	}
	type split struct {
		S string `query:"s" schema:"pattern=^a,b=c$"`
	}
	rejects(t, one("/x", get(func(context.Context, *split) (*ok, error) { return nil, nil })), `split.S: schema tag has unknown keyword "b"`)
	// A type with JSON methods of its own, undeclared, takes deprecated and
	// no other keyword.
	type deprecatedCents struct {
		C cents `json:"c" schema:"deprecated=true"`
	}
	accepts(t, one("/x", get(func(context.Context, *empty) (*deprecatedCents, error) { return nil, nil })))
	// An element takes no annotation.
	for _, tag := range []string{"items.examples=a", "items.deprecated=true", "items.default=a"} {
		if err := vet.CheckSchemaTag(tag, "[]string"); err == nil || !strings.Contains(err.Error(), "an element takes no") {
			t.Errorf("%s: %v", tag, err)
		}
	}
	// A default or an example the pattern or the format refuses.
	for _, c := range []struct{ tag, want string }{
		{"pattern=^[a-z]+$,default=X", `default "X": "X" does not match pattern ^[a-z]+$`},
		{"format=date,examples=x", `example "x": "x" is not a valid date`},
	} {
		if err := vet.CheckSchemaTag(c.tag, "string"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v\nwant %q", c.tag, err, c.want)
		}
	}
	type defaultCents struct {
		C cents `json:"c" schema:"default=1.00"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*defaultCents, error) { return nil, nil })),
		"defaultCents.C: schema keyword default applies to string or integer or number or boolean, not a type with its own JSON methods")
	type maxCents struct {
		C cents `json:"c" schema:"maximum=3"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*maxCents, error) { return nil, nil })),
		"maxCents.C: schema keyword maximum applies to integer or number, not a type with its own JSON methods; use geta.WithSchema")
}

type srEnumIn struct {
	S string `query:"s" schema:"enum=a|b"`
}

type srCeilingIn struct {
	S string `query:"s" schema:"pattern=^a+$"`
}

// A string enum is compared byte for byte; a string past the pattern
// ceiling is refused before its pattern runs, whatever the Limits.
func TestStringEnumAndPatternCeilingOverHTTP(t *testing.T) {
	c := getatest.New(t, one("/e", get(func(context.Context, *srEnumIn) (*ok, error) { return &ok{true}, nil })))
	same(t, violations(t, c.Get("/e?s=A")), `query s: "A" is not one of a, b`)
	same(t, violations(t, c.Get("/e?s=a%20")), `query s: "a " is not one of a, b`)
	l := geta.DefaultLimits
	l.MaxStringLength = 10000
	app, err := geta.New(one("/p", get(func(context.Context, *srCeilingIn) (*ok, error) { return &ok{true}, nil })), geta.WithLimits(l))
	if err != nil {
		t.Fatal(err)
	}
	if p := problemOf(t, do(t, app, "GET", "/p?s="+strings.Repeat("a", 5000))); fmt.Sprint(listed(p)) !=
		"[query s: string length 5000 exceeds the pattern-validation ceiling of 4096 code points]" {
		t.Fatal(listed(p))
	}
	if rec := do(t, app, "GET", "/p?s="+strings.Repeat("a", 4096)); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
}

type srTextStruct struct {
	V string `form:"v"`
}

func (s srTextStruct) MarshalText() ([]byte, error)  { return []byte(s.V), nil }
func (s *srTextStruct) UnmarshalText(b []byte) error { s.V = string(b); return nil }

// srTextStruct2 is a second text type: a struct embedding both promotes
// neither's methods, so it is no text type itself.
type srTextStruct2 struct {
	W string `form:"w"`
}

func (s srTextStruct2) MarshalText() ([]byte, error)  { return []byte(s.W), nil }
func (s *srTextStruct2) UnmarshalText(b []byte) error { s.W = string(b); return nil }

type srFormDate struct {
	Body struct {
		D geta.Date `form:"d"`
	} `body:"form"`
}

type srFormFiles struct {
	Body struct {
		Docs []geta.File `form:"docs" schema:"minItems=2,deprecated=true"`
	} `body:"multipart"`
}

// A form body is a struct with no text or JSON methods; an embedded text
// type and a schema tag on an embedded struct are refused in a form; a file
// takes no keyword, deprecated included, while a slice of files takes
// minItems, maxItems, and deprecated; a malformed escape is a malformed form.
func TestFormBodyRules(t *testing.T) {
	type textBody struct {
		Body srTextStruct `body:"form"`
	}
	rejects(t, one("/x", post(func(context.Context, *textBody) (*ok, error) { return nil, nil })),
		`Body: a body:"form" field has type geta_test.srTextStruct, not a struct`)
	type embeddedText struct {
		Body struct {
			srTextStruct
			srTextStruct2
		} `body:"form"`
	}
	rejects(t, one("/x", post(func(context.Context, *embeddedText) (*ok, error) { return nil, nil })),
		"srTextStruct: embedded geta_test.srTextStruct is a text type with no fields; tag it form")
	type embeddedSchema struct {
		Body struct {
			srPlain `schema:"deprecated=true"`
		} `body:"form"`
	}
	rejects(t, one("/x", post(func(context.Context, *embeddedSchema) (*ok, error) { return nil, nil })),
		"srPlain: an embedded struct takes no schema tag")
	type deprecatedFile struct {
		Body struct {
			F geta.File `form:"f" schema:"deprecated=true"`
		} `body:"multipart"`
	}
	rejects(t, one("/x", post(func(context.Context, *deprecatedFile) (*ok, error) { return nil, nil })),
		`schema tag "deprecated=true" on a geta.File`)
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/m", Route: post(func(context.Context, *srFormFiles) (*ok, error) { return &ok{true}, nil })},
		{Path: "/f", Route: post(func(context.Context, *rqLimitFormIn) (*ok, error) { return &ok{true}, nil })},
		{Path: "/d", Route: post(func(_ context.Context, in *srFormDate) (*srRawGot, error) { return &srRawGot{in.Body.D.String()}, nil })},
	}})
	// A form field of a text type is read by its UnmarshalText.
	if res := c.Content(http.MethodPost, "/d", "application/x-www-form-urlencoded", []byte("d=2024-02-29")); res.Status != 200 ||
		res.JSON[srRawGot]().Got != "2024-02-29" {
		t.Fatal(res.Status, res.Text())
	}
	same(t, violations(t, c.Content(http.MethodPost, "/d", "application/x-www-form-urlencoded", []byte("d=2023-02-29"))),
		`body $.d: "2023-02-29" is not a valid date`)
	same(t, violations(t, c.Multipart(http.MethodPost, "/m", nil, getatest.FilePart{Field: "docs", Filename: "a"})),
		"body $.docs: array length 1 is shorter than minItems 2")
	if res := c.Multipart(http.MethodPost, "/m", nil, getatest.FilePart{Field: "docs", Filename: "a"}, getatest.FilePart{Field: "docs", Filename: "b"}); res.Status != 200 {
		t.Fatal(res.Status, res.Text())
	}
	res := c.Content(http.MethodPost, "/f", "application/x-www-form-urlencoded", []byte("name=%zz"))
	if v := violations(t, res); len(v) != 1 || !strings.HasPrefix(v[0], "body $: the form body is malformed: ") {
		t.Fatal(v)
	}
}

type srOpenIn struct {
	Body struct {
		F geta.File `form:"f"`
	} `body:"multipart"`
}

// A file the server received may be opened any number of times while the
// handler runs; a file NewFile made has the name and media type it was
// given, Size -1, and reads its reader.
func TestFileAPI(t *testing.T) {
	var reads []string
	c := getatest.New(t, one("/m", post(func(_ context.Context, in *srOpenIn) (*ok, error) {
		for range 2 {
			rc, err := in.Body.F.Open()
			if err != nil {
				return nil, err
			}
			b, _ := io.ReadAll(rc)
			rc.Close()
			reads = append(reads, string(b))
		}
		return &ok{true}, nil
	})))
	if res := c.Multipart(http.MethodPost, "/m", nil, getatest.FilePart{Field: "f", Filename: "a", Content: []byte("xyz")}); res.Status != 200 ||
		fmt.Sprint(reads) != "[xyz xyz]" {
		t.Fatal(res.Status, res.Text(), reads)
	}
	f := geta.NewFile("n.txt", "", strings.NewReader("abc"))
	rc, err := f.Open()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	if f.Filename() != "n.txt" || f.ContentType() != "" || f.Size() != -1 || string(b) != "abc" {
		t.Fatal(f.Filename(), f.ContentType(), f.Size(), string(b))
	}
}

// Doc.Limits is called once, by geta.New, with the App's limits.
func TestDocLimitsIsCalledOnceWithTheAppsLimits(t *testing.T) {
	calls := 0
	var given geta.Limits
	l := geta.DefaultLimits
	l.MaxItems = 77
	own := func(in geta.Limits) geta.Limits {
		calls++
		given = in
		return in
	}
	app, err := geta.New(one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Limits: own})}), geta.WithLimits(l))
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		do(t, app, "GET", "/x")
	}
	if calls != 1 || given != l {
		t.Fatal(calls, given)
	}
}

// A request that does not meet its contract is a 400 problem with detail
// "the request does not match its contract" and an errors member, each
// violation an object of in, path, and message.
func TestAViolationIsAProblemWithErrors(t *testing.T) {
	app := accepts(t, one("/x", get(func(context.Context, *srEnumIn) (*ok, error) { return &ok{true}, nil })))
	rec := do(t, app, "GET", "/x?s=c")
	var p struct {
		Status int               `json:"status"`
		Detail string            `json:"detail"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || rec.Code != 400 || p.Status != 400 ||
		p.Detail != "the request does not match its contract" || len(p.Errors) != 1 ||
		string(p.Errors[0]) != `{"in":"query","path":"s","message":"\"c\" is not one of a, b"}` {
		t.Fatal(rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatal(ct)
	}
}

// A Nullable is absent at its zero value, null from Null, and holds a value
// from NotNull.
func TestNullableStates(t *testing.T) {
	var absent geta.Nullable[int]
	null := geta.Null[int]()
	val := geta.NotNull(7)
	if v, ok := absent.Get(); ok || v != 0 || absent.IsNull() || !absent.IsZero() {
		t.Error("absent")
	}
	if v, ok := null.Get(); ok || v != 0 || !null.IsNull() || null.IsZero() {
		t.Error("null")
	}
	if v, ok := val.Get(); !ok || v != 7 || val.IsNull() || val.IsZero() {
		t.Error("value")
	}
}

// A raw io.Reader body read past the body limit returns an error that is,
// or wraps, *http.MaxBytesError.
func TestARawReaderPastTheLimitReturnsMaxBytesError(t *testing.T) {
	l := geta.DefaultLimits
	l.MaxBodyBytes = 4
	var readErr error
	app, err := geta.New(one("/r", post(func(_ context.Context, in *srRawReaderIn) (*ok, error) {
		_, readErr = io.ReadAll(in.Body)
		return &ok{true}, nil
	})), geta.WithLimits(l))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/r", strings.NewReader("123456"))
	req.Header.Set("Content-Type", "text/csv")
	app.ServeHTTP(httptest.NewRecorder(), req)
	var mbe *http.MaxBytesError
	if !errors.As(readErr, &mbe) || mbe.Limit != 4 {
		t.Fatal(readErr)
	}
}
