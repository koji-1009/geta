package geta

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
	"uuid"
)

type fastLevel int

func (l fastLevel) MarshalText() ([]byte, error) { return []byte([]string{"low", "high"}[l]), nil }
func (l *fastLevel) UnmarshalText(b []byte) error {
	switch string(b) {
	case "low":
		*l = 0
	case "high":
		*l = 1
	default:
		return errFastLevel
	}
	return nil
}

var errFastLevel = errors.New("bad level")

type fastName string

type fastLeaf struct {
	S   string             `json:"s" schema:"minLength=1,maxLength=8,pattern=^[a-z]+$"`
	E   *string            `json:"e,omitzero" schema:"enum=a|b|\u00e9"`
	D   *string            `json:"d,omitzero" schema:"format=date"`
	I8  *int8              `json:"i8,omitzero" schema:"multipleOf=2"`
	U16 *uint16            `json:"u16,omitzero" schema:"exclusiveMinimum=1"`
	U   *uint              `json:"u,omitzero"`
	F32 *float32           `json:"f32,omitzero" schema:"exclusiveMaximum=100,multipleOf=0.5"`
	F64 *float64           `json:"f64,omitzero"`
	B   *bool              `json:"b,omitzero"`
	Lv  *fastLevel         `json:"lv,omitzero"`
	T   *time.Time         `json:"t,omitzero"`
	ID  *uuid.UUID         `json:"id,omitzero"`
	Raw *[]byte            `json:"raw,omitzero" schema:"maxLength=12"`
	M   *map[fastName]int8 `json:"m,omitzero"`
	// Bounds float64 cannot decide: one no float32 holds, and integers past
	// 2^53.
	F32b *float32    `json:"f32b,omitzero" schema:"maximum=0.1,exclusiveMinimum=-0.1"`
	Big  *int64      `json:"big,omitzero" schema:"maximum=9007199254740992,multipleOf=3"`
	O    *fastOpaque `json:"o,omitzero"`
	// Integers with no bounds but their width, a named one too.
	I32 *int32       `json:"i32,omitzero"`
	U64 *uint64      `json:"u64,omitzero"`
	Cnt *directCount `json:"cnt,omitzero"`
	// The format types, and a map whose key reads itself.
	Fd   *Date                           `json:"fd,omitzero"`
	Ft   *TimeOfDay                      `json:"ft,omitzero"`
	Fdu  *Duration                       `json:"fdu,omitzero"`
	Fe   *fastMail                       `json:"fe,omitzero"`
	Fh   *fastMail                       `json:"fh,omitzero" schema:"maxLength=12"`
	Fday *fastDay                        `json:"fday,omitzero"`
	F4   *IPv4                           `json:"f4,omitzero"`
	F6   *IPv6                           `json:"f6,omitzero"`
	Fp   *JSONPointer                    `json:"fp,omitzero"`
	Frp  *RelativeJSONPointer            `json:"frp,omitzero"`
	Fpw  *Password                       `json:"fpw,omitzero" schema:"minLength=2"`
	Mk   *map[fastKey]int8               `json:"mk,omitzero"`
	Mkp  *map[fastPtrKey]int8            `json:"mkp,omitzero"`
	Mfmt *map[string]RelativeJSONPointer `json:"mfmt,omitzero"`
	// Strings held to a format.
	Sdt *string `json:"sdt,omitzero" schema:"format=date-time"`
	Sip *string `json:"sip,omitzero" schema:"format=ipv6"`
	// Number enums, compared by value as written.
	Ne  *int64   `json:"ne,omitzero" schema:"enum=-1|3|9007199254740993"`
	Nfe *float64 `json:"nfe,omitzero" schema:"enum=0.5|1e1"`
	// Nullables: absent, null, or a value.
	Nl Nullable[string]    `json:"nl,omitzero" schema:"maxLength=3"`
	Nr Nullable[fastInner] `json:"nr,omitzero"`
	Nn *[]Nullable[int8]   `json:"nn,omitzero"`
	// Defaults, filled where a request leaves the member out: here, in a
	// map's values, in a slice's elements, and in a Nullable.
	Dn  int                        `json:"dn" schema:"default=7,minimum=1"`
	Dlv fastLevel                  `json:"dlv" schema:"default=high"`
	Dm  *map[fastKey]fastDefaulted `json:"dm,omitzero"`
	Ds  *[]fastDefaulted           `json:"ds,omitzero"`
	Dnl Nullable[fastDefaulted]    `json:"dnl,omitzero"`
	// And behind a pointer, in a map whose key is a named string, and in
	// Nullable elements.
	Dp  *fastDefaulted              `json:"dp,omitzero"`
	Dk  *map[fastName]fastDefaulted `json:"dk,omitzero"`
	Dnn *[]Nullable[fastDefaulted]  `json:"dnn,omitzero"`
	// Types with JSON methods of their own and no declared schema, which
	// take any JSON value, null among them: a pointer member given null is
	// nil, and an element or a map value is read by the type's methods, or by
	// encoding/json/v2 as a struct.
	Nul  *fastNullish            `json:"nul,omitzero"`
	Nuls *[]fastNullish          `json:"nuls,omitzero"`
	Nulm *map[string]fastNullish `json:"nulm,omitzero"`
	Os   *[]fastOpaque           `json:"os,omitzero"`
}

// fastNullish reads null as a state of its own and a string as its text,
// and refuses every other value.
type fastNullish struct {
	null bool
	s    string
}

func (n fastNullish) MarshalJSON() ([]byte, error) {
	if n.null {
		return []byte(`null`), nil
	}
	return json.Marshal(n.s)
}

func (n *fastNullish) UnmarshalJSON(b []byte) error {
	switch {
	case string(b) == "null":
		n.null = true
		return nil
	case len(b) > 0 && b[0] == '"':
		return json.Unmarshal(b, &n.s)
	}
	return errors.New("neither null nor a string")
}

// fastDefaulted has a member with a default.
type fastDefaulted struct {
	A string `json:"a" schema:"default=zz"`
	B *int   `json:"b,omitzero"`
}

// fastMail is an application's FormatType of format email, which takes
// any text with an @ between two non-empty parts.
type fastMail string

func (fastMail) SchemaFormat() string           { return "email" }
func (m fastMail) MarshalText() ([]byte, error) { return []byte(m), nil }
func (m *fastMail) UnmarshalText(b []byte) error {
	if i := bytes.LastIndexByte(b, '@'); i < 1 || i == len(b)-1 {
		return errFastLevel
	}
	*m = fastMail(b)
	return nil
}

