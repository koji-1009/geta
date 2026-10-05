package geta_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
	"uuid"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// problemOf reads the problem a recorder holds.
func problemOf(t *testing.T, rec *httptest.ResponseRecorder) geta.Problem {
	t.Helper()
	var p geta.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("%d %q: %v", rec.Code, rec.Body, err)
	}
	return p
}

// listed renders a problem's errors as "in path: message", in order.
func listed(p geta.Problem) []string {
	out := make([]string, len(p.Errors))
	for i, v := range p.Errors {
		out[i] = v.In + " " + v.Path + ": " + v.Message
	}
	return out
}

type rqListIn struct {
	Tags  []string     `header:"X-Tag"`
	Days  *[]geta.Date `header:"X-Day"`
	Nums  *[]int8      `header:"X-Num"`
	Codes *[]rqLevel   `header:"X-Level"`
}

// rqLevel is a text type that takes low and high only.
type rqLevel string

func (l rqLevel) MarshalText() ([]byte, error) { return []byte(l), nil }
func (l *rqLevel) UnmarshalText(b []byte) error {
	if s := string(b); s != "low" && s != "high" {
		return errors.New("not a level")
	}
	*l = rqLevel(b)
	return nil
}

// A header a slice binds is a list, as OpenAPI's style simple (a header
// parameter's) and RFC 9110 section 5.6.1 read one: the comma-separated
// elements of one line, and the lines of several joined by commas, each
// trimmed, an empty element ignored. Each element is judged by the
// element's own schema, and the document says an element holds no comma.
func TestHeaderListsAreReadAsTheDocumentSays(t *testing.T) {
	var got *rqListIn
	app := accepts(t, one("/l", get(func(_ context.Context, in *rqListIn) (*ok, error) {
		got = in
		return &ok{true}, nil
	})))
	for _, c := range []struct {
		lines []string
		want  string
	}{
		{[]string{"a,b"}, "[a b]"},
		{[]string{"a, b ,\tc"}, "[a b c]"},
		{[]string{"a,b", "c"}, "[a b c]"},
		{[]string{"a,,b", " , c"}, "[a b c]"},
		{[]string{"é"}, "[é]"},
	} {
		var hs []string
		for _, l := range c.lines {
			hs = append(hs, "X-Tag", l)
		}
		rec := do(t, app, "GET", "/l", hs...)
		if rec.Code != 200 || fmt.Sprint(got.Tags) != c.want {
			t.Errorf("%q: %d %s %v", c.lines, rec.Code, rec.Body, got.Tags)
		}
	}
	// A list of no element is no value: a required slice is missing.
	for _, lines := range [][]string{{","}, {" , ", ""}} {
		var hs []string
		for _, l := range lines {
			hs = append(hs, "X-Tag", l)
		}
		rec := do(t, app, "GET", "/l", hs...)
		if p := problemOf(t, rec); rec.Code != 400 || fmt.Sprint(listed(p)) != "[header X-Tag: missing required parameter]" {
			t.Errorf("%q: %d %v", lines, rec.Code, listed(p))
		}
	}
	// Each element is held to the element's schema, at its index.
	rec := do(t, app, "GET", "/l", "X-Tag", "a", "X-Day", "2026-01-01, 2026-02-30", "X-Num", "1,x, 300", "X-Level", "low,mid")
	if p := problemOf(t, rec); rec.Code != 400 || fmt.Sprint(listed(p)) != `[header X-Day[1]: "2026-02-30" is not a valid date `+
		`header X-Num[1]: expected integer, got string header X-Num[2]: 300 is out of range for int8 header X-Level[1]: "mid" is not a valid rqLevel]` {
		t.Fatal(rec.Code, listed(p))
	}
	rec = do(t, app, "GET", "/l", "X-Tag", "a", "X-Day", "2026-01-01,2026-02-28", "X-Num", "-1, 2", "X-Level", "high, low")
	if rec.Code != 200 || len(*got.Days) != 2 || (*got.Days)[1].String() != "2026-02-28" || fmt.Sprint(*got.Nums) != "[-1 2]" ||
		fmt.Sprint(*got.Codes) != "[high low]" {
		t.Fatal(rec.Code, rec.Body, got)
	}
	// The document states what an element carries: no comma, not empty.
	for _, p := range at(t, doc(t, app), "paths", "/l", "get", "parameters").([]any) {
		pm := p.(map[string]any)
		switch pm["name"] {
		case "X-Tag":
			if g := compact(t, pm["schema"]); g != compact(t, map[string]any{"type": "array", "maxItems": 8192,
				"items": map[string]any{"type": "string", "maxLength": 4096, "pattern": elementPattern}}) {
				t.Error(g)
			}
		case "X-Level":
			if g := compact(t, at(t, pm, "schema", "items", "pattern")); g != compact(t, elementPattern) {
				t.Error(g)
			}
		}
		if _, ok := pm["style"]; ok {
			t.Errorf("%v: a header's style is simple, its default", pm)
		}
	}
}

