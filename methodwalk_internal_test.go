package geta

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// refusing is read by its own UnmarshalJSON, which refuses null and false.
type refusing struct{}

func (refusing) MarshalJSON() ([]byte, error) { return []byte(`0`), nil }

func (*refusing) UnmarshalJSON(b []byte) error {
	if s := string(bytes.TrimSpace(b)); s == "null" || s == "false" {
		return errors.New("refused " + s)
	}
	return nil
}

// refusingKey is a map key read by its own UnmarshalJSON, which refuses "no"
// as written.
type refusingKey string

func (k refusingKey) MarshalJSON() ([]byte, error) { return []byte(`"` + k + `"`), nil }

func (k *refusingKey) UnmarshalJSON(b []byte) error {
	if string(b) == `"no"` {
		return errors.New("refused key")
	}
	*k = refusingKey(b)
	return nil
}

type walkShape interface{ isWalkShape() }

type walkDot struct {
	Kind string   `json:"kind"`
	R    refusing `json:"r"`
}

func (walkDot) isWalkShape() {}

var walkShapes = Sealed[walkShape]("kind", Case[walkDot]("dot"))

type walked struct {
	A refusing               `json:"a"`
	L []refusing             `json:"l"`
	M map[string][]refusing  `json:"m"`
	K map[refusingKey]string `json:"k"`
	N struct {
		R refusing `json:"r"`
	} `json:"n"`
	S []walkShape `json:"s"`
}

// walkedCodec returns walked's codec, in a registry with walkShapes
// declared.
func walkedCodec(t *testing.T) (*registry, *codec) {
	t.Helper()
	r := newRegistry()
	if err := r.declareUnion(walkShapes); err != nil {
		t.Fatal(err)
	}
	r.sealDeclarations()
	c, err := r.codecFor(reflect.TypeFor[walked]())
	if err != nil {
		t.Fatal(err)
	}
	return r, c
}

// Where the body passes check and one method refuses, the walk reports what
// encoding/json/v2 reading the whole body reports, path and words: the
// violation a request got before the walk, inside a sealed value too.
func TestTheMethodWalkReportsAsV2Does(t *testing.T) {
	r, c := walkedCodec(t)
	ok := `{"a":1,"l":[1],"m":{"x":[1]},"k":{"yes":"v"},"n":{"r":1},"s":[{"r":1,"kind":"dot"}]}`
	for _, swap := range [][2]string{
		{`"a":1`, `"a": null`},
		{`"l":[1]`, `"l":[1, 2 ,false]`},
		{`"m":{"x":[1]}`, `"m":{"x":[1,null]}`},
		{`"k":{"yes":"v"}`, `"k":{ "no" :"v"}`},
		{`"n":{"r":1}`, `"n":{"r":false}`},
		{`"m":{"x":[1]}`, `"m":{"a/b~c":[null]}`},
		{`"s":[{"r":1,`, `"s":[{"r":1,"kind":"dot"},{"r":null,`},
	} {
		data := []byte(strings.Replace(ok, swap[0], swap[1], 1))
		tree, err := parseJSON(data, DefaultLimits.MaxDepth)
		if err != nil {
			t.Fatal(err)
		}
		want := &decoder{limits: DefaultLimits, in: "body"}
		want.check(c, c.use(), tree, "$")
		if len(want.errs) > 0 {
			t.Fatalf("%s: %v", data, want.errs)
		}
		var v walked
		want.decodeJSON(data, reflect.ValueOf(&v).Elem(), r.decOpts)
		got := &decoder{limits: DefaultLimits, in: "body"}
		n := got.methods(c, c.use(), tree, data, r.decOpts, methodsUnder(c))
		if len(want.errs) != 1 || !reflect.DeepEqual(got.errs, want.errs) || n != 1 {
			t.Errorf("%s:\nwalk %v %d\nv2   %v", data, got.errs, n, want.errs)
		}
		// The reference path lists the walk's refusal alone.
		var dst walked
		b := &bodyPlan{c: c, use: c.use(), opts: r.decOpts, methods: methodsUnder(c)}
		if errs, _ := b.reference(jsontext.NewDecoder(bytes.NewReader(data)), data, reflect.ValueOf(&dst).Elem(), DefaultLimits); !reflect.DeepEqual(errs, want.errs) {
			t.Errorf("%s: reference %v", data, errs)
		}
	}
}

