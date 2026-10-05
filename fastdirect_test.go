package geta

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"math"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"
)

// directCount is a named integer with no methods.
type directCount uint16

// directJSON reads itself by UnmarshalJSON, in upper case.
type directJSON string

func (d directJSON) MarshalJSON() ([]byte, error) { return json.Marshal(string(d)) }
func (d *directJSON) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*d = directJSON(strings.ToUpper(s))
	return nil
}

// directPtrText reads and writes itself as text by pointer methods, doubled.
type directPtrText int

func (p *directPtrText) MarshalText() ([]byte, error) { return []byte(strconv.Itoa(int(*p))), nil }
func (p *directPtrText) UnmarshalText(b []byte) error {
	n, err := strconv.Atoi(string(b))
	*p = directPtrText(2 * n)
	return err
}

// directAlias is an alias of a type with text methods on its pointer.
type directAlias = fastKey

// The single pass sets a string, bool, integer, or float value itself
// exactly where encoding/json/v2 reads it by its kind alone; a type with JSON
// or text methods (on it or its pointer, through an alias too), a format
// type, and a sealed type are decoded by v2.
func TestSinglePassSetsPlainLeavesItself(t *testing.T) {
	r := sealedRegistry(t)
	for typ, want := range map[reflect.Type]bool{
		reflect.TypeFor[string]():        true,
		reflect.TypeFor[fastName]():      true,
		reflect.TypeFor[bool]():          true,
		reflect.TypeFor[int]():           true,
		reflect.TypeFor[int8]():          true,
		reflect.TypeFor[int16]():         true,
		reflect.TypeFor[int32]():         true,
		reflect.TypeFor[int64]():         true,
		reflect.TypeFor[uint]():          true,
		reflect.TypeFor[uint8]():         true,
		reflect.TypeFor[uint16]():        true,
		reflect.TypeFor[uint32]():        true,
		reflect.TypeFor[uint64]():        true,
		reflect.TypeFor[float32]():       true,
		reflect.TypeFor[float64]():       true,
		reflect.TypeFor[directCount]():   true,
		reflect.TypeFor[Password]():      true, // a string with format password and no methods v2 reads
		reflect.TypeFor[fastLevel]():     false,
		reflect.TypeFor[directJSON]():    false,
		reflect.TypeFor[directPtrText](): false,
		reflect.TypeFor[directAlias]():   false,
		reflect.TypeFor[fastMail]():      false,
		reflect.TypeFor[fastDay]():       false,
		reflect.TypeFor[Date]():          false,
		reflect.TypeFor[IPv4]():          false,
		reflect.TypeFor[uuid.UUID]():     false,
		reflect.TypeFor[time.Time]():     false,
		reflect.TypeFor[[]byte]():        false,
		reflect.TypeFor[fastShape]():     false,
	} {
		c, err := r.codecFor(typ)
		if err != nil {
			t.Fatal(err)
		}
		if c.direct != want {
			t.Errorf("%s: direct %v, want %v", typ, c.direct, want)
		}
	}
	// The types v2 decodes by their methods are read by them.
	type methods struct {
		J directJSON    `json:"j"`
		P directPtrText `json:"p"`
		A directAlias   `json:"a"`
	}
	ok, _ := agreeValue(t, r, reflect.TypeFor[methods](), []byte(`{"j":"ab","p":"21","a":"cd"}`), DefaultLimits)
	var got methods
	c, _ := r.codecFor(reflect.TypeFor[methods]())
	if !ok || !singlePassIn(r, c, c.use(), []byte(`{"j":"ab","p":"21","a":"cd"}`), DefaultLimits, reflect.ValueOf(&got).Elem()) ||
		got != (methods{J: "AB", P: 42, A: "CD"}) {
		t.Fatalf("%v %#v", ok, got)
	}
}

// directLoud is read by an unmarshaler of its pointer type in the options.
type directLoud string

// directQuiet is read by an unmarshaler of an interface its pointer
// implements.
type directQuiet string

func (*directQuiet) quiet() {}

type quieter interface{ quiet() }

type directOptioned struct {
	L directLoud  `json:"l"`
	Q directQuiet `json:"q"`
	S string      `json:"s"`
}

