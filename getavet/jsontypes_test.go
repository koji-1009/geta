package getavet

import (
	"go/types"
	"reflect"
	"strings"
	"testing"
)

// kindName spells each kind of type as reflect.Kind does, an interface's
// included: jsonType never hands it one (it judges no interface), and the
// spelling is reflect's all the same.
func TestKindNameIsReflects(t *testing.T) {
	str := types.Typ[types.String]
	for _, c := range []struct {
		t    types.Type
		want reflect.Kind
	}{
		{str, reflect.String},
		{types.Typ[types.UnsafePointer], reflect.UnsafePointer},
		{types.NewPointer(str), reflect.Pointer},
		{types.NewSlice(str), reflect.Slice},
		{types.NewArray(str, 1), reflect.Array},
		{types.NewMap(str, str), reflect.Map},
		{types.NewChan(types.SendRecv, str), reflect.Chan},
		{types.NewSignatureType(nil, nil, nil, nil, nil, false), reflect.Func},
		{types.NewStruct(nil, nil), reflect.Struct},
		{types.NewInterfaceType(nil, nil), reflect.Interface},
	} {
		if got := kindName(c.t); got != c.want.String() {
			t.Errorf("%s: %q, want %q", c.t, got, c.want)
		}
	}
}

// Every refusal geta.New makes of a JSON body's type, an embedded field, or
// an input's cookie name is reported, with geta.New's text; what geta.New
// accepts, sealed types declared with geta.WithUnion included, is not.
func TestTypeAndEmbeddedRefusals(t *testing.T) {
	out, err := vetModule(t, map[string]string{
		"lib/lib.go": `package lib

import (
	"context"
	"net/http"
	"time"

	"github.com/koji-1009/geta"
)

type Item struct {
	N int ` + "`json:\"n\"`" + `
}

type Hidden struct {
	x int
}

var _ = Hidden{}.x

type OnlyMarshal struct{ v string }

func (o OnlyMarshal) MarshalText() ([]byte, error) { return []byte(o.v), nil }

type TextA struct{ v string }

func (a TextA) MarshalText() ([]byte, error)  { return []byte(a.v), nil }
func (a *TextA) UnmarshalText(b []byte) error { a.v = string(b); return nil }

type TextB struct{ v string }

func (b TextB) MarshalText() ([]byte, error)  { return []byte(b.v), nil }
func (b *TextB) UnmarshalText(p []byte) error { b.v = string(p); return nil }

// Wrong signatures: neither a text nor a JSON type.
type NotText struct {
	S string ` + "`json:\"s\"`" + `
}

func (NotText) MarshalText() string      { return "" }
func (NotText) MarshalJSON(int) []byte   { return nil }

type Raw struct{ v []byte }

func (r Raw) MarshalJSON() ([]byte, error)  { return r.v, nil }
func (r *Raw) UnmarshalJSON(b []byte) error { r.v = b; return nil }

type Event interface{ event() }

type Created struct {
	Kind string ` + "`json:\"kind\"`" + `
}

func (Created) event() {}

type Ambiguous struct {
	TextA
	TextB
	N int ` + "`json:\"n\"`" + `
}

type Body struct {
	*Item
	Z  int               ` + "`json:\"z,omitzero\"`" + `
	P  *int              ` + "`json:\"p\"`" + `
	O  string            ` + "`json:\"o,omitempty\"`" + `
	A  string            ` + "`json:\"a\"`" + `
	A2 string            ` + "`json:\"a\"`" + `
	L  []*Item           ` + "`json:\"l\"`" + `
	M  map[string]*Item  ` + "`json:\"m\"`" + `
	K  map[int]string    ` + "`json:\"k\"`" + `
	C  chan int          ` + "`json:\"c\"`" + `
	F  func()            ` + "`json:\"f\"`" + `
	R  [2]int            ` + "`json:\"r\"`" + `
	OM OnlyMarshal       ` + "`json:\"om\"`" + `
	H  Hidden            ` + "`json:\"h\"`" + `
	AM Ambiguous         ` + "`json:\"am\"`" + `

	// Accepted. Any is a sealed type only when a geta.WithUnion option
	// declares it, which getavet does not read: it is not judged.
	E   Event             ` + "`json:\"e\"`" + `
	E2  Event             ` + "`json:\"e2,omitzero\"`" + `
	Any any               ` + "`json:\"any\"`" + `
	T   TextA             ` + "`json:\"t\"`" + `
	W   time.Time         ` + "`json:\"w\"`" + `
	J   Raw               ` + "`json:\"j\"`" + `
	NT  NotText           ` + "`json:\"nt\"`" + `
	B   []byte            ` + "`json:\"b\"`" + `
	S   map[string]Item   ` + "`json:\"s\"`" + `
	PO  *Item             ` + "`json:\"po,omitzero\"`" + `
	X   chan int          ` + "`json:\"-\"`" + `
}

type Stamp struct {
	time.Time
}

type meta struct {
	Req string       ` + "`header:\"X-Req\"`" + `
	CT  string       ` + "`header:\"Content-Type\"`" + `
	SID *http.Cookie ` + "`cookie:\"sid\"`" + `
}

type In struct {
	Q    **int   ` + "`query:\"q\"`" + `
	Ch   chan int ` + "`query:\"ch\"`" + `
	Ck   string  ` + "`cookie:\"a b\"`" + `
	Body Body    ` + "`body:\"json\"`" + `
}

type InTime struct {
	time.Time
	Stamp
}

type Env struct {
	meta
	Again string   ` + "`header:\"x-req\"`" + `
	H     chan int ` + "`header:\"X-H\"`" + `
	Body  Item     ` + "`body:\"json\"`" + `
}

type moreMeta struct {
	Extra string ` + "`header:\"X-Extra\"`" + `
}

type EnvEmbedded struct {
	*moreMeta
	Stamp
	Body Item ` + "`body:\"json\"`" + `
}

type tagsInside struct {
	Bad string ` + "`header:\"Content-Length\"`" + `
}

// An envelope by its embedded struct's tags alone.
type EnvInside struct {
	tagsInside
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{{Path: "/x", Route: geta.Route{
		Get:    geta.Op(http.StatusOK, func(context.Context, *InTime) (*EnvEmbedded, error) { return nil, nil }, geta.Doc{}),
		Put:    geta.Op(http.StatusOK, func(context.Context, *struct{}) (*EnvInside, error) { return nil, nil }, geta.Doc{}),
		Post:   geta.Op(http.StatusOK, func(context.Context, *In) (*Env, error) { return nil, nil }, geta.Doc{}),
		Delete: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*map[int]Item, error) { return nil, nil }, geta.Doc{}),
	}}}}, geta.WithUnion(geta.Sealed[Event]("kind", geta.Case[Created]("created"))))
}
`,
	})
	if err == nil {
		t.Fatalf("vet passed:\n%s", out)
	}
	wants := []string{
		// A JSON body's members and types.
		"Item: embedded pointer types are not supported",
		"Z: omitzero on a non-pointer field",
		"P is a pointer without omitzero; tag it `json:\"p,omitzero\"`",
		`O: json tag option "omitempty" is not supported`,
		`A2: two fields have the JSON name "a"`,
		"L: []*lib.Item: element type *lib.Item is a pointer; use lib.Item",
		"M: map[string]*lib.Item: element type *lib.Item is a pointer; use lib.Item",
		"K: map type map[int]string: only string keys have a JSON form",
		"C: type chan int has no JSON form geta can derive (chan)",
		"F: type func() has no JSON form geta can derive (func)",
		"R: type [2]int has no JSON form geta can derive (array)",
		"OM: type lib.OnlyMarshal implements encoding.TextMarshaler but not encoding.TextUnmarshaler",
		"H: lib.Hidden has fields but no JSON members",
		// A JSON body's own type, reported on the operation.
		"map type map[int]lib.Item: only string keys have a JSON form",
		"TextA: embedded lib.TextA has its own JSON or text methods; give it a json name",
		"TextB: embedded lib.TextB has its own JSON or text methods",
		// Parameter and header types only their codec refuses.
		"Q: type *int has no JSON form geta can derive (ptr)",
		"Ch: type chan int has no JSON form geta can derive (chan)",
		"H: type chan int has no JSON form geta can derive (chan)",
		// An input's cookie name.
		`Ck: "a b" is not a valid cookie name`,
		// Embedded text types, in an input and an envelope.
		"Time: embedded time.Time is a text type with no fields; tag it path, query, header, or cookie",
		"Stamp: embedded lib.Stamp is a text type with no fields; tag it path, query, header, or cookie",
		"Stamp: embedded lib.Stamp is a text type with no fields; tag it header",
		// An envelope's embedded fields are its own.
		"moreMeta: embedded pointer types are not supported in an envelope; embed lib.moreMeta",
		`CT: header "Content-Type" is reserved for the body geta writes`,
		`Again: empty or repeated header name "x-req"`,
		`Bad: header "Content-Length" is reserved for the body geta writes`,
	}
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "lib/lib.go:"); n != len(wants) {
		t.Errorf("%d diagnostics, want %d:\n%s", n, len(wants), out)
	}
}

