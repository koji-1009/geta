package geta_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// carryToken is a text type with no format: its text may be anything.
type carryToken struct{ s string }

func (k carryToken) MarshalText() ([]byte, error)  { return []byte(k.s), nil }
func (k *carryToken) UnmarshalText(b []byte) error { k.s = string(b); return nil }

type carryIn struct {
	Name   *string     `header:"X-Name"`
	Tags   []string    `header:"X-Tag"`
	Kind   *string     `header:"X-Kind" schema:"pattern=^[a-z ]+$"`
	Mode   *string     `header:"X-Mode" schema:"enum=a|b c"`
	Day    *geta.Date  `header:"X-Day"`
	Token  *carryToken `header:"X-Token"`
	Count  *int        `header:"X-Count"`
	Query  *string     `query:"q"`
	Sess   *string     `cookie:"sess"`
	Choice *string     `cookie:"choice" schema:"enum=x|y z"`
}

const (
	headerPattern = `^(?:[^\x00-\x20\x7F](?:[^\x00-\x08\x0A-\x1F\x7F]*[^\x00-\x20\x7F])?)?$`
	cookiePattern = `^[\x20\x21\x23-\x3A\x3C-\x5B\x5D-\x7E]*$`
	// An element of a header list: no comma, not empty.
	elementPattern = `^[^\x00-\x20\x7F,](?:[^\x00-\x08\x0A-\x1F\x7F,]*[^\x00-\x20\x7F,])?$`
)

func carryApp(t *testing.T, got **carryIn) geta.Table {
	t.Helper()
	return one("/c", get(func(_ context.Context, in *carryIn) (*ok, error) {
		*got = in
		return &ok{true}, nil
	}))
}

// A header parameter's document says what a header field value carries
// (RFC 9110 section 5.5), and a cookie's what net/http reads of a cookie,
// for every type whose text could hold more; an author's pattern keeps its
// place, the carrier's joining it in allOf.
func TestHeaderAndCookieParametersDocumentWhatTheyCarry(t *testing.T) {
	var got *carryIn
	d := doc(t, accepts(t, carryApp(t, &got)))
	schemas := map[string]any{}
	for _, p := range at(t, d, "paths", "/c", "get", "parameters").([]any) {
		schemas[p.(map[string]any)["name"].(string)] = p.(map[string]any)["schema"]
	}
	header := map[string]any{"pattern": headerPattern, "type": "string", "maxLength": 4096}
	for name, want := range map[string]any{
		"X-Name": header,
		"X-Tag": map[string]any{"type": "array", "items": map[string]any{"pattern": elementPattern, "type": "string", "maxLength": 4096},
			"maxItems": 8192},
		"X-Kind": map[string]any{"type": "string", "pattern": "^[a-z ]+$", "allOf": []any{map[string]any{"pattern": headerPattern}},
			"maxLength": 4096},
		"X-Mode":  map[string]any{"type": "string", "enum": []any{"a", "b c"}},
		"X-Day":   map[string]any{"type": "string", "format": "date"},
		"X-Token": header,
		"X-Count": map[string]any{"type": "integer", "format": "int64"},
		"q":       map[string]any{"type": "string", "maxLength": 4096},
		"sess":    map[string]any{"type": "string", "pattern": cookiePattern, "maxLength": 4096},
		"choice":  map[string]any{"type": "string", "enum": []any{"x", "y z"}},
	} {
		if g, w := compact(t, schemas[name]), compact(t, want); g != w {
			t.Errorf("%s: %s, want %s", name, g, w)
		}
	}
}

// A header value the document does not admit is refused, as the document
// states it, however it reached the server; one it admits binds as sent.
func TestHeaderParametersHoldToWhatAHeaderCarries(t *testing.T) {
	var got *carryIn
	app := accepts(t, carryApp(t, &got))
	r := do(t, app, "GET", "/c", "X-Name", "a \t b", "X-Tag", "é", "X-Tag", "", "X-Kind", "a b", "X-Token", "W/\"x\"",
		"X-Count", "1", "Cookie", `sess="a b,c"`)
	// The empty line is an empty element of the list, which is ignored.
	if r.Code != 200 || *got.Name != "a \t b" || len(got.Tags) != 1 || got.Tags[0] != "é" || *got.Kind != "a b" ||
		got.Token.s != `W/"x"` || *got.Sess != "a b,c" {
		t.Fatal(r.Code, r.Body, got)
	}
	r = do(t, app, "GET", "/c", "X-Name", " a b ", "X-Tag", "a", "X-Tag", "b\x01", "X-Kind", "ab ", "X-Token", "\tx")
	if r.Code != 400 {
		t.Fatal(r.Code, r.Body)
	}
	const why = `is not a valid header field value`
	for _, want := range []string{
		`"path":"X-Name","message":"\" a b \" ` + why,
		`"path":"X-Tag[1]","message":"\"b\\x01\" is not a valid header list element`,
		`"path":"X-Kind","message":"\"ab \" ` + why,
		`"path":"X-Token","message":"\"\\tx\" ` + why,
	} {
		if !strings.Contains(r.Body.String(), want) {
			t.Errorf("missing %s in %s", want, r.Body)
		}
	}
}