type rqManyBody struct {
	A string `json:"a"`
}

type rqManyIn struct {
	P    int        `query:"p"`
	Q    int        `query:"q"`
	Body rqManyBody `body:"json"`
}

type rqManyFormIn struct {
	P    int `query:"p"`
	Body struct {
		A string `form:"a"`
	} `body:"form"`
}

// A response lists at most the first 50 violations, however many places
// hold them: the parameters', then the body's.
func TestAtMostFiftyViolationsPerResponse(t *testing.T) {
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/j", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *rqManyIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}},
		{Path: "/f", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *rqManyFormIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}},
	}})
	var members []string
	form := url.Values{"a": {"x"}}
	for i := range 60 {
		members = append(members, fmt.Sprintf(`"m%02d":1`, i))
		form.Set(fmt.Sprintf("m%02d", i), "1")
	}
	// omitted counts the rest: 62 found, then 61, then 60.
	p := c.Post("/j", "{"+strings.Join(members, ",")+`,"a":"x"}`).Problem()
	if p.Status != 400 || len(p.Errors) != 50 || p.Errors[0].Path != "p" || p.Errors[1].Path != "q" || p.Errors[2].Path != "$.m00" ||
		p.Errors[49].Path != "$.m47" || p.Omitted != 12 {
		t.Fatal(p.Status, len(p.Errors), p.Omitted, listed(p))
	}
	p = c.Form(http.MethodPost, "/f", form).Problem()
	if p.Status != 400 || len(p.Errors) != 50 || p.Errors[0].Path != "p" || p.Errors[1].Path != "$.m00" || p.Omitted != 11 {
		t.Fatal(p.Status, len(p.Errors), p.Omitted, listed(p))
	}
	// Fifty at most in one place, too.
	p = c.Post("/j?p=1&q=2", "{"+strings.Join(members, ",")+`,"a":"x"}`).Problem()
	if len(p.Errors) != 50 || p.Errors[0].Path != "$.m00" || p.Omitted != 10 {
		t.Fatal(len(p.Errors), p.Omitted, listed(p))
	}
}

// rqTree nests in itself through a map, so a body chooses both the depth
// and the member names of a violation's path.
type rqTree struct {
	K map[string]rqTree `json:"k"`
}

type rqTreeIn struct {
	Body rqTree `body:"json"`
}

type rqLongIn struct {
	Body struct {
		Picks []string `json:"picks" schema:"items.enum=a|b"`
	} `body:"json"`
}