// fastDay is an application's FormatType of geta's own format date, which
// takes "today" besides a date: its UnmarshalText, not geta's check of the
// format, holds a request to it.
type fastDay string

func (fastDay) SchemaFormat() string           { return "date" }
func (d fastDay) MarshalText() ([]byte, error) { return []byte(d), nil }
func (d *fastDay) UnmarshalText(b []byte) error {
	if _, ok := parseDate(string(b)); !ok && string(b) != "today" {
		return errFastLevel
	}
	*d = fastDay(b)
	return nil
}

// fastKey is a string key that reads itself by UnmarshalText, in upper
// case, refusing the empty name: encoding/json/v2 reads it by that method,
// so "a" and "A" are one key. It writes itself as it holds itself.
type fastKey string

func (k *fastKey) MarshalText() ([]byte, error) { return []byte(*k), nil }

func (k *fastKey) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		return errFastLevel
	}
	*k = fastKey(strings.ToUpper(string(b)))
	return nil
}

// fastPtrKey writes itself by a pointer method and reads by UnmarshalText.
type fastPtrKey string

func (k *fastPtrKey) MarshalText() ([]byte, error) { return []byte("k-" + string(*k)), nil }
func (k *fastPtrKey) UnmarshalText(b []byte) error {
	s, ok := strings.CutPrefix(string(b), "k-")
	if !ok {
		return errFastLevel
	}
	*k = fastPtrKey(s)
	return nil
}

// fastOpaque has JSON methods of its own; v2 still reads it as a struct.
type fastOpaque struct {
	A int `json:"a"`
}

func (fastOpaque) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

// FastEmbedded is exported so that its fields can be set through the
// embedding.
type FastEmbedded struct {
	X *int `json:"x,omitzero"`
}

type fastBody struct {
	FastEmbedded
	Leaf  fastLeaf     `json:"leaf"`
	List  []fastLeaf   `json:"list" schema:"minItems=1,maxItems=3"`
	Words *[]string    `json:"words,omitzero" schema:"maxItems=4"`
	Next  *fastBody    `json:"next,omitzero"`
	Grid  *[][]float64 `json:"grid,omitzero"`
	Many  *fastMany    `json:"many,omitzero"`
	Set   *[]fastLeaf  `json:"set,omitzero" schema:"uniqueItems=true"`
	Nums  *[]float32   `json:"nums,omitzero" schema:"uniqueItems=true,maxItems=4"`
	Ints  *[]int64     `json:"ints,omitzero" schema:"uniqueItems=true"`
	// Keywords of the elements (items.), nested too.
	Picks *[]string `json:"picks,omitzero" schema:"items.enum=x|y"`
	Rows  *[][]int8 `json:"rows,omitzero" schema:"items.maxItems=2,items.uniqueItems=true,items.items.minimum=0"`
	// Keywords of a map's values (additionalProperties.), nested too.
	Tags *map[string]string `json:"tags,omitzero" schema:"additionalProperties.enum=a|bc"`
	Bag  *map[string][]int8 `json:"bag,omitzero" schema:"additionalProperties.items.minimum=0"`
}

// fastMany has enough fields that names are looked up in a map.
type fastMany struct {
	A *int `json:"A,omitzero"`
	B *int `json:"B,omitzero"`
	C *int `json:"C,omitzero"`
	D *int `json:"D,omitzero"`
	E *int `json:"E,omitzero"`
	F *int `json:"F,omitzero"`
	G *int `json:"G,omitzero"`
	H *int `json:"H,omitzero"`
	I *int `json:"I,omitzero"`
	J *int `json:"J,omitzero"`
}

// reference runs the two-pass path: parse, check, then encoding/json/v2
// with the registry's options.
// It is the body plan's own reference path, which a request reaches.
func reference(r *registry, c *codec, use *schema, data []byte, limits Limits, dst reflect.Value) []Violation {
	b := &bodyPlan{c: c, use: use, opts: r.decOpts, defaults: defaultsUnder(c)}
	return b.reference(jsontext.NewDecoder(bytes.NewReader(data)), data, dst, limits)
}

func singlePass(c *codec, use *schema, data []byte, limits Limits, dst reflect.Value) bool {
	return singlePassIn(newRegistry(), c, use, data, limits, dst)
}

// singlePassIn is singlePass with r's options, as a body plan carries them.
func singlePassIn(r *registry, c *codec, use *schema, data []byte, limits Limits, dst reflect.Value) bool {
	f := getFastDecoder(limits)
	defer f.release()
	f.opts = r.decOpts
	f.body.Write(data)
	return f.decode(c, use, dst)
}

// The single pass accepts exactly what the reference path accepts and
// produces the same value.
// It reports whether the body was accepted.
func agree[T any](t *testing.T, data []byte, limits Limits) bool {
	t.Helper()
	return agreeIn[T](t, newRegistry(), data, limits)
}

// agreeIn is agree with the sealed types r declares.
func agreeIn[T any](t *testing.T, r *registry, data []byte, limits Limits) bool {
	t.Helper()
	c, err := r.codecFor(reflect.TypeFor[T]())
	if err != nil {
		t.Fatal(err)
	}
	if !fastEligible(c, map[*codec]bool{}) {
		t.Fatalf("%T is not eligible", *new(T))
	}
	want, got := new(T), new(T)
	errs := reference(r, c, c.use(), data, limits, reflect.ValueOf(want).Elem())
	ok := singlePassIn(r, c, c.use(), data, limits, reflect.ValueOf(got).Elem())
	switch {
	case ok && len(errs) > 0:
		t.Fatalf("single pass accepted %q, which the reference refuses: %v", data, errs)
	case !ok && len(errs) == 0:
		t.Fatalf("single pass refused %q, which the reference accepts", data)
	case ok && !reflect.DeepEqual(want, got):
		t.Fatalf("values differ for %q:\nreference %#v\nsingle    %#v", data, want, got)
	}
	return ok
}

