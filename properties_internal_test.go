package geta

import (
	"reflect"
	"testing"

	"encoding/json/v2"
)

// fastSized bounds its maps' member counts: its own (minProperties,
// maxProperties), one past the backstop (maxProperties replaces it), and each
// element's of an array of maps (items.).
type fastSized struct {
	M    map[string]int     `json:"m" schema:"minProperties=1,maxProperties=2"`
	Wide *map[string]int    `json:"wide,omitzero" schema:"maxProperties=3"`
	Rows *[]map[string]bool `json:"rows,omitzero" schema:"items.minProperties=1,items.maxProperties=1"`
	Free *map[string]int    `json:"free,omitzero"`
	Keys *map[string]int    `json:"keys,omitzero" schema:"propertyNames.maxLength=5,propertyNames.pattern=^[a-z]+$"`
}

// The single pass holds a map to its minProperties and maxProperties as the
// reference path does, a declared maxProperties replacing the backstop; and
// its keys to the MaxStringLength backstop, or to their propertyNames.
// keywords, a declared maxLength replacing the backstop.
func TestSinglePassAgreesOnMapSizes(t *testing.T) {
	tight := DefaultLimits
	tight.MaxItems = 2
	tight.MaxStringLength = 3
	for body, valid := range map[string]bool{
		`{"m":{"abc":1}}`:                                true,
		`{"m":{"abcd":1}}`:                               false,
		`{"m":{"a":1},"free":{"ééé":1}}`:                 true,
		`{"m":{"a":1},"free":{"abéd":1}}`:                false,
		`{"m":{"a":1},"keys":{"abcde":1}}`:               true,
		`{"m":{"a":1},"keys":{"abcdef":1}}`:              false,
		`{"m":{"a":1},"keys":{"AB":1}}`:                  false,
		`{"m":{"a":1}}`:                                  true,
		`{"m":{"a":1,"b":2}}`:                            true,
		`{"m":{}}`:                                       false,
		`{"m":{"a":1,"b":2,"c":3}}`:                      false,
		`{"m":{"a":1},"wide":{"a":1,"b":2,"c":3}}`:       true,
		`{"m":{"a":1},"wide":{"a":1,"b":2,"c":3,"d":4}}`: false,
		`{"m":{"a":1},"free":{"a":1,"b":2}}`:             true,
		`{"m":{"a":1},"free":{"a":1,"b":2,"c":3}}`:       false,
		`{"m":{"a":1},"rows":[{"x":true},{"y":false}]}`:  true,
		`{"m":{"a":1},"rows":[{}]}`:                      false,
		`{"m":{"a":1},"rows":[{"x":true,"y":true}]}`:     false,
	} {
		if agree[fastSized](t, []byte(body), tight) != valid {
			t.Errorf("%s: want accepted %v", body, valid)
		}
	}
}

// fastKeyed is a map whose key type reads each name by its own methods.
type fastKeyed struct {
	M map[mkVNP]int `json:"m"`
}

// A key the type's methods read is held to the backstop as it is sent, the
// text before the methods read it, on the single pass and the reference path
// alike; a propertyNames.enum member the key type's methods refuse, which no
// request carries, is refused.
func TestKeysTheTypeReadsAreHeldAsSent(t *testing.T) {
	tight := DefaultLimits
	tight.MaxStringLength = 5
	for body, valid := range map[string]bool{
		`{"m":{"k-abc":1}}`:  true,
		`{"m":{"k-abcd":1}}`: false,
		`{"m":{"abc":1}}`:    false, // the methods refuse it
	} {
		if agree[fastKeyed](t, []byte(body), tight) != valid {
			t.Errorf("%s: want accepted %v", body, valid)
		}
	}
	reg := newRegistry()
	c, err := reg.codecFor(reflect.TypeFor[map[mkVNP]int]())
	if err != nil {
		t.Fatal(err)
	}
	s, err := parseConstraints("propertyNames.enum=k-a|b", c.use())
	if err != nil {
		t.Fatal(err)
	}
	const want = `propertyNames: enum member "b" is not a valid mkVNP`
	if err := s.checkValues(c); err == nil || err.Error() != want {
		t.Errorf("enum: %v", err)
	}
	if s, _ = parseConstraints("propertyNames.enum=k-a|k-b", c.use()); s.checkValues(c) != nil {
		t.Error("propertyNames.enum=k-a|k-b refused")
	}
}

