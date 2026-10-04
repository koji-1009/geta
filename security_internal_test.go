package geta

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

// The requirement objects state what the gates of a chain enforce together:
// each gate must admit, so with no Doc.Security the request needs one scheme
// of every gate's default.
func TestSecurityRequirementsMatchTheGates(t *testing.T) {
	scheme := func(name string) Scheme { return APIKeyHeader(name, "X-"+name) }
	a, b, c := scheme("a"), scheme("b"), scheme("c")
	pass := func(*http.Request) (context.Context, error) { return nil, nil }
	gate := func(def []Scheme, names ...string) Middleware {
		v := map[string]Verifier{}
		for _, n := range names {
			v[n] = pass
		}
		return Secure(Policy{Default: def, Verifiers: v})
	}
	for _, tc := range []struct {
		name     string
		declared []Scheme
		chain    []Middleware
		want     [][]Scheme
	}{
		{"one gate", nil, []Middleware{gate([]Scheme{a, b}, "a", "b")}, [][]Scheme{{a}, {b}}},
		{"two gates", nil, []Middleware{gate([]Scheme{a, b}, "a", "b"), gate([]Scheme{c}, "c")}, [][]Scheme{{a, c}, {b, c}}},
		{"same defaults", nil, []Middleware{gate([]Scheme{a, b}, "a", "b"), gate([]Scheme{b, a}, "a", "b")}, [][]Scheme{{a}, {b}}},
		{"overlapping defaults", nil, []Middleware{gate([]Scheme{a, b}, "a", "b"), gate([]Scheme{a}, "a")}, [][]Scheme{{a}}},
		{"an open gate", nil, []Middleware{gate(nil), gate([]Scheme{c}, "c")}, [][]Scheme{{c}}},
		{"all open", nil, []Middleware{gate(nil), gate([]Scheme{})}, nil},
		{"declared", []Scheme{a, c}, []Middleware{gate([]Scheme{b}, "a", "b", "c"), gate(nil, "a", "c")}, [][]Scheme{{a}, {c}}},
		{"declared public", []Scheme{}, []Middleware{gate([]Scheme{b}, "b")}, nil},
	} {
		got, err := checkSecurity(tc.declared, tc.chain)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	// A gate checks its own default only, so it needs no verifier for
	// another gate's; a declared scheme is checked by every gate.
	if _, err := checkSecurity(nil, []Middleware{gate([]Scheme{a}, "a"), gate([]Scheme{c}, "c")}); err != nil {
		t.Fatalf("separate defaults: %v", err)
	}
	if _, err := checkSecurity([]Scheme{a}, []Middleware{gate(nil, "a"), gate(nil, "c")}); err == nil {
		t.Fatal("a declared scheme one gate cannot verify was accepted")
	}
}
