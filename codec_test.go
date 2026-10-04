package geta_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/koji-1009/geta"
)

// JSON body types: what geta.New accepts and refuses of a type, its members,
// and its embedded fields, as encoding/json/v2 reads and writes them, with
// the text of the rules getavet shares (CheckMemberField, CheckJSONType,
// CheckStructMembers).

type bodyTextA struct{ v string }

func (a bodyTextA) MarshalText() ([]byte, error)  { return []byte(a.v), nil }
func (a *bodyTextA) UnmarshalText(b []byte) error { a.v = string(b); return nil }

type bodyTextB struct{ v string }

func (b bodyTextB) MarshalText() ([]byte, error)  { return []byte(b.v), nil }
func (b *bodyTextB) UnmarshalText(p []byte) error { b.v = string(p); return nil }

type bodyOnlyMarshal struct{ v string }

func (o bodyOnlyMarshal) MarshalText() ([]byte, error) { return []byte(o.v), nil }

type bodyItem struct {
	N int `json:"n"`
}

type bodyHidden struct {
	x int
}

var _ = bodyHidden{}.x

// geta.New refuses a JSON body's type with the text of the shared rules
// (CheckMemberField, CheckJSONType, CheckStructMembers), which getavet
// reports.
func TestBodyRefusalsAreSharedRules(t *testing.T) {
	cases := map[string]struct {
		tbl  geta.Table
		want string
	}{}
	add := func(name string, tbl geta.Table, want string) {
		cases[name] = struct {
			tbl  geta.Table
			want string
		}{tbl, want}
	}
	type embeddedPointer struct {
		*bodyItem
		M int `json:"m"`
	}
	add("embedded pointer", one("/x", get(func(context.Context, *empty) (*embeddedPointer, error) { return nil, nil })),
		"embeddedPointer.bodyItem: embedded pointer types are not supported")
	type omitzero struct {
		N int `json:"n,omitzero"`
	}
	add("omitzero", one("/x", get(func(context.Context, *empty) (*omitzero, error) { return nil, nil })),
		"omitzero.N: omitzero on a non-pointer field")
	type pointer struct {
		P *int `json:"p"`
	}
	add("pointer without omitzero", one("/x", get(func(context.Context, *empty) (*pointer, error) { return nil, nil })),
		"pointer.P is a pointer without omitzero; tag it `json:\"p,omitzero\"`")
	type option struct {
		S string `json:"s,omitempty"`
	}
	add("tag option", one("/x", get(func(context.Context, *empty) (*option, error) { return nil, nil })),
		`option.S: json tag option "omitempty" is not supported`)
	type duplicate struct {
		bodyItem
		B string `json:"n"`
	}
	add("duplicate name", one("/x", get(func(context.Context, *empty) (*duplicate, error) { return nil, nil })),
		`duplicate.B: two fields have the JSON name "n"`)
	type slice struct {
		L []*bodyItem `json:"l"`
	}
	add("pointer slice element", one("/x", get(func(context.Context, *empty) (*slice, error) { return nil, nil })),
		"[]*geta_test.bodyItem: element type *geta_test.bodyItem is a pointer; use geta_test.bodyItem")
	type mapElem struct {
		M map[string]*bodyItem `json:"m"`
	}
	add("pointer map element", one("/x", get(func(context.Context, *empty) (*mapElem, error) { return nil, nil })),
		"map[string]*geta_test.bodyItem: element type *geta_test.bodyItem is a pointer")
	type mapKey struct {
		M map[int]string `json:"m"`
	}
	add("map key", one("/x", get(func(context.Context, *empty) (*mapKey, error) { return nil, nil })),
		"map type map[int]string: only string keys have a JSON form")
	type withFunc struct {
		F func() `json:"f"`
	}
	add("func", one("/x", get(func(context.Context, *empty) (*withFunc, error) { return nil, nil })),
		"type func() has no JSON form geta can derive (func)")
	type withArray struct {
		A [2]int `json:"a"`
	}
	add("array", one("/x", get(func(context.Context, *empty) (*withArray, error) { return nil, nil })),
		"type [2]int has no JSON form geta can derive (array)")
	type oneSided struct {
		O bodyOnlyMarshal `json:"o"`
	}
	add("one-sided text", one("/x", get(func(context.Context, *empty) (*oneSided, error) { return nil, nil })),
		"type geta_test.bodyOnlyMarshal implements encoding.TextMarshaler but not encoding.TextUnmarshaler")
	type noMembers struct {
		H bodyHidden `json:"h"`
	}
	add("no members", one("/x", get(func(context.Context, *empty) (*noMembers, error) { return nil, nil })),
		"geta_test.bodyHidden has fields but no JSON members")
	// Two embedded text types promote neither's methods, so the struct is
	// not a text type; encoding/json/v2 refuses to promote their members.
	type ambiguous struct {
		bodyTextA
		bodyTextB
		N int `json:"n"`
	}
	_, _ = ambiguous{}.bodyTextA, ambiguous{}.bodyTextB // read by geta.New through reflection
	add("embedded text type", one("/x", get(func(context.Context, *empty) (*ambiguous, error) { return nil, nil })),
		"ambiguous.bodyTextA: embedded geta_test.bodyTextA has its own JSON or text methods; give it a json name")
	type pointerParam struct {
		Q **int `query:"q"`
	}
	add("pointer pointer parameter", one("/x", get(func(context.Context, *pointerParam) (*ok, error) { return nil, nil })),
		"pointerParam.Q: type *int has no JSON form geta can derive (ptr)")
	type chanHeader struct {
		H    chan int `header:"X-H"`
		Body ok       `body:"json"`
	}
	add("chan header", one("/x", get(func(context.Context, *empty) (*chanHeader, error) { return nil, nil })),
		"chanHeader.H: type chan int has no JSON form geta can derive (chan)")
	for name, c := range cases {
		t.Run(name, func(t *testing.T) { rejects(t, c.tbl, c.want) })
	}
	t.Run("accepted", testBodySharedRulesAccept)
}

