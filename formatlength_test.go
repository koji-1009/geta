package geta

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
	"uuid"
)

// formatExtremes are, for each format geta checks, a shortest string it takes
// and, where its strings have a longest, a longest; formatLengths states their
// lengths.
var formatExtremes = map[string][2]string{
	"date-time":             {"0000-01-01T00:00:00Z", ""},
	"date":                  {"2026-10-04", "2026-10-04"},
	"time":                  {"00:00:00Z", ""},
	"duration":              {"P1D", ""},
	"ipv4":                  {"0.0.0.0", "255.255.255.255"},
	"ipv6":                  {"::", "ffff:ffff:ffff:ffff:ffff:ffff:255.255.255.255"},
	"json-pointer":          {"", ""},
	"relative-json-pointer": {"0", ""},
	"uuid":                  {"6ba7b810-9dad-11d1-80b4-00c04fd430c8", "6ba7b810-9dad-11d1-80b4-00c04fd430c8"},
	"password":              {"", ""},
}

// formatLengths covers every format geta checks, and its lengths are those
// of strings the format takes: a shortest, and a longest where it has one,
// past which (one more character in each of a few ways) it takes none.
func TestFormatLengthsAreTheFormats(t *testing.T) {
	if got, want := slices.Sorted(maps.Keys(formatLengths)), slices.Sorted(maps.Keys(formatChecks)); !slices.Equal(got, want) {
		t.Fatalf("formatLengths has %v, formatChecks %v", got, want)
	}
	for f, l := range formatLengths {
		ex := formatExtremes[f]
		if !formatChecks[f](ex[0]) || utf8.RuneCountInString(ex[0]) != l.shortest {
			t.Errorf("%s: shortest %q (%d), stated %d", f, ex[0], utf8.RuneCountInString(ex[0]), l.shortest)
		}
		if l.longest == 0 {
			continue
		}
		if !formatChecks[f](ex[1]) || utf8.RuneCountInString(ex[1]) != l.longest {
			t.Errorf("%s: longest %q (%d), stated %d", f, ex[1], utf8.RuneCountInString(ex[1]), l.longest)
		}
		for _, longer := range []string{ex[1] + "0", "0" + ex[1], ex[1] + "f", "f" + ex[1], ex[1] + ":", ":" + ex[1], ex[1] + ".0"} {
			if formatChecks[f](longer) {
				t.Errorf("%s takes %q, past its longest %d", f, longer, l.longest)
			}
		}
	}
	// No ipv6 text, of the forms RFC 4291 writes, is longer than six full
	// groups before a dotted quad.
	for _, s := range []string{
		"ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
		"ffff:ffff:ffff:ffff:ffff::255.255.255.255",
		"ffff:ffff:ffff:ffff:ffff:ffff:ffff::",
		"::ffff:ffff:ffff:ffff:ffff:ffff:ffff",
	} {
		if !formatChecks["ipv6"](s) || len(s) > formatLengths["ipv6"].longest {
			t.Errorf("ipv6 %q", s)
		}
	}
	if _, err := uuid.Parse(formatExtremes["uuid"][0]); err != nil {
		t.Fatal(err)
	}
}

