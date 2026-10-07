package geta_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	mathbig "math/big"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// cents is money with its own JSON: a quoted decimal with two places, as a
// decimal library writes it.
type cents int64

func (c cents) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`"%d.%02d"`, c/100, c%100)), nil
}

func (c *cents) UnmarshalJSON(b []byte) error {
	s, err := strconv.Unquote(string(b))
	if err != nil {
		return err
	}
	whole, frac, ok := strings.Cut(s, ".")
	if !ok || len(frac) != 2 {
		return errors.New("want two decimal places")
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return err
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return err
	}
	*c = cents(w*100 + f)
	return nil
}

type account struct {
	Balance cents        `json:"balance"`
	Big     *mathbig.Int `json:"big,omitzero"`
}

type accountIn struct {
	Body account `body:"json"`
}

var moneySchemas = []geta.Option{
	geta.WithSchema[cents]("string", `pattern=^-?[0-9]+\.[0-9]+$`),
	geta.WithSchema[mathbig.Int]("integer", ""),
}

type taggedCents struct {
	Amount cents `json:"amount" schema:"minLength=1"`
}

// A schema keyword on a type with its own JSON methods is refused, naming
// what the type is and how to state its schema.
func TestKeywordOnOwnJSONTypeNamesTheType(t *testing.T) {
	rejects(t, geta.Table{Routes: []geta.Entry{{Path: "/x", Route: geta.Route{
		Get: geta.Op(http.StatusOK, func(ctx context.Context, _ *struct{}) (*taggedCents, error) { return nil, nil }, geta.Doc{}),
	}}}}, "not a type with its own JSON methods; use geta.WithSchema")
}

// A type with its own JSON methods is read and written by them; its
// declared schema is documented and checked.
func TestOwnJSONTypesUseTheirMethods(t *testing.T) {
	var got account
	h := func(ctx context.Context, in *accountIn) (*account, error) {
		got = in.Body
		return &in.Body, nil
	}
	c := getatest.New(t, one("/x", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}), moneySchemas...)
	res := c.Post("/x", `{"balance":"12.34","big":123456789012345678901234567890}`)
	if res.Status != 200 || got.Balance != 1234 || got.Big.String() != "123456789012345678901234567890" {
		t.Fatalf("%d %s %+v", res.Status, res.Body, got)
	}
	if res.Text() != `{"balance":"12.34","big":123456789012345678901234567890}` {
		t.Fatal(res.Text())
	}
	m := doc(t, c.App())
	// account is read and written: the request's schema states the
	// backstops, and is account-Input; the response's states the declaration.
	if got := compact(t, at(t, m, "components", "schemas", "account-Input", "properties")); got !=
		`{"balance":{"maxLength":4096,"pattern":"^-?[0-9]+\\.[0-9]+$","type":"string"},"big":{"type":"integer"}}` {
		t.Fatal(got)
	}
	if got := compact(t, at(t, m, "components", "schemas", "account", "properties")); got !=
		`{"balance":{"pattern":"^-?[0-9]+\\.[0-9]+$","type":"string"},"big":{"type":"integer"}}` {
		t.Fatal(got)
	}

	// The declared schema refuses first; the type's own method refuses the rest.
	for body, want := range map[string]string{
		`{"balance":"abc"}`:            "does not match pattern",
		`{"balance":12}`:               "expected string, got integer",
		`{"balance":"1.234"}`:          "want two decimal places",
		`{"balance":"1.00","big":1.5}`: "expected integer, got number",
	} {
		res := c.Post("/x", body)
		if res.Status != 400 || !strings.Contains(res.Text(), want) {
			t.Errorf("%s: %d %s", body, res.Status, res.Body)
		}
	}
}

