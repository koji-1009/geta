package getavet

import (
	"fmt"
	"strings"
	"testing"
)

// A string map key is judged as geta.New judges it, whatever receivers its
// methods have: refused when encoding/json/v2 would write it by a text
// method and read it otherwise, or the reverse, and accepted when it writes
// and reads it alike, by text methods, by none, or by JSON methods it calls
// in their place. getavet once reported a key with a value MarshalText as
// having no string form, and passed the same key with a pointer one.
func TestMapKeysAreJudgedForEveryReceiver(t *testing.T) {
	var src, routes strings.Builder
	src.WriteString(`package lib

import (
	"context"
	"encoding/json/jsontext"
	"net/http"

	"github.com/koji-1009/geta"
)

`)
	var wants []string
	route := func(name string) {
		fmt.Fprintf(&routes, "\t\t{Path: \"/%s\", Route: geta.Route{Get: geta.Op(http.StatusOK, "+
			"func(context.Context, *struct{}) (*map[%s]int, error) { return nil, nil }, geta.Doc{})}},\n", name, name)
	}
	// K<M><A><U>: MarshalText (M), AppendText (A), and UnmarshalText (U) on
	// no receiver (N), the value (V), or the pointer (P).
	recv := func(r byte, name string) string {
		if r == 'P' {
			return "(k *" + name + ")"
		}
		return "(k " + name + ")"
	}
	const rs = "NVP"
	for _, m := range []byte(rs) {
		for _, a := range []byte(rs) {
			for _, u := range []byte(rs) {
				name := "K" + string([]byte{m, a, u})
				fmt.Fprintf(&src, "type %s string\n\n", name)
				if m != 'N' {
					fmt.Fprintf(&src, "func %s MarshalText() ([]byte, error) { return nil, nil }\n", recv(m, name))
				}
				if a != 'N' {
					fmt.Fprintf(&src, "func %s AppendText(b []byte) ([]byte, error) { return b, nil }\n", recv(a, name))
				}
				if u != 'N' {
					fmt.Fprintf(&src, "func %s UnmarshalText([]byte) error { return nil }\n", recv(u, name))
				}
				src.WriteString("\n")
				route(name)
				switch writes, reads := m != 'N' || a != 'N', u != 'N'; {
				case writes && !reads:
					wants = append(wants, fmt.Sprintf("type lib.%s implements encoding.TextMarshaler but not encoding.TextUnmarshaler", name))
				case reads && !writes:
					wants = append(wants, fmt.Sprintf("type lib.%s implements encoding.TextUnmarshaler but not encoding.TextMarshaler", name))
				}
			}
		}
	}
	// Keys with JSON methods, which encoding/json/v2 calls in place of their
	// text methods: all accepted.
	src.WriteString(`type JVP string

func (k JVP) MarshalJSON() ([]byte, error)  { return nil, nil }
func (k *JVP) UnmarshalJSON([]byte) error   { return nil }

type JPP string

func (k *JPP) MarshalJSON() ([]byte, error) { return nil, nil }
func (k *JPP) UnmarshalJSON([]byte) error   { return nil }

type JVN string

func (k JVN) MarshalJSON() ([]byte, error) { return nil, nil }

type JNP string

func (k *JNP) UnmarshalJSON([]byte) error { return nil }

type JVPM string

func (k JVPM) MarshalJSON() ([]byte, error)  { return nil, nil }
func (k *JVPM) UnmarshalJSON([]byte) error   { return nil }
func (k JVPM) MarshalText() ([]byte, error)  { return nil, nil }

type JVU string

func (k JVU) MarshalJSON() ([]byte, error) { return nil, nil }
func (k *JVU) UnmarshalText([]byte) error  { return nil }

type JTo string

func (k JTo) MarshalJSONTo(*jsontext.Encoder) error     { return nil }
func (k *JTo) UnmarshalJSONFrom(*jsontext.Decoder) error { return nil }

type JPPMU string

func (k *JPPMU) MarshalJSON() ([]byte, error) { return nil, nil }
func (k *JPPMU) UnmarshalJSON([]byte) error   { return nil }
func (k JPPMU) MarshalText() ([]byte, error)  { return nil, nil }
func (k *JPPMU) UnmarshalText([]byte) error   { return nil }

`)
	for _, name := range []string{"JVP", "JPP", "JVN", "JNP", "JVPM", "JVU", "JTo", "JPPMU"} {
		route(name)
	}
	src.WriteString("func Build() (*geta.App, error) {\n\treturn geta.New(geta.Table{Routes: []geta.Entry{\n")
	src.WriteString(routes.String())
	src.WriteString("\t}})\n}\n")
	diagnostics, built := vetAndNew(t, src.String())
	checkSame(t, diagnostics, built, wants)
	if len(wants) != 10 {
		t.Fatalf("%d refusals, want 10", len(wants))
	}
}