var fastSeeds = []string{
	`{"leaf":{"s":"abc"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","e":"\u00e9","d":"2026-02-28","i8":-128,"u16":2,"u":0,"f32":99.5,"f64":-1e-7,"b":false,` +
		`"lv":"high","t":"2026-07-18T09:30:00.5+09:00","id":"123e4567-e89b-12d3-a456-426614174000","raw":"AAEC","m":{"k":1,"\u006b2":-1},` +
		`"fd":"2024-02-29","ft":"08:59:60.123456789012+09:00","fdu":"P1Y2M3DT4H5M6S","fe":"a..b@docomo.ne.jp","fh":"é@x",` +
		`"fday":"today","f4":"192.000.002.001","f6":"::ffff:192.0.2.1","fp":"/a~1b/~0","frp":"0#","fpw":"été","mk":{"a":1,"b":2},"mkp":{"k-a":1},"mfmt":{"x":"1/a"}},` +
		`"list":[{"s":"a"},{"s":"b"}],"words":[],"grid":[[1,2.5],[]],"x":3,"many":{"A":1,"J":2}}`,
	`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"next":{"leaf":{"s":"z"},"list":[{"s":"y"}]}}`,
	`{"leaf":{"s":"abc","raw":"AAE=","t":"2026-07-18t09:30:00z","sdt":"1998-12-31T23:59:60Z","sip":"::1"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","id":"123E4567-E89B-12D3-A456-426614174000"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","e":"b"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"set":[{"s":"a"}, {"b":true,"s":"a"}],"nums":[1,2.5]}`,
	`{"leaf":{"s":"abc","f32b":0.1,"big":9007199254740990,"o":{"a":1}},"list":[{"s":"a"}],"ints":[9007199254740992,9007199254740993]}`,
	// Invalid from here on.
	`{"leaf":{"s":"abc","f32b":0.10000000000000001},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","f32b":-0.1},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","big":9007199254740993},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","big":1500000001},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","o":{"a":1,"bogus":2}},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"ints":[9007199254740993,9007199254740993]}`,
	`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"set":[{"s":"a","b":true}, {"b":true,"s":"a"}]}`,
	`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"nums":[2.5,1,25e-1]}`,
	`{"leaf":{"s":"ABC"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc"},"list":[]}`,
	`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"x":null}`,
	`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"X":1}`,
	`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"leaf":{"s":"abc"}}`,
	`{"leaf":{"s":"abc","i8":3},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","i8":128},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","u":-0},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","u16":1.0},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","f32":1e40},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","t":"2026-07-18T9:30:00Z"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","t":"2026-07-18T09:30:00+24:00"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","t":"1998-12-31T23:59:60Z"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","t":"2026-07-18T09:30:00,5Z"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","sdt":"1990-12-31T15:59:59-24:00"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","sip":"x"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","raw":"AA\nEC"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","lv":"mid"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","m":{"a":1,"a":2}},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc"},"list":[{"s":"a"}]} x`,
	`{"leaf":{"s":"ab\u0063"},"list":[{"s":"a"}],"z":0}`,
	`{"leaf":{"s":"abc"},"list":[{"s":"a"},{"s":"a"},{"s":"a"},{"s":"a"}]}`,
	`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"grid":[[[1]]]}`,
	`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"many":{"K":1}}`,
	// The format types refuse what their grammars do not hold.
	`{"leaf":{"s":"abc","fd":"2023-02-29"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","ft":"22:59:60Z"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","fdu":"P1Y2D"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","fe":"a..b@"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","fh":"abcdefghi@klm"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","fday":"yesterday"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","f4":"1.2.3.256"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","f6":"fe80::1%eth0"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","fday":"2023-02-29"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","fe":"a@b","fi":"https://h/"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","fh":"@ab"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","fp":"a"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","frp":"01#"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","fpw":"x"},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","mk":{"a":1,"A":2}},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","mk":{"":1}},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","mkp":{"a":1}},"list":[{"s":"a"}]}`,
	`{"leaf":{"s":"abc","mfmt":{"x":"a"}},"list":[{"s":"a"}]}`,
	`[]`, `null`, `"x"`, ``, `{`,
}

