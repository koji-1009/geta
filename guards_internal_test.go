package geta

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unsafe"
)

// codecFor makes a codec, with a schema, for every kind CheckJSONType
// passes, and is refused every other: its switch on the kind has no default,
// so a kind CheckJSONType came to pass without a case there would be a codec
// with no schema. Every kind is sampled but Interface, a sealed type's
// (unionCodec).
func TestCodecForTakesTheKindsCheckJSONTypePasses(t *testing.T) {
	type member struct {
		A int `json:"a"`
	}
	samples := []reflect.Type{
		reflect.TypeFor[bool](), reflect.TypeFor[string](),
		reflect.TypeFor[int](), reflect.TypeFor[int8](), reflect.TypeFor[int16](), reflect.TypeFor[int32](), reflect.TypeFor[int64](),
		reflect.TypeFor[uint](), reflect.TypeFor[uint8](), reflect.TypeFor[uint16](), reflect.TypeFor[uint32](), reflect.TypeFor[uint64](),
		reflect.TypeFor[uintptr](), reflect.TypeFor[float32](), reflect.TypeFor[float64](),
		reflect.TypeFor[complex64](), reflect.TypeFor[complex128](),
		reflect.TypeFor[[2]int](), reflect.TypeFor[chan int](), reflect.TypeFor[func()](), reflect.TypeFor[map[string]int](),
		reflect.TypeFor[*int](), reflect.TypeFor[[]int](), reflect.TypeFor[member](), reflect.TypeFor[unsafe.Pointer](),
	}
	sampled := map[reflect.Kind]bool{}
	for _, typ := range samples {
		sampled[typ.Kind()] = true
		passes := CheckJSONType(vetType(typ)) == nil
		c, err := newRegistry().codecFor(typ)
		switch {
		case passes != (err == nil):
			t.Errorf("%s: CheckJSONType passes it %v, codecFor refuses it with %v", typ, passes, err)
		case err == nil && c.schema == nil:
			t.Errorf("%s: a codec with no schema", typ)
		}
	}
	for k := reflect.Bool; k <= reflect.UnsafePointer; k++ {
		if k != reflect.Interface && !sampled[k] {
			t.Errorf("kind %s is not sampled", k)
		}
	}
}