// A type an unmarshaler in the body options applies to, as v2 matches it, is
// decoded by v2 and so by the unmarshaler.
func TestSinglePassYieldsToUnmarshalers(t *testing.T) {
	r := newRegistry()
	r.decOpts = json.JoinOptions(r.decOpts, json.WithUnmarshalers(json.JoinUnmarshalers(
		json.UnmarshalFromFunc(func(dec *jsontext.Decoder, v *directLoud) error {
			tok, err := dec.ReadToken()
			*v = directLoud(strings.ToUpper(tok.String()))
			return err
		}),
		json.UnmarshalFromFunc(func(dec *jsontext.Decoder, v quieter) error {
			tok, err := dec.ReadToken()
			p, ok := v.(*directQuiet)
			if !ok {
				return errors.ErrUnsupported
			}
			*p = directQuiet(strings.ToLower(tok.String()))
			return err
		}))))
	r.unmarshaled = append(r.unmarshaled, reflect.TypeFor[*directLoud](), reflect.TypeFor[quieter]())
	c, err := r.codecFor(reflect.TypeFor[directOptioned]())
	if err != nil {
		t.Fatal(err)
	}
	if c.fields[0].c.direct || c.fields[1].c.direct || !c.fields[2].c.direct {
		t.Fatalf("direct: %v %v %v", c.fields[0].c.direct, c.fields[1].c.direct, c.fields[2].c.direct)
	}
	body := []byte(`{"l":"ab","q":"CD","s":"Ef"}`)
	if ok, _ := agreeValue(t, r, c.t, body, DefaultLimits); !ok {
		t.Fatal("refused")
	}
	var got directOptioned
	if !singlePassIn(r, c, c.use(), body, DefaultLimits, reflect.ValueOf(&got).Elem()) ||
		got != (directOptioned{L: "AB", Q: "cd", S: "Ef"}) {
		t.Fatalf("%#v", got)
	}
}

type directBounds struct {
	V   string       `json:"v"`
	S   *string      `json:"s,omitzero"`
	N   *fastName    `json:"n,omitzero" schema:"maxLength=3"`
	B   *bool        `json:"b,omitzero"`
	I   *int         `json:"i,omitzero"`
	I8  *int8        `json:"i8,omitzero"`
	I32 *int32       `json:"i32,omitzero"`
	I64 *int64       `json:"i64,omitzero"`
	U   *uint        `json:"u,omitzero"`
	U8  *uint8       `json:"u8,omitzero"`
	U32 *uint32      `json:"u32,omitzero"`
	U64 *uint64      `json:"u64,omitzero"`
	C   *directCount `json:"c,omitzero"`
	F32 *float32     `json:"f32,omitzero"`
	F64 *float64     `json:"f64,omitzero"`
}