// keywordSeeds exercise number enums, each with whether it is valid.
var keywordSeeds = map[string]bool{
	`{"leaf":{"s":"abc","ne":-1,"nfe":0.5},"list":[{"s":"a"}]}`:                                   true,
	`{"leaf":{"s":"abc","ne":9007199254740993,"nfe":10},"list":[{"s":"a"}]}`:                      true,
	`{"leaf":{"s":"abc","nfe":1.0e1},"list":[{"s":"a"}]}`:                                         true,
	`{"leaf":{"s":"abc","nfe":0.50},"list":[{"s":"a"}]}`:                                          true,
	`{"leaf":{"s":"abc","ne":9007199254740992},"list":[{"s":"a"}]}`:                               false,
	`{"leaf":{"s":"abc","ne":2},"list":[{"s":"a"}]}`:                                              false,
	`{"leaf":{"s":"abc","ne":3.0},"list":[{"s":"a"}]}`:                                            false,
	`{"leaf":{"s":"abc","nfe":0.50000000000000001},"list":[{"s":"a"}]}`:                           false,
	`{"leaf":{"s":"abc","nfe":"0.5"},"list":[{"s":"a"}]}`:                                         false,
	`{"leaf":{"s":"abc","nl":null,"nr":null,"nn":[null,1]},"list":[{"s":"a"}]}`:                   true,
	`{"leaf":{"s":"abc","nl":"abc","nr":{"a":"x"},"nn":[]},"list":[{"s":"a"}]}`:                   true,
	`{"leaf":{"nn":[1,null,-128],"s":"abc","nr":{"a":""}},"list":[{"s":"a"}]}`:                    true,
	`{"leaf":{"s":"abc","nl":"abcd"},"list":[{"s":"a"}]}`:                                         false,
	`{"leaf":{"s":"abc","nr":{}},"list":[{"s":"a"}]}`:                                             false,
	`{"leaf":{"s":"abc","nr":{"a":null}},"list":[{"s":"a"}]}`:                                     false,
	`{"leaf":{"s":"abc","nn":null},"list":[{"s":"a"}]}`:                                           false,
	`{"leaf":{"s":"abc","nn":[128]},"list":[{"s":"a"}]}`:                                          false,
	`{"leaf":{"s":"abc","nl":nul},"list":[{"s":"a"}]}`:                                            false,
	`{"leaf":{"s":"abc","dn":2,"dlv":"low"},"list":[{"s":"a"}]}`:                                  true,
	`{"leaf":{"s":"abc","dm":{"x":{},"y":{"a":"q","b":1}},"ds":[{},{"b":2}]},"list":[{"s":"a"}]}`: true,
	`{"leaf":{"s":"abc","dnl":{"b":3}},"list":[{"s":"a"}]}`:                                       true,
	`{"leaf":{"s":"abc","dnl":null,"ds":[]},"list":[{"s":"a"}]}`:                                  true,
	`{"leaf":{"s":"abc","dn":0},"list":[{"s":"a"}]}`:                                              false,
	`{"leaf":{"s":"abc","dn":null},"list":[{"s":"a"}]}`:                                           false,
	`{"leaf":{"s":"abc","dlv":"mid"},"list":[{"s":"a"}]}`:                                         false,
	`{"leaf":{"s":"abc","ds":[{"a":null}]},"list":[{"s":"a"}]}`:                                   false,
	// Defaults behind a pointer, under a named key, in Nullable elements.
	`{"leaf":{"s":"abc","dp":{},"dk":{"x":{},"y":{"a":"q"}},"dnn":[null,{},{"b":1}]},"list":[{"s":"a"}]}`: true,
	`{"leaf":{"dnn":[],"dk":{},"s":"abc","dp":{"b":2,"a":"q"}},"list":[{"s":"a"}]}`:                       true,
	`{"leaf":{"s":"abc","dk":{"x":{"a":1}}},"list":[{"s":"a"}]}`:                                          false,
	`{"leaf":{"s":"abc","dnn":[{"c":1}]},"list":[{"s":"a"}]}`:                                             false,
	`{"leaf":{"s":"abc","dp":null},"list":[{"s":"a"}]}`:                                                   false,
	// Number enums, by value as written.
	`{"leaf":{"s":"abc","nfe":5e-1,"ne":-1},"list":[{"s":"a"}]}`: true,
	`{"leaf":{"s":"abc","ne":3e0},"list":[{"s":"a"}]}`:           false,
	`{"leaf":{"s":"abc","ne":-1.0},"list":[{"s":"a"}]}`:          false,
	`{"leaf":{"s":"abc","nfe":-0.5},"list":[{"s":"a"}]}`:         false,
	// The elements' keywords (items.), at each level.
	`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"picks":["x","y","x"],"rows":[[0,1],[],[2]]}`: true,
	`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"picks":["x","z"]}`:                           false,
	`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"rows":[[0,1,2]]}`:                            false,
	`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"rows":[[1,1]]}`:                              false,
	`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"rows":[[0],[-1]]}`:                           false,
	// The values' keywords (additionalProperties.), at each level.
	`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"tags":{"k":"a","l":"bc"},"bag":{"b":[0,1],"c":[]}}`: true,
	`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"tags":{"k":"b"}}`:                                   false,
	`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"bag":{"b":[0,-1]}}`:                                 false,
	// Null for a type with JSON methods of its own and schema {}: valid where
	// its methods, or encoding/json/v2, take it.
	`{"leaf":{"s":"abc","o":null,"nul":null,"nuls":[null,"x"],"nulm":{"k":null},"os":[null,{"a":1}]},"list":[{"s":"a"}]}`: true,
	`{"leaf":{"s":"abc","nul":"x","nuls":[],"nulm":{"k":"y"}},"list":[{"s":"a"}]}`:                                        true,
	`{"leaf":{"s":"abc","nul":1},"list":[{"s":"a"}]}`:                                                                     false,
	`{"leaf":{"s":"abc","nuls":[null,1]},"list":[{"s":"a"}]}`:                                                             false,
	`{"leaf":{"s":"abc","nulm":{"k":2}},"list":[{"s":"a"}]}`:                                                              false,
	`{"leaf":{"s":"abc","nul":nul},"list":[{"s":"a"}]}`:                                                                   false,
	`{"leaf":{"s":"abc","os":[null,{"b":1}]},"list":[{"s":"a"}]}`:                                                         false,
	// Integers at the ends of their widths, and escaped strings.
	`{"leaf":{"s":"abc","i32":-2147483648,"u64":18446744073709551615,"cnt":65535},"list":[{"s":"a"}]}`:             true,
	jsonEsc(`{"leaf":{"s":"ab%u0063","e":"%u00e9","fpw":"\"\\\/\b\f\n\r\t%ud83d%ude00"},"list":[{"s":"%u0061"}]}`): true,
	`{"leaf":{"s":"abc","i32":2147483648},"list":[{"s":"a"}]}`:                                                     false,
	`{"leaf":{"s":"abc","u64":18446744073709551616},"list":[{"s":"a"}]}`:                                           false,
	`{"leaf":{"s":"abc","cnt":-0},"list":[{"s":"a"}]}`:                                                             false,
	`{"leaf":{"s":"abc","fpw":"\ud800x"},"list":[{"s":"a"}]}`:                                                      false,
}

func TestSinglePassAgreesOnKeywords(t *testing.T) {
	for s, valid := range keywordSeeds {
		if agree[fastBody](t, []byte(s), DefaultLimits) != valid {
			t.Errorf("%s: want accepted %v", s, valid)
		}
	}
}

func TestSinglePassAgreesWithTheReference(t *testing.T) {
	accepted := 0
	for _, s := range fastSeeds {
		if agree[fastBody](t, []byte(s), DefaultLimits) {
			accepted++
		}
	}
	// The first eight seeds are valid, the rest are not.
	for _, s := range fastSeeds[:8] {
		if !agree[fastBody](t, []byte(s), DefaultLimits) {
			t.Fatalf("refused %s", s)
		}
	}
	if accepted != 8 {
		t.Fatalf("%d seeds accepted, want 8", accepted)
	}
	nest := func(depth int) string {
		return strings.Repeat(`{"leaf":{"s":"a"},"list":[{"s":"a"}],"next":`, depth) +
			`{"leaf":{"s":"a"},"list":[{"s":"a"}]}` + strings.Repeat("}", depth)
	}
	tight := DefaultLimits
	tight.MaxDepth = 8
	tight.MaxItems = 2
	tight.MaxStringLength = 3
	for d := range 6 {
		agree[fastBody](t, []byte(nest(d)), tight)
	}
	agree[fastBody](t, []byte(`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"words":["abcd"]}`), tight)
	agree[fastBody](t, []byte(`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"grid":[[1,2,3]]}`), tight)
	agree[fastBody](t, []byte(`{"leaf":{"s":"abc","m":{"a":1,"b":2,"c":3}},"list":[{"s":"a"}]}`), tight)
	agree[map[string][]int](t, []byte(`{"a":[1,2],"b":[]}`), DefaultLimits)
	agree[[]string](t, []byte(`["a","b"]`), DefaultLimits)
	agree[string](t, []byte(`"\ud800"`), DefaultLimits)
	agree[string](t, []byte("\"\xff\""), DefaultLimits)
	agree[float32](t, []byte(`3.4028235e38`), DefaultLimits)
}