// A map key type with one text method only is refused as a value type with
// one is, and a schema tag names only a format geta checks; getavet says so
// with geta.New's text.
func TestOneSidedKeysAndUncheckedFormatsMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"
	"strings"

	"github.com/koji-1009/geta"
)

// ReadKey reads itself in upper case, but is written as it is held.
type ReadKey string

func (k *ReadKey) UnmarshalText(b []byte) error { *k = ReadKey(strings.ToUpper(string(b))); return nil }

// WriteKey writes itself by a pointer method, and is read as it is sent.
type WriteKey string

func (k *WriteKey) MarshalText() ([]byte, error) { return []byte("k-" + string(*k)), nil }

// BothKey reads and writes itself.
type BothKey string

func (k *BothKey) MarshalText() ([]byte, error) { return []byte(*k), nil }
func (k *BothKey) UnmarshalText(b []byte) error { *k = BothKey(b); return nil }

type Read struct {
	M map[ReadKey]int `+"`json:\"m\"`"+`
}

type Write struct {
	M map[WriteKey]int `+"`json:\"m\"`"+`
}

type Both struct {
	M map[BothKey]int `+"`json:\"m\"`"+`
	E string          `+"`json:\"e\" schema:\"format=ipv4\"`"+`
}

type Unchecked struct {
	E string `+"`json:\"e\" schema:\"format=idn-email\"`"+`
}

// Mail names a format whose grammar is an application's own: an
// application type carries it (geta.FormatType), not a schema tag.
type Mail struct {
	E string `+"`json:\"e\" schema:\"format=email\"`"+`
}

type Custom struct {
	E string `+"`query:\"e\" schema:\"format=custom\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Read, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Write, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Both, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Unchecked, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/e", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *Custom) (*Both, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/f", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*Mail, error) { return nil, nil }, geta.Doc{})}},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		"type lib.ReadKey implements encoding.TextUnmarshaler but not encoding.TextMarshaler",
		"type lib.WriteKey implements encoding.TextMarshaler but not encoding.TextUnmarshaler",
		`schema keyword format: unknown format "idn-email"`,
		`schema keyword format: unknown format "custom"`,
		`schema keyword format: unknown format "email"`,
	})
}

// A schema tag on a map, a named struct, or an unnamed struct is refused in
// geta.New's words: a map's and an unnamed struct's schema is an object, a
// named struct's a reference. A map takes minProperties and maxProperties
// (an array of maps, by items.), which an unnamed struct, whose members are
// its fields, does not.
func TestSchemaTagsOnObjectsMatchGetaNew(t *testing.T) {
	diagnostics, built := vetAndNew(t, `package lib

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Inner struct {
	X int `+"`json:\"x\"`"+`
}

type OnMap struct {
	M map[string]int `+"`json:\"m\" schema:\"minLength=1\"`"+`
}

type OnStruct struct {
	S Inner `+"`json:\"s\" schema:\"minLength=2\"`"+`
}

type OnAnonymous struct {
	A struct {
		X int `+"`json:\"x\"`"+`
	} `+"`json:\"a\" schema:\"maxItems=3\"`"+`
}

type Sized struct {
	M map[string]int   `+"`json:\"m\" schema:\"minProperties=1,maxProperties=10\"`"+`
	L []map[string]int `+"`json:\"l\" schema:\"items.maxProperties=2\"`"+`
}

type SizedAnonymous struct {
	A struct {
		X int `+"`json:\"x\"`"+`
	} `+"`json:\"a\" schema:\"maxProperties=1\"`"+`
}

type SizedEmpty struct {
	M map[string]int `+"`json:\"m\" schema:\"minProperties=3,maxProperties=2\"`"+`
}

type SizedBody struct {
	B Sized `+"`body:\"json\"`"+`
}

func Build() (*geta.App, error) {
	return geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/a", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*OnMap, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/b", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*OnStruct, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/c", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*OnAnonymous, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/d", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *SizedBody) (*Sized, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/e", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*SizedAnonymous, error) { return nil, nil }, geta.Doc{})}},
		{Path: "/f", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*SizedEmpty, error) { return nil, nil }, geta.Doc{})}},
	}})
}
`)
	checkSame(t, diagnostics, built, []string{
		"schema keyword minLength applies to string, not object",
		`schema tag "minLength=2" on a struct type`,
		"schema keyword maxItems applies to array, not object",
		"schema keyword maxProperties applies to a map, not a struct",
		"minProperties 3 exceeds maxProperties 2",
	})
}