// getavet names types as reflect does, so that its text is geta.New's: an
// instantiated generic type's arguments by package path, and predeclared
// and literal types in reflect's spelling.
func TestGenericTypeNamesMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type User struct {
	N int `+"`json:\"n\"`"+`
}

type Box[T any] struct{ v T }

type Pair[K comparable, V any] struct {
	k K
	v V
}

var _ = Box[int]{}.v
var _ = Pair[int, int]{}.k
var _ = Pair[int, int]{}.v

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Box[User], error) { return nil, nil }, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Pair[string, User], error) { return nil, nil }, geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Box[any], error) { return nil, nil }, geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*map[int]Box[[]User], error) { return nil, nil }, geta.Doc{})}},
		{Path: "/e", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*map[int][]byte, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/f", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*map[int]struct{ X int `+"`json:\"x\"`"+`; Y any }, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/g", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*map[int]func(int, ...string) (bool, error), error) { return nil, nil }, geta.Doc{})}},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		"lib.Box[example.com/app/lib.User] has fields but no JSON members",
		"lib.Pair[string,example.com/app/lib.User] has fields but no JSON members",
		`type lib.Box[interface {}]: component name "Box_interface{}" does not match`,
		"map type map[int]lib.Box[[]example.com/app/lib.User]: only string keys have a JSON form",
		"map type map[int][]uint8: only string keys have a JSON form",
		`map type map[int]struct { X int "json:\"x\""; Y interface {} }: only string keys have a JSON form`,
		"map type map[int]func(int, ...string) (bool, error): only string keys have a JSON form",
	})
}

// The rest of reflect's spellings: a channel of each direction (a
// bidirectional one of receive-only channels parenthesised), an interface
// with methods, a function of one result, and unsafe.Pointer as a kind.
func TestMoreTypeSpellingsMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"
	"unsafe"

	"github.com/koji-1009/geta"
)

type Body struct {
	P unsafe.Pointer `+"`json:\"p\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*map[int]chan<- int, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*map[int]<-chan int, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*map[int]chan (<-chan int), error) { return nil, nil }, geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*map[int]interface{ M(int) bool; N() }, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/e", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*map[int]func() int, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/f", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Body, error) { return nil, nil }, geta.Doc{})}},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		"map type map[int]chan<- int: only string keys have a JSON form",
		"map type map[int]<-chan int: only string keys have a JSON form",
		"map type map[int]chan (<-chan int): only string keys have a JSON form",
		"map type map[int]interface { M(int) bool; N() }: only string keys have a JSON form",
		"map type map[int]func() int: only string keys have a JSON form",
		"P: type unsafe.Pointer has no JSON form geta can derive (unsafe.Pointer)",
	})
}