// A map key whose type reads or writes itself by a method is read as
// encoding/json/v2 reads it, by that method: the single pass once took the
// member name as it was, so it built {"a":1} where the reference built
// {"A":1}, and took {"a":1,"A":2}, two names v2 reads as one key.
func TestSinglePassReadsMapKeysByTheirMethods(t *testing.T) {
	for body, want := range map[string]bool{
		`{"a":1}`:         true,
		`{"a":1,"b":2}`:   true,
		`{"a":1,"A":2}`:   false,
		`{"":1}`:          false,
		`{"a":1,"a":2}`:   false,
		`{"a":1,"b":300}`: false,
	} {
		if agree[map[fastKey]int8](t, []byte(body), DefaultLimits) != want {
			t.Errorf("%s: want accepted %v", body, want)
		}
	}
	var m map[fastKey]int8
	c := mustCodec[map[fastKey]int8](t)
	if !singlePass(c, c.use(), []byte(`{"a":1}`), DefaultLimits, reflect.ValueOf(&m).Elem()) || m["A"] != 1 {
		t.Fatalf("%v", m)
	}
	for body, want := range map[string]bool{`{"k-a":1}`: true, `{"a":1}`: false} {
		if agree[map[fastPtrKey]int8](t, []byte(body), DefaultLimits) != want {
			t.Errorf("%s: want accepted %v", body, want)
		}
	}
}

