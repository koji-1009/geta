package geta_test

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/internal/vet"
)

// Schema tags: where they may stand, and the bounds a type can meet.

// A schema tag on an untagged embedded struct, whose members are promoted,
// constrains nothing, so it is refused.
func TestRejectsSchemaTagOnEmbeddedStruct(t *testing.T) {
	type Base struct {
		Name string `json:"name"`
	}
	type body struct {
		Base `schema:"minLength=1"`
		Age  int `json:"age"`
	}
	type in struct {
		B body `body:"json"`
	}
	rejects(t, one("/x", geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *in) (*ok, error) { return nil, nil }, geta.Doc{})}),
		"body.Base", "an embedded struct takes no schema tag")
	type bodyOut struct {
		Base `schema:"pattern=x"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*bodyOut, error) { return nil, nil })),
		"an embedded struct takes no schema tag")
}

type SchemaPaging struct {
	Limit int `query:"limit"`
}

type schemaAuth struct {
	Token string `header:"X-Token"`
}

// An input's untagged embedded struct binds its fields by their own tags; a
// schema tag on the embedded struct itself would constrain nothing, so it
// is refused, exported or not.
func TestRejectsSchemaTagOnEmbeddedInputStruct(t *testing.T) {
	type in struct {
		SchemaPaging `schema:"minimum=1"`
	}
	rejects(t, one("/x", get(func(context.Context, *in) (*ok, error) { return nil, nil })),
		"in.SchemaPaging", "an embedded struct takes no schema tag")
	type hidden struct {
		schemaAuth `schema:"maxLength=3"`
	}
	rejects(t, one("/x", get(func(context.Context, *hidden) (*ok, error) { return nil, nil })),
		"hidden.schemaAuth", "an embedded struct takes no schema tag")
	type plain struct {
		SchemaPaging
		schemaAuth
	}
	accepts(t, one("/x", get(func(context.Context, *plain) (*ok, error) { return nil, nil })))
}

// Numeric keywords no value meets are refused: exclusive bounds, integrality,
// the Go type's own range, and multipleOf all count.
func TestRejectsUnreachableBounds(t *testing.T) {
	for _, tc := range []struct{ tag, kind, want string }{
		{"exclusiveMinimum=1,exclusiveMaximum=1", "float64", "no number meets exclusiveMinimum 1, exclusiveMaximum 1"},
		{"minimum=5,exclusiveMaximum=5", "float64", "no number meets minimum 5, exclusiveMaximum 5"},
		{"exclusiveMinimum=5,maximum=5", "float64", "no number meets"},
		{"minimum=0.2,maximum=0.1", "float64", "minimum 0.2 exceeds maximum 0.1"},
		{"exclusiveMinimum=0.3,exclusiveMaximum=0.1", "float32", "no float32 value (-3.4028234663852886e+38 to 3.4028234663852886e+38) meets exclusiveMinimum 0.3, exclusiveMaximum 0.1"},
		{"minimum=1.5,maximum=1.9", "int", "no int value (-9223372036854775808 to 9223372036854775807) meets minimum 1.5, maximum 1.9"},
		{"exclusiveMinimum=1,exclusiveMaximum=2", "int64", "no int64 value"},
		{"exclusiveMinimum=200", "int8", "no int8 value (-128 to 127) meets"},
		{"maximum=-1", "uint8", "minimum 0 exceeds maximum -1"},
		{"maximum=-1", "uint64", "minimum 0 exceeds maximum -1"},
		{"exclusiveMaximum=0", "uint", "no uint value (0 to 18446744073709551615) meets"},
		{"minimum=1e19", "int64", "no int64 value (-9223372036854775808 to 9223372036854775807) meets minimum 10000000000000000000"},
		{"exclusiveMinimum=18446744073709552000", "uint64", "no uint64 value"},
		{"exclusiveMinimum=2147483647", "int32", "no int32 value (-2147483648 to 2147483647)"},
		{"maximum=-2147483649", "int32", "no int32 value"},
		{"minimum=1,maximum=4,multipleOf=5", "int", "multipleOf 5"},
		{"exclusiveMinimum=0,exclusiveMaximum=5,multipleOf=5", "int16", "no int16 value"},
		{"minimum=0.1,maximum=0.2,multipleOf=0.3", "float64", "no number meets minimum 0.1, maximum 0.2, multipleOf 0.3"},
		{"minimum=253,multipleOf=7", "uint8", "no uint8 value (0 to 255) meets"}, // 252 is the last multiple
	} {
		err := vet.CheckSchemaTag(tc.tag, tc.kind)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s on %s: %v; want %q", tc.tag, tc.kind, err, tc.want)
		}
	}
	for _, tc := range []struct{ tag, kind string }{
		{"minimum=5,maximum=5", "float64"},
		{"minimum=5,maximum=5", "int"},
		{"minimum=1.5,maximum=2", "int"},
		{"exclusiveMinimum=1.5,exclusiveMaximum=2.5", "int"},
		{"exclusiveMinimum=126", "int8"},
		{"exclusiveMinimum=126.5", "int8"},
		{"exclusiveMaximum=1", "uint8"},
		{"exclusiveMinimum=2147483646.5", "int32"},
		{"minimum=-1000,maximum=1000", "int8"}, // the type's range narrows it
		{"minimum=250,multipleOf=5", "uint8"},
		{"minimum=0.1,maximum=0.2,multipleOf=0.15", "float64"},
		{"exclusiveMinimum=0,exclusiveMaximum=10,multipleOf=5", "int"},
		{"exclusiveMinimum=1e300", "float64"},
	} {
		if err := vet.CheckSchemaTag(tc.tag, tc.kind); err != nil {
			t.Errorf("%s on %s: %v", tc.tag, tc.kind, err)
		}
	}
	type unreachable struct {
		Q int8 `query:"q" schema:"exclusiveMinimum=200"`
	}
	rejects(t, one("/x", get(func(context.Context, *unreachable) (*ok, error) { return nil, nil })),
		"unreachable.Q", "no int8 value (-128 to 127) meets")
	type noInteger struct {
		N int `json:"n" schema:"minimum=1.5,maximum=1.9"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*noInteger, error) { return nil, nil })),
		"noInteger.N", "minimum 1.5, maximum 1.9")
}