// A map's minProperties and maxProperties are written into its schema, a
// declared maxProperties in place of the backstop a request's schema states
// otherwise; a response's schema states what was declared alone.
func TestDocumentStatesMapSizes(t *testing.T) {
	lim := DefaultLimits
	lim.MaxItems = 9
	reg := newRegistry()
	c, err := reg.codecFor(reflect.TypeFor[map[string]bool]())
	if err != nil {
		t.Fatal(err)
	}
	for tag, want := range map[string][2]string{
		"": {`{"additionalProperties":{"type":"boolean"},"maxProperties":9,"propertyNames":{"maxLength":4096},"type":"object"}`,
			`{"additionalProperties":{"type":"boolean"},"type":"object"}`},
		"minProperties=1": {`{"additionalProperties":{"type":"boolean"},"maxProperties":9,"minProperties":1,"propertyNames":{"maxLength":4096},"type":"object"}`,
			`{"additionalProperties":{"type":"boolean"},"minProperties":1,"type":"object"}`},
		"maxProperties=20": {`{"additionalProperties":{"type":"boolean"},"maxProperties":20,"propertyNames":{"maxLength":4096},"type":"object"}`,
			`{"additionalProperties":{"type":"boolean"},"maxProperties":20,"type":"object"}`},
		"minProperties=2,maxProperties=3": {`{"additionalProperties":{"type":"boolean"},"maxProperties":3,"minProperties":2,"propertyNames":{"maxLength":4096},"type":"object"}`,
			`{"additionalProperties":{"type":"boolean"},"maxProperties":3,"minProperties":2,"type":"object"}`},
		// The keys: a declared maxLength in place of the backstop, a pattern's
		// ceiling, an enum that fits.
		"propertyNames.maxLength=8": {`{"additionalProperties":{"type":"boolean"},"maxProperties":9,"propertyNames":{"maxLength":8},"type":"object"}`,
			`{"additionalProperties":{"type":"boolean"},"propertyNames":{"maxLength":8},"type":"object"}`},
		"propertyNames.pattern=^[a-z]+$": {`{"additionalProperties":{"type":"boolean"},"maxProperties":9,"propertyNames":{"maxLength":4096,"pattern":"^[a-z]+$"},"type":"object"}`,
			`{"additionalProperties":{"type":"boolean"},"propertyNames":{"pattern":"^[a-z]+$"},"type":"object"}`},
		"propertyNames.enum=a|b": {`{"additionalProperties":{"type":"boolean"},"maxProperties":9,"propertyNames":{"enum":["a","b"]},"type":"object"}`,
			`{"additionalProperties":{"type":"boolean"},"propertyNames":{"enum":["a","b"]},"type":"object"}`},
	} {
		s, err := parseConstraints(tag, c.use())
		if err != nil {
			t.Fatal(err)
		}
		if err := s.withinLimits(&lim); err != nil {
			t.Fatalf("%q: %v", tag, err)
		}
		for i, l := range []*Limits{&lim, nil} {
			b, _ := json.Marshal(s.document(l), json.Deterministic(true))
			if string(b) != want[i] {
				t.Errorf("%q (request %v): %s, want %s", tag, i == 0, b, want[i])
			}
		}
	}
	// A lower bound past the backstop no map meets, unless a maxProperties
	// replaces the backstop.
	s, err := parseConstraints("minProperties=10", c.use())
	if err != nil {
		t.Fatal(err)
	}
	const want = "minProperties 10 exceeds Limits.MaxItems 9; declare a maxProperties"
	if err := s.withinLimits(&lim); err == nil || err.Error() != want {
		t.Errorf("minProperties=10: %v", err)
	}
	if s, _ = parseConstraints("minProperties=10,maxProperties=10", c.use()); s.withinLimits(&lim) != nil {
		t.Error("minProperties=10,maxProperties=10 refused")
	}
	// An element's, by items.
	arr, err := reg.codecFor(reflect.TypeFor[[]map[string]bool]())
	if err != nil {
		t.Fatal(err)
	}
	if s, _ = parseConstraints("items.minProperties=10", arr.use()); s.withinLimits(&lim) == nil {
		t.Error("items.minProperties=10 accepted")
	}
}