// A violation's path is cut to 256 bytes, keeping its end after "…", a
// value its message quotes to 128 bytes, then "…", and the violations listed
// to 16 KiB, always the first; omitted counts the rest. A short path and a
// problem under both caps are as before, byte for byte.
func TestViolationListsAreBounded(t *testing.T) {
	tbl := geta.Table{Routes: []geta.Entry{
		{Path: "/t", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *rqTreeIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}},
		{Path: "/l", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *rqLongIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}},
		{Path: "/j", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *rqManyIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}},
	}}
	c := getatest.New(t, tbl)

	// About 1 MB: 230 levels of 4000-byte names, then 60 values that are no
	// object. Unbounded, the 50 paths listed would be some 46 MB.
	key := strings.Repeat("k", 4000)
	var b strings.Builder
	for range 230 {
		b.WriteString(`{"k":{"` + key + `":`)
	}
	b.WriteString(`{"k":{`)
	for i := range 60 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"v%02d":1`, i)
	}
	b.WriteString("}}" + strings.Repeat("}}", 230))
	res := c.Post("/t", b.String())
	p := res.Problem()
	if res.Status != 400 || len(res.Body) >= 32<<10 || len(p.Errors) != 50 || p.Omitted != 10 {
		t.Fatalf("%d, %d bytes, %d listed, %d omitted", res.Status, len(res.Body), len(p.Errors), p.Omitted)
	}
	for i, v := range p.Errors {
		want := "…" + strings.Repeat("k", 256-len("…")-len(".k.v00")) + fmt.Sprintf(".k.v%02d", i)
		if v.Path != want || v.Message != "expected object, got integer" {
			t.Fatalf("%d: %q %q", i, v.Path, v.Message)
		}
	}

	// A path cut within a multi-byte rune starts at the next rune.
	b.Reset()
	b.WriteString(`{"k":{"` + strings.Repeat("é", 300) + `":{"k":{"x":1}}}}`)
	p = c.Post("/t", b.String()).Problem()
	if len(p.Errors) != 1 || !utf8.ValidString(p.Errors[0].Path) || !strings.HasPrefix(p.Errors[0].Path, "…é") ||
		len(p.Errors[0].Path) > 256 || !strings.HasSuffix(p.Errors[0].Path, "é.k.x") {
		t.Fatalf("%q", listed(p))
	}

	// A path of 256 bytes is whole; of 257, cut.
	for n, want := range map[int]string{
		256: "$.k." + strings.Repeat("p", 252),
		257: "…" + strings.Repeat("p", 253),
	} {
		p = c.Post("/t", `{"k":{"`+strings.Repeat("p", n-len("$.k."))+`":1}}`).Problem()
		if len(p.Errors) != 1 || p.Errors[0].Path != want {
			t.Fatalf("%d: %q", n, listed(p))
		}
	}

	// A duplicate key names its path as the path, cut, whatever the name's
	// length: some 500 KB of name, six bytes a byte in JSON, answers under 2
	// KiB.
	name := strings.Repeat("<", 500<<10)
	res = c.Post("/t", `{"k":{"`+name+`":{},"`+name+`":{}}}`)
	p = res.Problem()
	if res.Status != 400 || len(res.Body) >= 2<<10 || len(p.Errors) != 1 ||
		p.Errors[0].Path != "…"+strings.Repeat("<", 253) || p.Errors[0].Message != "duplicate object key" {
		t.Fatalf("%d, %d bytes: %q", res.Status, len(res.Body), listed(p))
	}

	// A message quotes at most 128 bytes of a value, then "…": a string, and
	// a number past its type's range.
	quoted := `"` + strings.Repeat("<", 128) + `…"`
	res = c.Post("/l", `{"picks":["`+strings.Repeat("<", 4000)+`"]}`)
	if p = res.Problem(); len(p.Errors) != 1 || p.Errors[0].Message != quoted+" is not one of a, b" || len(res.Body) >= 2<<10 {
		t.Fatalf("%d bytes: %q", len(res.Body), listed(p))
	}
	p = c.Post("/j?q=1&p="+strings.Repeat("9", 4000), `{"a":"x"}`).Problem()
	if len(p.Errors) != 1 || p.Errors[0].Message != strings.Repeat("9", 128)+"… is out of range for int" {
		t.Fatalf("%q", listed(p))
	}

	// Each message quotes a value JSON writes at six bytes a byte: the list
	// stops before the errors array passes 16 KiB.
	vals := make([]string, 60)
	for i := range vals {
		vals[i] = `"` + strings.Repeat("<", 3000) + `"`
	}
	res = c.Post("/l", `{"picks":[`+strings.Join(vals, ",")+`]}`)
	p = res.Problem()
	var raw struct {
		Errors json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(res.Body, &raw); err != nil {
		t.Fatal(err)
	}
	if res.Status != 400 || len(p.Errors) != 19 || p.Omitted != 41 || len(raw.Errors) > 16<<10 || p.Errors[18].Message != quoted+" is not one of a, b" {
		t.Fatalf("%d: %d listed in %d bytes, %d omitted", res.Status, len(p.Errors), len(raw.Errors), p.Omitted)
	}

	// The Debug line counts what was listed and omitted, naming no path.
	var logged strings.Builder
	log := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	app, err := geta.New(tbl, geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/l", strings.NewReader(`{"picks":[`+strings.Join(vals, ",")+`]}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(rec, req)
	if l := logged.String(); rec.Code != 400 || !strings.Contains(l, "violations=19 ") || !strings.Contains(l, "omitted=41") ||
		strings.Contains(l, "picks") || strings.Count(l, "\n") != 1 {
		t.Fatalf("%d %q", rec.Code, l)
	}

	// Under both caps a problem is as it was, and states nothing omitted.
	res = c.Post("/j?q=1", `{"a":"x"}`)
	if string(res.Body) != `{"type":"about:blank","title":"Bad Request","status":400,"detail":"the request does not match its contract",`+
		`"errors":[{"in":"query","path":"p","message":"missing required parameter"}]}` {
		t.Fatalf("%s", res.Body)
	}
}