// A bound a float64 cannot hold as written would be published and enforced
// as another number, so it is refused, naming the number it would become.
func TestRejectsBoundsFloat64CannotHold(t *testing.T) {
	for _, tc := range []struct{ tag, kind, want string }{
		{"maximum=9007199254740993", "int64", `"9007199254740993" is not exact as a float64; use 9007199254740992`},
		{"minimum=18446744073709551615", "uint64", "use 18446744073709552000"},
		{"minimum=9223372036854775807", "int64", "use 9223372036854776000"},
		{"exclusiveMinimum=1e-400", "float64", "use 0"},
		{"multipleOf=0.10000000000000000001", "float64", "use 0.1"},
		{"maximum=0x1.999999999999ap-4", "float64", "use 0.1"},
		{"minimum=1_000.000_000_000_000_000_1", "float64", "use 1000"},
	} {
		err := vet.CheckSchemaTag(tc.tag, tc.kind)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s on %s: %v; want %q", tc.tag, tc.kind, err, tc.want)
		}
	}
	for _, tc := range []struct{ tag, kind string }{
		{"maximum=0.1", "float64"},
		{"maximum=0.1", "float32"},
		{"minimum=-0.3,multipleOf=0.1", "float64"},
		{"maximum=9007199254740992", "int64"},
		{"maximum=18446744073709552000", "uint64"},
		{"maximum=1e21", "float64"},
		{"maximum=1.5e-7", "float64"},
		{"minimum=+5", "int"},
		{"minimum=5.", "int"},
		{"minimum=005.50", "float64"},
		{"minimum=1_000", "int"},
		{"maximum=0x1p-2", "float64"},
		{"maximum=-0X1.8P1", "float64"},
		{"minimum=-0", "int"},
		{"maximum=5e-324", "float64"},
	} {
		if err := vet.CheckSchemaTag(tc.tag, tc.kind); err != nil {
			t.Errorf("%s on %s: %v", tc.tag, tc.kind, err)
		}
	}
	type wide struct {
		N int64 `json:"n" schema:"maximum=9007199254740993"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*wide, error) { return nil, nil })),
		"wide.N", "use 9007199254740992")
}

// A schema tag cannot widen a fixed-size integer's range in the document:
// the tighter of the type's bound and the tag's is documented and enforced.
func TestSchemaBoundsKeepTheIntegerRange(t *testing.T) {
	type small struct {
		Wide   int8   `json:"wide" schema:"minimum=-1000,maximum=1000"`
		Narrow int8   `json:"narrow" schema:"minimum=-5,maximum=5"`
		Excl   uint8  `json:"excl" schema:"exclusiveMinimum=-1,exclusiveMaximum=1000"`
		Tight  uint16 `json:"tight" schema:"exclusiveMaximum=10"`
		I32    int32  `json:"i32" schema:"minimum=-1e12"`
		Plain  int32  `json:"plain" schema:"multipleOf=2"`
	}
	h := func(_ context.Context, in *struct {
		B small `body:"json"`
	}) (*ok, error) {
		return &ok{true}, nil
	}
	a := accepts(t, one("/i", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}))
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]map[string]any `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(a.OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	props := doc.Components.Schemas["small"].Properties
	for name, want := range map[string]map[string]any{
		"wide":   {"type": "integer", "minimum": -128.0, "maximum": 127.0},
		"narrow": {"type": "integer", "minimum": -5.0, "maximum": 5.0},
		"excl":   {"type": "integer", "minimum": 0.0, "maximum": 255.0},
		"tight":  {"type": "integer", "minimum": 0.0, "maximum": 65535.0, "exclusiveMaximum": 10.0},
		"i32":    {"type": "integer", "format": "int32", "minimum": -2147483648.0},
		"plain":  {"type": "integer", "format": "int32", "multipleOf": 2.0},
	} {
		if got := props[name]; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: %v; want %v", name, got, want)
		}
	}
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/i", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	if w := post(`{"wide":-500,"narrow":0,"excl":0,"tight":0,"i32":0,"plain":0}`); w.Code != 400 || !strings.Contains(w.Body.String(), `"path":"$.wide"`) {
		t.Errorf("-500 for an int8: %d %s", w.Code, w.Body)
	}
	if w := post(`{"wide":-128,"narrow":0,"excl":0,"tight":0,"i32":0,"plain":0}`); w.Code != 200 {
		t.Errorf("-128 for an int8: %d %s", w.Code, w.Body)
	}

	// A declared bound the type cannot reach leaves no value.
	type unreachable struct {
		N int8 `json:"n" schema:"minimum=-1000,maximum=-500"`
	}
	rejects(t, one("/u", geta.Route{Post: geta.Op(http.StatusOK, func(_ context.Context, in *struct {
		B unreachable `body:"json"`
	}) (*ok, error) {
		return &ok{true}, nil
	}, geta.Doc{})}), "minimum -128 exceeds maximum -500")
}

