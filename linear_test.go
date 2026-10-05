package geta

import (
	"bytes"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The work geta does on a request body grows linearly with the body, within
// the limits: one request cannot burn limit × limit CPU.

// linMapNode nests maps under client-chosen keys, so a violation's path can be
// as long as the body.
type linMapNode struct {
	K map[string]linMapNode `json:"k"`
}

type linMapRoot struct {
	N linMapNode `json:"n"`
	X int        `json:"x"`
}

// linMapBody is a body of about n bytes: a chain of depth maps under keys of
// one length, half the body, then leaves under the deepest map, and a value
// the single pass refuses, so that the reference path reads it all.
func linMapBody(n, depth int) []byte {
	var b bytes.Buffer
	key := strings.Repeat("k", max(1, n/2/depth-10))
	b.WriteString(`{"n":`)
	for range depth {
		b.WriteString(`{"k":{"` + key + `":`)
	}
	b.WriteString(`{"k":{`)
	for i := 0; b.Len() < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"a` + strconv.Itoa(i) + `":{"k":{}}`)
	}
	b.WriteString(`}}`)
	for range depth {
		b.WriteString(`}}`)
	}
	b.WriteString(`,"x":"no"}`)
	return b.Bytes()
}

// linDefNode nests maps whose values take a default.
type linDefNode struct {
	A string                `json:"a" schema:"default=x"`
	K map[string]linDefNode `json:"k"`
}

// linDefBody is linMapBody for linDefNode, valid, every default omitted.
func linDefBody(n, depth int) []byte {
	var b bytes.Buffer
	key := strings.Repeat("k", max(1, n/2/depth-10))
	for range depth {
		b.WriteString(`{"k":{"` + key + `":`)
	}
	b.WriteString(`{"k":{`)
	for i := 0; b.Len() < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"a` + strconv.Itoa(i) + `":{"k":{}}`)
	}
	b.WriteString(`}}`)
	for range depth {
		b.WriteString(`}}`)
	}
	return b.Bytes()
}

// linSet nests uniqueItems arrays, so each array's elements hold the deeper
// arrays.
type linSet struct {
	Name string   `json:"name"`
	S    []linSet `json:"s" schema:"uniqueItems=true"`
}

type linSetRoot struct {
	S linSet `json:"s"`
	X int    `json:"x"`
}

// linSetBody is a body of about n bytes: a chain of depth uniqueItems arrays,
// then distinct leaves in the deepest. bad adds a value the single pass
// refuses.
func linSetBody(n, depth int, bad bool) []byte {
	var b bytes.Buffer
	b.WriteString(`{"s":`)
	for range depth {
		b.WriteString(`{"name":"","s":[`)
	}
	b.WriteString(`{"name":"","s":[`)
	for i := 0; b.Len() < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"name":"` + strconv.Itoa(i) + `","s":[]}`)
	}
	b.WriteString(`]}`)
	for range depth {
		b.WriteString(`]}`)
	}
	if bad {
		b.WriteString(`,"x":"no"}`)
	} else {
		b.WriteString(`,"x":1}`)
	}
	return b.Bytes()
}

var linLimits = func() Limits {
	l := DefaultLimits
	l.MaxItems = 1 << 20
	l.MaxBodyBytes = 4 << 20
	return l
}()

func linCodec[T any](tb testing.TB) (*registry, *codec) {
	tb.Helper()
	r := newRegistry()
	c, err := r.codecFor(reflect.TypeFor[T]())
	if err != nil {
		tb.Fatal(err)
	}
	return r, c
}

// runReference reads data on the reference path into a fresh T.
func runReference[T any](tb testing.TB, data []byte) []Violation {
	r, c := linCodec[T](tb)
	return reference(r, c, c.use(), data, linLimits, reflect.ValueOf(new(T)).Elem())
}

// runSinglePass reads data on the single pass into a fresh T.
func runSinglePass[T any](tb testing.TB, data []byte) bool {
	r, c := linCodec[T](tb)
	return singlePassIn(r, c, c.use(), data, linLimits, reflect.ValueOf(new(T)).Elem())
}

// timeOf returns the least time of a few runs of f.
func timeOf(f func()) time.Duration {
	best := time.Duration(1 << 62)
	for range 3 {
		start := time.Now()
		f()
		best = min(best, time.Since(start))
	}
	return best
}

// linear fails unless 4× the size takes about 4× the time: quadratic work
// takes 16×.
func linear(t *testing.T, name string, run func(n int) func()) {
	t.Helper()
	const n = 256 << 10
	small, large := timeOf(run(n)), timeOf(run(4*n))
	ratio := float64(large) / float64(small)
	t.Logf("%s: %v at %d bytes, %v at %d bytes (×%.1f)", name, small, n, large, 4*n, ratio)
	if ratio > 10 {
		t.Errorf("%s: 4× the body took %.1f× the time", name, ratio)
	}
}

type linWide struct {
	M map[string]int `json:"m"`
	X int            `json:"x"`
}

type linList struct {
	L []int `json:"l"`
	X int   `json:"x"`
}

type linNum struct {
	F float64 `json:"f" schema:"minimum=0,maximum=10,multipleOf=0.5"`
	X int     `json:"x"`
}

type linExp struct {
	F float64 `json:"f" schema:"minimum=0,maximum=10"`
	X int     `json:"x"`
}

type linStr struct {
	S string `json:"s" schema:"maxLength=10000000"`
	X int    `json:"x"`
}

type linBad struct {
	L []int `json:"l" schema:"items.maximum=0"`
}

func TestBodyWorkIsLinear(t *testing.T) {
	tail := func(bad bool) string {
		if bad {
			return `,"x":"no"}`
		}
		return `,"x":1}`
	}
	wide := func(n int, bad bool) []byte {
		var b bytes.Buffer
		b.WriteString(`{"m":{`)
		for i := 0; b.Len() < n; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`"k` + strconv.Itoa(i) + `":1`)
		}
		b.WriteString(`}` + tail(bad))
		return b.Bytes()
	}
	list := func(n int, bad bool) []byte {
		return []byte(`{"l":[` + strings.Repeat("1,", n/2) + `1]` + tail(bad))
	}
	num := func(n int, bad bool) []byte {
		return []byte(`{"f":0.5` + strings.Repeat("0", n) + `e0` + tail(bad))
	}
	exp := func(n int, bad bool) []byte {
		return []byte(`{"f":1e-` + strings.Repeat("9", n) + tail(bad))
	}
	str := func(n int, bad bool) []byte {
		return []byte(`{"s":"` + strings.Repeat(`éa`, n/7) + `"` + tail(bad))
	}
	for _, c := range []struct {
		name string
		body func(int, bool) []byte
		ref  func(testing.TB, []byte) []Violation
		fast func(testing.TB, []byte) bool
	}{
		{"wide map", wide, runReference[linWide], runSinglePass[linWide]},
		{"long array", list, runReference[linList], runSinglePass[linList]},
		{"long number", num, runReference[linNum], runSinglePass[linNum]},
		{"long exponent", exp, runReference[linExp], runSinglePass[linExp]},
		{"long string", str, runReference[linStr], runSinglePass[linStr]},
	} {
		linear(t, c.name+" (reference)", func(n int) func() {
			data := c.body(n, true)
			return func() {
				if errs := c.ref(t, data); len(errs) != 1 || errs[0].Path != "$.x" {
					t.Fatalf("%d violations, the first at %.80s: %.80s", len(errs), errs[0].Path, errs[0].Message)
				}
			}
		})
		linear(t, c.name+" (single pass)", func(n int) func() {
			data := c.body(n, false)
			return func() {
				if !c.fast(t, data) {
					t.Fatal("refused")
				}
			}
		})
	}
	// Paths as long as half the body, at each of the leaves.
	linear(t, "long paths", func(n int) func() {
		data := linMapBody(n, 200)
		return func() {
			if errs := runReference[linMapRoot](t, data); len(errs) != 1 || errs[0].Path != "$.x" {
				t.Fatal(len(errs))
			}
		}
	})
	// uniqueItems arrays nested as deep as the body is long: each element is
	// compared without being read again for each array it is nested in.
	linear(t, "nested uniqueItems (reference)", func(n int) func() {
		data := linSetBody(n, n/5000, true)
		return func() {
			if errs := runReference[linSetRoot](t, data); len(errs) != 1 || errs[0].Path != "$.x" {
				t.Fatal(errs)
			}
		}
	})
	linear(t, "nested uniqueItems (single pass)", func(n int) func() {
		data := linSetBody(n, n/5000, false)
		return func() {
			if !runSinglePass[linSetRoot](t, data) {
				t.Fatal("refused")
			}
		}
	})
	// A valid body read on the reference path, defaults filled at every
	// depth.
	linear(t, "defaults at depth (reference)", func(n int) func() {
		data := linDefBody(n, 200)
		return func() {
			var v linDefNode
			r, c := linCodec[linDefNode](t)
			if errs := reference(r, c, c.use(), data, linLimits, reflect.ValueOf(&v).Elem()); len(errs) != 0 {
				t.Fatal(errs[0].Message)
			}
		}
	})
	// Sealed values nested as deep as the body is long, each discriminator
	// first, on the single pass.
	linear(t, "nested sealed values (single pass)", func(n int) func() {
		depth := n / 5000
		pad := strings.Repeat(" ", n/depth)
		var b strings.Builder
		b.WriteString(`{"others":[],"main":`)
		for range depth {
			b.WriteString(`{"kind":"square","side":1,` + pad + `"inner":`)
		}
		b.WriteString(`{"kind":"circle","r":1}`)
		b.WriteString(strings.Repeat("}", depth) + "}")
		data := []byte(b.String())
		r := sealedRegistry(t)
		c, err := r.codecFor(reflect.TypeFor[fastScene]())
		if err != nil {
			t.Fatal(err)
		}
		return func() {
			if !singlePassIn(r, c, c.use(), data, linLimits, reflect.ValueOf(new(fastScene)).Elem()) {
				t.Fatal("refused")
			}
		}
	})
	// Every element fails; only the first 50 violations are kept.
	linear(t, "many violations", func(n int) func() {
		data := []byte(`{"l":[` + strings.Repeat("1,", n/2) + `1]}`)
		return func() {
			if errs := runReference[linBad](t, data); len(errs) != maxViolations {
				t.Fatal(len(errs))
			}
		}
	})
}

// A path is rendered as building it step by step would: path + "." + name,
// path + "[i]".
func TestViolationPathRendering(t *testing.T) {
	p := rootPath("$").member("").item(3).member("a.b").item(0).item(12)
	if got := p.String(); got != "$.[3].a.b[0][12]" {
		t.Fatal(got)
	}
	if got := rootPath("q").String(); got != "q" {
		t.Fatal(got)
	}
}

// linAnySet holds values of a JSON-method type in a uniqueItems array.
type linAnySet struct {
	A []linAny `json:"a" schema:"uniqueItems=true"`
}

type linAny struct{ raw []byte }

func (v linAny) MarshalJSON() ([]byte, error) { return v.raw, nil }
func (v *linAny) UnmarshalJSON(b []byte) error {
	v.raw = append([]byte(nil), b...)
	return nil
}

// Nested uniqueItems arrays find the same duplicates on both paths as
// canonical decides them: member order and a number's spelling do not
// count, and the first pair is named.
func TestNestedUniqueItems(t *testing.T) {
	for _, c := range []struct {
		body string
		errs []string
	}{
		{`{"s":{"name":"","s":[{"name":"a","s":[]},{"name":"b","s":[]}]},"x":1}`, nil},
		{`{"s":{"name":"","s":[{"name":"a","s":[]},{"s":[],"name":"a"}]},"x":1}`,
			[]string{"$.s.s: array items at [0] and [1] are equal (uniqueItems)"}},
		{`{"s":{"name":"","s":[{"name":"","s":[{"name":"a","s":[]},{"name":"b","s":[]}]},` +
			`{"name":"","s":[{"name":"b","s":[]},{"name":"a","s":[]}]},{"s":[{"name":"a","s":[]},{"name":"b","s":[]}],"name":""}]},"x":1}`,
			[]string{"$.s.s: array items at [0] and [2] are equal (uniqueItems)"}},
		{`{"s":{"name":"","s":[{"name":"","s":[{"name":"a","s":[]},{"name":"a","s":[]}]}]},"x":1}`,
			[]string{"$.s.s[0].s: array items at [0] and [1] are equal (uniqueItems)"}},
	} {
		var got []string
		for _, v := range runReference[linSetRoot](t, []byte(c.body)) {
			got = append(got, v.Path+": "+v.Message)
		}
		want(t, got, c.errs...)
		agree[linSetRoot](t, []byte(c.body), DefaultLimits)
	}
	// A JSON-method type's values compare by canonical form too.
	for _, body := range []string{
		`{"a":[1,{"x":[1.0,"é"]},null,true]}`,
		`{"a":[{"x":[1,"é"],"y":{}},{"y":{},"x":[10e-1,"é"]}]}`,
		`{"a":[[1,2],[2,1],[1,2.0]]}`,
	} {
		agree[linAnySet](t, []byte(body), DefaultLimits)
	}
}

// Two distinct values whose hashes match are told apart by canonical form,
// on both paths.
func TestUniqueItemsHashCollision(t *testing.T) {
	tree, err := parseJSON([]byte(`[{"a":1},{"a":2},{"a":1}]`), 10)
	if err != nil {
		t.Fatal(err)
	}
	a := tree.([]any)
	memo := &hashMemo{arrays: map[*any]uint64{}, objects: map[uintptr]uint64{}}
	for _, el := range a {
		memo.objects[reflect.ValueOf(el).Pointer()] = 7 // every element collides
	}
	if j, i, dup := duplicate(a, memo); !dup || j != 0 || i != 2 {
		t.Fatal(j, i, dup)
	}
	if _, _, dup := duplicate(a[:2], memo); dup {
		t.Fatal("distinct values taken as equal")
	}
	f := getFastDecoder(DefaultLimits)
	defer f.release()
	f.data = []byte(`[{"a":1}, {"a":2}]`)
	spans := []int64{1, 8, 8, 17}
	if !f.unique(spans, []uint64{7, 7}) {
		t.Fatal("distinct values taken as equal")
	}
	f.data = []byte(`[{"a":1}, {"a":1}]`)
	if f.unique(spans, []uint64{7, 7}) {
		t.Fatal("equal values taken as distinct")
	}
}

func BenchmarkLinearMapPaths(b *testing.B) {
	for _, n := range []int{128 << 10, 256 << 10, 512 << 10, 1 << 20} {
		data := linMapBody(n, 200)
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if errs := runReference[linMapRoot](b, data); len(errs) != 1 {
					b.Fatal(errs)
				}
			}
		})
	}
}

func BenchmarkLinearUniqueReference(b *testing.B) {
	for _, n := range []int{128 << 10, 256 << 10, 512 << 10, 1 << 20} {
		data := linSetBody(n, 200, true)
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if errs := runReference[linSetRoot](b, data); len(errs) != 1 {
					b.Fatal(errs)
				}
			}
		})
	}
}

func BenchmarkLinearUniqueDepth(b *testing.B) {
	for _, depth := range []int{1, 50, 100, 200} {
		data := linSetBody(1<<20, depth, false)
		b.Run(fmt.Sprint(depth), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if !runSinglePass[linSetRoot](b, data) {
					b.Fatal("refused")
				}
			}
		})
	}
}

func BenchmarkLinearUniqueSinglePass(b *testing.B) {
	for _, n := range []int{128 << 10, 256 << 10, 512 << 10, 1 << 20} {
		data := linSetBody(n, 200, false)
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if !runSinglePass[linSetRoot](b, data) {
					b.Fatal("refused")
				}
			}
		})
	}
}
