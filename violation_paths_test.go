package geta_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// foldKey is a map key read by its own UnmarshalJSON, case folded: "A" and
// "a" are one key, which encoding/json/v2 refuses as a duplicate.
type foldKey string

func (k foldKey) MarshalJSON() ([]byte, error) { return json.Marshal(string(k)) }

func (k *foldKey) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*k = foldKey(strings.ToLower(s))
	return nil
}

type pathShape interface{ isPathShape() }

type pathDot struct {
	Kind string          `json:"kind"`
	M    map[foldKey]int `json:"m"`
}

func (pathDot) isPathShape() {}

var pathShapes = geta.Sealed[pathShape]("kind", geta.Case[pathDot]("dot"))

type pathBody struct {
	S []pathShape          `json:"s"`
	F map[foldKey]int      `json:"f"`
	N map[string][]foldKey `json:"n"`
}

type pathBodyIn struct {
	Body pathBody `body:"json"`
}

func pathClient(t *testing.T) *getatest.Client {
	t.Helper()
	h := func(context.Context, *pathBodyIn) (*empty, error) { return &empty{}, nil }
	return getatest.New(t, one("/x", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}), geta.WithUnion(pathShapes))
}

// A refusal encoding/json/v2 makes inside a sealed value, other than a
// method's, is at its path in the body, not in the sealed value.
func TestARefusalInsideASealedValueIsAtItsBodyPath(t *testing.T) {
	c := pathClient(t)
	res := c.Post("/x", `{"s":[{"kind":"dot","m":{}},{"kind":"dot","m":{"A":1,"a":2}}],"f":{},"n":{}}`)
	if got := strings.Join(paths(t, res), " "); got != "$.s[1].m.a" {
		t.Fatalf("%s, want $.s[1].m.a: %s", got, res.Body)
	}
	if msg := res.Problem().Errors[0].Message; !strings.Contains(msg, `within "/s/1/m"`) {
		t.Fatal(msg)
	}
}

// A sealed type's JSONOptions place an error inside a sealed value where
// encoding/json/v2 places any other: at its offset and pointer in the input.
func TestSealedJSONOptionsPlaceErrorsInTheInput(t *testing.T) {
	for _, opts := range []json.Options{pathShapes.JSONOptions(), json.JoinOptions(pathShapes.JSONOptions(), jsontext.AllowDuplicateNames(true))} {
		var v []pathShape
		data := `[{"kind":"dot","m":{}}, {"kind":"dot","m":{"x":"no"}}]`
		err := json.Unmarshal([]byte(data), &v, opts)
		var se *json.SemanticError
		if !errors.As(err, &se) || se.JSONPointer != "/1/m/x" || se.ByteOffset != int64(strings.Index(data, `"no"`)) {
			t.Fatalf("%v", err)
		}
	}
}

// A member is named as RFC 9535 (JSONPath) names it: .name where the name is
// a member-name-shorthand (§2.5.1.1), and otherwise ['name'], escaped as in a
// Normalized Path (§2.7), so that a name of digits, or one holding a dot or a
// bracket, is told from an element and from two members. The name reads the
// same wherever the violation comes from: the schema check, encoding/json/v2,
// a duplicate key, or a method.
func TestAMemberIsNamedAsJSONPathNamesIt(t *testing.T) {
	c := pathClient(t)
	for body, want := range map[string]string{
		// encoding/json/v2: two names the key's method reads as one.
		`{"s":[],"f":{"0":1,"-1":2,"A":3,"a":4},"n":{}}`: "$.f.a",
		`{"s":[],"f":{"X.Y":1,"x.y":2},"n":{}}`:          "$.f['x.y']",
		// A duplicate key, refused by the parse.
		`{"s":[],"f":{},"n":{"0":[],"0":[]}}`:     "$.n['0']",
		`{"s":[],"f":{},"n":{"a.b":[],"a.b":[]}}`: "$.n['a.b']",
		// The schema check.
		`{"s":[],"f":{},"n":{"7":["a",true]}}`:      "$.n['7'][1]",
		`{"s":[],"f":{},"n":{"a.b":["a",true]}}`:    "$.n['a.b'][1]",
		`{"s":[],"f":{},"n":{"a[0]":["a",true]}}`:   "$.n['a[0]'][1]",
		`{"s":[],"f":{},"n":{"it's":["a",true]}}`:   `$.n['it\'s'][1]`,
		`{"s":[],"f":{},"n":{"a\\b":["a",true]}}`:   `$.n['a\\b'][1]`,
		`{"s":[],"f":{},"n":{"a\nb":["a",true]}}`:   `$.n['a\nb'][1]`,
		`{"s":[],"f":{},"n":{"\u0001":["a",true]}}`: `$.n['\u0001'][1]`,
		`{"s":[],"f":{},"n":{"":["a",true]}}`:       "$.n[''][1]",
		`{"s":[],"f":{},"n":{"é_1":["a",true]}}`:    "$.n.é_1[1]",
		`{"s":[],"f":{},"n":{},"a.b":1}`:            "$['a.b']",
		// A method's refusal of an element under such a member.
		`{"s":[],"f":{},"n":{"7":[1]}}`:   "$.n['7'][0]",
		`{"s":[],"f":{},"n":{"a.b":[1]}}`: "$.n['a.b'][0]",
	} {
		if got := strings.Join(paths(t, c.Post("/x", body)), " "); got != want {
			t.Errorf("%s: %s, want %s", body, got, want)
		}
	}
	// A method's refusal under a digit member.
	h := func(context.Context, *ownEventIn) (*empty, error) { return &empty{}, nil }
	oc := getatest.New(t, one("/x", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}),
		geta.WithSchema[cents]("string", `pattern=^-?[0-9]+\.[0-9]+$`))
	if got := strings.Join(paths(t, oc.Post("/x", `{"type":"x","data":1,"map":{"0":null,"-1":null}}`)), " "); got != "$.map['-1'] $.map['0']" && got != "$.map['0'] $.map['-1']" {
		t.Errorf("method refusals at %s", got)
	}
	// A form's field, named as a body's member.
	type formBody struct {
		Name string `form:"name"`
	}
	type formIn struct {
		Body formBody `body:"form"`
	}
	fh := func(context.Context, *formIn) (*empty, error) { return &empty{}, nil }
	fc := getatest.New(t, one("/f", geta.Route{Post: geta.Op(http.StatusOK, fh, geta.Doc{})}))
	res := fc.With("Content-Type", "application/x-www-form-urlencoded").Post("/f", "name=a&a.b=1&c=2")
	if got := strings.Join(paths(t, res), " "); got != "$['a.b'] $.c" {
		t.Errorf("form fields at %s", got)
	}
}