// The leaves the single pass sets itself take and refuse what v2 takes and
// refuses, at the bounds of each type, and hold the value v2 sets.
func TestSinglePassAgreesOnPlainLeafBounds(t *testing.T) {
	r := newRegistry()
	c, err := r.codecFor(reflect.TypeFor[directBounds]())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range c.fields {
		if !f.c.direct {
			t.Fatalf("%s is not set directly", f.json)
		}
	}
	for body, valid := range map[string]bool{
		`"i8":-128`: true, `"i8":127`: true, `"i8":128`: false, `"i8":-129`: false,
		`"i32":-2147483648`: true, `"i32":2147483647`: true, `"i32":2147483648`: false, `"i32":-2147483649`: false,
		`"u32":4294967295`: true, `"u32":4294967296`: false,
		`"i64":9223372036854775807`: true, `"i64":-9223372036854775808`: true,
		`"i64":9223372036854775808`: false, `"i64":-9223372036854775809`: false,
		`"i":9223372036854775807`: true, `"i":-9223372036854775808`: true, `"i":9223372036854775808`: false,
		`"i":-0`: true, `"i":1.0`: false, `"i":1e2`: false, `"i":"1"`: false,
		`"u":18446744073709551615`: true, `"u64":18446744073709551615`: true,
		`"u64":18446744073709551616`: false, `"u64":99999999999999999999`: false,
		`"u":0`: true, `"u":-0`: false, `"u":-1`: false, `"u8":-0`: false, `"u8":255`: true, `"u8":256`: false,
		`"c":65535`: true, `"c":65536`: false,
		`"f32":3.4028234663852886e38`: true, `"f32":-3.4028235e38`: true, `"f32":3.4028236e38`: false, `"f32":1e39`: false,
		`"f32":1e-46`: true, `"f32":1.401298464324817e-45`: true,
		`"f64":1.7976931348623157e308`: true, `"f64":1e400`: false, `"f64":-1e400`: false,
		`"f64":5e-324`: true, `"f64":1e-400`: true, `"f64":-0`: true, `"f64":-0.0e5`: true,
		jsonEsc(`"s":"%u00e9\n\t\"\\\/\b\f\r"`): true, jsonEsc(`"s":"%ud83d%ude00"`): true, `"s":"a\u0000b"`: true,
		`"s":"é😀"`:     true,
		`"s":"\ud800"`: false, `"s":"\udc00\ud800"`: false, "\"s\":\"\xff\"": false, "\"s\":\"\x01\"": false,
		`"s":"` + strings.Repeat("x", 300) + `"`: true,
		jsonEsc(`"n":"%u00e9%u00e9%u00e9"`):      true, `"n":"ééé"`: true, `"n":"abcd"`: false,
		`"s":null`: false, `"b":true`: true, `"b":false`: true, `"b":"true"`: false, `"b":1`: false,
	} {
		data := []byte(`{"v":"x",` + body + `}`)
		if ok, _ := agreeValue(t, r, c.t, data, DefaultLimits); ok != valid {
			t.Errorf("%s: accepted %v, want %v", data, ok, valid)
		}
	}
	if ok, _ := agreeValue(t, r, c.t, []byte(`{"v":null}`), DefaultLimits); ok {
		t.Error("null accepted for a string")
	}
	// Values DeepEqual does not tell apart: the sign of zero, and a float32
	// that underflows.
	var got directBounds
	data := []byte(jsonEsc(`{"v":"x","f64":-0,"f32":1e-46,"i":-0,"s":"%u00e9\n\"\\"}`))
	if !singlePassIn(r, c, c.use(), data, DefaultLimits, reflect.ValueOf(&got).Elem()) {
		t.Fatal("refused")
	}
	if !math.Signbit(*got.F64) || *got.F32 != 0 || math.Signbit(float64(*got.F32)) || *got.I != 0 || *got.S != "é\n\"\\" {
		t.Fatalf("%v %v %v %q", *got.F64, *got.F32, *got.I, *got.S)
	}
	var want directBounds
	if err := json.Unmarshal(data, &want, r.decOpts); err != nil || math.Signbit(*want.F64) != math.Signbit(*got.F64) {
		t.Fatalf("%v %v", err, *want.F64)
	}
}