// Undeclared, such a type's schema is what its own methods read and write:
// documented as {}, valid on input when its UnmarshalJSON takes the value,
// valid on output when its MarshalJSON writes JSON.
func TestOwnJSONTypesNeedNoDeclaration(t *testing.T) {
	h := func(ctx context.Context, in *accountIn) (*account, error) { return &in.Body, nil }
	c := getatest.New(t, one("/x", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}))
	body := `{"balance":"12.34","big":123456789012345678901234567890}`
	if res := c.Post("/x", body); res.Status != 200 || res.Text() != body {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
	if got := compact(t, at(t, doc(t, c.App()), "components", "schemas", "account", "properties")); got != `{"balance":{},"big":{}}` {
		t.Fatal(got)
	}
	for body, want := range map[string]string{
		`{"balance":12}`:               `"path":"$.balance","message":"json: `,
		`{"balance":"1.234"}`:          "want two decimal places",
		`{"balance":"1.00","big":"x"}`: `"path":"$.big","message":"json: `,
		`{"balance":"1.00","extra":1}`: "unknown member",
		`{"big":1}`:                    "missing required member",
	} {
		res := c.Post("/x", body)
		if res.Status != 400 || !strings.Contains(res.Text(), want) {
			t.Errorf("%s: %d %s", body, res.Status, res.Body)
		}
	}
}

// broken writes something that is not JSON.
type broken struct{}

func (broken) MarshalJSON() ([]byte, error) { return []byte(`{oops`), nil }

type brokenOut struct {
	V broken `json:"v"`
}