type rqTextIn struct {
	Q    *string     `query:"q"`
	T    *carryToken `query:"t"`
	H    *string     `header:"X-H"`
	C    *string     `cookie:"c"`
	Deep *struct {
		M *string `form:"m"`
	} `query:"deep"`
}

type rqTextFormIn struct {
	Body struct {
		F *string `form:"f"`
	} `body:"form"`
}

type rqTextPathIn struct {
	S string `path:"s"`
}

type rqEcho struct {
	Q string `json:"q"`
}

// A parameter's, a header's, and a form field's text is a string only when
// it is UTF-8, as a JSON body's strings are by JSON's grammar: other bytes
// are a 400 wherever they are sent, not a value bound into a string no JSON
// writes (a handler echoing it answered 500). A cookie net/http reads holds
// printable ASCII alone, so such a cookie is never read. A name the form
// does not declare is named with the bytes that are not UTF-8 as U+FFFD, so
// the problem can be written.
func TestTextThatIsNotUTF8IsRefused(t *testing.T) {
	const why = "the text is not valid UTF-8"
	var got *rqTextIn
	app := accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/t", Route: get(func(_ context.Context, in *rqTextIn) (*rqEcho, error) {
			got = in
			if in.Q != nil {
				return &rqEcho{Q: *in.Q}, nil
			}
			return &rqEcho{}, nil
		})},
		{Path: "/f", Route: geta.Route{Post: geta.Op(http.StatusOK, func(_ context.Context, in *rqTextFormIn) (*rqEcho, error) {
			return &rqEcho{Q: *in.Body.F}, nil
		}, geta.Doc{})}},
		{Path: "/p/{s}", Route: get(func(_ context.Context, in *rqTextPathIn) (*rqEcho, error) { return &rqEcho{Q: in.S}, nil })},
	}})
	for _, c := range []struct {
		target  string
		headers []string
		want    string
	}{
		{"/t?q=%FF", nil, "query q: " + why},
		{"/t?q=a%C3", nil, "query q: " + why},
		{"/t?t=%ED%A0%80", nil, "query t: " + why},
		{"/t?deep[m]=%FF", nil, "query deep[m]: " + why},
		{"/t", []string{"X-H", "a\xffb"}, "header X-H: " + why},
		{"/p/%FF", nil, "path s: " + why},
	} {
		rec := do(t, app, "GET", c.target, c.headers...)
		if p := problemOf(t, rec); rec.Code != 400 || fmt.Sprint(listed(p)) != "["+c.want+"]" {
			t.Errorf("%s %q: %d %v", c.target, c.headers, rec.Code, listed(p))
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/f", strings.NewReader("f=%FF"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if p := problemOf(t, rec); rec.Code != 400 || fmt.Sprint(listed(p)) != "[body $.f: "+why+"]" {
		t.Errorf("form: %d %v", rec.Code, listed(p))
	}
	req = httptest.NewRequest(http.MethodPost, "/f", strings.NewReader("f=a&%FFx=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if p := problemOf(t, rec); rec.Code != 400 || fmt.Sprint(listed(p)) != "[body $."+string(utf8.RuneError)+"x: unknown field]" {
		t.Errorf("form name: %d %q", rec.Code, rec.Body)
	}
	if rec := do(t, app, "GET", "/t", "Cookie", "c=a\xffb"); rec.Code != 200 || got.C != nil {
		t.Errorf("cookie: %d %s %v", rec.Code, rec.Body, got.C)
	}
	if rec := do(t, app, "GET", "/t?q=%C3%A9&deep[m]=%E2%82%AC", "X-H", "é"); rec.Code != 200 || *got.Q != "é" || *got.H != "é" || *got.Deep.M != "€" {
		t.Errorf("UTF-8: %d %s", rec.Code, rec.Body)
	}
}

type rqEnumDate struct {
	D *string `query:"d" schema:"format=date,enum=foo|2026-01-01"`
}

type rqEnumDateType struct {
	D *geta.Date `query:"d" schema:"enum=foo"`
}

type rqEnumUUID struct {
	ID *uuid.UUID `json:"id,omitzero" schema:"enum=nope"`
}

type rqEnumText struct {
	L *rqLevel `query:"l" schema:"enum=low|mid"`
}

type rqEnumGood struct {
	D *geta.Date `query:"d" schema:"enum=2026-01-01|2026-02-28"`
	L *rqLevel   `query:"l" schema:"enum=low"`
}

// An enum member a request's value could never be is refused, as a member
// past a pattern or a backstop is: one the format a string is held to
// refuses, and one a text type's UnmarshalText refuses (a format type's
// included), by geta.New and CheckSchemaTag alike.
func TestEnumMembersTheFormatOrTypeRefusesAreRefused(t *testing.T) {
	rejects(t, one("/x", get(func(context.Context, *rqEnumDate) (*ok, error) { return nil, nil })),
		`rqEnumDate.D: enum member "foo" is not a valid date`)
	rejects(t, one("/x", get(func(context.Context, *rqEnumDateType) (*ok, error) { return nil, nil })),
		`rqEnumDateType.D`, `enum member "foo" is not a valid date`)
	rejects(t, one("/x", get(func(context.Context, *empty) (*rqEnumUUID, error) { return nil, nil })),
		`rqEnumUUID.ID`, `enum member "nope" is not a valid uuid`)
	rejects(t, one("/x", get(func(context.Context, *rqEnumText) (*ok, error) { return nil, nil })),
		`rqEnumText.L`, `enum member "mid" is not a valid rqLevel`)
	app := accepts(t, one("/x", get(func(context.Context, *rqEnumGood) (*ok, error) { return &ok{true}, nil })))
	if rec := do(t, app, "GET", "/x?d=2026-02-28&l=low"); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
	for _, c := range []struct{ tag, kind, want string }{
		{"format=date,enum=foo|2026-01-01", "string", `enum member "foo" is not a valid date`},
		{"enum=foo", "geta.Date", `enum member "foo" is not a valid date`},
		{"enum=nope", "uuid.UUID", `enum member "nope" is not a valid uuid`},
		{"enum=nope", "?uuid.UUID", `enum member "nope" is not a valid uuid`},
		{"enum=1.2.3.4|1.2.3", "geta.IPv4", `enum member "1.2.3" is not a valid ipv4`},
		{"enum=2026-01-01", "geta.Date", ""},
		{"format=ipv4,enum=10.0.0.1", "string", ""},
		{"enum=anything", "text", ""}, // the type is the program's: geta.New judges it
	} {
		err := geta.CheckSchemaTag(c.tag, c.kind)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s on %s: %v", c.tag, c.kind, err)
		}
	}
}

