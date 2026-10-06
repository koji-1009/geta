package geta_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaclient"
	"github.com/koji-1009/geta/getatest"
	"github.com/koji-1009/geta/internal/vet"
)

type nullFriend struct {
	Name string `json:"name"`
}

// nullPatch is a JSON Merge Patch's shape: an optional Nullable is absent
// (keep), null (clear), or a value (set); a required one is null or a value.
type nullPatch struct {
	Nickname geta.Nullable[string]     `json:"nickname,omitzero" schema:"maxLength=5"`
	Age      geta.Nullable[int8]       `json:"age"`
	Friend   geta.Nullable[nullFriend] `json:"friend,omitzero"`
	Tags     *[]geta.Nullable[string]  `json:"tags,omitzero" schema:"maxItems=3"`
	Level    geta.Nullable[string]     `json:"level,omitzero" schema:"enum=low|high"`
}

type nullPatchIn struct {
	Body nullPatch `body:"json"`
}

// nullSeen says what the request held of each member.
type nullSeen struct {
	Nickname string    `json:"nickname"`
	Age      string    `json:"age"`
	Friend   string    `json:"friend"`
	Echo     nullPatch `json:"echo"`
}

func state[T any](n geta.Nullable[T]) string {
	if v, ok := n.Get(); ok {
		if f, ok := any(v).(nullFriend); ok {
			return "value:name:" + f.Name
		}
		return fmt.Sprint("value:", v)
	}
	if n.IsNull() {
		return "null"
	}
	return "absent"
}

func nullTable() geta.Table {
	h := func(_ context.Context, in *nullPatchIn) (*nullSeen, error) {
		b := in.Body
		return &nullSeen{Nickname: state(b.Nickname), Age: state(b.Age), Friend: state(b.Friend), Echo: b}, nil
	}
	return one("/users/1", geta.Route{Patch: geta.Op(http.StatusOK, h, geta.Doc{})})
}

// A Nullable tells absent, null, and a value apart, on the single pass and
// the reference path alike, and writes each back as it is: absent omitted
// when optional, null as null, a value as itself.
func TestNullableTellsAbsentFromNull(t *testing.T) {
	c := getatest.New(t, nullTable())
	for _, tc := range []struct {
		body, nickname, age, friend, echo string
	}{
		{`{"age":1}`, "absent", "value:1", "absent", `{"age":1}`},
		{`{"age":null,"nickname":null,"friend":null}`, "null", "null", "null", `{"nickname":null,"age":null,"friend":null}`},
		{`{"nickname":"bob","age":2,"friend":{"name":"al"}}`, "value:bob", "value:2", "value:name:al", `{"nickname":"bob","age":2,"friend":{"name":"al"}}`},
		{`{"age":3,"tags":["a",null]}`, "absent", "value:3", "absent", `{"age":3,"tags":["a",null]}`},
		{`{"friend":{"name":"x"},"age":null,"level":null}`, "absent", "null", "value:name:x", `{"age":null,"friend":{"name":"x"},"level":null}`},
	} {
		res := c.Patch("/users/1", tc.body)
		if res.Status != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.body, res.Status, res.Text())
		}
		got := res.JSON[nullSeen]()
		if got.Nickname != tc.nickname || got.Age != tc.age || got.Friend != tc.friend {
			t.Errorf("%s: %+v", tc.body, got)
		}
		_, echo, _ := strings.Cut(res.Text(), `"echo":`)
		if echo = strings.TrimSuffix(echo, "}"); echo != tc.echo {
			t.Errorf("%s: echoed %s, want %s", tc.body, echo, tc.echo)
		}
	}
	for body, want := range map[string]string{
		`{}`:                                  "$.age: missing required member",
		`{"age":1,"nickname":"toolong"}`:      "$.nickname: string length 7 exceeds maxLength 5",
		`{"age":1,"nickname":5}`:              "$.nickname: expected string, got integer",
		`{"age":300}`:                         "$.age: 300 is out of range for int8",
		`{"age":1,"friend":{}}`:               "$.friend.name: missing required member",
		`{"age":1,"level":"mid"}`:             `$.level: "mid" is not one of low, high`,
		`{"age":1,"tags":[null,null,1]}`:      "$.tags[2]: expected string, got integer",
		`{"age":1,"tags":["a","b","c",null]}`: "$.tags: array length 4 exceeds maxItems 3",
	} {
		res := c.Patch("/users/1", body)
		p := res.Problem()
		if res.Status != http.StatusBadRequest || len(p.Errors) == 0 || p.Errors[0].Path+": "+p.Errors[0].Message != want {
			t.Errorf("%s: %d %+v; want %s", body, res.Status, p.Errors, want)
		}
	}

	// The typed client sends each state as the server reads it.
	out, err := getaclient.Call[nullPatchIn, nullSeen](t.Context(), c.Typed(), http.MethodPatch, "/users/1",
		&nullPatchIn{Body: nullPatch{Nickname: geta.Null[string](), Age: geta.NotNull[int8](7)}})
	if err != nil || out.Nickname != "null" || out.Age != "value:7" || out.Friend != "absent" {
		t.Fatal(out, err)
	}
}