// A float32 past its finite range does not decode, so a bound that leaves no
// value inside it is refused, and a looser declared bound is documented as
// the type's.
func TestFloat32Range(t *testing.T) {
	for _, tc := range []struct{ tag, want string }{
		{"exclusiveMinimum=1e39", "no float32 value"},
		{"minimum=3.5e38", "no float32 value"},
		{"maximum=-1e39", "no float32 value"},
		{"minimum=-1e39,exclusiveMaximum=-3.4028234663852886e38", "no float32 value"},
	} {
		err := vet.CheckSchemaTag(tc.tag, "float32")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s on float32: %v; want %q", tc.tag, err, tc.want)
		}
	}
	for _, tag := range []string{
		"exclusiveMinimum=1e38",
		"maximum=3.4028234663852886e38",
		"minimum=-1e39",
		"exclusiveMaximum=1e39",
		"minimum=1e38,multipleOf=1e38",
	} {
		if err := vet.CheckSchemaTag(tag, "float32"); err != nil {
			t.Errorf("%s on float32: %v", tag, err)
		}
	}
	// float64 keeps no such range.
	if err := vet.CheckSchemaTag("exclusiveMinimum=1e39", "float64"); err != nil {
		t.Errorf("exclusiveMinimum=1e39 on float64: %v", err)
	}

	type unreachable struct {
		F float32 `json:"f" schema:"exclusiveMinimum=1e39"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*unreachable, error) { return nil, nil })),
		"unreachable.F", "no float32 value")

	type small struct {
		Lo    float32 `json:"lo" schema:"minimum=-1e39"`
		Hi    float32 `json:"hi" schema:"exclusiveMaximum=1e39"`
		Plain float32 `json:"plain"`
		Own   float32 `json:"own" schema:"minimum=-1,maximum=1"`
	}
	h := func(_ context.Context, in *struct {
		B small `body:"json"`
	}) (*ok, error) {
		return &ok{true}, nil
	}
	a := accepts(t, one("/f", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}))
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]map[string]any `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(a.OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	props := doc.Components.Schemas["small"].Properties
	for name, want := range map[string]map[string]any{
		"lo":    {"type": "number", "format": "float", "minimum": -float64(math.MaxFloat32)},
		"hi":    {"type": "number", "format": "float", "maximum": float64(math.MaxFloat32)},
		"plain": {"type": "number", "format": "float"},
		"own":   {"type": "number", "format": "float", "minimum": -1.0, "maximum": 1.0},
	} {
		if len(props[name]) != len(want) {
			t.Errorf("%s: %v; want %v", name, props[name], want)
			continue
		}
		for k, v := range want {
			if props[name][k] != v {
				t.Errorf("%s.%s: %v; want %v", name, k, props[name][k], v)
			}
		}
	}
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/f", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	if w := post(`{"lo":-3.4028234663852886e38,"hi":3.4028234663852886e38,"plain":0,"own":0}`); w.Code != 200 {
		t.Errorf("float32's own bounds: %d %s", w.Code, w.Body)
	}
	if w := post(`{"lo":-3.5e38,"hi":0,"plain":0,"own":0}`); w.Code != 400 {
		t.Errorf("-3.5e38 for a float32: %d %s", w.Code, w.Body)
	}
}