type rqShape interface{ isRqShape() }

type rqDefaultKind struct {
	Kind string `json:"kind" schema:"default=dk"`
}

type rqNarrowKind struct {
	Kind string `json:"kind" schema:"enum=x|y"`
}

type rqPlainKind struct {
	Kind string `json:"kind" schema:"maxLength=2"`
}

func (rqDefaultKind) isRqShape() {}
func (rqNarrowKind) isRqShape()  {}
func (rqPlainKind) isRqShape()   {}

type rqShapeIn struct {
	Body struct {
		S rqShape `json:"s"`
	} `body:"json"`
}

// A discriminator selects the variant, so a request always sends it: a
// default on it, which would tell a client it may leave it out, and a
// schema that refuses the variant's own tag, which no request of the
// variant would then meet, are refused.
func TestDiscriminatorsAreTruthful(t *testing.T) {
	tbl := one("/s", geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *rqShapeIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})})
	for _, c := range []struct {
		u    geta.Union
		want string
	}{
		{geta.Sealed[rqShape]("kind", geta.Case[rqDefaultKind]("dk")), `variant geta_test.rqDefaultKind: the discriminator member "kind" takes no default`},
		{geta.Sealed[rqShape]("kind", geta.Case[rqNarrowKind]("z")), `variant geta_test.rqNarrowKind: tag "z" does not fit discriminator member "kind": "z" is not one of x, y`},
		{geta.Sealed[rqShape]("kind", geta.Case[rqPlainKind]("abc")), `tag "abc" does not fit discriminator member "kind": string length 3 exceeds maxLength 2`},
	} {
		if _, err := geta.New(tbl, geta.WithUnion(c.u)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v\nwant %s", err, c.want)
		}
	}
	c := getatest.New(t, tbl, geta.WithUnion(geta.Sealed[rqShape]("kind", geta.Case[rqNarrowKind]("x"), geta.Case[rqPlainKind]("p"))))
	if res := c.Post("/s", `{"s":{"kind":"x"}}`); res.Status != 200 {
		t.Fatal(res.Status, res.Text())
	}
}

