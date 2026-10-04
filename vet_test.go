package geta_test

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
)

// The exported rules getavet applies give the verdict, and the text,
// geta.New gives.

// CheckSchemaTag gives getavet the verdict geta.New gives.
func TestCheckSchemaTag(t *testing.T) {
	for _, c := range []struct {
		tag, kind string
		ok        bool
	}{
		{"maxLength=64", "string", true},
		{"maxLength=abc", "string", false},
		{"maxLength=3", "int", false},
		{"minimum=0,maximum=100", "int", true},
		{"minimum=200", "int8", false},
		{"format=int64", "int64", false},
		{"format=ipv4", "string", true},
		{"format=email", "string", false},
		{"format=uri", "string", false},
		{"pattern=^a+$", "text", true},
		{"format=x", "time.Time", false},
		{"maxItems=3", "slice", true},
		{"maxLength=3", "struct", false},
		{"bogus=1", "string", false},
		{"", "geta.File", true},
		{"maxLength=3", "geta.File", false},
		{"minProperties=1,maxProperties=10", "map", true},
		{"maxProperties=10", "?map", true},
		{"items.maxProperties=2", "[]map", true},
		{"minProperties=3,maxProperties=2", "map", false},
		{"minProperties=-1", "map", false},
		{"minProperties=1", "object", false},
		{"items.maxProperties=2", "[]object", false},
		{"maxProperties=2", "struct", false},
		{"maxProperties=2", "string", false},
		{"maxProperties=2", "slice", false},
		{"deprecated=true", "object", true},
	} {
		err := geta.CheckSchemaTag(c.tag, c.kind)
		if (err == nil) != c.ok {
			t.Errorf("%s on %s: %v", c.tag, c.kind, err)
		}
	}
	if geta.CheckSchemaTag("", "complex128") == nil {
		t.Error("an unknown kind passed")
	}
	// A map's schema is an object, as geta.New writes it; a named struct's
	// is a reference; an unnamed struct's, an object of its fields.
	for kind, want := range map[string]string{
		"map":    "schema keyword minLength applies to string, not object",
		"object": "schema keyword minLength applies to string, not object",
		"struct": `schema tag "minLength=1" on a struct type`,
	} {
		if err := geta.CheckSchemaTag("minLength=1", kind); err == nil || err.Error() != want {
			t.Errorf("%s: %v; want %s", kind, err, want)
		}
	}
	if err := geta.CheckSchemaTag("minProperties=1", "object"); err == nil ||
		err.Error() != "schema keyword minProperties applies to a map, not a struct" {
		t.Errorf("minProperties on an unnamed struct: %v", err)
	}
}

type VetPaging struct {
	Limit int `query:"limit"`
}

type vetAuth struct {
	Token string `header:"X-Token"`
}

type VetToken string

func (t VetToken) MarshalText() ([]byte, error)  { return []byte(t), nil }
func (t *VetToken) UnmarshalText(b []byte) error { *t = VetToken(b); return nil }

type vetBody struct {
	N int `json:"n"`
}

// refusalCase is a struct, used as an input or output, whose field F geta.New
// refuses.
type refusalCase struct {
	name string
	typ  reflect.Type
	op   geta.Operation
	kind string // F's kind, for the rules that judge it
}

func outCase[Out any](name, kind string) refusalCase {
	return refusalCase{name, reflect.TypeFor[Out](),
		geta.Op(http.StatusOK, func(context.Context, *empty) (*Out, error) { return nil, nil }, geta.Doc{}), kind}
}

func inCase[In any](name, kind string) refusalCase {
	return refusalCase{name, reflect.TypeFor[In](),
		geta.Op(http.StatusOK, func(context.Context, *In) (*ok, error) { return nil, nil }, geta.Doc{}), kind}
}

func describeField(f reflect.StructField, kind string) geta.VetField {
	return geta.VetField{Name: f.Name, Exported: f.IsExported(), Embedded: f.Anonymous, Tag: f.Tag, Type: f.Type.String(),
		Pointer: f.Type.Kind() == reflect.Pointer, Struct: f.Type.Kind() == reflect.Struct,
		Cookie: f.Type == reflect.TypeFor[*http.Cookie](), Kind: kind}
}

// refusal runs check over c's fields, as getavet does, and requires that
// geta.New refuses c's operation with the text check gives for F.
func (c refusalCase) refusal(t *testing.T, check func(geta.VetField) error) {
	t.Helper()
	var want error
	for i := range c.typ.NumField() {
		f := c.typ.Field(i)
		kind := ""
		if strings.EqualFold(f.Name, "F") {
			kind = c.kind
		}
		if err := check(describeField(f, kind)); err != nil {
			want = err
			break
		}
	}
	if want == nil || !strings.HasPrefix(strings.ToUpper(want.Error()), "F") {
		t.Fatalf("check: %v, want a refusal of F", want)
	}
	_, err := geta.New(one("/x", geta.Route{Post: c.op}))
	if err == nil || !strings.Contains(err.Error(), "."+want.Error()) {
		t.Errorf("geta.New: %v\nwant it to contain %q", err, "."+want.Error())
	}
}

