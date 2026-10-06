package geta_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// keyMail is a FormatType a map key may be: a string kind with text methods.
type keyMail string

func (keyMail) SchemaFormat() string { return "email" }

func (m keyMail) MarshalText() ([]byte, error) { return []byte(m), nil }

func (m *keyMail) UnmarshalText(b []byte) error {
	if !strings.Contains(string(b), "@") {
		return errors.New("no @")
	}
	*m = keyMail(b)
	return nil
}

// keyNoText names a format but is no text type.
type keyNoText string

func (keyNoText) SchemaFormat() string { return "tag" }

// keyOwnJSON names a format but reads itself by JSON methods.
type keyOwnJSON string

func (keyOwnJSON) SchemaFormat() string           { return "tag" }
func (k keyOwnJSON) MarshalJSON() ([]byte, error) { return []byte(`"` + string(k) + `"`), nil }
func (k *keyOwnJSON) UnmarshalJSON(b []byte) error {
	*k = keyOwnJSON(strings.Trim(string(b), `"`))
	return nil
}

type keyFormats struct {
	Mail    map[keyMail]int       `json:"mail"`
	Secrets map[geta.Password]int `json:"secrets"`
	Capped  *map[keyMail]int      `json:"capped,omitzero" schema:"propertyNames.maxLength=64"`
}

type keyFormatsBody struct {
	Body keyFormats `body:"json"`
}

type keyFormatsOut struct {
	Mail map[keyMail]int `json:"mail"`
}

// A map's key type's format is its keys', as a value's format is the value's:
// the document states it in propertyNames, a request's beside the backstop and
// a response's alone, for geta's own format types and a FormatType alike; a
// request's key is held to it as a value of the type is (a FormatType's by its
// UnmarshalText, on both decode paths), and so is a response's (Conforms).
func TestAKeyTypesFormatIsStatedAndHeld(t *testing.T) {
	out := &keyFormatsOut{Mail: map[keyMail]int{"a@b": 1}}
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/b", Route: post(func(context.Context, *keyFormatsBody) (*ok, error) { return &ok{true}, nil })},
		{Path: "/o", Route: get(func(context.Context, *empty) (*keyFormatsOut, error) { return out, nil })},
	}})
	d := doc(t, c.App())
	props := at(t, d, "components", "schemas", "keyFormats", "properties")
	for name, want := range map[string]string{
		"mail":    `{"format":"email","maxLength":4096}`,
		"secrets": `{"format":"password","maxLength":4096}`,
		"capped":  `{"format":"email","maxLength":64}`,
	} {
		if g := compact(t, at(t, props, name, "propertyNames")); g != want {
			t.Errorf("%s: %s, want %s", name, g, want)
		}
	}
	if g := compact(t, at(t, d, "components", "schemas", "keyFormatsOut", "properties", "mail", "propertyNames")); g != `{"format":"email"}` {
		t.Errorf("response: %s", g)
	}
	if res := c.Post("/b", `{"mail":{"a@b":1},"secrets":{"x":1},"capped":{"c@d":2}}`); res.Status != 200 {
		t.Fatal(res.Status, res.Text())
	}
	same(t, violations(t, c.Post("/b", `{"mail":{"nope":1,"a@b":"x"},"secrets":{}}`)),
		"body $.mail.a@b: expected integer, got string",
		`body $.mail.nope: key "nope" is not a valid email`)
	same(t, violations(t, c.Post("/b", `{"mail":{"nope":-1},"secrets":{}}`)), `body $.mail.nope: key "nope" is not a valid email`)
	c.Get("/o")
	if err := c.App().Conforms(geta.Match{Template: "/o", Method: "GET"}, http.StatusOK, nil, []byte(`{"mail":{"nope":1}}`)); err == nil ||
		!strings.Contains(err.Error(), `key "nope" is not a valid email`) {
		t.Errorf("Conforms: %v", err)
	}
}

type keyFormatOnFormatType struct {
	M map[keyMail]int `json:"m" schema:"propertyNames.format=uuid"`
}

type keyFormatOnPassword struct {
	M map[geta.Password]int `json:"m" schema:"propertyNames.format=date"`
}

type keyNotText struct {
	M map[keyNoText]int `json:"m"`
}

type keyJSONFormat struct {
	M map[keyOwnJSON]int `json:"m"`
}

// A key type is judged as a value of its type is: a FormatType key that is no
// text type, or reads itself by JSON methods, is refused, and so is a
// propertyNames.format on a key type that carries a format, as format= on
// such a value is; CheckSchemaTag, which getavet applies, names such a key's
// kind in the map's ("map[format]int", "map[geta.Password]int").
func TestAKeyTypeIsJudgedAsAValueOfItsType(t *testing.T) {
	refused := func(err error, want string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v; want %q", err, want)
		}
	}
	refused(sizedRoute[keyFormatOnFormatType](false),
		"keyFormatOnFormatType.M: propertyNames: schema keyword format: the type has its own format")
	refused(sizedRoute[keyFormatOnPassword](true), `keyFormatOnPassword.M: propertyNames: schema keyword format: the type already has format "password"`)
	refused(sizedRoute[keyNotText](false), "type geta_test.keyNoText has a SchemaFormat method but is not a text type")
	refused(sizedRoute[keyJSONFormat](false), "type geta_test.keyOwnJSON has both a SchemaFormat method and its own JSON methods")
	for kind, want := range map[string]string{
		"map[format]int":           "the type has its own format",
		"map[format]":              "the type has its own format",
		"map[geta.Password]int":    `the type already has format "password"`,
		"[]map[geta.Password]?int": "",
	} {
		tag := "propertyNames.format=uuid"
		if want == "" {
			tag = "items.propertyNames.minLength=1"
			if err := geta.CheckSchemaTag(tag, kind); err != nil {
				t.Errorf("CheckSchemaTag(%q, %q): %v", tag, kind, err)
			}
			continue
		}
		refused(geta.CheckSchemaTag(tag, kind), want)
	}
	refused(geta.CheckSchemaTag("propertyNames.maxLength=1", "map[int]int"), `kind "map[int]int" has a non-string key`)
	if err := geta.CheckSchemaTag("propertyNames.format=uuid", "map[string]int"); err != nil {
		t.Error(err)
	}
}