// A format whose strings have a longest bounds a string itself, as an enum
// does: a request's schema states no backstop beside it while the backstop
// is no shorter, and a backstop shorter than its longest string, or than the
// shortest of any format, is refused, as an enum member or a minLength past
// it is. A declared maxLength replaces the backstop; a length keyword no
// string of the format meets is refused whatever the Limits.
func TestFormatsBoundTheirOwnLength(t *testing.T) {
	lim := func(n int) *Limits {
		l := DefaultLimits
		l.MaxStringLength = n
		return &l
	}
	for _, c := range []struct {
		tag     string
		n       int
		stated  any    // the request's schema's maxLength
		refused string // what withinLimits says
	}{
		{"format=uuid", 36, nil, ""},
		{"format=uuid", 4096, nil, ""},
		{"format=uuid", 35, nil, "format uuid strings exceed Limits.MaxStringLength 35"},
		{"format=uuid,maxLength=36", 10, 36, ""},
		{"format=date", 10, nil, ""},
		{"format=ipv4", 15, nil, ""},
		{"format=ipv4", 14, nil, "format ipv4 takes up to 15 code points, past Limits.MaxStringLength 14"},
		{"format=ipv6", 45, nil, ""},
		{"format=ipv6", 40, nil, "format ipv6 takes up to 45 code points, past Limits.MaxStringLength 40"},
		{"format=ipv6,maxLength=39", 20, 39, ""},
		{"format=ipv6,pattern=^[0-9a-f:]+$", 4096, nil, ""},
		{"format=date-time", 20, 20, ""},
		{"format=date-time", 19, nil, "format date-time strings exceed Limits.MaxStringLength 19"},
		{"format=time", 9, 9, ""},
		{"format=duration", 2, nil, "format duration strings exceed Limits.MaxStringLength 2"},
		{"format=json-pointer", 1, 1, ""},
		{"format=relative-json-pointer", 1, 1, ""},
		{"format=password", 1, 1, ""},
	} {
		s, err := parseConstraints(c.tag, &schema{Type: "string"})
		if err != nil {
			t.Fatal(c.tag, err)
		}
		err = s.withinLimits(lim(c.n))
		if c.refused != "" {
			if err == nil || !strings.Contains(err.Error(), c.refused) {
				t.Errorf("%s under %d: %v, want %q", c.tag, c.n, err, c.refused)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s under %d: %v", c.tag, c.n, err)
		}
		if got := s.document(lim(c.n))["maxLength"]; got != c.stated {
			t.Errorf("%s under %d: maxLength %v, want %v", c.tag, c.n, got, c.stated)
		}
		if got := s.document(nil)["maxLength"]; c.stated == nil && got != nil {
			t.Errorf("%s: a response's maxLength %v", c.tag, got)
		}
	}
	// Of a map's keys, as of any string.
	k, err := parseConstraints("propertyNames.format=uuid", &schema{Type: "object", Additional: &schema{Type: "integer"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := k.document(lim(36))["propertyNames"]; !maps.Equal(got.(map[string]any), map[string]any{"format": "uuid"}) {
		t.Errorf("keys: %v", got)
	}
	if err := k.withinLimits(lim(35)); err == nil || !strings.Contains(err.Error(), "propertyNames: format uuid strings exceed Limits.MaxStringLength 35") {
		t.Errorf("keys: %v", err)
	}
	for tag, want := range map[string]string{
		"format=uuid,maxLength=35": "maxLength 35 is below format uuid's minimum length 36",
		"format=date,minLength=11": "minLength 11 exceeds format date's maximum length 10",
		"format=time,maxLength=8":  "maxLength 8 is below format time's minimum length 9",
	} {
		if _, err := parseConstraints(tag, &schema{Type: "string"}); err == nil || err.Error() != want && !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: %v, want %q", tag, err, want)
		}
	}
	for _, tag := range []string{"format=ipv6,maxLength=2", "format=time,minLength=4000", "format=uuid,minLength=36,maxLength=36"} {
		if _, err := parseConstraints(tag, &schema{Type: "string"}); err != nil {
			t.Errorf("%s: %v", tag, err)
		}
	}
	// A format type's schema is held alike, and getavet's kinds too.
	if err := checkTag("maxLength=9", "geta.Date"); err == nil || !strings.Contains(err.Error(), "maxLength 9 is below format date's minimum length 10") {
		t.Errorf("geta.Date: %v", err)
	}
	if err := checkTag("maxLength=9", "format"); err != nil {
		t.Errorf("a FormatType's grammar is its own: %v", err)
	}
}

type uuidMember struct {
	ID   uuid.UUID      `json:"id"`
	Keys map[string]int `json:"keys" schema:"propertyNames.format=uuid"`
}

type uuidParam struct {
	ID uuid.UUID `query:"id"`
}

// geta.New refuses Limits under which no uuid is read, in a body, of a map's
// keys, and as a parameter, and not for a type only a response writes; under
// Limits that hold one, a uuid is read and its schema states no backstop.
func TestABackstopNoUUIDFitsIsRefused(t *testing.T) {
	short := DefaultLimits
	short.MaxStringLength = 35
	const fits = "format uuid strings exceed Limits.MaxStringLength 35"
	if _, err := readOnly[uuidMember](WithLimits(short)); err == nil || !strings.Contains(err.Error(), fits) {
		t.Errorf("member: %v", err)
	}
	if _, err := newWith[uuidParam, struct{}](WithLimits(short)); err == nil || !strings.Contains(err.Error(), "uuidParam.ID: "+fits) {
		t.Errorf("parameter: %v", err)
	}
	if _, err := writtenOnly[uuidMember](WithLimits(short)); err != nil {
		t.Errorf("written only: %v", err)
	}
	short.MaxStringLength = 36
	h := func(context.Context, *bodyOf[uuidMember]) (*struct{}, error) { return &struct{}{}, nil }
	app, err := New(Table{Routes: []Entry{{Path: "/x", Route: Route{Post: Op(http.StatusOK, h, Doc{})}}}}, WithLimits(short))
	if err != nil {
		t.Fatal(err)
	}
	id := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"id":"`+id+`","keys":{"`+id+`":1}}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body)
	}
	if d := string(app.OpenAPI()); strings.Contains(d, `"maxLength"`) {
		t.Errorf("document states a maxLength: %s", d)
	}
}
