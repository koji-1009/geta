package geta_test

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/koji-1009/geta"
)

// An App's JSONOptions read a time.Time as geta reads one in a request, by
// RFC 3339's grammar: a lower-case t and z are taken, and a leap second, a
// value that is no date-time, one cut off, and a number are refused.
func TestJSONOptionsReadATimeAsARequestDoes(t *testing.T) {
	opts := accepts(t, one("/x", get(okHandler))).JSONOptions()
	var at time.Time
	if err := json.Unmarshal([]byte(`"2026-07-18t09:30:00z"`), &at, opts); err != nil || !at.Equal(time.Date(2026, 7, 18, 9, 30, 0, 0, time.UTC)) {
		t.Fatalf("%v %v", at, err)
	}
	for _, s := range []string{`"1998-12-31T23:59:60Z"`, `"yesterday"`, `"2026-07-18`, `5`} {
		if err := json.Unmarshal([]byte(s), &at, opts); err == nil {
			t.Errorf("%s accepted", s)
		}
	}
}

// Failure.Status is the row's status.
func TestFailureStatusIsTheRows(t *testing.T) {
	if s := geta.On(errNotFound, http.StatusNotFound, "").Status(); s != http.StatusNotFound {
		t.Fatal(s)
	}
}

// A body's example the body's schema refuses is refused at assembly.
func TestABodysExampleTheSchemaRefusesIsRefused(t *testing.T) {
	type in struct {
		N *int `body:"json" schema:"maximum=3,examples=5"`
	}
	rejects(t, one("/x", geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *in) (*ok, error) { return nil, nil }, geta.Doc{})}),
		`example "5": 5 is greater than maximum 3`)
}

// jeThing is a struct whose schema name a sealed type of the same name, in
// another scope, would take too.
type jeThing struct {
	A int `json:"a"`
}

type jeVariant struct {
	Kind string `json:"kind"`
}

func (jeVariant) isJEThing() {}

// Two types of one name, a struct and a sealed interface, would be one
// component: New refuses them.
func TestASealedTypeAndAStructOfOneNameAreRefused(t *testing.T) {
	type jeThing interface{ isJEThing() }
	type both struct {
		S jeThing `json:"s"`
	}
	// Either met first: the struct, then the sealed type, and the other way.
	type structFirst struct {
		T outerJEThing `json:"t"`
		B both         `json:"b"`
	}
	type sealedFirst struct {
		B both         `json:"b"`
		T outerJEThing `json:"t"`
	}
	sealed := geta.WithUnion(geta.Sealed[jeThing]("kind", geta.Case[jeVariant]("v")))
	for _, tbl := range []geta.Table{
		one("/x", get(func(context.Context, *empty) (*structFirst, error) { return nil, nil })),
		one("/x", get(func(context.Context, *empty) (*sealedFirst, error) { return nil, nil })),
	} {
		_, err := geta.New(tbl, sealed)
		if want := `schema name "jeThing" is taken by both`; err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v, want %q", err, want)
		}
	}
}

// outerJEThing is the package's jeThing, under a name the test function's
// own jeThing does not shadow.
type outerJEThing = jeThing
