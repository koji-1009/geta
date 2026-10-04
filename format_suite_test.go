package geta

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// suiteDivergences are the cases of the JSON Schema Test Suite
// (testdata/jsonschema-suite, from json-schema-org/JSON-Schema-Test-Suite at
// 5b0ee16, its optional format tests for draft 2020-12) where geta follows
// the text of the specification a format names rather than the suite. Each
// is keyed by format and data, with geta's reason; the formats geta has a
// type for follow the suite throughout.
var suiteDivergences = map[string]string{}

// suiteCases reads the string cases of the suite's file for format: the
// data and whether the suite holds it valid.
func suiteCases(t *testing.T, file string) []suiteCase {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var groups []struct {
		Tests []struct {
			Description string `json:"description"`
			Data        any    `json:"data"`
			Valid       bool   `json:"valid"`
		} `json:"tests"`
	}
	if err := json.Unmarshal(data, &groups); err != nil {
		t.Fatal(err)
	}
	var cases []suiteCase
	for _, g := range groups {
		for _, c := range g.Tests {
			if s, ok := c.Data.(string); ok { // a format asserts nothing of another JSON type
				cases = append(cases, suiteCase{c.Description, s, c.Valid})
			}
		}
	}
	return cases
}

type suiteCase struct {
	description, data string
	valid             bool
}

// formatTypeOf is the format type of each format; time.Time, which shares
// date-time with DateTime but holds no leap second, is judged by its own
// schema (TestTimeSchemaAdmitsWhatTimeHolds).
func formatTypeOf() map[string]reflect.Type {
	byFormat := map[string]reflect.Type{}
	for typ, f := range formatTypes {
		if typ != timeType {
			byFormat[f] = typ
		}
	}
	return byFormat
}

// The format types accept exactly what the suite's format tests accept, but
// for suiteDivergences.
func TestFormatTypesAgreeWithTheJSONSchemaTestSuite(t *testing.T) {
	files, err := filepath.Glob("testdata/jsonschema-suite/*.json")
	if err != nil || len(files) == 0 {
		t.Fatal("no suite files", err)
	}
	byFormat := formatTypeOf()
	seen := map[string]bool{}
	for _, file := range files {
		format := strings.TrimSuffix(filepath.Base(file), ".json")
		typ, ok := byFormat[format]
		if !ok {
			t.Fatalf("%s: no format type", format)
		}
		cases := suiteCases(t, file)
		for _, c := range cases {
			got := accepts(typ, format, c.data)
			key := format + " " + c.data
			if why, diverges := suiteDivergences[key]; diverges {
				seen[key] = true
				if got == c.valid {
					t.Errorf("%s %q: listed as a divergence (%s), but agrees with the suite", format, c.data, why)
				}
				continue
			}
			if got != c.valid {
				t.Errorf("%s %q (%s): accepted %v, suite says %v", format, c.data, c.description, got, c.valid)
			}
		}
		if len(cases) == 0 {
			t.Errorf("%s: no string cases read", format)
		}
	}
	for key := range suiteDivergences {
		if !seen[key] {
			t.Errorf("divergence %q is no case of the suite", key)
		}
	}
}

// accepts reports whether a request's s passes as the format type typ: the
// format's check, as the decoder makes it (schema.validFormat), and the
// type's reading, as geta reads it (unmarshalText).
func accepts(typ reflect.Type, format, s string) bool {
	if check := formatChecks[format]; check != nil && !check(s) {
		return false
	}
	if typ.Kind() == reflect.String {
		return true // password: any string
	}
	return unmarshalText(reflect.New(typ).Interface(), []byte(s)) == nil
}

// A string tagged with a format is held to it as the format's type holds its
// text, on every case of the suite.
func TestFormatChecksAgreeWithTheFormatTypes(t *testing.T) {
	byFormat := formatTypeOf()
	for f := range formatChecks {
		if byFormat[f] == nil {
			t.Errorf("format %s is checked but has no type", f)
		}
	}
	files, err := filepath.Glob("testdata/jsonschema-suite/*.json")
	if err != nil || len(files) == 0 {
		t.Fatal("no suite files", err)
	}
	var cases []string
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var groups []struct {
			Tests []struct {
				Data any `json:"data"`
			} `json:"tests"`
		}
		if err := json.Unmarshal(data, &groups); err != nil {
			t.Fatal(err)
		}
		for _, g := range groups {
			for _, c := range g.Tests {
				if s, ok := c.Data.(string); ok {
					cases = append(cases, s)
				}
			}
		}
	}
	for f, typ := range byFormat {
		check, ok := formatChecks[f]
		if !ok {
			t.Errorf("format %s of %s has no check", f, typ)
			continue
		}
		for _, s := range cases {
			got, want := check(s), accepts(typ, f, s)
			if got != want {
				t.Errorf("format %s on %q: the string check says %v, %s says %v", f, s, got, typ, want)
			}
		}
	}
}
