package geta

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// agreeValue is agree for a type known only by reflection: the single pass
// accepts exactly what the reference path accepts, with the same value, and
// it returns the reference's violations.
func agreeValue(t *testing.T, r *registry, typ reflect.Type, data []byte, limits Limits) (bool, []Violation) {
	t.Helper()
	c, err := r.codecFor(typ)
	if err != nil {
		t.Fatal(err)
	}
	want, got := reflect.New(typ), reflect.New(typ)
	errs := reference(r, c, c.use(), data, limits, want.Elem())
	ok := singlePassIn(r, c, c.use(), data, limits, got.Elem())
	switch {
	case ok && len(errs) > 0:
		t.Fatalf("single pass accepted %q, which the reference refuses: %v", data, errs)
	case !ok && len(errs) == 0:
		t.Fatalf("single pass refused %q, which the reference accepts", data)
	case ok && !reflect.DeepEqual(want.Interface(), got.Interface()):
		t.Fatalf("values differ for %q:\nreference %#v\nsingle    %#v", data, want.Interface(), got.Interface())
	}
	return ok, errs
}

// The single pass and the reference path agree on a boolean given a number,
// a map given a number, bytes past their maxLength, and an array longer than
// the first allocation holds.
func TestSinglePassAgreesOnLeafKindsAndLongArrays(t *testing.T) {
	for body, valid := range map[string]bool{
		`{"leaf":{"s":"abc","b":1},"list":[{"s":"a"}]}`:                         false,
		`{"leaf":{"s":"abc","m":1},"list":[{"s":"a"}]}`:                         false,
		`{"leaf":{"s":"abc","raw":"AAAAAAAAAAAAAAAA"},"list":[{"s":"a"}]}`:      false,
		`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"grid":[[1,2,3,4,5,6,7,8,9]]}`:  true,
		`{"leaf":{"s":"abc"},"list":[{"s":"a"}],"words":["a","b","c","d","e"]}`: false,
	} {
		if agree[fastBody](t, []byte(body), DefaultLimits) != valid {
			t.Errorf("%s: want accepted %v", body, valid)
		}
	}
}

// An array nested past MaxDepth is refused by both paths, the reference
// naming the ceiling.
func TestSinglePassAgreesOnArraysPastTheCeiling(t *testing.T) {
	tight := DefaultLimits
	tight.MaxDepth = 2
	c := mustCodec[fastBody](t)
	body := []byte(`{"grid":[[1]],"leaf":{"s":"a"},"list":[{"s":"a"}]}`)
	if agree[fastBody](t, body, tight) {
		t.Fatal("accepted an array past the ceiling")
	}
	var dst fastBody
	errs := reference(newRegistry(), c, c.use(), body, tight, reflect.ValueOf(&dst).Elem())
	if len(errs) != 1 || !strings.Contains(errs[0].Message, "JSON nesting exceeds the ceiling of 2") {
		t.Fatalf("%v", errs)
	}
}

// fastRec nests itself without bound.
type fastRec struct {
	N *fastRec `json:"n,omitzero"`
}

// Nesting past what encoding/json/jsontext reads at all (10000 levels) is
// refused by both paths even under a MaxDepth above it.
func TestSinglePassAgreesPastJSONTextsOwnNesting(t *testing.T) {
	deep := DefaultLimits
	deep.MaxDepth = 20000
	deep.MaxBodyBytes = 1 << 20
	const n = 10001
	body := []byte(strings.Repeat(`{"n":`, n) + `{}` + strings.Repeat(`}`, n))
	if agree[fastRec](t, body, deep) {
		t.Fatal("accepted nesting jsontext refuses")
	}
	if !agree[fastRec](t, []byte(strings.Repeat(`{"n":`, 100)+`{}`+strings.Repeat(`}`, 100)), deep) {
		t.Fatal("refused nesting within both ceilings")
	}
}

// A struct of more than 64 members tracks which were sent in a slice: a
// required member left out is refused, one sent is taken.
func TestSinglePassAgreesOnStructsOfManyMembers(t *testing.T) {
	var fields []reflect.StructField
	for i := range 65 {
		typ, tag := reflect.TypeFor[*int](), fmt.Sprintf(`json:"f%d,omitzero"`, i)
		if i == 64 {
			typ, tag = reflect.TypeFor[int](), `json:"f64"`
		}
		fields = append(fields, reflect.StructField{Name: fmt.Sprintf("F%d", i), Type: typ, Tag: reflect.StructTag(tag)})
	}
	typ := reflect.StructOf(fields)
	for body, valid := range map[string]bool{
		`{"f64":1}`:                true,
		`{"f0":1,"f63":2,"f64":3}`: true,
		`{"f0":1}`:                 false,
		`{"f64":1,"f65":2}`:        false,
	} {
		if ok, _ := agreeValue(t, newRegistry(), typ, []byte(body), DefaultLimits); ok != valid {
			t.Errorf("%s: want accepted %v", body, valid)
		}
	}
}

