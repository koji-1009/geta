package geta

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"
)

// check decodes body into a fresh T under the schema tag and returns the
// violation messages, each prefixed with its path.
func check[T any](t *testing.T, tag, body string) []string {
	t.Helper()
	reg := newRegistry()
	typ := reflect.TypeFor[T]()
	c, err := reg.codecFor(typ)
	if err != nil {
		t.Fatalf("codec: %v", err)
	}
	use, err := parseConstraints(tag, c.use())
	if err != nil {
		t.Fatalf("constraints: %v", err)
	}
	tree, err := parseJSON([]byte(body), DefaultLimits.MaxDepth)
	if err != nil {
		return []string{"parse: " + err.Error()}
	}
	d := &decoder{limits: DefaultLimits, in: "body"}
	d.check(c, use, tree, "$")
	var out []string
	for _, v := range d.errs {
		out = append(out, v.Path+": "+v.Message)
	}
	return out
}

func want(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func q(s string) string { return `"` + s + `"` }

func TestStringLength(t *testing.T) {
	const tag = "minLength=2,maxLength=4"
	want(t, check[string](t, tag, q("ab")))
	want(t, check[string](t, tag, q("abcd")))
	want(t, check[string](t, tag, q("a")), "$: string length 1 is shorter than minLength 2")
	want(t, check[string](t, tag, q("abcde")), "$: string length 5 exceeds maxLength 4")
	// Code points, not bytes or UTF-16 units.
	want(t, check[string](t, "maxLength=4", q("😀😀😀")))
	want(t, check[string](t, "maxLength=4", q("😀😀😀😀😀")), "$: string length 5 exceeds maxLength 4")
	want(t, check[string](t, "minLength=1", q("😀")))
}

func TestPattern(t *testing.T) {
	want(t, check[string](t, `pattern=\d{3}`, q("abc123def")))
	want(t, check[string](t, `pattern=\d{3}`, q("ab")), `$: "ab" does not match pattern \d{3}`)
	want(t, check[string](t, `pattern=^\d{3}$`, q("x123")), `$: "x123" does not match pattern ^\d{3}$`)
	// A comma inside a pattern stays in the pattern.
	want(t, check[string](t, `pattern=^\d{1,3}$,maxLength=3`, q("12")))
}

// A pattern never runs over a long string: maxLength condemns it first, and
// the pattern ceiling holds where the backstop is larger. A declared
// maxLength past the ceiling beside a pattern would document strings geta
// refuses, so it is refused where a request reads it; with none, a request's
// schema states the ceiling.
func TestPatternCeiling(t *testing.T) {
	want(t, check[string](t, `maxLength=20,pattern=^(a+)+$`, q(strings.Repeat("a", 40)+"!")),
		"$: string length 41 exceeds maxLength 20")
	for _, tag := range []string{`maxLength=1000000,pattern=^(a+)+$`, `maxLength=4097,pattern=a`, `pattern=a,maxLength=4097`} {
		// The tag is sound for every use; a request reads no string past
		// the ceiling, so where one is read it is refused.
		s, err := parseConstraints(tag, &schema{Type: "string"})
		if err != nil {
			t.Fatalf("%s: %v", tag, err)
		}
		if err := s.readable(&DefaultLimits); err == nil || !strings.Contains(err.Error(), "exceeds the pattern ceiling of 4096 code points") {
			t.Errorf("%s: %v", tag, err)
		}
	}
	want(t, check[string](t, `maxLength=4096,pattern=a`, q(strings.Repeat("a", 4096))))
	// A backstop past the ceiling: the pattern ceiling holds, and the
	// document says so.
	wide := DefaultLimits
	wide.MaxStringLength = 10000
	s, err := parseConstraints(`pattern=^(a+)+$`, &schema{Type: "string"})
	if err != nil {
		t.Fatal(err)
	}
	d := &decoder{limits: wide, in: "body"}
	d.str(s, strings.Repeat("a", 5000)+"!", "$")
	if len(d.errs) != 1 || d.errs[0].Message != "string length 5001 exceeds the pattern-validation ceiling of 4096 code points" {
		t.Fatal(d.errs)
	}
	if n := s.document(&wide)["maxLength"]; n != 4096 {
		t.Fatal(n)
	}
	if n := (&schema{Type: "string"}).document(&wide)["maxLength"]; n != 10000 {
		t.Fatal(n)
	}
}

// The document states the backstops a request is held to where the schema
// declares no bound: a string's maxLength, an array's maxItems, a map's
// maxProperties; an enum whose members fit needs none, and a declared bound
// is stated as declared.
func TestDocumentStatesTheBackstops(t *testing.T) {
	lim := DefaultLimits
	lim.MaxStringLength, lim.MaxItems = 7, 9
	reg := newRegistry()
	for tag, want := range map[string]string{
		"":                 `{"items":{"maxLength":7,"type":"string"},"maxItems":9,"type":"array"}`,
		"maxItems=3":       `{"items":{"maxLength":7,"type":"string"},"maxItems":3,"type":"array"}`,
		"minItems=1":       `{"items":{"maxLength":7,"type":"string"},"maxItems":9,"minItems":1,"type":"array"}`,
		"uniqueItems=true": `{"items":{"maxLength":7,"type":"string"},"maxItems":9,"type":"array","uniqueItems":true}`,
	} {
		c, err := reg.codecFor(reflect.TypeFor[[]string]())
		if err != nil {
			t.Fatal(err)
		}
		s, err := parseConstraints(tag, c.use())
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(s.document(&lim), json.Deterministic(true))
		if string(b) != want {
			t.Errorf("%q: %s, want %s", tag, b, want)
		}
	}
	for tag, want := range map[string]any{"maxLength=3": 3, "enum=a|b": nil, "enum=a|bbbbbbbb,maxLength=8": 8, "": 7} {
		s, err := parseConstraints(tag, &schema{Type: "string"})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.withinLimits(&lim); err != nil {
			t.Fatalf("%q: %v", tag, err)
		}
		if got := s.document(&lim)["maxLength"]; got != want {
			t.Errorf("%q: maxLength %v, want %v", tag, got, want)
		}
	}
	// A member past the backstop no request carries: refused, not documented
	// beside a maxLength it exceeds.
	s, err := parseConstraints("enum=a|bbbbbbbb", &schema{Type: "string"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.withinLimits(&lim); err == nil || !strings.Contains(err.Error(), `enum member "bbbbbbbb" exceeds Limits.MaxStringLength 7`) {
		t.Errorf("enum=a|bbbbbbbb: %v", err)
	}
	c, err := reg.codecFor(reflect.TypeFor[map[string]int]())
	if err != nil {
		t.Fatal(err)
	}
	if got := c.use().document(&lim)["maxProperties"]; got != 9 {
		t.Errorf("map: maxProperties %v", got)
	}
	if got := c.use().document(nil)["maxProperties"]; got != nil {
		t.Errorf("no limits: maxProperties %v", got)
	}
}

// newWith builds an app with one operation of In and Out, under opts.
func newWith[In, Out any](opts ...Option) (*App, error) {
	h := func(context.Context, *In) (*Out, error) { return nil, nil }
	return New(Table{Routes: []Entry{{Path: "/x", Route: Route{Post: Op(http.StatusOK, h, Doc{})}}}}, opts...)
}

type lowerString struct {
	S string `json:"s" schema:"minLength=5000"`
}

// lowerStringBody reads a lowerString, whose schema then states the backstop.
type lowerStringBody struct {
	B lowerString `body:"json"`
}

type lowerStringCapped struct {
	S string `json:"s" schema:"minLength=5000,maxLength=6000"`
}

type lowerPattern struct {
	S string `json:"s" schema:"pattern=^a+$,minLength=101"`
}

type lowerItems struct {
	L []string `json:"l" schema:"minItems=9000"`
}

type lowerItemsCapped struct {
	L []string `json:"l" schema:"minItems=9000,maxItems=9000"`
}

type lowerParams struct {
	Q   string `query:"q" schema:"minLength=11"`
	IDs []int  `query:"id" schema:"minItems=3"`
}

// ownLower writes its own JSON, and WithSchema declares it a string.
type ownLower struct{ s string }

func (o ownLower) MarshalJSON() ([]byte, error)  { return json.Marshal(o.s) }
func (o *ownLower) UnmarshalJSON(b []byte) error { return json.Unmarshal(b, &o.s) }

type lowerDeclared struct {
	O ownLower `json:"o"`
}

// bodyOf reads a T as its body.
type bodyOf[T any] struct {
	B T `body:"json"`
}

// readOnly reads a T as its body and answers nothing.
func readOnly[T any](opts ...Option) (*App, error) { return newWith[bodyOf[T], struct{}](opts...) }

// writtenOnly answers a T and reads nothing.
func writtenOnly[T any](opts ...Option) (*App, error) { return newWith[struct{}, T](opts...) }

// geta.New refuses a declared lower bound that the backstop, which a
// request's schema states as the upper bound where none is declared, leaves no
// value to meet: the app's own Limits, in a body, a parameter, and a
// WithSchema declaration. A declared upper bound replaces the backstop. A
// minLength past the pattern ceiling beside a pattern is refused whatever
// the Limits, and by checkRequestTag, which getavet applies to what a
// request reads, too. A type only a response writes is refused neither: its
// schema states no backstop and no ceiling, and Conforms holds a response
// to neither.
func TestLowerBoundsPastTheBackstopAreRefused(t *testing.T) {
	refused := func(err error, fragments ...string) {
		t.Helper()
		if err == nil {
			t.Fatalf("accepted; want %q", fragments)
		}
		for _, f := range fragments {
			if !strings.Contains(err.Error(), f) {
				t.Errorf("error lacks %q:\n%v", f, err)
			}
		}
	}
	accepted := func(_ *App, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	const strCeiling = "exceeds Limits.MaxStringLength %d; declare a maxLength"
	_, err := readOnly[lowerString]()
	refused(err, "lowerString.S: minLength 5000 "+fmt.Sprintf(strCeiling, 4096))
	accepted(writtenOnly[lowerString]())
	// Read and written, as an echo is: read.
	_, err = newWith[bodyOf[lowerString], lowerString]()
	refused(err, "lowerString.S: minLength 5000 "+fmt.Sprintf(strCeiling, 4096))
	wide := DefaultLimits
	wide.MaxStringLength = 5000
	app, err := newWith[lowerStringBody, struct{}](WithLimits(wide))
	if err != nil {
		t.Fatal(err)
	}
	if d := string(app.OpenAPI()); !strings.Contains(d, `"maxLength": 5000,`) || !strings.Contains(d, `"minLength": 5000,`) {
		t.Errorf("document: %s", app.OpenAPI())
	}
	accepted(readOnly[lowerStringCapped]())

	narrow := DefaultLimits
	narrow.MaxStringLength, narrow.MaxItems = 100, 2
	_, err = readOnly[lowerPattern](WithLimits(narrow))
	refused(err, "lowerPattern.S: minLength 101 "+fmt.Sprintf(strCeiling, 100))
	accepted(writtenOnly[lowerPattern](WithLimits(narrow)))
	accepted(readOnly[lowerPattern]())
	_, err = newWith[lowerParams, struct{}](WithLimits(narrow))
	refused(err, "lowerParams.IDs: minItems 3 exceeds Limits.MaxItems 2; declare a maxItems")
	narrow.MaxStringLength, narrow.MaxItems = 10, 3
	_, err = newWith[lowerParams, struct{}](WithLimits(narrow))
	refused(err, "lowerParams.Q: minLength 11 "+fmt.Sprintf(strCeiling, 10))
	narrow.MaxStringLength = 11
	if _, err := newWith[lowerParams, struct{}](WithLimits(narrow)); err != nil {
		t.Fatal(err)
	}

	_, err = readOnly[lowerItems]()
	refused(err, "lowerItems.L: minItems 9000 exceeds Limits.MaxItems 8192")
	accepted(writtenOnly[lowerItems]())
	accepted(readOnly[lowerItemsCapped]())

	withSchema := WithSchema[ownLower]("string", "minLength=5000")
	_, err = readOnly[lowerDeclared](withSchema)
	refused(err, "geta.WithSchema[", "minLength 5000 "+fmt.Sprintf(strCeiling, 4096))
	accepted(writtenOnly[lowerDeclared](withSchema))

	// The pattern ceiling, which no Limits moves.
	const pat = "minLength 4097 with a pattern exceeds the pattern ceiling of 4096 code points"
	refused(checkRequestTag("pattern=a,minLength=4097", "string"), pat)
	refused(checkRequestTag("minLength=4097,pattern=a", "text"), pat)
	if err := checkRequestTag("pattern=a,minLength=4096", "string"); err != nil {
		t.Fatal(err)
	}
	// What a response writes is held to no ceiling.
	if err := checkTag("pattern=a,minLength=4097", "string"); err != nil {
		t.Fatal(err)
	}
	// Without a pattern, only the Limits make it unreachable, which
	// checkRequestTag does not see.
	if err := checkRequestTag("minLength=5000", "string"); err != nil {
		t.Fatal(err)
	}
	if err := checkRequestTag("minItems=9000", "slice"); err != nil {
		t.Fatal(err)
	}
	wide.MaxStringLength = 10000
	type patternLong struct {
		S string `json:"s" schema:"pattern=a,minLength=4097"`
	}
	_, err = readOnly[patternLong](WithLimits(wide))
	refused(err, pat)
	accepted(writtenOnly[patternLong]())
	type patternLongParam struct {
		Q string `query:"q" schema:"pattern=a,minLength=4097"`
	}
	_, err = newWith[patternLongParam, struct{}](WithLimits(wide))
	refused(err, pat)

	// An enum member past the backstop, which no request carries.
	type enumLong struct {
		S string `json:"s" schema:"enum=a|bbbbbbbb"`
	}
	type enumLongCapped struct {
		S string `json:"s" schema:"enum=a|bbbbbbbb,maxLength=8"`
	}
	type enumLongParam struct {
		Q string `query:"q" schema:"enum=a|bbbbbbbb"`
	}
	seven := DefaultLimits
	seven.MaxStringLength = 7
	const enumCeiling = `enum member "bbbbbbbb" exceeds Limits.MaxStringLength 7; declare a maxLength`
	_, err = readOnly[enumLong](WithLimits(seven))
	refused(err, "enumLong.S: "+enumCeiling)
	accepted(writtenOnly[enumLong](WithLimits(seven)))
	_, err = newWith[enumLongParam, struct{}](WithLimits(seven))
	refused(err, "enumLongParam.Q: "+enumCeiling)
	accepted(readOnly[enumLongCapped](WithLimits(seven)))
	accepted(readOnly[enumLong]())
	// Past the pattern ceiling, whatever the Limits: checkRequestTag
	// too, for what a request reads.
	long := strings.Repeat("a", 4097)
	const enumPat = "with a pattern exceeds the pattern ceiling of 4096 code points"
	refused(checkRequestTag("pattern=^a+$,enum=a|"+long, "string"), enumPat)
	if err := checkRequestTag("pattern=^a+$,enum=a|"+long[1:], "string"); err != nil {
		t.Fatal(err)
	}
	if err := checkRequestTag("enum=a|"+long, "string"); err != nil {
		t.Fatal(err)
	}
	if err := checkTag("pattern=^a+$,enum=a|"+long, "string"); err != nil {
		t.Fatal(err)
	}
}

// A string with no maxLength is held to the backstop.
func TestStringBackstop(t *testing.T) {
	want(t, check[string](t, "", q(strings.Repeat("x", 4096))))
	want(t, check[string](t, "", q(strings.Repeat("x", 4097))),
		"$: string length 4097 exceeds the ceiling of 4096 code points")
	want(t, check[string](t, "maxLength=5000", q(strings.Repeat("x", 4097))))
}

func TestFormats(t *testing.T) {
	want(t, check[time.Time](t, "", q("2026-07-18T09:30:00Z")))
	want(t, check[time.Time](t, "", q("2026-07-18T09:30:00.123+09:00")))
	want(t, check[time.Time](t, "", q("2026-07-18 09:30:00")), `$: "2026-07-18 09:30:00" is not a valid date-time`)
	want(t, check[string](t, "format=date", q("2026-07-18")))
	want(t, check[string](t, "format=date", q("2026-02-30")), `$: "2026-02-30" is not a valid date`)
	want(t, check[string](t, "format=date", q("2026-13-01")), `$: "2026-13-01" is not a valid date`)
	want(t, check[uuid.UUID](t, "", q("123e4567-e89b-12d3-a456-426614174000")))
	want(t, check[uuid.UUID](t, "", q("123e4567e89b12d3a456426614174000")),
		`$: "123e4567e89b12d3a456426614174000" is not a valid uuid`)
	// Every format geta has a type for is checked on a string as the type
	// checks its text.
	want(t, check[string](t, "format=ipv4", q("1.2.3.256")), `$: "1.2.3.256" is not a valid ipv4`)
	want(t, check[string](t, "format=ipv6", q("::ffff:1.2.3.4")))
	want(t, check[string](t, "format=uuid", q("{123e4567-e89b-12d3-a456-426614174000}")),
		`$: "{123e4567-e89b-12d3-a456-426614174000}" is not a valid uuid`)
	want(t, check[string](t, "format=time", q("08:59:60+09:00")))
	want(t, check[string](t, "format=time", q("22:59:60Z")), `$: "22:59:60Z" is not a valid time`)
	want(t, check[string](t, "format=duration", q("P1Y2D")), `$: "P1Y2D" is not a valid duration`)
	want(t, check[string](t, "format=json-pointer", q("a")), `$: "a" is not a valid json-pointer`)
	want(t, check[string](t, "format=password", q("")))
}

// A date-time is RFC 3339's, exactly: a leap second and a lower-case t or z
// are valid; an offset past 23:59 and a comma before the fraction are not.
// A time.Time takes every one a string takes but the leap second, which it
// cannot hold, and says so.
func TestDateTimeIsRFC3339(t *testing.T) {
	for _, s := range []string{"1998-12-31T23:59:60Z", "1998-12-31T15:59:60.123-08:00", "1963-06-19t08:30:06.283185z",
		"2026-07-18T09:30:00Z", "2026-07-18t09:30:00.5+09:00"} {
		want(t, check[string](t, "format=date-time", q(s)))
	}
	for _, s := range []string{"1990-12-31T15:59:59-24:00", "1990-12-31T10:00:00+10:60", "2026-07-18T09:30:00,5Z",
		"1998-12-31T22:59:60Z", "2026-07-18T9:30:00Z", "2026-07-18 09:30:00Z"} {
		want(t, check[string](t, "format=date-time", q(s)), `$: "`+s+`" is not a valid date-time`)
		want(t, check[time.Time](t, "", q(s)), `$: "`+s+`" is not a valid date-time`)
	}
	want(t, check[time.Time](t, "", q("1998-12-31T23:59:60Z")),
		`$: "1998-12-31T23:59:60Z" is a leap second, which a time.Time does not hold`)
	for s, at := range map[string]time.Time{
		"1963-06-19t08:30:06.283185z": time.Date(1963, 6, 19, 8, 30, 6, 283185000, time.UTC),
		"2026-07-18t09:30:00+09:00":   time.Date(2026, 7, 18, 0, 30, 0, 0, time.UTC),
	} {
		want(t, check[time.Time](t, "", q(s)))
		var got struct {
			At time.Time `json:"at"`
		}
		r := newRegistry()
		r.sealDeclarations()
		if err := json.Unmarshal([]byte(`{"at":`+q(s)+`}`), &got, r.decOpts); err != nil || !got.At.Equal(at) {
			t.Errorf("%s: read %v, %v; want %v", s, got.At, err, at)
		}
		var p time.Time
		if err := unmarshalText(&p, []byte(s)); err != nil || !p.Equal(at) {
			t.Errorf("%s: read %v, %v; want %v", s, p, err, at)
		}
	}
}

func TestNumericBounds(t *testing.T) {
	const tag = "minimum=0,maximum=10"
	want(t, check[float64](t, tag, "0"))
	want(t, check[float64](t, tag, "10"))
	want(t, check[float64](t, tag, "5.5"))
	want(t, check[float64](t, tag, "-1"), "$: -1 is less than minimum 0")
	want(t, check[float64](t, tag, "11"), "$: 11 is greater than maximum 10")
	const ex = "exclusiveMinimum=0,exclusiveMaximum=10"
	want(t, check[float64](t, ex, "0"), "$: 0 is not greater than exclusiveMinimum 0")
	want(t, check[float64](t, ex, "10"), "$: 10 is not less than exclusiveMaximum 10")
	want(t, check[int](t, "minimum=1,maximum=3", "0"), "$: 0 is less than minimum 1")
}

func TestMultipleOf(t *testing.T) {
	want(t, check[int](t, "multipleOf=3", "9"))
	want(t, check[int](t, "multipleOf=3", "0"))
	want(t, check[int](t, "multipleOf=3", "7"), "$: 7 is not a multiple of 3")
	want(t, check[float64](t, "multipleOf=0.1", "0.3"))
	want(t, check[float64](t, "multipleOf=0.1", "0.35"), "$: 0.35 is not a multiple of 0.1")
}

func TestIntegerRangeAndForm(t *testing.T) {
	want(t, check[int8](t, "", "127"))
	want(t, check[int8](t, "", "128"), "$: 128 is out of range for int8")
	want(t, check[int64](t, "", "9223372036854775808"), "$: 9223372036854775808 is out of range for int64")
	want(t, check[uint](t, "", "-1"), "$: -1 is out of range for uint")
	// 1.0 is a number on the wire, not an integer.
	want(t, check[int](t, "minimum=10", "5.0"), "$: expected integer, got number")
	want(t, check[float64](t, "", "1"))
}

func TestArrayKeywords(t *testing.T) {
	const tag = "minItems=1,maxItems=3"
	want(t, check[[]string](t, tag, `["a"]`))
	want(t, check[[]string](t, tag, `["a","b","c"]`))
	want(t, check[[]string](t, tag, `[]`), "$: array length 0 is shorter than minItems 1")
	want(t, check[[]string](t, tag, `["a","b","c","d"]`), "$: array length 4 exceeds maxItems 3")
	// An array-level fact is reported beside an element's.
	want(t, check[[]string](t, "minItems=3", `["a",1]`),
		"$: array length 2 is shorter than minItems 3", "$[1]: expected string, got integer")
}

func TestUniqueItems(t *testing.T) {
	want(t, check[[]int](t, "uniqueItems=true", `[1,2,3]`))
	want(t, check[[]int](t, "uniqueItems=true", `[1,2,1]`), "$: array items at [0] and [2] are equal (uniqueItems)")
	want(t, check[[]float64](t, "uniqueItems=true", `[1,1.0]`), "$: array items at [0] and [1] are equal (uniqueItems)")
	type kv struct {
		K int `json:"k"`
	}
	want(t, check[[]kv](t, "uniqueItems=true", `[{"k":1},{"k":2}]`))
	want(t, check[[]kv](t, "uniqueItems=true", `[{"k":1},{"k":1}]`), "$: array items at [0] and [1] are equal (uniqueItems)")
	want(t, check[[]int](t, "uniqueItems=false", `[1,1,1]`))
	// maxItems gates the scan: an over-long array is reported by length only.
	want(t, check[[]int](t, "maxItems=3,uniqueItems=true", `[1,1,1,1]`), "$: array length 4 exceeds maxItems 3")
}

func TestArrayBackstop(t *testing.T) {
	big := func(n int) string {
		var b strings.Builder
		b.WriteByte('[')
		for i := range n {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString("1")
		}
		b.WriteByte(']')
		return b.String()
	}
	want(t, check[[]int](t, "", big(8192)))
	want(t, check[[]int](t, "uniqueItems=true", big(8193)), "$: array length 8193 exceeds the ceiling of 8192 items")
	start := time.Now()
	want(t, check[[]int](t, "uniqueItems=true", big(150000)), "$: array length 150000 exceeds the ceiling of 8192 items")
	if time.Since(start) > 5*time.Second {
		t.Fatal("the over-ceiling array was scanned")
	}
}

func TestWrongTypeSkipsKeywords(t *testing.T) {
	want(t, check[string](t, `minLength=3,pattern=\d`, "42"), "$: expected string, got integer")
	want(t, check[string](t, "", "null"), "$: expected string, got null")
}

type keyUser struct {
	Name string   `json:"name" schema:"minLength=1,maxLength=3"`
	Age  int      `json:"age" schema:"minimum=0"`
	Nick *string  `json:"nick,omitzero"`
	Tags []string `json:"tags"`
}

func TestObjectPaths(t *testing.T) {
	want(t, check[keyUser](t, "", `{"name":"abc","age":1,"tags":[]}`))
	want(t, check[keyUser](t, "", `{"name":"abcd","age":-1,"tags":["a",2]}`),
		"$.name: string length 4 exceeds maxLength 3",
		"$.age: -1 is less than minimum 0",
		"$.tags[1]: expected string, got integer")
	want(t, check[keyUser](t, "", `{"age":1}`), "$.name: missing required member", "$.tags: missing required member")
	want(t, check[keyUser](t, "", `{"name":"a","age":1,"tags":[],"Name":"b","zz":1}`),
		"$.Name: unknown member", "$.zz: unknown member")
	want(t, check[keyUser](t, "", `{"name":"a","age":1,"tags":[],"nick":null}`), "$.nick: expected string, got null")
	want(t, check[keyUser](t, "", `[]`), "$: expected object, got array")
}

func TestMapValues(t *testing.T) {
	want(t, check[map[string]int](t, "", `{"a":1,"b":2}`))
	want(t, check[map[string]int](t, "", `{"a":1,"b":"x"}`), "$.b: expected integer, got string")
}

type node struct {
	Next *node `json:"next,omitzero"`
}

func TestNestingCeiling(t *testing.T) {
	nest := func(depth int) string {
		return strings.Repeat(`{"next":`, depth) + "{}" + strings.Repeat("}", depth)
	}
	want(t, check[node](t, "", nest(50)))
	got := check[node](t, "", nest(5000))
	if len(got) != 1 || !strings.Contains(got[0], "nesting exceeds the ceiling of 512") {
		t.Fatalf("got %q", got)
	}
}

func TestParseRejectsAmbiguousJSON(t *testing.T) {
	want(t, check[keyUser](t, "", `{"name":"a","name":"b","age":1,"tags":[]}`), "parse: duplicate object key at $.name")
	want(t, check[keyUser](t, "", `{"name":"a","age":1,"tags":[]} {}`), "parse: trailing data after the JSON value")
	want(t, check[keyUser](t, "", `{"name":`), "parse: unexpected end of JSON input")
}

func TestEncodeShapesMatchTheSchema(t *testing.T) {
	if _, err := newRegistry().codecFor(reflect.TypeFor[keyUser]()); err != nil {
		t.Fatal(err)
	}
	b, err := marshal(&keyUser{Name: "a<b>"}, marshalOptions)
	if err != nil {
		t.Fatal(err)
	}
	// A nil slice is [], never null; a nil pointer is omitted.
	// < and > are escaped, so the body is safe to embed in HTML.
	bs := string(rune(92))
	wantJSON := strings.NewReplacer("<", bs+"u003c", ">", bs+"u003e").Replace(`{"name":"a<b>","age":0,"tags":[]}`)
	if got := string(b); got != wantJSON {
		t.Fatalf("got %q, want %q", got, wantJSON)
	}
}

// Numbers are checked exactly as written against the bound as written, on
// the body path (agree) and the parameter path (bindsAlike) alike.

// bindsAlike binds s as T's only parameter with bind and with bindFast,
// fails if they disagree, and reports whether s was taken.
func bindsAlike[T any](t *testing.T, s string) bool {
	t.Helper()
	p, err := newRegistry().inPlan(reflect.TypeFor[T]())
	if err != nil {
		t.Fatal(err)
	}
	pp := p.params[0]
	var want, got T
	d := &decoder{limits: DefaultLimits, in: pp.in}
	pp.bind(d, []string{s}, reflect.ValueOf(&want).Elem().FieldByIndex(pp.index))
	ok := pp.bindFast(s, reflect.ValueOf(&got).Elem().FieldByIndex(pp.index), DefaultLimits)
	switch {
	case ok && len(d.errs) > 0:
		t.Fatalf("%q: bindFast took it, bind refuses it: %v", s, d.errs)
	case !ok && len(d.errs) == 0:
		t.Fatalf("%q: bindFast refused it, bind takes it", s)
	case ok && !reflect.DeepEqual(want, got):
		t.Fatalf("%q: values differ: %#v %#v", s, want, got)
	}
	return ok
}

type numF32Max struct {
	F float32 `json:"f" schema:"maximum=0.1"`
}

type numF32ExclMin struct {
	F float32 `json:"f" schema:"exclusiveMinimum=0.1"`
}

type numF64Max struct {
	F float64 `json:"f" schema:"maximum=0.1"`
}

type numF32MaxParam struct {
	F float32 `query:"f" schema:"maximum=0.1"`
}

type numF32ExclMinParam struct {
	F float32 `query:"f" schema:"exclusiveMinimum=0.1"`
}

// A bound is met by the number as written, not as the Go type holds it:
// 0.1 is at most 0.1 and not above it, though a float32 holds it as
// 0.100000001…, and 0.10000000000000001 is above 0.1, though it reads as the
// same float64.
func TestFloatBoundsCompareTheWrittenNumber(t *testing.T) {
	for body, want := range map[string]bool{
		`{"f":0.1}`:                 true,
		`{"f":1e-1}`:                true,
		`{"f":0.0999999999999999}`:  true,
		`{"f":0.10000000000000001}`: false,
		`{"f":0.10000001}`:          false,
	} {
		if got := agree[numF32Max](t, []byte(body), DefaultLimits); got != want {
			t.Errorf("float32 maximum=0.1, %s: accepted %v, want %v", body, got, want)
		}
		if got := agree[numF64Max](t, []byte(body), DefaultLimits); got != want {
			t.Errorf("float64 maximum=0.1, %s: accepted %v, want %v", body, got, want)
		}
		if got := agree[numF32ExclMin](t, []byte(body), DefaultLimits); got != !want {
			t.Errorf("float32 exclusiveMinimum=0.1, %s: accepted %v, want %v", body, got, !want)
		}
	}
	for s, want := range map[string]bool{"0.1": true, "0.100": true, "1e-1": true, "0.10000000000000001": false} {
		if got := bindsAlike[numF32MaxParam](t, s); got != want {
			t.Errorf("query maximum=0.1, f=%s: taken %v, want %v", s, got, want)
		}
		if got := bindsAlike[numF32ExclMinParam](t, s); got != !want {
			t.Errorf("query exclusiveMinimum=0.1, f=%s: taken %v, want %v", s, got, !want)
		}
	}
}

type numUniqueInts struct {
	L []int64 `json:"l" schema:"uniqueItems=true"`
}

type numUniqueFloats struct {
	L []float64 `json:"l" schema:"uniqueItems=true"`
}

// uniqueItems compares numbers by their exact value: distinct integers past
// 2^53 are distinct, and 1, 1.0 and 1e0 are one number.
func TestUniqueItemsComparesNumbersExactly(t *testing.T) {
	for body, want := range map[string]bool{
		`{"l":[9007199254740992,9007199254740993]}`:       true,
		`{"l":[9223372036854775806,9223372036854775807]}`: true,
		`{"l":[9007199254740993,9007199254740993]}`:       false,
		`{"l":[-9007199254740993,-9007199254740992]}`:     true,
	} {
		if got := agree[numUniqueInts](t, []byte(body), DefaultLimits); got != want {
			t.Errorf("%s: accepted %v, want %v", body, got, want)
		}
	}
	for body, want := range map[string]bool{
		`{"l":[1,1.0]}`:                                  false,
		`{"l":[1,1e0]}`:                                  false,
		`{"l":[0,-0]}`:                                   false,
		`{"l":[0.1,0.10000000000000001]}`:                true,
		`{"l":[100,1e2,10]}`:                             false,
		`{"l":[1e-400,2e-400]}`:                          true,
		`{"l":[9007199254740992.5,9007199254740992.25]}`: true,
	} {
		if got := agree[numUniqueFloats](t, []byte(body), DefaultLimits); got != want {
			t.Errorf("%s: accepted %v, want %v", body, got, want)
		}
	}
}

// canonical writes equal numbers alike and distinct ones apart, whatever the
// exponent.
func TestCanonicalNumbers(t *testing.T) {
	same := [][]string{
		{"1", "1.0", "1e0", "1E+0", "10e-1", "0.1e1", "100e-2", "0001", "+1"},
		{"0", "-0", "0.0", "0e5", "-0.000e-9"},
		{"-12.5", "-125e-1", "-0.125e2", "-1250E-2"},
		{"9007199254740993", "9.007199254740993e15", "90071992547409930e-1"},
		{"1e99999999999999999999", "10e99999999999999999998", "0.1e100000000000000000000"},
		{"1e-99999999999999999999", "0.1e-99999999999999999998", "100e-100000000000000000001"},
		{"1e+00000000000000000000000001", "10"},
	}
	for _, group := range same {
		want := canonical(number(group[0]))
		for _, lit := range group[1:] {
			if got := canonical(number(lit)); got != want {
				t.Errorf("%s is %s, but %s is %s", group[0], want, lit, got)
			}
		}
	}
	distinct := []string{"9007199254740992", "9007199254740993", "1", "-1", "0.1", "0.10000000000000001",
		"1e99999999999999999999", "1e99999999999999999998", "1e-99999999999999999999", "1e-99999999999999999998",
		"1e10000000000000000", "1e9999999999999999"}
	seen := map[string]string{}
	for _, lit := range distinct {
		k := canonical(number(lit))
		if prev, dup := seen[k]; dup {
			t.Errorf("%s and %s both render as %s", prev, lit, k)
		}
		seen[k] = lit
	}
}

type numMult3 struct {
	N int64 `json:"n" schema:"multipleOf=3"`
}

type numMultTenth struct {
	F float64 `json:"f" schema:"multipleOf=0.1"`
}

type numMult3Param struct {
	N int64 `query:"n" schema:"multipleOf=3"`
}

// multipleOf is decided exactly: no tolerance lets a large quotient through,
// and decimal multiples are multiples.
func TestMultipleOfIsExact(t *testing.T) {
	for _, n := range []int64{0, 3, -3, 1500000001, 1500000002, 1500000003, 9223372036854775806, 9223372036854775807,
		-9223372036854775808, 9007199254740993, 9007199254740994} {
		lit := []byte(`{"n":` + itoa64(n) + `}`)
		want := n%3 == 0
		if got := agree[numMult3](t, lit, DefaultLimits); got != want {
			t.Errorf("%d multipleOf=3: accepted %v, want %v", n, got, want)
		}
		if got := bindsAlike[numMult3Param](t, itoa64(n)); got != want {
			t.Errorf("query %d multipleOf=3: taken %v, want %v", n, got, want)
		}
	}
	for body, want := range map[string]bool{
		`{"f":0.3}`:                 true,
		`{"f":3e-1}`:                true,
		`{"f":-0.7}`:                true,
		`{"f":12345678.9}`:          true,
		`{"f":1e300}`:               true,
		`{"f":0}`:                   true,
		`{"f":0.30000000000000004}`: false,
		`{"f":0.35}`:                false,
		`{"f":1e-400}`:              false,
	} {
		if got := agree[numMultTenth](t, []byte(body), DefaultLimits); got != want {
			t.Errorf("%s multipleOf=0.1: accepted %v, want %v", body, got, want)
		}
	}
	for _, c := range []struct {
		lit  string
		m    float64
		want bool
	}{
		{"0.5", 0.25, true}, {"1", 0.3, false}, {"7.5", 2.5, true}, {"1e20", 7, false}, {"7e20", 7, true},
		{"1e100", 1024, true}, {"1e100", 3, false}, {"1e-99999999999999999999", 0.1, false},
		{"1e99999999999999999999", 1024, true}, {"1e99999999999999999999", 3, false},
	} {
		if got := isMultiple(c.lit, c.m); got != c.want {
			t.Errorf("isMultiple(%s, %v) = %v", c.lit, c.m, got)
		}
	}
}

func itoa64(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

type numIntMax struct {
	N int64 `json:"n" schema:"maximum=9007199254740992"`
}

type numIntExclMax struct {
	N int64 `json:"n" schema:"exclusiveMaximum=9007199254740992"`
}

type numUintMin struct {
	N uint64 `json:"n" schema:"minimum=18446744073709551615"`
}

type numIntMaxParam struct {
	N int64 `query:"n" schema:"maximum=9007199254740992"`
}

type numUintMinParam struct {
	N uint64 `query:"n" schema:"minimum=18446744073709551615"`
}

// Integer bounds are compared exactly, past 2^53 too.
func TestIntegerBoundsAreExact(t *testing.T) {
	for n, want := range map[string]bool{"9007199254740991": true, "9007199254740992": true, "9007199254740993": false,
		"9223372036854775807": false} {
		if got := agree[numIntMax](t, []byte(`{"n":`+n+`}`), DefaultLimits); got != want {
			t.Errorf("maximum=9007199254740992, %s: accepted %v, want %v", n, got, want)
		}
		if got := bindsAlike[numIntMaxParam](t, n); got != want {
			t.Errorf("query maximum=9007199254740992, %s: taken %v, want %v", n, got, want)
		}
	}
	for n, want := range map[string]bool{"9007199254740991": true, "9007199254740992": false, "9007199254740993": false} {
		if got := agree[numIntExclMax](t, []byte(`{"n":`+n+`}`), DefaultLimits); got != want {
			t.Errorf("exclusiveMaximum=9007199254740992, %s: accepted %v, want %v", n, got, want)
		}
	}
	// A float64 holds the tag's 18446744073709551615 as 2^64, whose shortest
	// decimal 18446744073709552000 the document would write and the check
	// would enforce in its place, so the tag is refused.
	const held = `"18446744073709551615" is not exact as a float64; use 18446744073709552000`
	if _, err := newRegistry().codecFor(reflect.TypeFor[numUintMin]()); err == nil || !strings.Contains(err.Error(), held) {
		t.Errorf("minimum=18446744073709551615 on a member: %v; want %q", err, held)
	}
	if _, err := newRegistry().inPlan(reflect.TypeFor[numUintMinParam]()); err == nil || !strings.Contains(err.Error(), held) {
		t.Errorf("minimum=18446744073709551615 on a parameter: %v; want %q", err, held)
	}
}