func mustCodec[T any](t *testing.T) *codec {
	t.Helper()
	c, err := newRegistry().codecFor(reflect.TypeFor[T]())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type fastInner struct {
	A string `json:"a"`
}

// fastHidden promotes an exported field from an unexported embedded struct,
// which reflection can still set.
type fastHidden struct{ fastInner }

type fastUnique struct {
	L []float64    `json:"l" schema:"uniqueItems=true"`
	O []fastInner  `json:"o" schema:"uniqueItems=true,maxItems=3"`
	M []fastHidden `json:"m" schema:"uniqueItems=true,minItems=1"`
}

func TestSinglePassEdgeTypes(t *testing.T) {
	for body, ok := range map[string]bool{
		`{"a":"x"}`: true,
		`{"b":"x"}`: false,
	} {
		if agree[fastHidden](t, []byte(body), DefaultLimits) != ok {
			t.Errorf("%s: want accepted %v", body, ok)
		}
	}
	for body, ok := range map[string]bool{
		`{"l":[1, 2 ,3],"o":[],"m":[{"a":"x"}]}`:                                   true,
		`{"l":[1,1.0],"o":[],"m":[{"a":"x"}]}`:                                     false,
		`{"l":[1e1,10],"o":[],"m":[{"a":"x"}]}`:                                    false,
		"{\"l\":[],\"o\":[{\"a\":\"x\"},\n\t{\"a\":\"x\"}],\"m\":[{\"a\":\"x\"}]}": false,
		`{"l":[],"o":[{"a":"x"},{"a":"y"}],"m":[{"a":"x"},{"a":"x"}]}`:             false,
		`{"l":[],"o":[{"a":"x"},{"a":"y"}],"m":[{"a":"x"},{"a":"z"}]}`:             true,
		`{"l":[],"o":[{"a":"x"},{"a":"x"},{"a":"x"},{"a":"x"}],"m":[]}`:            false,
	} {
		if agree[fastUnique](t, []byte(body), DefaultLimits) != ok {
			t.Errorf("%s: want accepted %v", body, ok)
		}
	}
}

// Sealed types: required, nested in a variant, optional (omitzero, and a
// pointer), in a slice, in a map, and under uniqueItems.

type fastShape interface{ isFastShape() }

type fastCircle struct {
	Kind string    `json:"kind" schema:"minLength=1"`
	R    float64   `json:"r" schema:"minimum=0"`
	Leaf *fastLeaf `json:"leaf,omitzero"`
	Fill string    `json:"fill" schema:"default=none,enum=none|red"`
}

type fastSquare struct {
	Kind  string       `json:"kind"`
	Side  int8         `json:"side"`
	Inner fastShape    `json:"inner,omitzero"`
	Ptr   *fastShape   `json:"ptr,omitzero"`
	Many  *[]fastShape `json:"many,omitzero" schema:"maxItems=2"`
}

// fastDot has no member but its discriminator.
type fastDot struct {
	Kind string `json:"kind"`
}

func (fastCircle) isFastShape() {}
func (fastSquare) isFastShape() {}
func (fastDot) isFastShape()    {}

var fastShapes = Sealed[fastShape]("kind", Case[fastCircle]("circle"), Case[fastSquare]("square"), Case[fastDot]("döt"))

type fastScene struct {
	Main   fastShape             `json:"main"`
	Others []fastShape           `json:"others" schema:"maxItems=3"`
	Opt    fastShape             `json:"opt,omitzero"`
	By     *map[string]fastShape `json:"by,omitzero"`
	Set    *[]fastShape          `json:"set,omitzero" schema:"uniqueItems=true"`
	Nul    Nullable[fastShape]   `json:"nul,omitzero"`
}

// sealedRegistry is a registry with fastShapes declared, as New makes it.
func sealedRegistry(t testing.TB) *registry {
	r := newRegistry()
	if err := r.declareUnion(fastShapes); err != nil {
		t.Fatal(err)
	}
	r.sealDeclarations()
	return r
}

var sealedSeeds = []string{
	`{"main":{"kind":"circle","r":1},"others":[]}`,
	`{"main":{"r":1,"kind":"circle"},"others":[{"side":2,"kind":"square"}]}`,
	`{"main":{"kind":"square","side":-3,"inner":{"kind":"circle","r":0.5},"ptr":{"r":2,"kind":"circle"},"many":[{"kind":"döt"},{"kind":"döt"}]},"others":[]}`,
	`{"main":{"kind":"square","side":1,"many":[{"kind":"döt"},{"kind":"döt"}]},"others":[]}`,
	`{"main":{"kind":"circle","r":1},"others":[]}`,
	`{"main":{"kind":"circle","r":1},"others":[]}`,
	` { "others" : [ { "kind" : "d` + "ö" + `t" } ] , "main" : { "r" : 1e0 , "kind" : "circle" } , "opt" : {"r":1,"kind":"circle"}}`,
	`{"main":{"kind":"circle","r":1},"others":[],"opt":{"kind":"square","side":0,"inner":{"side":1,"inner":{"kind":"circle","r":3},"kind":"square"}}}`,
	`{"main":{"kind":"circle","r":1,"leaf":{"s":"abc","i8":2}},"others":[],"by":{"a":{"r":1,"kind":"circle"},"b":{"kind":"d` + "ö" + `t"}}}`,
	`{"main":{"kind":"circle","r":1},"others":[],"set":[{"kind":"circle","r":1},{"r":2,"kind":"circle"}],"nul":null}`,
	// Invalid from here on.
	`{"main":{"r":1},"others":[]}`,
	`{"main":{"kind":7,"r":1},"others":[]}`,
	`{"main":{"r":1,"kind":null},"others":[]}`,
	`{"main":{"kind":"hexagon"},"others":[]}`,
	`{"main":{"kind":"circle","r":-1},"others":[]}`,
	`{"main":{"r":-1,"kind":"circle"},"others":[]}`,
	`{"main":{"kind":"circle","r":1,"side":2},"others":[]}`,
	`{"main":{"kind":"square"},"others":[]}`,
	`{"main":"circle","others":[]}`,
	`{"main":null,"others":[]}`,
	`{"main":[],"others":[]}`,
	`{"main":{"kind":"circle","r":1},"others":[null]}`,
	`{"main":{"kind":"circle","r":1},"others":[],"opt":null}`,
	`{"main":{"kind":"circle","kind":"circle","r":1},"others":[]}`,
	`{"main":{"r":1,"kind":"circle","kind":"circle"},"others":[]}`,
	`{"main":{"kind":"circle","r":1},"others":[{"kind":"square","side":1,"ptr":null}]}`,
	`{"main":{"kind":"circle","r":1},"others":[],"set":[{"kind":"circle","r":1},{"r":1,"kind":"circle"}]}`,
	`{"main":{"kind":"","r":1},"others":[]}`,
	`{"main":{"kind":"circle","r":1}`,
	`{"main":{"r":1,"kind":"circle"},"others":[]`,
	`{"main":{"kind":"circle","r":1,},"others":[]}`,
	`{"main":{"kind":"circle" "r":1},"others":[]}`,
	`{"main":{"kind":"circle\u0000","r":1},"others":[]}`,
	"{\"main\":{\"kind\":\"circle\xff\",\"r\":1},\"others\":[]}",
	"{\"main\":{\"kind\":\"circle\x01\",\"r\":1},\"others\":[]}",
	`{"main":{"kind":"circle","r":1},"others":[{"kind":"dot"},{"kind":"dot"},{"kind":"dot"},{"kind":"dot"}]}`,
	`{"main":{"kind":"circle","r":1},"others":[],"nul":{"kind":"hexagon"}}`,
}

// Nullable sealed values, valid each.
var nullSealedSeeds = []string{
	`{"main":{"kind":"circle","r":1},"others":[],"nul":{"r":2,"kind":"circle"}}`,
	`{"main":{"kind":"circle","r":1},"others":[],"nul":{"kind":"d` + "ö" + `t"}}`,
}

func TestSinglePassAgreesOnNullableSealedTypes(t *testing.T) {
	r := sealedRegistry(t)
	for _, s := range nullSealedSeeds {
		if !agreeIn[fastScene](t, r, []byte(s), DefaultLimits) {
			t.Errorf("%s refused", s)
		}
	}
}

// defaultSealedSeeds leave out, or send, a variant's member with a default
// (fastCircle.Fill), wherever a sealed value is, the discriminator first or
// late; each with whether it is valid.
var defaultSealedSeeds = map[string]bool{
	`{"main":{"r":1,"kind":"circle"},"others":[{"kind":"circle","r":2,"fill":"red"},{"r":3,"kind":"circle"}],"by":{"a":{"r":1,"kind":"circle"}},"nul":{"r":3,"kind":"circle"}}`: true,
	`{"main":{"kind":"square","side":1,"inner":{"r":1,"kind":"circle"},"many":[{"kind":"circle","r":0}]},"others":[],"opt":{"fill":"none","r":1,"kind":"circle"}}`:              true,
	`{"main":{"kind":"circle","r":1,"fill":"blue"},"others":[]}`:                                                         false,
	`{"main":{"r":1,"fill":null,"kind":"circle"},"others":[]}`:                                                           false,
	`{"main":{"kind":"circle","r":1},"others":[],"set":[{"kind":"circle","r":1},{"kind":"circle","r":1,"fill":"none"}]}`: true, // as sent, they differ
}

// lateSealedSeeds put the discriminator after members that hold strings with
// quotes, backslashes, and brackets, after nested objects and arrays, or
// escape it; each with whether it is valid.
var lateSealedSeeds = map[string]bool{
	`{"main":{"leaf":{"s":"abc","fpw":"x\"},\"kind\":\"square\\"},"r":1,"kind":"circle"},"others":[]}`: true,
	`{"main":{"leaf":{"s":"abc","fpw":"]}[{,:"},"r":1,"kind":"circle"},"others":[]}`:                   true,
	`{"main":{"side":1,"many":[{"kind":"döt"},{"r":2,"kind":"circle"}],"kind":"square"},"others":[]}`:  true,
	jsonEsc(`{"main":{"r":1,"%u006bind":"circle"},"others":[]}`):                                       true,
	jsonEsc(`{"main":{"r":1,"kind":"circl%u0065"},"others":[]}`):                                       true,
	jsonEsc(`{"main":{"kind":"d%u00f6t"},"others":[]}`):                                                true,
	jsonEsc(`{"main":{"%u006bind":"d%u00f6t"},"others":[]}`):                                           true,
	"{\"main\":{\"r\":1 ,\n\t\"kind\" : \"circle\" },\"others\":[]}":                                   true,
	`{"main":{"r":1,"kind":"circle\u0000"},"others":[]}`:                                               false,
	`{"main":{"r":1,"kind":"\ud800"},"others":[]}`:                                                     false,
	`{"main":{"r":1,"kind":"circle","kind":"square"},"others":[]}`:                                     false,
	`{"main":{"r":1,"kind\"":"circle"},"others":[]}`:                                                   false,
	`{"main":{"r":[1,"kind","circle"],"kind":"circle"},"others":[]}`:                                   false,
	`{"main":{"r":1,"x":{"kind":"circle"}},"others":[]}`:                                               false,
	`{"main":{"r":1 "kind":"circle"},"others":[]}`:                                                     false,
	`{"main":{"r":1,"kind":"circle"`:                                                                   false,
}

func TestSinglePassFindsLateDiscriminators(t *testing.T) {
	r := sealedRegistry(t)
	for s, valid := range lateSealedSeeds {
		if agreeIn[fastScene](t, r, []byte(s), DefaultLimits) != valid {
			t.Errorf("%s: want accepted %v", s, valid)
		}
	}
}

func TestSinglePassAgreesOnSealedDefaults(t *testing.T) {
	r := sealedRegistry(t)
	for s, valid := range defaultSealedSeeds {
		if agreeIn[fastScene](t, r, []byte(s), DefaultLimits) != valid {
			t.Errorf("%s: want accepted %v", s, valid)
		}
	}
	// The default is the value either path binds.
	var got fastScene
	c, _ := r.codecFor(reflect.TypeFor[fastScene]())
	if !singlePassIn(r, c, c.use(), []byte(`{"main":{"r":1,"kind":"circle"},"others":[{"kind":"circle","r":2}]}`), DefaultLimits, reflect.ValueOf(&got).Elem()) ||
		got.Main.(fastCircle).Fill != "none" || got.Others[0].(fastCircle).Fill != "none" {
		t.Fatalf("%#v", got)
	}
}

func TestSinglePassAgreesOnSealedTypes(t *testing.T) {
	r := sealedRegistry(t)
	const valid = 10
	for i, s := range sealedSeeds {
		if ok := agreeIn[fastScene](t, r, []byte(s), DefaultLimits); ok != (i < valid) {
			t.Errorf("%s: accepted %v, want %v", s, ok, i < valid)
		}
	}
	// Nesting is held to the ceiling across the bodies read ahead.
	tight := DefaultLimits
	tight.MaxDepth = 6
	for _, late := range []bool{false, true} {
		for d := range 6 {
			sq := `{"kind":"square","side":1,"inner":`
			if late {
				sq = `{"side":1,"inner":`
			}
			end := "}"
			if late {
				end = `,"kind":"square"}`
			}
			body := `{"others":[],"main":` + strings.Repeat(sq, d) + `{"r":1,"kind":"circle"}` + strings.Repeat(end, d) + `}`
			for _, limits := range []Limits{DefaultLimits, tight} {
				want := d+2 <= limits.MaxDepth
				if ok := agreeIn[fastScene](t, r, []byte(body), limits); ok != want {
					t.Errorf("depth %d, late %v, ceiling %d: accepted %v", d, late, limits.MaxDepth, ok)
				}
			}
		}
	}
	agreeIn[[]fastShape](t, r, []byte(`[{"r":1,"kind":"circle"},{"kind":"circle","r":2}]`), DefaultLimits)
	agreeIn[fastShape](t, r, []byte(` {"r":1,"kind":"circle"} `), DefaultLimits)
	agreeIn[fastShape](t, r, []byte(`{"kind":"circle","r":1} x`), DefaultLimits)
	agreeIn[fastShape](t, r, []byte(`{"r":1,"kind":"circle"} x`), DefaultLimits)
}

func FuzzSinglePassSealed(f *testing.F) {
	for _, s := range append(sealedSeeds, nullSealedSeeds...) {
		f.Add([]byte(s))
	}
	for s := range defaultSealedSeeds {
		f.Add([]byte(s))
	}
	for s := range lateSealedSeeds {
		f.Add([]byte(s))
	}
	r := sealedRegistry(f)
	tight := DefaultLimits
	tight.MaxDepth = 6
	tight.MaxItems = 2
	tight.MaxStringLength = 3
	f.Fuzz(func(t *testing.T, data []byte) {
		agreeIn[fastScene](t, r, data, DefaultLimits)
		agreeIn[fastScene](t, r, data, tight)
	})
}

type fastParams struct {
	S  string     `query:"s" schema:"minLength=2,maxLength=5,pattern=^[a-z]"`
	E  *string    `query:"e" schema:"enum=x|y"`
	D  *string    `query:"d" schema:"format=date"`
	I  int16      `query:"i" schema:"minimum=-3,multipleOf=3"`
	U  *uint8     `query:"u" schema:"exclusiveMaximum=200"`
	F  *float32   `query:"f" schema:"maximum=1.5"`
	B  *bool      `query:"b"`
	ID *uuid.UUID `header:"X-Id"`
	T  *time.Time `query:"t"`
	Lv fastLevel  `path:"lv"`
	N  *int64     `query:"n" schema:"maximum=9007199254740992,multipleOf=3"`
	G  *float32   `query:"g" schema:"exclusiveMinimum=0.1"`
	// The format types.
	Fd  *Date                `query:"fd"`
	Ft  *TimeOfDay           `query:"ft"`
	Fdu *Duration            `query:"fdu"`
	Fe  *fastMail            `query:"fe"`
	Fh  *fastMail            `header:"X-Fh" schema:"minLength=4"`
	Fdy *fastDay             `cookie:"fdy"`
	F4  *IPv4                `query:"f4"`
	F6  *IPv6                `header:"X-F6"`
	Fp  *JSONPointer         `query:"fp"`
	Frp *RelativeJSONPointer `query:"frp"`
	Fpw *Password            `header:"X-Fpw" schema:"maxLength=4"`
	// Strings held to a format.
	Sdt *string `query:"sdt" schema:"format=date-time"`
	Sip *string `header:"X-Sip" schema:"format=ipv4"`
	// Number enums.
	En  *int8    `query:"en" schema:"enum=-3|0|4"`
	Enf *float32 `query:"enf" schema:"enum=1.5|0.1"`
}

// A parameter is bound by bindFast exactly when bind would bind it, to the
// same value.
func agreeParams(t *testing.T, s string) {
	t.Helper()
	p, err := newRegistry().inPlan(reflect.TypeFor[fastParams]())
	if err != nil {
		t.Fatal(err)
	}
	for _, pp := range p.params {
		var want, got fastParams
		d := &decoder{limits: DefaultLimits, in: pp.in}
		pp.bind(d, []string{s}, reflect.ValueOf(&want).Elem().FieldByIndex(pp.index))
		ok := pp.bindFast(s, reflect.ValueOf(&got).Elem().FieldByIndex(pp.index), DefaultLimits)
		switch {
		case ok && len(d.errs) > 0:
			t.Fatalf("%s: bindFast took %q, which bind refuses: %v", pp.name, s, d.errs)
		case !ok && len(d.errs) == 0:
			t.Fatalf("%s: bindFast refused %q, which bind takes", pp.name, s)
		case ok && !reflect.DeepEqual(want, got):
			t.Fatalf("%s: values differ for %q: %#v %#v", pp.name, s, want, got)
		}
	}
}

var paramSeeds = []string{"", "ab", "abcdef", "Ab", "x", "z", "2026-02-28", "2026-02-30", "-3", "3", "4", "-6", "0",
	"199", "200", "-0", "1.5", "1.50", "1e0", "1.6", "NaN", "true", "false", "TRUE",
	"123e4567-e89b-12d3-a456-426614174000", "2026-07-18T09:30:00Z", "low", "high", "mid", "😀😀",
	"9007199254740990", "9007199254740993", "1500000001", "0.1", "0.10000000000000001",
	"23:59:60Z", "12:00:00.5-00:00", "P2W", "PT1H2S", "a@b", "\"a\"@[1.2.3.4]", "a..b@docomo.ne.jp", "@b", " a@b", "a@b\t", "today", "h.example", "-h", "010.0.0.1",
	"::1.2.3.4", "1::2::3", "https://x/%7e", "x:", "//[v1.x]/", "?é", "http://例/", "{+a,b:3}", "{a.}", "/~2",
	"/a~1b", "0/x", "10", "#", "2026-07-18t09:30:00z", "1998-12-31T23:59:60Z", "1990-12-31T15:59:59-24:00",
	"2026-07-18T09:30:00,5Z", "15e-1", "0.10", "-4",
	// Integers as JSON writes them, and text that is not UTF-8.
	"007", "-07", "00", "+7", "-03", "01.5", "-0.0", "\xff", "a\xc3", "\xed\xa0\x80", "2026-02-2\xff"}

func TestParamFastPathAgreesWithBind(t *testing.T) {
	for _, s := range paramSeeds {
		agreeParams(t, s)
	}
}

func FuzzParamFastPath(f *testing.F) {
	for _, s := range paramSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { agreeParams(t, s) })
}