type rqNumIn struct {
	N *int     `query:"n"`
	U *uint8   `query:"u"`
	E *int     `query:"e" schema:"enum=7|8"`
	F *float64 `query:"f"`
	H *int     `header:"X-N"`
	D *struct {
		M *int `form:"m"`
	} `query:"d"`
}

type rqNumFormIn struct {
	Body struct {
		N *int `form:"n"`
	} `body:"form"`
}

// An integer parameter is written as JSON writes an integer, as a number
// parameter is written as a JSON number and a default is: no leading zero
// and no plus, in a query, a header, a deepObject, and a form alike.
func TestIntegerParametersAreJSONIntegers(t *testing.T) {
	var got *rqNumIn
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/n", Route: get(func(_ context.Context, in *rqNumIn) (*ok, error) {
			got = in
			return &ok{true}, nil
		})},
		{Path: "/f", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *rqNumFormIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}},
	}})
	for _, q := range []string{"n=007", "n=-07", "n=00", "n=+7", "u=01", "e=007", "f=007", "f=01.5", "d[m]=010"} {
		p := c.Get("/n?" + q).Problem()
		if p.Status != 400 || len(p.Errors) != 1 || !strings.HasPrefix(p.Errors[0].Message, "expected ") {
			t.Errorf("%s: %d %v", q, p.Status, listed(p))
		}
	}
	if p := c.With("X-N", "012").Get("/n").Problem(); p.Status != 400 || p.Errors[0].Message != "expected integer, got string" {
		t.Error("header:", p.Status, listed(p))
	}
	if p := c.Form(http.MethodPost, "/f", url.Values{"n": {"09"}}).Problem(); p.Status != 400 || p.Errors[0].Message != "expected integer, got string" {
		t.Error("form:", p.Status, listed(p))
	}
	if res := c.Get("/n?n=0&u=0&e=7&f=0.5&d[m]=-0"); res.Status != 200 || *got.N != 0 || *got.E != 7 || *got.D.M != 0 {
		t.Fatal(res.Status, res.Text())
	}
	if res := c.Get("/n?n=-0&u=10"); res.Status != 200 || *got.N != 0 || *got.U != 10 {
		t.Fatal(res.Status, res.Text())
	}
}