// jsonEsc writes each %u in s as the JSON escape backslash-u.
func jsonEsc(s string) string { return strings.ReplaceAll(s, "%u", `\`+"u") }

// Interned strings are equal to the bytes they were made from, whichever
// strings share a slot.
func TestInternKeepsStringsApart(t *testing.T) {
	f := new(fastDecoder)
	seen := map[string]bool{}
	for i := range 4096 {
		s := strconv.Itoa(i*7919) + strings.Repeat("x", i%300)
		if got := f.intern([]byte(s)); got != s {
			t.Fatalf("intern(%q) = %q", s, got)
		}
		seen[s] = true
	}
	for s := range seen {
		if got := f.intern([]byte(s)); got != s {
			t.Fatalf("intern(%q) = %q", s, got)
		}
	}
}

// A decoder returned to the pool keeps no string of the request it read.
func TestReleaseClearsInternedStrings(t *testing.T) {
	r := newRegistry()
	c, err := r.codecFor(reflect.TypeFor[directOptioned]())
	if err != nil {
		t.Fatal(err)
	}
	f := new(fastDecoder)
	f.limits, f.opts = DefaultLimits, r.decOpts
	f.body.WriteString(`{"l":"secret-one","q":"secret-two","s":"secret-three"}`)
	var got directOptioned
	if !f.decode(c, c.use(), reflect.ValueOf(&got).Elem()) || len(f.touched) == 0 {
		t.Fatalf("%#v %v", got, f.touched)
	}
	f.release()
	if f.interned != ([256][2]string{}) || len(f.touched) != 0 {
		t.Fatal("interned strings outlive the request")
	}
}

// nestedLate is a sealed value nested depth times in squares whose
// discriminator comes last.
func nestedLate(depth int) []byte {
	return []byte(`{"others":[],"main":` + strings.Repeat(`{"side":1,"inner":`, depth) + `{"r":1,"kind":"circle"}` +
		strings.Repeat(`,"kind":"square"}`, depth) + `}`)
}

// scannedFor reads data in one pass and returns whether it was accepted and
// the bytes the look-ahead stepped over.
func scannedFor(t testing.TB, r *registry, data []byte) (bool, int) {
	c, err := r.codecFor(reflect.TypeFor[fastScene]())
	if err != nil {
		t.Fatal(err)
	}
	f := new(fastDecoder)
	f.limits, f.opts = DefaultLimits, r.decOpts
	f.body.Write(data)
	var got fastScene
	return f.decode(c, c.use(), reflect.ValueOf(&got).Elem()), f.ahead.scanned
}

// The look-ahead for sealed values nested in one another, each with its
// discriminator last, steps over the body a bounded number of times, not
// once per level.
func TestLateDiscriminatorLookAheadIsLinear(t *testing.T) {
	r := sealedRegistry(t)
	for _, depth := range []int{1, 100, 200} {
		data := nestedLate(depth)
		agreeIn[fastScene](t, r, data, DefaultLimits)
		ok, n := scannedFor(t, r, data)
		if !ok {
			t.Fatalf("depth %d refused", depth)
		}
		if n > 2*len(data) {
			t.Errorf("depth %d: the look-ahead stepped over %d bytes of a %d-byte body", depth, n, len(data))
		}
	}
}

// wholeOptions are r's body options with fastShapes read by
// sealedReader.whole alone, as Sealed read them before reading from the
// decoder itself.
func wholeOptions() json.Options {
	types := map[string]reflect.Type{}
	for _, c := range fastShapes.cases {
		types[c.tag] = c.t
	}
	s := &sealedReader{t: fastShapes.t, disc: fastShapes.disc, types: types}
	return json.JoinOptions(json.RejectUnknownMembers(true), json.WithUnmarshalers(json.JoinUnmarshalers(timeUnmarshaler,
		json.UnmarshalFromFunc(func(dec *jsontext.Decoder, v *fastShape) error {
			x, err := s.whole(dec, nil)
			if err == nil {
				*v = x.(fastShape)
			}
			return err
		}))))
}

// agreeWithWhole decodes data into a fastScene with Sealed's unmarshaler and
// with whole alone, and fails unless both take it to the same value or
// refuse it with the same error, word for word.
func agreeWithWhole(t *testing.T, opts, whole json.Options, data []byte) {
	t.Helper()
	var got, want fastScene
	errGot := json.Unmarshal(data, &got, opts)
	errWant := json.Unmarshal(data, &want, whole)
	switch {
	case (errGot == nil) != (errWant == nil):
		t.Fatalf("%q: error %v, want %v", data, errGot, errWant)
	case errGot != nil && errGot.Error() != errWant.Error():
		t.Fatalf("%q: error\n%v\nwant\n%v", data, errGot, errWant)
	case errGot == nil && !reflect.DeepEqual(got, want):
		t.Fatalf("%q: %#v, want %#v", data, got, want)
	}
}

// sealedErrorSeeds are bodies whole refuses at each depth, each way: a member
// of the wrong type, an unknown member, a type's own method refusing its
// value, and a discriminator missing, not a string, null, or unknown.
var sealedErrorSeeds = []string{
	`{"main":{"r":"x","kind":"circle"},"others":[]}`,
	`{"main":{"kind":"circle","r":"x"},"others":[]}`,
	`{"main":{"side":1,"inner":{"r":"x","kind":"circle"},"kind":"square"},"others":[]}`,
	`{"main":{"side":1,"inner":{"side":1,"inner":{"kind":"circle","r":true},"kind":"square"},"kind":"square"},"others":[]}`,
	`{"main":{"side":1,"inner":{"bogus":1,"kind":"circle"},"kind":"square"},"others":[]}`,
	`{"main":{"side":1,"inner":{"r":1,"leaf":{"s":"a","nul":1},"kind":"circle"},"kind":"square"},"others":[]}`,
	`{"main":{"side":1,"inner":{"r":1,"leaf":{"s":"a","lv":"mid"},"kind":"circle"},"kind":"square"},"others":[]}`,
	`{"main":{"side":1,"inner":{"r":1},"kind":"square"},"others":[]}`,
	`{"main":{"side":1,"inner":{"r":1,"kind":2},"kind":"square"},"others":[]}`,
	`{"main":{"side":1,"inner":{"r":1,"kind":null},"kind":"square"},"others":[]}`,
	`{"main":{"side":1,"inner":{"r":1,"kind":"hexagon"},"kind":"square"},"others":[]}`,
	`{"main":{"side":1,"many":[{"kind":"circle","r":1},{"kind":"square","side":300}],"kind":"square"},"others":[]}`,
	`{"main":{"kind":"square","side":1,"ptr":{"r":1,"kind":"circle","x":[1,{"y":2}]}},"others":[]}`,
	`{"main":{"r":1,"kind":"circle"},"others":[{"kind":"square","side":1,"inner":{"kind":"döt","r":1}}]}`,
	`{"main":{"r":1,"kind":"circle"},"others":[],"by":{"a":{"side":"1","kind":"square"}}}`,
	`{"main":{"side":1,"inner":{"r":1,"kind":"circle"},"kind":"square","inner":{}},"others":[]}`,
	`{"main":{"side":1,"inner":{"r":1,"kind":"circle"} "kind":"square"},"others":[]}`,
	`{"main":{"side":1,"inner":[],"kind":"square"},"others":[]}`,
	`{"main":{"side":1,"inner":"circle","kind":"square"},"others":[]}`,
}

// Sealed's unmarshaler takes and refuses what reading each sealed object
// whole took and refused, with the same errors, wherever the discriminator
// is.
func TestSealedReaderAgreesWithWhole(t *testing.T) {
	opts, whole := sealedRegistry(t).decOpts, wholeOptions()
	for _, s := range append(append(append([]string{}, sealedSeeds...), sealedErrorSeeds...), nullSealedSeeds...) {
		agreeWithWhole(t, opts, whole, []byte(s))
	}
	for s := range lateSealedSeeds {
		agreeWithWhole(t, opts, whole, []byte(s))
	}
	for _, depth := range []int{1, 2, 50} {
		agreeWithWhole(t, opts, whole, nestedLate(depth))
	}
}

func FuzzSealedReaderAgreesWithWhole(f *testing.F) {
	for _, s := range append(append([]string{}, sealedSeeds...), sealedErrorSeeds...) {
		f.Add([]byte(s))
	}
	for s := range lateSealedSeeds {
		f.Add([]byte(s))
	}
	opts, whole := sealedRegistry(f).decOpts, wholeOptions()
	f.Fuzz(func(t *testing.T, data []byte) { agreeWithWhole(t, opts, whole, data) })
}

// The reference path reads sealed values nested in one another, each with
// its discriminator last, in time linear in the body: Sealed's unmarshaler
// once read each one whole, then again, at every level. A body the single
// pass refuses only for an error at its very end is refused by the reference
// path in linear time too.
func TestReferencePathReadsNestedSealedValuesInLinearTime(t *testing.T) {
	r := sealedRegistry(t)
	c, err := r.codecFor(reflect.TypeFor[fastScene]())
	if err != nil {
		t.Fatal(err)
	}
	// The bytes allocated stand for the work, and unlike time do not depend
	// on what else the machine runs: the quadratic reads allocated copies
	// at every level.
	cost := func(data []byte, valid bool) uint64 {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		var v fastScene
		errs := reference(r, c, c.use(), data, DefaultLimits, reflect.ValueOf(&v).Elem())
		runtime.ReadMemStats(&after)
		if (len(errs) == 0) != valid {
			t.Fatalf("%d bytes: %v", len(data), errs)
		}
		return after.TotalAlloc - before.TotalAlloc
	}
	invalid := func(depth int) []byte {
		b := nestedLate(depth)
		return append(b[:len(b)-1:len(b)-1], `,"bogus":1}`...)
	}
	for _, valid := range []bool{true, false} {
		body := nestedLate
		if !valid {
			body = invalid
		}
		small, large := cost(body(100), valid), cost(body(400), valid)
		t.Logf("valid %v: depth 100 allocated %d bytes, depth 400 %d", valid, small, large)
		// Four times the body: linear is about 4, quadratic about 16.
		if large > 8*small {
			t.Errorf("valid %v: depth 400 allocated %d bytes, depth 100 %d", valid, large, small)
		}
	}
}

func BenchmarkReferencePathNestedLate(b *testing.B) {
	r := sealedRegistry(b)
	c, err := r.codecFor(reflect.TypeFor[fastScene]())
	if err != nil {
		b.Fatal(err)
	}
	valid := nestedLate(200)
	invalid := append(valid[:len(valid)-1:len(valid)-1], `,"bogus":1}`...)
	for _, data := range [][]byte{valid, invalid} {
		name := "valid"
		if len(data) != len(valid) {
			name = "invalid"
		}
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for b.Loop() {
				var v fastScene
				if errs := reference(r, c, c.use(), data, DefaultLimits, reflect.ValueOf(&v).Elem()); (len(errs) == 0) != (name == "valid") {
					b.Fatal(errs)
				}
			}
		})
	}
}

func BenchmarkSinglePassNestedLate(b *testing.B) {
	r := sealedRegistry(b)
	data := nestedLate(200)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		if ok, _ := scannedFor(b, r, data); !ok {
			b.Fatal("refused")
		}
	}
}