// A pooled body buffer, as a problem is written into, holds what marshal
// writes.
func TestPooledBufferMatchesMarshal(t *testing.T) {
	nick := "n"
	for _, v := range []any{&keyUser{Name: "a<b>&"}, &keyUser{Name: "x", Nick: &nick, Tags: []string{" "}}, &map[string]int{"b": 1, "a": 2}} {
		want, err := marshal(v, marshalOptions)
		if err != nil {
			t.Fatal(err)
		}
		b := bodyBuffers.Get().(*bytes.Buffer)
		if err := json.MarshalWrite(b, v, marshalOptions); err != nil {
			t.Fatal(err)
		}
		if b.String() != string(want) {
			t.Fatalf("got %s, want %s", b, want)
		}
		releaseBuffer(b)
	}
}

func FuzzSinglePass(f *testing.F) {
	for _, s := range fastSeeds {
		f.Add([]byte(s))
	}
	for s := range keywordSeeds {
		f.Add([]byte(s))
	}
	tight := DefaultLimits
	tight.MaxDepth = 8
	tight.MaxItems = 2
	tight.MaxStringLength = 3
	f.Fuzz(func(t *testing.T, data []byte) {
		agree[fastBody](t, data, DefaultLimits)
		agree[fastBody](t, data, tight)
	})
}