// geta.New refuses an envelope's field with the text CheckEnvelopeField
// gives for it, which getavet reports.
func TestEnvelopeRefusalsAreCheckEnvelopeFields(t *testing.T) {
	for _, c := range []refusalCase{
		outCase[struct {
			F string `header:"Content-Type"`
		}]("Content-Type", ""),
		outCase[struct {
			F string `header:"content-length"`
		}]("Content-Length", ""),
		outCase[struct {
			F string `header:"X-Content-Type-Options"`
		}]("X-Content-Type-Options", ""),
		outCase[struct {
			F string `header:"Set-Cookie"`
		}]("Set-Cookie", ""),
		outCase[struct {
			F string `header:"X Bad"`
		}]("invalid header name", ""),
		outCase[struct {
			F string `header:""`
		}]("empty header name", ""),
		outCase[struct {
			A string `header:"X-A"`
			F string `header:"x-a"`
		}]("repeated header", ""),
		outCase[struct {
			F int `header:"X-N" schema:"minimum=1"`
		}]("header schema", ""),
		outCase[struct {
			F []string `header:"X-List"`
		}]("header type", "[]string"),
		outCase[struct {
			A *http.Cookie `cookie:"sid"`
			F *http.Cookie `cookie:"sid"`
		}]("repeated cookie", ""),
		outCase[struct {
			F *http.Cookie `cookie:"a b"`
		}]("invalid cookie name", ""),
		outCase[struct {
			F string `cookie:"sid"`
		}]("cookie type", ""),
		outCase[struct {
			F *vetBody `body:"json"`
		}]("pointer body", ""),
		outCase[struct {
			F vetBody `body:"xml"`
		}]("body tag", ""),
		outCase[struct {
			A vetBody `body:"json"`
			F vetBody `body:"json"`
		}]("second body", ""),
		outCase[struct {
			A vetBody `body:"json"`
			F int
		}]("untagged", ""),
	} {
		t.Run(c.name, func(t *testing.T) {
			seen := map[string]bool{}
			c.refusal(t, func(f geta.VetField) error { return geta.CheckEnvelopeField(f, seen) })
		})
	}
}

// geta.New refuses an input's field with the text CheckInputField gives for
// it, which getavet reports.
func TestInputRefusalsAreCheckInputFields(t *testing.T) {
	for _, c := range []refusalCase{
		inCase[struct {
			F string `query:"a" header:"A"`
		}]("two tags", ""),
		inCase[struct {
			F string
		}]("untagged", ""),
		inCase[struct {
			f string `query:"a"`
		}]("unexported", ""),
		inCase[struct {
			F vetBody `body:"xml"`
		}]("body tag", ""),
		inCase[struct {
			A vetBody `body:"json"`
			F vetBody `body:"json"`
		}]("second body", ""),
		inCase[struct {
			F string `query:""`
		}]("empty name", ""),
		inCase[struct {
			A string `header:"X-A"`
			F string `header:"x-a"`
		}]("repeated header", ""),
		inCase[struct {
			F []int `cookie:"c"`
		}]("cookie slice", "[]int"),
		inCase[struct {
			F vetBody `header:"X-S"`
		}]("struct header", "struct"),
		inCase[struct {
			F []byte `header:"X-B"`
		}]("bytes header", "[]byte"),
		inCase[struct {
			*VetPaging
		}]("embedded pointer", ""),
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "embedded pointer" {
				// The embedded pointer is the refused field here.
				var want error
				f := describeField(c.typ.Field(0), "")
				if _, err := geta.CheckInputField(f, map[string]string{}); err != nil {
					want = err
				}
				_, err := geta.New(one("/x", geta.Route{Post: c.op}))
				if want == nil || err == nil || !strings.Contains(err.Error(), "."+want.Error()) {
					t.Errorf("geta.New: %v\nCheckInputField: %v", err, want)
				}
				return
			}
			seen := map[string]string{}
			c.refusal(t, func(f geta.VetField) error { _, err := geta.CheckInputField(f, seen); return err })
		})
	}
}

// What geta.New accepts, CheckInputField and CheckEnvelopeField accept.
func TestVetFieldAcceptsWhatNewAccepts(t *testing.T) {
	type in struct {
		VetPaging
		vetAuth
		ID    string    `path:"id"`
		Tags  []string  `query:"tag"`
		Token *VetToken `header:"X-Token-2"`
		Body  *vetBody  `body:"json"`
		note  string
	}
	type out struct {
		N       int          `header:"X-N"`
		Session *http.Cookie `cookie:"sid"`
		Body    vetBody      `body:"json"`
		note    string
	}
	_ = in{}.note
	_ = out{}.note
	accepts(t, one("/x/{id}", geta.Route{Post: geta.Op(http.StatusOK,
		func(context.Context, *in) (*out, error) { return nil, nil }, geta.Doc{})}))
	kinds := map[string]string{"ID": "string", "Tags": "[]string", "Token": "text", "N": "int"}
	seenIn := map[string]string{}
	var walk func(reflect.Type)
	walk = func(st reflect.Type) {
		for i := range st.NumField() {
			f := st.Field(i)
			deeper, err := geta.CheckInputField(describeField(f, kinds[f.Name]), seenIn)
			if err != nil {
				t.Errorf("CheckInputField(%s): %v", f.Name, err)
			}
			if deeper {
				walk(f.Type)
			}
		}
	}
	walk(reflect.TypeFor[in]())
	seenOut := map[string]bool{}
	ot := reflect.TypeFor[out]()
	for i := range ot.NumField() {
		f := ot.Field(i)
		if err := geta.CheckEnvelopeField(describeField(f, kinds[f.Name]), seenOut); err != nil {
			t.Errorf("CheckEnvelopeField(%s): %v", f.Name, err)
		}
	}
}
