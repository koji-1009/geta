package geta_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/koji-1009/geta"
)

// The fuzz targets of this file hold what geta reads of a request (header
// parameters, preconditions, the content's headers, path values) to small
// reference statements of the rules, written apart from geta's own code
// from the RFCs and the spec, so a fuzzed input that the two read
// differently fails.

// fuzzProblem fails unless body, the response to a request answered with
// status, is an application/problem+json problem whose status and title are
// the response's, and returns it.
func fuzzProblem(t *testing.T, where string, status int, h http.Header, body []byte) geta.Problem {
	t.Helper()
	if ct := h.Get("Content-Type"); ct != geta.ProblemContentType {
		t.Fatalf("%s: a %d with Content-Type %q: %q", where, status, ct, body)
	}
	var p geta.Problem
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("%s: a %d whose problem does not decode (%v): %q", where, status, err, body)
	}
	if p.Status != status || p.Title != http.StatusText(status) {
		t.Fatalf("%s: a %d whose problem says %d %q", where, status, p.Status, p.Title)
	}
	return p
}

// refFieldValue reports whether s is a header field value (RFC 9110 §5.5):
// no whitespace at either end, no control character but a tab.
func refFieldValue(s string) bool {
	if s != "" && (s[0] == ' ' || s[0] == '\t' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		return false
	}
	for _, b := range []byte(s) {
		if b == 0x7F || b < 0x20 && b != '\t' {
			return false
		}
	}
	return true
}

// refListElements reads the lines of a header list (RFC 9110 §5.6.1): each
// line split at its commas, each element trimmed of spaces and tabs, empty
// elements dropped.
func refListElements(lines []string) []string {
	var out []string
	for _, l := range lines {
		for _, e := range strings.Split(l, ",") {
			if e = strings.Trim(e, " \t"); e != "" {
				out = append(out, e)
			}
		}
	}
	return out
}

var refJSONInteger = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

// refInt reads s as a parameter's integer of bits bits: an integer as JSON
// writes one, in the type's range. -0 is 0 to a signed type, and refused by
// an unsigned one, as encoding/json/v2 refuses it there in a body.
func refInt(s string, bits int, unsigned bool) (int64, uint64, bool) {
	if !refJSONInteger.MatchString(s) {
		return 0, 0, false
	}
	if unsigned {
		u, err := strconv.ParseUint(s, 10, bits)
		return 0, u, err == nil
	}
	n, err := strconv.ParseInt(s, 10, bits)
	return n, 0, err == nil
}

var refJSONNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

var refUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var refDateTimeRe = regexp.MustCompile(`^([0-9]{4})-([0-9]{2})-([0-9]{2})[Tt]([0-9]{2}):([0-9]{2}):([0-9]{2})(\.[0-9]+)?([Zz]|([+-])([0-9]{2}):([0-9]{2}))$`)