// opaqueJSON has JSON methods of its own, so it is opaque to geta; v2 still
// reads it as a struct, with the body's options.
type opaqueJSON struct {
	A int `json:"a"`
}

func (opaqueJSON) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

type opaqueJSONBody struct {
	O opaqueJSON `json:"o"`
}

// The single pass reads a type with its own JSON methods with the reference
// path's options, so an unknown member in it is refused on both.
func TestSinglePassReadsOpaqueTypesWithTheBodyOptions(t *testing.T) {
	if !agree[opaqueJSONBody](t, []byte(`{"o":{"a":1}}`), DefaultLimits) {
		t.Error(`{"o":{"a":1}} refused`)
	}
	if agree[opaqueJSONBody](t, []byte(`{"o":{"a":1,"bogus":2}}`), DefaultLimits) {
		t.Error("an unknown member of an opaque type accepted")
	}
	// And through a body plan, as a request reaches it.
	p, err := newRegistry().inPlan(reflect.TypeFor[struct {
		Body opaqueJSONBody `body:"json"`
	}]())
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"o":{"a":1,"bogus":2}}`))
	r.Header.Set("Content-Type", "application/json")
	var dst opaqueJSONBody
	if be := p.body.bind(httptest.NewRecorder(), r, reflect.ValueOf(&dst).Elem(), DefaultLimits); be == nil {
		t.Error("the body plan accepted an unknown member of an opaque type")
	}
}

type fastNullMembers struct {
	N fastNullish   `json:"n"`
	P *fastNullish  `json:"p,omitzero"`
	O fastOpaque    `json:"o"`
	S *fastStrict   `json:"s,omitzero"`
	L *[]fastStrict `json:"l,omitzero"`
}

// A type with JSON methods of its own and no declared schema takes null as
// check takes it, any JSON value, and the single pass reads it as
// encoding/json/v2 does: into a pointer member as nil, calling no method,
// and into a value by the type's UnmarshalJSON, or as a struct, zero, where
// it has none. The single pass once refused every such null, so the body
// took the reference path.
func TestSinglePassTakesNullForAnOwnJSONType(t *testing.T) {
	for body, valid := range map[string]bool{
		`{"n":null,"o":null}`:                       true,
		`{"n":null,"p":null,"o":null,"s":null}`:     true,
		`{"n":"x","p":"y","o":{"a":1},"s":"ok"}`:    true,
		`{"n":"x","o":{"a":1},"l":["ok"]}`:          true,
		`{"n":1,"o":null}`:                          false,
		`{"n":null,"p":1,"o":null}`:                 false,
		`{"n":null,"o":null,"s":"no"}`:              false,
		`{"n":null,"o":null,"l":[null]}`:            false, // fastStrict's UnmarshalJSON refuses null
		`{"n":null,"o":null,"l":null}`:              false, // a slice is no type with methods
		`{"n":null,"o":{"a":1,"bogus":2},"p":null}`: false,
	} {
		if agree[fastNullMembers](t, []byte(body), DefaultLimits) != valid {
			t.Errorf("%s: want accepted %v", body, valid)
		}
	}
	var got fastNullMembers
	c := mustCodec[fastNullMembers](t)
	if !singlePass(c, c.use(), []byte(`{"n":null,"p":null,"o":null,"s":null}`), DefaultLimits, reflect.ValueOf(&got).Elem()) {
		t.Fatal("refused")
	}
	if !got.N.null || got.P != nil || got.O != (fastOpaque{}) || got.S != nil {
		t.Fatalf("%#v", got)
	}
}

type fastFlagPtr struct {
	F *fastFlag `json:"f,omitzero"`
}

// A declared schema states a JSON type, so null is refused on both paths,
// behind a pointer too.
func TestSinglePassRefusesNullForADeclaredOwnJSONType(t *testing.T) {
	r := newRegistry()
	if err := r.declare(declaredSchema{t: reflect.TypeFor[fastFlag](), jsonType: "boolean"}); err != nil {
		t.Fatal(err)
	}
	r.sealDeclarations()
	for _, typ := range []reflect.Type{reflect.TypeFor[fastFlagged](), reflect.TypeFor[fastFlagPtr]()} {
		if ok, _ := agreeValue(t, r, typ, []byte(`{"f":null}`), DefaultLimits); ok {
			t.Errorf("%s: null accepted", typ)
		}
		if ok, _ := agreeValue(t, r, typ, []byte(`{"f":true}`), DefaultLimits); !ok {
			t.Errorf("%s: true refused", typ)
		}
	}
}