// A type that holds itself other than through a named struct type is
// reported as geta.New refuses it, in an output and an input; one that
// holds itself through a named struct is not.
func TestSelfHoldingTypesMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Tree map[string]Tree

type Chain []Chain

type Anon map[string]struct {
	M Anon `+"`json:\"m\"`"+`
}

type Node struct {
	Kids map[string]Node `+"`json:\"kids\"`"+`
	Next []Node          `+"`json:\"next\"`"+`
}

type TreeIn struct {
	Body Tree `+"`body:\"json\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Tree, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Chain, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Anon, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Node, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/e", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *TreeIn) (*Node, error) { return nil, nil }, geta.Doc{})}},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		"lib.Tree: lib.Tree holds itself other than through a named struct type",
		"lib.Chain: lib.Chain holds itself other than through a named struct type",
		"M: lib.Anon holds itself other than through a named struct type",
		"Body: lib.Tree: lib.Tree holds itself other than through a named struct type",
	})
}

// An operation built in a generic function is judged where it is
// instantiated, by geta.New: an input or a problem description that is a
// type parameter, and a member of a type parameter's type, are not judged.
func TestGenericOperationsAreJudgedWhereInstantiated(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Page[T any] struct {
	Items []T `+"`json:\"items\"`"+`
}

type Item struct {
	ID string `+"`json:\"id\"`"+`
}

type Bad struct {
	P *int `+"`json:\"p\"`"+`
}

type QuotaError struct{}

func (*QuotaError) Error() string { return "quota" }

func list[In, T any]() geta.Operation {
	return geta.Op(http.StatusOK, func(context.Context, *In) (*Page[T], error) { return nil, nil }, geta.Doc{
		Failures: []geta.Failure{geta.OnAsProblem(429, "q", func(*QuotaError) T { var t T; return t })},
	})
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: list[struct{}, Item]()}},
		{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Bad, error) { return nil, nil }, geta.Doc{})}},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		"P is a pointer without omitzero; tag it `json:\"p,omitzero\"`",
	})
}