// Every kind CheckSchemaTag names by its type (vetKinds) has a codec, which
// kindSchema takes the schema of.
func TestEveryVetKindHasACodec(t *testing.T) {
	for kind, typ := range vetKinds {
		if _, err := newRegistry().codecFor(typ); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
}

// A leaf of a kind the single pass has no case for is refused there, so the
// body takes the reference path, where check judges it.
func TestTheSinglePassRefusesALeafKindItHasNoCaseFor(t *testing.T) {
	c := &codec{t: reflect.TypeFor[string](), kind: ckind(-1), schema: &schema{Type: "string"}}
	var s string
	if singlePass(c, c.schema, []byte(`"x"`), DefaultLimits, reflect.ValueOf(&s).Elem()) {
		t.Fatal("the single pass took a leaf of a kind it has no case for")
	}
}

type veilHidden struct {
	N int `json:"n"`
}

// veilCase's member hidden is an unexported embedded struct with a json
// name: encoding/json/v2 sets it, reflection cannot.
type veilCase struct {
	Kind       string `json:"kind"`
	veilHidden `json:"hidden"`
}

type veil interface{ isVeil() }

func (veilCase) isVeil() {}

// A sealed type whose variant has a member reflection cannot set does not
// read in one pass (fastEligible), and the reference path reads it.
func TestASealedVariantWithAnUnsettableMemberTakesTheReferencePath(t *testing.T) {
	r := newRegistry()
	if err := r.declareUnion(Sealed[veil]("kind", Case[veilCase]("v"))); err != nil {
		t.Fatal(err)
	}
	r.sealDeclarations()
	type body struct {
		V veil `json:"v"`
	}
	c, err := r.codecFor(reflect.TypeFor[body]())
	if err != nil {
		t.Fatal(err)
	}
	if fastEligible(c, map[*codec]bool{}) {
		t.Fatal("a variant with an unsettable member reads in one pass")
	}
	var got body
	errs := reference(r, c, c.use(), []byte(`{"v":{"kind":"v","hidden":{"n":3}}}`), DefaultLimits, reflect.ValueOf(&got).Elem())
	if v, ok := got.V.(veilCase); len(errs) > 0 || !ok || v.N != 3 {
		t.Fatalf("%v %#v", errs, got)
	}
}

type nullOpaqueBody struct {
	O *opaqueJSON `json:"o,omitzero"`
	D string      `json:"d" schema:"default=x"`
}

// A pointer member given null, which its schema admits (a type with JSON
// methods of its own, undeclared), stays nil on the reference path, and the
// members beside it take their defaults.
func TestTheReferencePathLeavesAPointerGivenNullNil(t *testing.T) {
	r := newRegistry()
	c, err := r.codecFor(reflect.TypeFor[nullOpaqueBody]())
	if err != nil {
		t.Fatal(err)
	}
	var got nullOpaqueBody
	errs := reference(r, c, c.use(), []byte(`{"o":null}`), DefaultLimits, reflect.ValueOf(&got).Elem())
	if len(errs) > 0 || got.O != nil || got.D != "x" {
		t.Fatalf("%v %#v", errs, got)
	}
}

// spelledKey is a map key as encoding/json/v2 hands its UnmarshalJSON a
// member name: as written, quotes and escapes included.
type spelledKey string

func (k *spelledKey) UnmarshalJSON(b []byte) error { *k = spelledKey(b); return nil }
func (k spelledKey) MarshalJSON() ([]byte, error)  { return []byte(k), nil }

type filledValue struct {
	A string `json:"a" schema:"default=zz"`
}

// The reference path reads a key whose type has JSON methods of its own
// from the name as written, as encoding/json/v2 and the single pass read it,
// so a name written with an escape names the same key, whose value takes its
// defaults: both paths build the same map.
func TestTheReferencePathReadsAKeyFromItsSpelling(t *testing.T) {
	r := newRegistry()
	c, err := r.codecFor(reflect.TypeFor[map[spelledKey]filledValue]())
	if err != nil {
		t.Fatal(err)
	}
	// a, written as a \u escape (% stands for the backslash).
	esc := strings.ReplaceAll(`"%u0061"`, "%", `\`)
	data := []byte(`{ ` + esc + ` : {}, "b":{},"c\"":{"a":"set"}}`)
	want := map[spelledKey]filledValue{spelledKey(esc): {"zz"}, `"b"`: {"zz"}, `"c\""`: {"set"}}
	var ref, one map[spelledKey]filledValue
	errs := reference(r, c, c.use(), data, DefaultLimits, reflect.ValueOf(&ref).Elem())
	ok := singlePassIn(r, c, c.use(), data, DefaultLimits, reflect.ValueOf(&one).Elem())
	if len(errs) > 0 || !ok || !reflect.DeepEqual(ref, want) || !reflect.DeepEqual(one, want) {
		t.Fatalf("%v %v\nreference   %q\nsingle pass %q", errs, ok, ref, one)
	}
}

// turnKey reads a name as a key that changes at each read, and, with
// turnRefuses, refuses every second read: a key type whose method is not a
// function of its input.
type turnKey string

var (
	turnReads   int
	turnRefuses bool
)

func (k *turnKey) UnmarshalJSON([]byte) error {
	turnReads++
	if turnRefuses && turnReads%2 == 0 {
		return errors.New("refused")
	}
	*k = turnKey(strconv.Itoa(turnReads))
	return nil
}

func (k turnKey) MarshalJSON() ([]byte, error) { return []byte(strconv.Quote(string(k))), nil }

// A key type whose method answers the bytes decodeJSON read the key from
// otherwise the second time (refusing them, or naming another key) leaves
// that value's defaults unfilled on the reference path, as no entry of the
// map is the name's; the body is still bound.
func TestTheReferencePathSkipsAKeyItCannotReadAgain(t *testing.T) {
	r := newRegistry()
	c, err := r.codecFor(reflect.TypeFor[map[turnKey]filledValue]())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { turnReads, turnRefuses = 0, false }()
	for _, refuses := range []bool{true, false} {
		turnReads, turnRefuses = 0, refuses
		var got map[turnKey]filledValue
		errs := reference(r, c, c.use(), []byte(`{"a":{}}`), DefaultLimits, reflect.ValueOf(&got).Elem())
		if len(errs) > 0 || !reflect.DeepEqual(got, map[turnKey]filledValue{"1": {}}) || turnReads != 2 {
			t.Fatalf("refuses %v: %v %q, %d reads", refuses, errs, got, turnReads)
		}
	}
}

// heedless writes a large array by MarshalJSONTo and ignores the encoder's
// errors, so it writes on after the writer under the encoder has failed.
type heedless struct{}

func (heedless) MarshalJSONTo(enc *jsontext.Encoder) error {
	enc.WriteToken(jsontext.BeginArray)
	for range 1024 {
		enc.WriteToken(jsontext.String(strings.Repeat("x", 64)))
	}
	enc.WriteToken(jsontext.EndArray)
	return nil
}

// failingText is a header value that cannot be written.
type failingText struct{}

func (failingText) MarshalText() ([]byte, error) { return nil, errors.New("no text") }
func (*failingText) UnmarshalText([]byte) error  { return nil }

// A body past the buffer whose commit fails (a header that cannot be
// written) is a clean 500, though its encoding writes on after the failure:
// every later write is refused with the same failure, and none reaches the
// connection.
func TestABodyThatWritesOnAfterAFailedCommitIsA500(t *testing.T) {
	type env struct {
		Bad  failingText `header:"X-Bad"`
		Body heedless    `body:"json"`
	}
	limits := DefaultLimits
	limits.MaxResponseBuffer = 1024
	a, err := New(Table{Routes: []Entry{{Path: "/x", Route: Route{Get: Op(http.StatusOK, func(context.Context, *struct{}) (*env, error) {
		return &env{}, nil
	}, Doc{})}}}}, WithLimits(limits), WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusInternalServerError || rec.Header().Get("Content-Type") != ProblemContentType || strings.Contains(rec.Body.String(), "xxxx") {
		t.Fatalf("%d %v %.80s", rec.Code, rec.Header(), rec.Body)
	}
}