// bodyNotText has a method named as encoding.TextMarshaler's, of another
// signature: it is a struct.
type bodyNotText struct {
	S string `json:"s"`
}

func (bodyNotText) MarshalText() string { return "" }

type bodyRaw struct{ v []byte }

func (r bodyRaw) MarshalJSON() ([]byte, error)  { return r.v, nil }
func (r *bodyRaw) UnmarshalJSON(b []byte) error { r.v = b; return nil }

// What the shared rules accept, and getavet does not report, geta.New
// accepts.
func testBodySharedRulesAccept(t *testing.T) {
	type body struct {
		bodyItem
		hidden bodyHidden
		P      *int              `json:"p,omitzero"`
		L      []bodyItem        `json:"l"`
		M      map[string]string `json:"m"`
		B      []byte            `json:"b"`
		T      bodyTextA         `json:"t"`
		W      time.Time         `json:"w"`
		NT     bodyNotText       `json:"nt"`
		J      bodyRaw           `json:"j"`
		E      bodyShape         `json:"e"`
		E2     bodyShape         `json:"e2,omitzero"`
		Skip   chan int          `json:"-"`
	}
	_ = body{}.hidden
	if _, err := geta.New(one("/x", get(func(context.Context, *empty) (*body, error) { return nil, nil })),
		geta.WithUnion(geta.Sealed[bodyShape]("kind", geta.Case[bodySquare]("square")))); err != nil {
		t.Fatal(err)
	}
}

type bodyShape interface{ shape() }

type bodySquare struct {
	Kind string `json:"kind"`
}

func (bodySquare) shape() {}

type MemberTok string

type memberTok string

type MemberPaging struct {
	N int `json:"n"`
}

type memberItem struct {
	N int `json:"n"`
}