func TestOwnJSONOutputThatIsNotJSONIsADefect(t *testing.T) {
	h := func(context.Context, *empty) (*brokenOut, error) { return &brokenOut{}, nil }
	if res := getatest.New(t, one("/x", get(h))).Get("/x"); res.Status != 500 || strings.Contains(res.Text(), "oops") {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
}

func TestWithSchemaMistakes(t *testing.T) {
	tbl := one("/x", geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *accountIn) (*account, error) { return nil, nil }, geta.Doc{})})
	for name, c := range map[string]struct {
		opts []geta.Option
		want string
	}{
		"not own JSON":      {append(moneySchemas, geta.WithSchema[item]("object", "")), "does not write its own JSON"},
		"unknown JSON type": {[]geta.Option{geta.WithSchema[cents]("decimal", ""), moneySchemas[1]}, `JSON type "decimal" is not one of`},
		"bad constraint":    {[]geta.Option{geta.WithSchema[cents]("string", "minimum=1"), moneySchemas[1]}, "minimum applies to integer or number, not string"},
		"declared twice":    {append(moneySchemas, moneySchemas[0]), "declared twice"},
	} {
		_, err := geta.New(tbl, c.opts...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// liar declares a string but writes a number.
type liar struct{}

func (liar) MarshalJSON() ([]byte, error) { return []byte(`7`), nil }

type liarOut struct {
	V liar `json:"v"`
}

// A type whose output leaves its declaration fails the getatest that
// provoked it.
func TestGetatestChecksBodiesAgainstTheDocument(t *testing.T) {
	rec := &recorder{TB: t}
	h := func(context.Context, *empty) (*liarOut, error) { return &liarOut{}, nil }
	c := getatest.New(rec, one("/x", get(h)), geta.WithSchema[liar]("string", ""))
	c.Get("/x")
	if len(rec.errs) != 1 || !strings.Contains(rec.errs[0], "$.v: expected string, got integer") {
		t.Fatalf("%q", rec.errs)
	}
}

// ownApp writes itself with AppendText alone.
type ownApp struct {
	V string `json:"v"`
}

func (a ownApp) AppendText(b []byte) ([]byte, error) { return append(b, "app:"+a.V...), nil }

// ownAppText writes itself with AppendText and reads with UnmarshalText.
type ownAppText struct {
	V string `json:"v"`
}

func (a ownAppText) AppendText(b []byte) ([]byte, error) { return append(b, a.V...), nil }
func (a *ownAppText) UnmarshalText(b []byte) error       { a.V = string(b); return nil }

// ownBoth has MarshalText and AppendText; encoding/json/v2 calls
// AppendText.
type ownBoth struct{ v string }

func (ownBoth) MarshalText() ([]byte, error)        { return []byte("marshal"), nil }
func (ownBoth) AppendText(b []byte) ([]byte, error) { return append(b, "append"...), nil }
func (x *ownBoth) UnmarshalText(b []byte) error     { x.v = string(b); return nil }

// ownOnlyUnmarshal reads text but writes its fields.
type ownOnlyUnmarshal struct {
	V string `json:"v"`
}

func (x *ownOnlyUnmarshal) UnmarshalText(b []byte) error { x.V = string(b); return nil }

// ownJSONText writes JSON and reads text.
type ownJSONText struct{ v string }

func (x ownJSONText) MarshalJSON() ([]byte, error)  { return []byte(`{"v":1}`), nil }
func (x *ownJSONText) UnmarshalText(b []byte) error { x.v = string(b); return nil }

// ownAppJSON writes text and reads JSON.
type ownAppJSON struct{ v string }

func (x ownAppJSON) AppendText(b []byte) ([]byte, error) { return append(b, x.v...), nil }
func (x *ownAppJSON) UnmarshalJSON(b []byte) error       { x.v = string(b); return nil }

// ownTo writes JSON through MarshalJSONTo alone.
type ownTo struct {
	V string `json:"v"`
}

func (x ownTo) MarshalJSONTo(e *jsontext.Encoder) error { return e.WriteToken(jsontext.String(x.V)) }

// ownFrom reads JSON through UnmarshalJSONFrom alone.
type ownFrom struct {
	V string `json:"v"`
}

func (x *ownFrom) UnmarshalJSONFrom(d *jsontext.Decoder) error {
	return json.UnmarshalDecode(d, &x.V)
}

// Each method encoding/json/v2 calls for a type's text or JSON decides its
// kind as v2 decides it: AppendText writes text as MarshalText does, a JSON
// method on either side makes the type's JSON its own, and a type that
// writes text but reads otherwise, or the reverse, is refused.
func TestTextAndJSONMethodsAsV2CallsThem(t *testing.T) {
	schemaOf := func(t *testing.T, tbl geta.Table) string {
		t.Helper()
		a := accepts(t, tbl)
		return compact(t, at(t, doc(t, a), "paths", "/x", "get", "responses", "200", "content", "application/json", "schema"))
	}
	for name, c := range map[string]struct {
		tbl  geta.Table
		want string
	}{
		"AppendText and UnmarshalText":  {one("/x", get(func(context.Context, *empty) (*ownAppText, error) { return nil, nil })), `{"type":"string"}`},
		"all three text methods":        {one("/x", get(func(context.Context, *empty) (*ownBoth, error) { return nil, nil })), `{"type":"string"}`},
		"MarshalJSON and UnmarshalText": {one("/x", get(func(context.Context, *empty) (*ownJSONText, error) { return nil, nil })), `{}`},
		"AppendText and UnmarshalJSON":  {one("/x", get(func(context.Context, *empty) (*ownAppJSON, error) { return nil, nil })), `{}`},
		"MarshalJSONTo alone":           {one("/x", get(func(context.Context, *empty) (*ownTo, error) { return nil, nil })), `{}`},
		"UnmarshalJSONFrom alone":       {one("/x", get(func(context.Context, *empty) (*ownFrom, error) { return nil, nil })), `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			if got := schemaOf(t, c.tbl); got != c.want {
				t.Fatalf("schema %s, want %s", got, c.want)
			}
		})
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*ownApp, error) { return nil, nil })),
		"type geta_test.ownApp implements encoding.TextMarshaler but not encoding.TextUnmarshaler")
	rejects(t, one("/x", get(func(context.Context, *empty) (*ownOnlyUnmarshal, error) { return nil, nil })),
		"type geta_test.ownOnlyUnmarshal implements encoding.TextUnmarshaler but not encoding.TextMarshaler")
}

type ownTextEnv struct {
	H    ownBoth    `header:"X-B"`
	A    ownAppText `header:"X-A"`
	Body ownBoth    `body:"json"`
}

// A text type is written as encoding/json/v2 writes it, by AppendText when
// it has one, in a header as in a body, and read as a parameter.
func TestAppendTextWritesHeadersAndBodies(t *testing.T) {
	r := geta.Route{
		Get: geta.Op(http.StatusOK, func(context.Context, *empty) (*ownTextEnv, error) {
			return &ownTextEnv{A: ownAppText{"a1"}}, nil
		}, geta.Doc{}),
	}
	a := accepts(t, one("/x", r))
	rec := do(t, a, http.MethodGet, "/x")
	if rec.Code != 200 || rec.Header().Get("X-B") != "append" || rec.Header().Get("X-A") != "a1" ||
		strings.TrimSpace(rec.Body.String()) != `"append"` {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	type in struct {
		Q ownAppText `query:"q"`
	}
	a = accepts(t, one("/y", get(func(_ context.Context, in *in) (*ownAppText, error) { return &in.Q, nil })))
	rec = do(t, a, http.MethodGet, "/y?q=hello")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `"hello"` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