// daysIn is the number of days of month m in year y.
func daysIn(y, m int) int {
	return time.Date(y, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// refDateTime reads an RFC 3339 date-time (§5.6) that a time.Time holds: no
// leap second.
func refDateTime(s string) (time.Time, bool) {
	m := refDateTimeRe.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	n := func(i int) int { v, _ := strconv.Atoi(m[i]); return v }
	y, mo, d, h, mi, sec := n(1), n(2), n(3), n(4), n(5), n(6)
	if mo < 1 || mo > 12 || d < 1 || d > daysIn(y, mo) || h > 23 || mi > 59 || sec > 59 {
		return time.Time{}, false
	}
	ns := 0
	if f := m[7]; f != "" {
		digits := (f[1:] + "000000000")[:9]
		ns, _ = strconv.Atoi(digits)
	}
	off := 0
	if m[9] != "" {
		oh, om := n(10), n(11)
		if oh > 23 || om > 59 {
			return time.Time{}, false
		}
		off = (oh*60 + om) * 60
		if m[9] == "-" {
			off = -off
		}
	}
	return time.Date(y, time.Month(mo), d, h, mi, sec, ns, time.UTC).Add(-time.Duration(off) * time.Second), true
}

type fzHeaderIn struct {
	S   *string      `header:"X-S"`
	E   *string      `header:"X-E" schema:"enum=a|b c"`
	I   *int64       `header:"X-I"`
	U8  *uint8       `header:"X-U8"`
	F   *float64     `header:"X-F"`
	B   *bool        `header:"X-B"`
	T   *time.Time   `header:"X-T"`
	ID  *uuid.UUID   `header:"X-Id"`
	Ss  *[]string    `header:"X-Ss"`
	Is  *[]int16     `header:"X-Is"`
	Ids *[]uuid.UUID `header:"X-Ids"`
	Es  *[]string    `header:"X-Es" schema:"items.enum=x|y z"`
	Ts  *[]time.Time `header:"X-Ts"`
}

// fzHeaderLimits are the limits FuzzHeaderParameters binds under: a string
// or a date-time, which state no length, past MaxStringLength within the
// fuzzHeaderTextMax bytes a value is cut to.
var fzHeaderLimits = func() geta.Limits {
	l := geta.DefaultLimits
	l.MaxStringLength = 64
	return l
}()

// fuzzHeaderTextMax is the bytes FuzzHeaderParameters cuts each value to.
const fuzzHeaderTextMax = 128

// fzRefValue reads s as a value of the kind a header of fzHeaderIn holds,
// by the reference rules: the canonical text of what it binds, or false
// when it is refused.
func fzRefValue(kind, s string) (string, bool) {
	within := utf8.RuneCountInString(s) <= fzHeaderLimits.MaxStringLength
	switch kind {
	case "string":
		return s, utf8.ValidString(s) && refFieldValue(s) && within
	case "enum":
		return s, s == "a" || s == "b c"
	case "listenum":
		return s, s == "x" || s == "y z"
	case "int64":
		n, _, ok := refInt(s, 64, false)
		return strconv.FormatInt(n, 10), ok
	case "int16":
		n, _, ok := refInt(s, 16, false)
		return strconv.FormatInt(n, 10), ok
	case "uint8":
		_, u, ok := refInt(s, 8, true)
		return strconv.FormatUint(u, 10), ok
	case "float64":
		if !refJSONNumber.MatchString(s) {
			return "", false
		}
		f, err := strconv.ParseFloat(s, 64)
		return strconv.FormatFloat(f, 'g', -1, 64), err == nil
	case "bool":
		return s, s == "true" || s == "false"
	case "time":
		tm, ok := refDateTime(s)
		return tm.UTC().Format(time.RFC3339Nano), ok && within
	case "uuid":
		return strings.ToLower(s), refUUID.MatchString(s)
	}
	panic(kind)
}

// fzCanon renders a bound value as fzRefValue's canonical text.
func fzCanon(v reflect.Value) string {
	switch x := v.Interface().(type) {
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	case uuid.UUID:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	return fmt.Sprint(v.Interface())
}

// fzHeaderKinds are the kinds of fzHeaderIn's headers, in its field order; a
// list's is its element's, after "[]".
var fzHeaderKinds = []string{"string", "enum", "int64", "uint8", "float64", "bool", "time", "uuid",
	"[]string", "[]int16", "[]uuid", "[]listenum", "[]time"}

// fzMaxViolations is the most violations geta reports in one response.
const fzMaxViolations = 50

// FuzzHeaderParameters sends each header parameter of fzHeaderIn as no line,
// one line, or two, of two fuzzed values, and holds the answer to the
// reference reading of every header at once: a header that is not a list
// binds one line held to its type (an integer as JSON writes one in range, a
// number as JSON writes one, a date-time of RFC 3339, a 36-character uuid,
// an enum member, a string that is a UTF-8 header field value; a string and
// a date-time within MaxStringLength, fzHeaderLimits'), and two
// lines are "given 2 times"; a list is its lines split at commas, trimmed,
// empty elements dropped, each element held to the element's type at
// name[i], and a list of no element is absent. Every header the reference
// takes binds the reference's value; a request with any it refuses is a 400
// problem whose violations name exactly the first of the refused, in order
// (50 at most, fewer past 16 KiB of violations), omitted counting the rest,
// and the handler does not run. A bound list, joined again with ", ", reads
// back as itself.
func FuzzHeaderParameters(f *testing.F) {
	var got *fzHeaderIn
	app, err := geta.New(one("/h", get(func(_ context.Context, in *fzHeaderIn) (*ok, error) {
		got = in
		return &ok{true}, nil
	})), geta.WithLimits(fzHeaderLimits))
	if err != nil {
		f.Fatal(err)
	}
	// Masks: every header two lines; every header the first value alone;
	// every header the second alone.
	all := uint32(1<<(2*len(fzHeaderNames))) - 1
	first, second := uint32(0x55555555)&all, uint32(0xAAAAAAAA)&all
	f.Add("a,b", "c", all)
	f.Add("a, b ,\tc", "a,,b", first)
	f.Add(" , c", ",", second)
	f.Add("é", "", all)
	f.Add(strings.Repeat("x,", 60), "", first) // past the 50 violations reported
	for _, pair := range [][2]string{
		{"2026-01-01T00:00:00Z", "2026-02-30T00:00:00Z"},
		{"123e4567-e89b-12d3-a456-426614174000", "123E4567-E89B-12D3-A456-426614174000"},
		{"-0", "007"}, {"1.5e3", "1e400"}, {"true", "TRUE"}, {"b c", "y z"},
		{" a b ", "b\x01"}, {"a \t b", "\tx"},
		{"1998-12-31T23:59:60Z", "1990-12-31T15:59:59-24:00"},
		{"2026-07-18t09:30:00.123456789123z", "2026-07-18T09:30:00,5Z"},
		{"\xff", "a\xc3"}, {"-1, 2", "300"}, {"1e-400", "-0.0"},
		// At MaxStringLength and past it.
		{strings.Repeat("a", fzHeaderLimits.MaxStringLength), strings.Repeat("a", fzHeaderLimits.MaxStringLength+1)},
		{"2026-07-18T09:30:00." + strings.Repeat("1", fzHeaderLimits.MaxStringLength-21) + "Z",
			"2026-07-18T09:30:00." + strings.Repeat("1", fzHeaderLimits.MaxStringLength-20) + "Z"},
	} {
		f.Add(pair[0], pair[1], first)
		f.Add(pair[0], pair[1], second)
	}
	f.Fuzz(func(t *testing.T, a, b string, mask uint32) {
		a, b = fzCut(a, fuzzHeaderTextMax), fzCut(b, fuzzHeaderTextMax)
		req := httptest.NewRequest(http.MethodGet, "/h", nil)
		lines := make([][]string, len(fzHeaderNames))
		for i, name := range fzHeaderNames {
			switch mask >> (2 * i) & 3 {
			case 1:
				lines[i] = []string{a}
			case 2:
				lines[i] = []string{b}
			case 3:
				lines[i] = []string{a, b}
			}
			if lines[i] != nil {
				req.Header[name] = lines[i]
			}
		}
		// The reference: what each header binds, and the violations.
		want := make([]*string, len(fzHeaderNames))
		var refused []string
		for i, name := range fzHeaderNames {
			kind, list := strings.CutPrefix(fzHeaderKinds[i], "[]")
			switch {
			case list:
				elems := refListElements(lines[i])
				if len(elems) == 0 {
					continue
				}
				canon := make([]string, len(elems))
				for j, e := range elems {
					c, ok := fzRefValue(kind, e)
					if !ok {
						refused = append(refused, fmt.Sprintf("%s[%d]", name, j))
					}
					canon[j] = c
				}
				w := strings.Join(canon, "\x00")
				want[i] = &w
			case len(lines[i]) > 1:
				refused = append(refused, name)
			case len(lines[i]) == 1:
				c, ok := fzRefValue(kind, lines[i][0])
				if !ok {
					refused = append(refused, name)
				}
				want[i] = &c
			}
		}
		got = nil
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		where := fmt.Sprintf("%q", req.Header)
		if len(refused) > 0 {
			if rec.Code != http.StatusBadRequest || got != nil {
				t.Fatalf("%s: %d (handler ran: %v), want 400 naming %q: %s", where, rec.Code, got != nil, refused, rec.Body)
			}
			p := fuzzProblem(t, where, rec.Code, rec.Header(), rec.Body.Bytes())
			var named []string
			for _, v := range p.Errors {
				if v.In != "header" {
					t.Fatalf("%s: a violation in %q: %+v", where, v.In, v)
				}
				named = append(named, v.Path)
			}
			// geta lists the first violations in its order (the headers in
			// field order, a list's elements in order, which is the order
			// refused was built in): at most fzMaxViolations, and fewer where
			// their messages, which quote the values, pass 16 KiB, but at
			// least one; omitted counts the rest.
			n := len(named)
			if n == 0 || n > fzMaxViolations || n > len(refused) || !slices.Equal(named, refused[:n]) || p.Omitted != len(refused)-n {
				t.Fatalf("%s: violations at %q (%d omitted), want %q: %s", where, named, p.Omitted, refused, rec.Body)
			}
			return
		}
		if rec.Code != http.StatusOK || got == nil {
			t.Fatalf("%s: %d, want 200: %s", where, rec.Code, rec.Body)
		}
		v := reflect.ValueOf(got).Elem()
		for i, name := range fzHeaderNames {
			fv := v.Field(i)
			if fv.IsNil() != (want[i] == nil) {
				t.Fatalf("%s: %s bound %v, want %v", where, name, fv, want[i])
			}
			if want[i] == nil {
				continue
			}
			fv = fv.Elem()
			var canon string
			if fv.Kind() == reflect.Slice {
				parts := make([]string, fv.Len())
				for j := range parts {
					parts[j] = fzCanon(fv.Index(j))
				}
				canon = strings.Join(parts, "\x00")
				// What the handler holds, sent again as one line, is the list
				// it holds.
				if fzHeaderKinds[i] == "[]string" {
					again := refListElements([]string{strings.Join(fv.Interface().([]string), ", ")})
					if !slices.Equal(again, fv.Interface().([]string)) {
						t.Fatalf("%s: %s bound %q, which reads back as %q", where, name, fv, again)
					}
				}
			} else {
				canon = fzCanon(fv)
			}
			if canon != *want[i] {
				t.Fatalf("%s: %s bound %q, want %q", where, name, canon, *want[i])
			}
		}
	})
}

// fzHeaderNames are the headers of fzHeaderIn, in its field order.
var fzHeaderNames = []string{"X-S", "X-E", "X-I", "X-U8", "X-F", "X-B", "X-T", "X-Id", "X-Ss", "X-Is", "X-Ids", "X-Es", "X-Ts"}