// A Nullable is documented as its value's schema with null admitted: type
// [T, "null"] (an enum lists null too), or anyOf a component and null; the
// keywords of its tag constrain the value.
func TestNullableIsDocumentedAsTypeOrNull(t *testing.T) {
	a := accepts(t, nullTable())
	m := doc(t, a)
	props := at(t, m, "components", "schemas", "nullPatch-Input", "properties").(map[string]any)
	for name, want := range map[string]string{
		"nickname": `{"maxLength":5,"type":["string","null"]}`,
		"age":      `{"maximum":127,"minimum":-128,"type":["integer","null"]}`,
		"friend":   `{"anyOf":[{"$ref":"#/components/schemas/nullFriend-Input"},{"type":"null"}]}`,
		"tags":     `{"items":{"maxLength":4096,"type":["string","null"]},"maxItems":3,"type":"array"}`,
		"level":    `{"enum":["low","high",null],"type":["string","null"]}`,
	} {
		if got := compact(t, props[name]); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
	if got := compact(t, at(t, m, "components", "schemas", "nullPatch-Input", "required")); got != `["age"]` {
		t.Fatal(got)
	}
	// The response's schema states no backstop.
	if got := compact(t, at(t, m, "components", "schemas", "nullPatch", "properties", "tags", "items")); got != `{"type":["string","null"]}` {
		t.Fatal(got)
	}
}

type nullShapes struct {
	Main geta.Nullable[shape] `json:"main"`
	Opt  geta.Nullable[shape] `json:"opt,omitzero"`
}

type nullShapesIn struct {
	Body nullShapes `body:"json"`
}

// A Nullable of a sealed type reads null or a variant, and writes them back;
// a value that holds no variant is the 500 a required sealed value left nil
// is.
func TestNullableSealedType(t *testing.T) {
	var out *nullShapes
	h := func(_ context.Context, in *nullShapesIn) (*nullShapes, error) {
		if out != nil {
			return out, nil
		}
		return &in.Body, nil
	}
	c := getatest.New(t, one("/s", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}), geta.WithUnion(shapes))
	for _, body := range []string{
		`{"main":null}`,
		`{"main":{"kind":"circle","radius":1},"opt":null}`,
		`{"opt":{"side":2,"kind":"square"},"main":{"kind":"square","side":1,"inner":{"kind":"circle","radius":2}}}`,
	} {
		res := c.Post("/s", body)
		if res.Status != http.StatusOK {
			t.Fatalf("%s: %d %s", body, res.Status, res.Text())
		}
	}
	if res := c.Post("/s", `{"main":{"kind":"hexagon"}}`); res.Status != http.StatusBadRequest {
		t.Fatal(res.Status, res.Text())
	}
	res := c.Post("/s", `{"main":null,"opt":{"kind":"circle","radius":3}}`)
	if res.Text() != `{"main":null,"opt":{"kind":"circle","radius":3}}` {
		t.Fatal(res.Text())
	}
	// A value holding no variant.
	out = &nullShapes{Main: geta.NotNull[shape](nil)}
	if res := c.Post("/s", `{"main":null}`); res.Status != http.StatusInternalServerError {
		t.Fatal(res.Status, res.Text())
	}
	out = &nullShapes{Main: geta.NotNull[shape](square{Kind: "square"}), Opt: geta.Null[shape]()}
	if res := c.Post("/s", `{"main":null}`); res.Status != http.StatusOK || res.Text() != `{"main":{"kind":"square","side":0},"opt":null}` {
		t.Fatal(res.Status, res.Text())
	}
}

// What a Nullable cannot say is refused: null and absent made one by a
// pointer, a Nullable of a Nullable, and null where no null is carried (a
// parameter, a form field, a header).
func TestNullableMistakesAreRefused(t *testing.T) {
	type pointerMember struct {
		N *geta.Nullable[int] `json:"n,omitzero"`
	}
	type pointerIn struct {
		Body pointerMember `body:"json"`
	}
	rejects(t, one("/x", geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *pointerIn) (*ok, error) { return nil, nil }, geta.Doc{})}),
		"N is a pointer to geta.Nullable", "use geta.Nullable[int] tagged `json:\"n,omitzero\"`")
	type nested struct {
		N geta.Nullable[geta.Nullable[int]] `json:"n"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*nested, error) { return nil, nil })),
		"a geta.Nullable of a geta.Nullable")
	type param struct {
		N geta.Nullable[int] `query:"n"`
	}
	rejects(t, one("/x", get(func(context.Context, *param) (*ok, error) { return nil, nil })),
		`query parameter "n" has unsupported type geta.Nullable[int]`)
	type tagged struct {
		F geta.Nullable[nullFriend] `json:"f" schema:"maxLength=3"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*tagged, error) { return nil, nil })),
		"schema tag", "on a struct type")
	if err := vet.CheckSchemaTag("maxLength=3,enum=a|b", "?string"); err != nil {
		t.Error(err)
	}
	if err := vet.CheckSchemaTag("maxLength=3", "?int"); err == nil {
		t.Error("maxLength on a Nullable[int] passed")
	}
}