type carryEnumHeader struct {
	Mode *string `header:"X-Mode" schema:"enum=a| b"`
}

type carryEnumCookie struct {
	Mode *string `cookie:"mode" schema:"enum=a|b;c"`
}

type carryEnumQuery struct {
	Mode *string `query:"mode" schema:"enum=a| b"`
}

// An enum member a header or a cookie cannot carry would be documented and
// never received: geta.New refuses it.
func TestEnumMembersAHeaderOrCookieCannotCarryAreRefused(t *testing.T) {
	rejects(t, one("/x", get(func(context.Context, *carryEnumHeader) (*ok, error) { return nil, nil })),
		`carryEnumHeader.Mode: header parameter "X-Mode": enum member " b" is not a valid header field value`)
	rejects(t, one("/x", get(func(context.Context, *carryEnumCookie) (*ok, error) { return nil, nil })),
		`carryEnumCookie.Mode: cookie parameter "mode": enum member "b;c" is not a valid cookie value`)
	accepts(t, one("/x", get(func(context.Context, *carryEnumQuery) (*ok, error) { return nil, nil })))
}

type carryCondIn struct {
	geta.Conditional
}

// A precondition's document says what a header carries, and no
// MaxStringLength: geta reads it by RFC 9110's grammar, not as a string
// parameter. Sent on several lines, one of them empty, it is the list of the
// others: the empty element is ignored, not left as whitespace at the end of
// the value the document would then refuse.
func TestPreconditionHeadersCarryTheirLists(t *testing.T) {
	var seen string
	app := accepts(t, one("/x", get(func(_ context.Context, in *carryCondIn) (*ok, error) {
		seen = *in.IfMatch
		return &ok{true}, nil
	})))
	for _, p := range at(t, doc(t, app), "paths", "/x", "get", "parameters").([]any) {
		if g := compact(t, p.(map[string]any)["schema"]); g != compact(t, map[string]any{"type": "string", "pattern": headerPattern}) {
			t.Errorf("%v: %s", p.(map[string]any)["name"], g)
		}
	}
	if r := do(t, app, "GET", "/x", "If-Match", `"a"`, "If-Match", "", "If-Match", `"b"`, "If-Match", ""); r.Code != 200 || seen != `"a", "b"` {
		t.Fatal(r.Code, r.Body, seen)
	}
}

type carryOut struct {
	Note  string      `header:"X-Note"`
	Token *carryToken `header:"X-Token"`
	Day   geta.Date   `header:"X-Day"`
	Count int         `header:"X-Count"`
	Body  ok          `body:"json"`
}

// An output header's document says what a header field value carries, as a
// header parameter's does, for every type whose text could hold more. A
// value the handler sets that a header would not carry as written — net/http
// would send a line break as a space, a client strips whitespace at either
// end — is the handler's defect, a 500, not a value sent altered; one it
// carries arrives as set.
func TestOutputHeadersHoldToWhatAHeaderCarries(t *testing.T) {
	var note string
	var token *carryToken
	tbl := one("/o", get(func(context.Context, *empty) (*carryOut, error) {
		return &carryOut{Note: note, Token: token, Count: 1, Body: ok{true}}, nil
	}))
	app := accepts(t, tbl)
	headers := at(t, doc(t, app), "paths", "/o", "get", "responses", "200", "headers").(map[string]any)
	header := map[string]any{"pattern": headerPattern, "type": "string"}
	for name, want := range map[string]any{
		"X-Note":  header,
		"X-Token": header,
		"X-Day":   map[string]any{"type": "string", "format": "date"},
		"X-Count": map[string]any{"type": "integer", "format": "int64"},
	} {
		if g, w := compact(t, at(t, headers, name, "schema")), compact(t, want); g != w {
			t.Errorf("%s: %s, want %s", name, g, w)
		}
	}
	for _, c := range []struct {
		note  string
		token *carryToken
	}{{"a\r\nb", nil}, {" a", nil}, {"a\t", nil}, {"a\x00", nil}, {"a", &carryToken{"x\n"}}} {
		note, token = c.note, c.token
		r := do(t, app, "GET", "/o")
		if r.Code != http.StatusInternalServerError || r.Header().Get("X-Note") != "" {
			t.Errorf("%q %v: %d %v", c.note, c.token, r.Code, r.Header())
		}
	}
	c := getatest.New(t, tbl)
	for _, v := range []string{"a \t b", "é", ""} {
		note, token = v, &carryToken{`W/"x"`}
		r := c.Get("/o")
		if r.Status != http.StatusOK || r.Header.Get("X-Note") != v || r.Header.Get("X-Token") != `W/"x"` {
			t.Errorf("%q: %d %v", v, r.Status, r.Header)
		}
	}
}