// What encoding/json/v2 refuses at runtime, or writes otherwise than the
// schema says, geta.New refuses with the text CheckMemberField gives.
func TestMemberFieldsAsV2ReadsThem(t *testing.T) {
	type embeddedString struct {
		MemberTok
		N int `json:"n"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*embeddedString, error) { return nil, nil })),
		"embeddedString.MemberTok: embedded geta_test.MemberTok is not a struct; give it a json name")
	type embeddedHidden struct {
		memberTok
		N int `json:"n"`
	}
	_ = embeddedHidden{}.memberTok // read by geta.New through reflection
	rejects(t, one("/x", get(func(context.Context, *empty) (*embeddedHidden, error) { return nil, nil })),
		"embeddedHidden.memberTok: embedded geta_test.memberTok is not a struct")
	// go vet refuses to build a struct type with such a field, so the rule
	// is asked of its description (getavet's fixture declares one).
	_, _, err := geta.CheckMemberField(geta.VetField{Name: "h", Tag: `json:"h"`, Type: "int", Kind: "int"}, map[string]bool{})
	if err == nil || err.Error() != "h is unexported but has a json tag" {
		t.Errorf("unexported tagged field: %v", err)
	}
	if _, _, err := geta.CheckMemberField(geta.VetField{Name: "h", Tag: `json:"-"`, Type: "int"}, map[string]bool{}); err != nil {
		t.Errorf("unexported field tagged -: %v", err)
	}
	type namedPointer struct {
		*MemberPaging `json:"paging"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*namedPointer, error) { return nil, nil })),
		"namedPointer.MemberPaging is a pointer without omitzero; tag it `json:\"paging,omitzero\"`")
	type hiddenPointer struct {
		*memberItem `json:"item,omitzero"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*hiddenPointer, error) { return nil, nil })),
		"hiddenPointer.memberItem: embedded *geta_test.memberItem is unexported; export the type or embed geta_test.memberItem")
	type hiddenString struct {
		memberTok `json:"t"`
	}
	_ = hiddenString{}.memberTok // read by geta.New through reflection
	rejects(t, one("/x", get(func(context.Context, *empty) (*hiddenString, error) { return nil, nil })),
		"hiddenString.memberTok: embedded geta_test.memberTok is unexported and not a struct")
	// Two embedded text types promote neither's methods, so the struct is
	// not a text type, and each is a member.
	type hiddenText struct {
		bodyTextA `json:"a"`
		bodyTextB `json:"b"`
	}
	_, _ = hiddenText{}.bodyTextA, hiddenText{}.bodyTextB // read by geta.New through reflection
	rejects(t, one("/x", get(func(context.Context, *empty) (*hiddenText, error) { return nil, nil })),
		"hiddenText.bodyTextA: embedded geta_test.bodyTextA is unexported and has JSON or text methods")
}

type memberNamed struct {
	*MemberPaging `json:"paging,omitzero"`
	memberItem    `json:"item"`
}

// An embedded field with a json name is one member, as encoding/json/v2
// reads and writes it: a pointer is optional and absent when nil, and an
// unexported struct is a member the closed schema has.
func TestNamedEmbeddedFieldsAreMembers(t *testing.T) {
	var got memberNamed
	r := geta.Route{
		Get: geta.Op(http.StatusOK, func(context.Context, *empty) (*memberNamed, error) {
			return &memberNamed{memberItem: memberItem{7}}, nil
		}, geta.Doc{}),
		Post: geta.Op(http.StatusOK, func(_ context.Context, in *struct {
			Body memberNamed `body:"json"`
		}) (*ok, error) {
			got = in.Body
			return &ok{true}, nil
		}, geta.Doc{}),
	}
	a := accepts(t, one("/x", r))
	rec := do(t, a, http.MethodGet, "/x")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"item":{"n":7}}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	s := at(t, doc(t, a), "components", "schemas", "memberNamed").(map[string]any)
	if compact(t, s["required"]) != `["item"]` {
		t.Errorf("required: %s", compact(t, s["required"]))
	}
	props := s["properties"].(map[string]any)
	if _, ok := props["paging"]; !ok {
		t.Errorf("paging is not documented: %v", props)
	}
	if _, ok := props["item"]; !ok {
		t.Errorf("item is not documented: %v", props)
	}
	for _, body := range []string{`{"item":{"n":4}}`, `{"item":{"n":4},"paging":{"n":2}}`} {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, req)
		if rec.Code != 200 || got.memberItem.N != 4 {
			t.Fatalf("%s: %d %s %+v", body, rec.Code, rec.Body, got)
		}
	}
	if got.MemberPaging == nil || got.MemberPaging.N != 2 {
		t.Fatalf("%+v", got)
	}
}

type genericUser struct {
	N int `json:"n"`
}

type genericBox[T any] struct{ v T }

type genericPair[K comparable, V any] struct {
	k K
	v V
}

// geta.New names an instantiated generic type as reflect does, its type
// arguments qualified by package path; getavet prints the same text
// (getavet's TestGenericTypeNamesMatchGetaNew).
func TestGenericTypeNames(t *testing.T) {
	_ = genericBox[int]{}.v
	_ = genericPair[int, int]{}.k
	_ = genericPair[int, int]{}.v
	rejects(t, one("/x", get(func(context.Context, *empty) (*genericBox[genericUser], error) { return nil, nil })),
		"geta_test.genericBox[github.com/koji-1009/geta_test.genericUser] has fields but no JSON members")
	rejects(t, one("/x", get(func(context.Context, *empty) (*genericPair[string, genericUser], error) { return nil, nil })),
		"geta_test.genericPair[string,github.com/koji-1009/geta_test.genericUser] has fields but no JSON members")
}

// A map key type with one text method only is refused, as a value type with
// one is: v2 would write a key as one name and read it as another.
type oneSidedReadKey string

func (k *oneSidedReadKey) UnmarshalText(b []byte) error {
	*k = oneSidedReadKey(strings.ToUpper(string(b)))
	return nil
}

type oneSidedWriteKey string

func (k *oneSidedWriteKey) MarshalText() ([]byte, error) { return []byte("k-" + string(*k)), nil }

func TestOneSidedMapKeysAreRefused(t *testing.T) {
	read := func(context.Context, *empty) (*map[oneSidedReadKey]int, error) { return nil, nil }
	rejects(t, one("/r", get(read)), "type geta_test.oneSidedReadKey implements encoding.TextUnmarshaler but not encoding.TextMarshaler")
	write := func(context.Context, *empty) (*map[oneSidedWriteKey]int, error) { return nil, nil }
	rejects(t, one("/w", get(write)), "type geta_test.oneSidedWriteKey implements encoding.TextMarshaler but not encoding.TextUnmarshaler")
}