// pointerPath names a token by what it steps into in the data, as check
// names it: an element's index, a member's name, digits or not.
func TestPointerPathFollowsTheData(t *testing.T) {
	data := []byte(`{"0":[{"a/b":{"-1":[7,8]}}],"x":{"9":1}, "x":{"9":2}}`)
	for p, want := range map[jsontext.Pointer]string{
		"":               "$",
		"/0":             "$['0']",
		"/0/0":           "$['0'][0]",
		"/0/0/a~1b/-1":   "$['0'][0]['a/b']['-1']",
		"/0/0/a~1b/-1/1": "$['0'][0]['a/b']['-1'][1]",
		"/x/9":           "$.x['9']",
		// Past what data holds, a token is taken as a name.
		"/0/5/1": "$['0'][5]['1']",
		"/y/0":   "$.y['0']",
	} {
		if got := pointerPath(data, p); got != want {
			t.Errorf("%q: %s, want %s", p, got, want)
		}
	}
	// A path as check renders the same place.
	if got, want := pointerPath(data, "/0/0/a~1b/-1/1"), rootPath("$").member("0").item(0).member("a/b").member("-1").item(1).String(); got != want {
		t.Errorf("%s, check %s", got, want)
	}
}

// For any member name, check (vpath), encoding/json/v2's pointer
// (pointerPath), and the method walk name the member alike, and the selector
// reads back as the name: the shorthand only for a member-name-shorthand,
// else a bracketed name in Normalized Path escapes (RFC 9535 §2.5.1.1, §2.7).
func FuzzMemberSelector(f *testing.F) {
	for _, s := range []string{"a", "_1", "é", "0", "-1", "a.b", "a[0]", "it's", `a\b`, "a\nb", "\x01", "", "\x7f", "\xff", "�", "a b", "a/b~c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		check := rootPath("$").member(name).item(0).render(math.MaxInt)
		data, _ := json.Marshal(map[string][]int{name: {1}})
		ptr := jsontext.Pointer("").AppendToken(name).AppendToken("0")
		if got := pointerPath(data, ptr); utf8.ValidString(name) && got != check {
			t.Fatalf("%q: pointer %s, check %s", name, got, check)
		}
		sel := strings.TrimSuffix(strings.TrimPrefix(check, "$"), "[0]")
		// Each byte of invalid UTF-8 reads back as U+FFFD.
		if back, ok := readSelector(sel); !ok || back != string([]rune(name)) {
			t.Fatalf("%q: selector %s reads %q", name, sel, back)
		}
	})
}

// readSelector reads a name selector as RFC 9535 writes one: .name, a
// member-name-shorthand, or ['name'] in Normalized Path escapes.
func readSelector(sel string) (string, bool) {
	if name, ok := strings.CutPrefix(sel, "."); ok {
		for i, r := range name {
			if !(r == '_' || r >= 0x80 || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || i > 0 && '0' <= r && r <= '9') {
				return "", false
			}
		}
		return name, name != ""
	}
	body, ok := strings.CutPrefix(sel, "['")
	if body, ok = strings.CutSuffix(body, "']"); !ok {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '\'':
			return "", false // unescaped
		case c < 0x20:
			return "", false
		case c != '\\':
			b.WriteByte(c)
			continue
		}
		if i++; i == len(body) {
			return "", false
		}
		switch body[i] {
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '\'', '\\':
			b.WriteByte(body[i])
		case 'u':
			// normal-hexchar: \u00 then 0-7, b, e-f, or 1 and a hex digit,
			// lower case: the control characters without a short escape.
			if i+4 >= len(body) || body[i+1:i+3] != "00" {
				return "", false
			}
			h := body[i+3 : i+5]
			v, err := strconv.ParseUint(h, 16, 8)
			if err != nil || h != strings.ToLower(h) || v >= 0x20 || strings.ContainsRune("\b\f\n\r\t", rune(v)) {
				return "", false
			}
			b.WriteByte(byte(v))
			i += 4
		default:
			return "", false
		}
	}
	return b.String(), true
}

// A codec under which no type reads itself is not walked.
func TestMethodsUnder(t *testing.T) {
	r := newRegistry()
	plain, _ := r.codecFor(reflect.TypeFor[struct {
		A []map[string]int `json:"a"`
	}]())
	if methodsUnder(plain) != nil {
		t.Fatal("walked a body with no method")
	}
	_, c := walkedCodec(t)
	under := methodsUnder(c)
	for _, f := range c.fields {
		if !under[f.c] {
			t.Errorf("%s not walked", f.json)
		}
	}
}
