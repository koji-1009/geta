package geta_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// someValue is any JSON value but null, read by its own UnmarshalJSON.
type someValue struct{ raw string }

func (v someValue) MarshalJSON() ([]byte, error) { return []byte(v.raw), nil }

func (v *someValue) UnmarshalJSON(b []byte) error {
	if string(bytes.TrimSpace(b)) == "null" {
		return errors.New("must not be null")
	}
	v.raw = string(b)
	return nil
}

// someKey is a map key read by its own UnmarshalJSON, which refuses "bad".
type someKey string

func (k someKey) MarshalJSON() ([]byte, error) { return []byte(`"` + string(k) + `"`), nil }

func (k *someKey) UnmarshalJSON(b []byte) error {
	if string(b) == `"bad"` {
		return errors.New("bad key")
	}
	*k = someKey(strings.Trim(string(b), `"`))
	return nil
}

type ownEvent struct {
	Type  string                   `json:"type" schema:"minLength=1,maxLength=50"`
	Data  someValue                `json:"data"`
	More  *someValue               `json:"more,omitzero"`
	List  *[]someValue             `json:"list,omitzero" schema:"maxItems=3"`
	Map   *map[string]someValue    `json:"map,omitzero"`
	Keys  *map[someKey]int         `json:"keys,omitzero"`
	Maybe geta.Nullable[someValue] `json:"maybe,omitzero"`
	Money *cents                   `json:"money,omitzero"`
}

type ownEventIn struct {
	Body ownEvent `body:"json"`
}

func ownEventClient(t *testing.T) *getatest.Client {
	t.Helper()
	h := func(context.Context, *ownEventIn) (*empty, error) { return &empty{}, nil }
	return getatest.New(t, one("/x", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}),
		geta.WithSchema[cents]("string", `pattern=^-?[0-9]+\.[0-9]+$`))
}

// paths returns the paths a 400 lists, in order.
func paths(t *testing.T, res *getatest.Response) []string {
	t.Helper()
	if res.Status != http.StatusBadRequest {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
	var ps []string
	for _, v := range res.Problem().Errors {
		ps = append(ps, v.Path)
	}
	return ps
}

// A member's own JSON method refusal is listed with every other violation of
// the body, not only when the schema checks pass (exp13 sse).
func TestOwnJSONRefusalIsListedWithTheOtherViolations(t *testing.T) {
	c := ownEventClient(t)
	for body, want := range map[string]string{
		// The reported case: schema violations beside a method's refusal.
		`{"type":"","data":null,"colour":"red"}`: "$.type $.colour $.data",
		// Alone, as before.
		`{"type":"x","data":null}`: "$.data",
		// Every refusal, not only the first v2 meets.
		`{"type":"x","data":null,"list":[1,null,null],"map":{"a":null,"b":2},"maybe":null}`: "$.data $.list[1] $.list[2] $.map.a",
		// A map key's own method, beside another violation.
		`{"type":"","data":1,"keys":{"bad":1,"ok":2}}`: "$.type $.keys.bad",
		// A pointer member sent null stays nil, and its method does not run.
		`{"type":"","data":1,"more":null}`: "$.type",
		// A value the schema refuses is not read again by its method: an
		// array past its bound, a declared type's keyword.
		`{"type":"","data":1,"list":[null,null,null,null],"money":"abc"}`: "$.type $.list $.money",
		// A declared type's method refusal is listed beside the rest.
		`{"type":"","data":null,"money":"1.234"}`: "$.type $.data $.money",
	} {
		if got := strings.Join(paths(t, c.Post("/x", body)), " "); got != want {
			t.Errorf("%s: %s, want %s", body, got, want)
		}
	}
	// The message is the one v2 gives reading the whole body.
	res := c.Post("/x", `{"type":"","data":null}`)
	if errs := res.Problem().Errors; len(errs) != 2 || !strings.Contains(errs[1].Message, "must not be null") || !strings.Contains(errs[1].Message, `within "/data"`) {
		t.Fatalf("%s", res.Body)
	}
}

// A method's refusals count toward maxViolations like any other: past 50,
// they are counted in omitted.
func TestOwnJSONRefusalsAreBoundedLikeTheRest(t *testing.T) {
	c := ownEventClient(t)
	var b strings.Builder
	b.WriteString(`{"type":"","data":1,"map":{`)
	for i := range 60 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"k`)
		b.WriteString(strings.Repeat("a", i+1))
		b.WriteString(`":null`)
	}
	b.WriteString(`}}`)
	res := c.Post("/x", b.String())
	p := res.Problem()
	if res.Status != 400 || len(p.Errors) != 50 || p.Omitted != 11 || p.Errors[0].Path != "$.type" {
		t.Fatalf("%d %d %d %s", res.Status, len(p.Errors), p.Omitted, res.Body)
	}
}