// The members encoding/json/v2 refuses at runtime, or writes otherwise than
// the schema says, and the text methods it calls, are judged by the rules
// geta.New applies, with its text.
func TestMemberAndTextRefusalsMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Tok string

type tok string

type Paging struct {
	N int `+"`json:\"n\"`"+`
}

type item struct {
	N int `+"`json:\"n\"`"+`
}

type textA struct{ v string }

func (a textA) MarshalText() ([]byte, error)  { return []byte(a.v), nil }
func (a *textA) UnmarshalText(b []byte) error { a.v = string(b); return nil }

type textB struct{ v string }

func (b textB) MarshalText() ([]byte, error)  { return []byte(b.v), nil }
func (b *textB) UnmarshalText(p []byte) error { b.v = string(p); return nil }

// App writes text by AppendText alone.
type App struct {
	V string `+"`json:\"v\"`"+`
}

func (a App) AppendText(b []byte) ([]byte, error) { return append(b, a.V...), nil }

// AppText writes by AppendText and reads by UnmarshalText: a text type.
type AppText struct {
	V string `+"`json:\"v\"`"+`
}

func (a AppText) AppendText(b []byte) ([]byte, error) { return append(b, a.V...), nil }
func (a *AppText) UnmarshalText(b []byte) error      { a.V = string(b); return nil }

// OnlyUnmarshal reads text alone.
type OnlyUnmarshal struct {
	V string `+"`json:\"v\"`"+`
}

func (o *OnlyUnmarshal) UnmarshalText(b []byte) error { o.V = string(b); return nil }

type EmbeddedString struct {
	Tok
	N int `+"`json:\"n\"`"+`
}

type EmbeddedHidden struct {
	tok
	N int `+"`json:\"n\"`"+`
}

type TaggedHidden struct {
	h int `+"`json:\"h\"`"+`
	N int `+"`json:\"n\"`"+`
}

var _ = TaggedHidden{}.h

type NamedPointer struct {
	*Paging `+"`json:\"paging\"`"+`
}

type HiddenPointer struct {
	*item `+"`json:\"item,omitzero\"`"+`
}

type HiddenString struct {
	tok `+"`json:\"t\"`"+`
}

// textB, ignored, still keeps textA's methods from being promoted.
type HiddenText struct {
	textA `+"`json:\"a\"`"+`
	textB `+"`json:\"-\"`"+`
}

// Accepted: one member each, as encoding/json/v2 reads and writes them.
type Named struct {
	*Paging `+"`json:\"paging,omitzero\"`"+`
	item    `+"`json:\"item\"`"+`
	T       AppText `+"`json:\"t\" schema:\"minLength=1\"`"+`
}

type In struct {
	Q AppText `+"`query:\"q\" schema:\"minLength=1\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*EmbeddedString, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*EmbeddedHidden, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*TaggedHidden, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*NamedPointer, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/e", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*HiddenPointer, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/f", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*HiddenString, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/g", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*HiddenText, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/h", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*App, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/i", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*OnlyUnmarshal, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/j", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *In) (*Named, error) { return nil, nil }, geta.Doc{})}},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		"Tok: embedded lib.Tok is not a struct; give it a json name",
		"tok: embedded lib.tok is not a struct",
		"h is unexported but has a json tag",
		"Paging is a pointer without omitzero; tag it `json:\"paging,omitzero\"`",
		"item: embedded *lib.item is unexported; export the type or embed lib.item",
		"tok: embedded lib.tok is unexported and not a struct",
		"textA: embedded lib.textA is unexported and has JSON or text methods",
		"type lib.App implements encoding.TextMarshaler but not encoding.TextUnmarshaler",
		"type lib.OnlyUnmarshal implements encoding.TextUnmarshaler but not encoding.TextMarshaler",
	})
}