type rqOrderIn struct {
	N    int        `query:"n"`
	Body rqManyBody `body:"json"`
}

type rqOrderRawIn struct {
	N    int    `query:"n"`
	Body []byte `body:"text/csv"`
}

type rqOrderFormIn struct {
	N    int `query:"n"`
	Body struct {
		A string `form:"a"`
	} `body:"form"`
}

// A body geta does not take (415) or that is past the body limit (413) is
// answered alone: one response has one status, and those say the content
// was not read, so the parameters' violations found before it are not
// listed; they are on the next response. A body that is read and refused
// (400) is listed after the parameters' violations.
func TestABodyAnsweredOnItsOwnComesBeforeParameterViolations(t *testing.T) {
	limits := geta.DefaultLimits
	limits.MaxBodyBytes = 16
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/j", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *rqOrderIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}},
		{Path: "/r", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *rqOrderRawIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}},
		{Path: "/f", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *rqOrderFormIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}},
	}}, geta.WithLimits(limits))
	for _, path := range []string{"/j?n=x", "/r?n=x", "/f?n=x"} {
		res := c.Content(http.MethodPost, path, "text/plain", []byte("a"))
		if path == "/r?n=x" {
			res = c.Content(http.MethodPost, path, "application/json", []byte("a"))
		}
		if p := res.Problem(); p.Status != 415 || len(p.Errors) != 0 {
			t.Errorf("%s 415: %d %v", path, p.Status, listed(p))
		}
		ct := map[string]string{"/j?n=x": "application/json", "/r?n=x": "text/csv", "/f?n=x": "application/x-www-form-urlencoded"}[path]
		if p := c.Content(http.MethodPost, path, ct, []byte(strings.Repeat("a", 17))).Problem(); p.Status != 413 || len(p.Errors) != 0 ||
			p.Detail != "the request body exceeds 16 bytes" {
			t.Errorf("%s 413: %d %v", path, p.Status, listed(p))
		}
	}
	if p := c.Post("/j?n=x", `{}`).Problem(); p.Status != 400 ||
		fmt.Sprint(listed(p)) != "[query n: expected integer, got string body $.a: missing required member]" {
		t.Error(p.Status, listed(p))
	}
	if p := c.Form(http.MethodPost, "/f?n=x", url.Values{"b": {"1"}}).Problem(); p.Status != 400 ||
		fmt.Sprint(listed(p)) != "[query n: expected integer, got string body $.a: missing required field body $.b: unknown field]" {
		t.Error(p.Status, listed(p))
	}
	if p := c.Content(http.MethodPost, "/r?n=x", "text/csv", nil).Problem(); p.Status != 400 ||
		fmt.Sprint(listed(p)) != "[query n: expected integer, got string body $: missing required request body]" {
		t.Error(p.Status, listed(p))
	}
}