// fastFlag reads and writes itself as a JSON boolean, which WithSchema
// declares.
type fastFlag struct{ on bool }

func (f fastFlag) MarshalJSON() ([]byte, error) { return json.Marshal(f.on) }
func (f *fastFlag) UnmarshalJSON(b []byte) error {
	return json.Unmarshal(b, &f.on)
}

type fastFlagged struct {
	F fastFlag `json:"f"`
}

// A type declared a boolean takes true and false and nothing else, on both
// paths.
func TestSinglePassAgreesOnADeclaredBoolean(t *testing.T) {
	r := newRegistry()
	if err := r.declare(declaredSchema{t: reflect.TypeFor[fastFlag](), jsonType: "boolean"}); err != nil {
		t.Fatal(err)
	}
	r.sealDeclarations()
	for body, valid := range map[string]bool{`{"f":true}`: true, `{"f":false}`: true, `{"f":1}`: false, `{"f":"true"}`: false} {
		if ok, _ := agreeValue(t, r, reflect.TypeFor[fastFlagged](), []byte(body), DefaultLimits); ok != valid {
			t.Errorf("%s: want accepted %v", body, valid)
		}
	}
}

// fastStrict refuses every value but "ok" in its own UnmarshalJSON.
type fastStrict struct{}

func (fastStrict) MarshalJSON() ([]byte, error) { return []byte(`"ok"`), nil }
func (*fastStrict) UnmarshalJSON(b []byte) error {
	if string(b) != `"ok"` {
		return errors.New("not ok")
	}
	return nil
}

// A type's own UnmarshalJSON refusing an element of an array is a violation
// at that element's index.
func TestAnOwnJSONRefusalNamesTheArrayIndex(t *testing.T) {
	ok, errs := agreeValue(t, newRegistry(), reflect.TypeFor[[]fastStrict](), []byte(`["ok","no"]`), DefaultLimits)
	if ok || len(errs) != 1 || errs[0].Path != "$[1]" {
		t.Fatalf("%v %v", ok, errs)
	}
}

type fastNullSet struct {
	N []Nullable[int8] `json:"n" schema:"uniqueItems=true"`
}

// Under uniqueItems, two nulls are equal elements and a null and a number
// are not.
func TestUniqueItemsComparesNulls(t *testing.T) {
	for body, valid := range map[string]bool{`{"n":[null,1]}`: true, `{"n":[null,1,null]}`: false} {
		if agree[fastNullSet](t, []byte(body), DefaultLimits) != valid {
			t.Errorf("%s: want accepted %v", body, valid)
		}
	}
}

// A discriminator not followed by a colon, and a tag cut off before its
// closing quote, are no hint: the object is read in full and refused there,
// on both paths.
func TestSinglePassAgreesOnABrokenLeadingTag(t *testing.T) {
	r := sealedRegistry(t)
	for _, s := range []string{
		`{"main":{"kind" "circle","r":1},"others":[]}`,
		`{"main":{"kind" `,
		`{"main":{"kind":"circ`,
	} {
		if agreeIn[fastScene](t, r, []byte(s), DefaultLimits) {
			t.Errorf("%s accepted", s)
		}
	}
}

// A sealed type's JSONOptions, as a client decodes with them, refuse what is
// not one of its variants: no object, a discriminator that is no string, an
// unknown tag, a variant's member of the wrong type, and a value cut off.
func TestSealedOptionsRefuseWhatIsNoVariant(t *testing.T) {
	// encoding/json/v2 words its own errors differently from run to run: its
	// refusals are matched by what they name.
	for body, want := range map[string]string{
		`[]`:                        "JSON array into Go map[string]jsontext.Value",
		`{"kind":1}`:                "kind: discriminator must be a string",
		`{"kind":"hexagon"}`:        `kind: unknown fastShape "hexagon"`,
		`{"kind":"circle","r":"x"}`: `JSON string into Go float64 within "/r"`,
		`{"kind":`:                  "unexpected EOF",
	} {
		var s fastShape
		err := json.Unmarshal([]byte(body), &s, fastShapes.JSONOptions())
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", body, err, want)
		}
	}
	var s fastShape
	if err := json.Unmarshal([]byte(`{"r":2,"kind":"circle"}`), &s, fastShapes.JSONOptions()); err != nil || s.(fastCircle).R != 2 {
		t.Fatalf("%v %#v", err, s)
	}
}

// A pooled buffer that grew past maxPooledBody is dropped, not kept: it is
// left as it was, unreset.
func TestOversizedBuffersAreNotPooled(t *testing.T) {
	b := bytes.NewBuffer(make([]byte, 0, maxPooledBody+1))
	b.WriteString("held")
	releaseBuffer(b)
	if b.String() != "held" {
		t.Fatalf("an oversized buffer was reset for the pool: %q", b)
	}
}
