package geta

import (
	"bufio"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// carryAlphabet holds every class of byte the carriers tell apart, and a
// rune past ASCII, whole and cut short.
var carryAlphabet = []string{"\x00", "\t", "\n", "\r", "\x1f", " ", "!", `"`, ",", ";", `\`, "a", "~", "\x7f",
	"\x80", "\xff", "é", " ", "\U0001F600"}

// carryStrings are every string of up to three symbols of carryAlphabet,
// and random longer ones.
func carryStrings() []string {
	out := []string{""}
	level := []string{""}
	for range 3 {
		var next []string
		for _, s := range level {
			for _, a := range carryAlphabet {
				next = append(next, s+a)
			}
		}
		out = append(out, next...)
		level = next
	}
	r := rand.New(rand.NewPCG(1, 2))
	for range 20000 {
		var b strings.Builder
		for range 4 + r.IntN(12) {
			b.WriteString(carryAlphabet[r.IntN(len(carryAlphabet))])
		}
		out = append(out, b.String())
	}
	return out
}

// Each carrier passes exactly the strings its documented pattern matches.
func TestCarriersHoldWhatTheirPatternsMatch(t *testing.T) {
	strs := carryStrings()
	for loc, c := range carriers {
		re := regexp.MustCompile(c.pattern)
		for _, s := range strs {
			if got, want := c.holds(s), re.MatchString(s); got != want {
				t.Errorf("%s %q: holds %v, the pattern %v", loc, s, got, want)
			}
		}
	}
}

// The cookie carrier passes exactly what net/http's Request.CookiesNamed
// returns as it was sent: unquoted, or quoted as net/http's client quotes a
// value with a space or a comma.
func TestCookieCarrierIsWhatNetHTTPReads(t *testing.T) {
	read := func(header string) (string, bool) {
		r := &http.Request{Header: http.Header{"Cookie": {header}}}
		cs := r.CookiesNamed("c")
		if len(cs) == 0 {
			return "", false
		}
		return cs[0].Value, true
	}
	var values []string
	for b := range 256 {
		values = append(values, "a"+string([]byte{byte(b)})+"b", string([]byte{byte(b)}))
	}
	values = append(values, " a", "a ", " ", "a b,c", "é")
	for _, v := range values {
		got, ok := read(`c="` + v + `"`)
		carried := ok && got == v
		if carried != cookieCarries(v) {
			t.Errorf("%q: net/http reads %q (%v) from the quoted value; cookieCarries says %v", v, got, ok, cookieCarries(v))
		}
	}
	// What a value with no space or comma needs no quotes for, unquoted.
	for _, v := range []string{"a", "a=b", "!#$%&'()*+-./:<=>?@[]^_`{|}~"} {
		if got, ok := read("c=" + v); !ok || got != v || !cookieCarries(v) {
			t.Errorf("%q: read %q, %v", v, got, ok)
		}
	}
}

// The header carrier passes exactly what a net/http server receives as it
// was sent, over a connection.
func TestHeaderCarrierIsWhatNetHTTPReceives(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Values("X-A")
	}))
	defer srv.Close()
	for _, v := range []string{"", "a", "a b", "a\tb", "a  b", " a", "a ", "\ta", "a\t", "a\x01b", "a\x7fb", "a\rb",
		"a\x00b", "é", "a\x80b", "\xff", `"q", W/"r"`, "\x1f"} {
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		got = nil
		fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\nX-A: %s\r\nConnection: close\r\n\r\n", v)
		res, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		conn.Close()
		received := res.StatusCode == http.StatusOK && slices.Equal(got, []string{v})
		if received != headerCarries(v) {
			t.Errorf("%q: status %d, received %q; headerCarries says %v", v, res.StatusCode, got, headerCarries(v))
		}
	}
}

// A format the carriers pass is one each of whose values its place carries:
// every case of the suite the format takes is carried, and so is each with
// a symbol of carryAlphabet written into it, or the format refuses it.
func TestCarriedFormatsCarryEveryValue(t *testing.T) {
	files, err := filepath.Glob("testdata/jsonschema-suite/*.json")
	if err != nil || len(files) == 0 {
		t.Fatal("no suite files", err)
	}
	var cases []string
	for _, file := range files {
		for _, c := range suiteCases(t, file) {
			cases = append(cases, c.data)
		}
	}
	for loc, formats := range carriedFormats {
		for _, f := range formats {
			check := formatChecks[f]
			for _, s := range cases {
				if !check(s) {
					continue
				}
				variants := []string{s}
				for i := range len(s) + 1 {
					for _, a := range carryAlphabet {
						variants = append(variants, s[:i]+a+s[i:])
					}
				}
				for _, v := range variants {
					if check(v) && !carriers[loc].holds(v) {
						t.Errorf("%s format %s takes %q, which a %s cannot carry", loc, f, v, loc)
					}
				}
			}
		}
	}
	// The formats left out have a value their place cannot carry.
	for loc, uncarried := range map[string]map[string]string{
		"header": {"json-pointer": "/a ", "relative-json-pointer": "0/a\t", "password": " "},
		"cookie": {"json-pointer": "/;", "relative-json-pointer": "0/;", "password": ";"},
	} {
		for f, v := range uncarried {
			if slices.Contains(carriedFormats[loc], f) || !formatChecks[f](v) || carriers[loc].holds(v) {
				t.Errorf("%s format %s: %q", loc, f, v)
			}
		}
		if n := len(carriedFormats[loc]) + len(uncarried); n != len(formatChecks) {
			t.Errorf("%s: %d formats judged, %d checked", loc, n, len(formatChecks))
		}
	}
}

// A time.Time's schema, its format and the pattern it adds, admits exactly
// the date-times a time.Time holds, on every case of the suite and each
// leap second.
func TestTimeSchemaAdmitsWhatTimeHolds(t *testing.T) {
	c, err := newRegistry().codecFor(timeType)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(c.schema.document(nil)["pattern"].(string))
	files, _ := filepath.Glob("testdata/jsonschema-suite/*.json")
	var cases []string
	for _, file := range files {
		for _, sc := range suiteCases(t, file) {
			cases = append(cases, sc.data)
		}
	}
	cases = append(cases, "2016-12-31T23:59:60Z", "2016-12-31t23:59:60z", "2016-12-31T15:59:60-08:00",
		"2016-12-31T23:59:59Z", "2016-12-31T23:59:60.999Z", "2017-01-01T08:59:60+09:00", "2016-12-31T23:59:50Z")
	leaps := 0
	for _, s := range cases {
		d := &decoder{limits: DefaultLimits}
		admitted := d.str(c.schema, s, "$")
		_, err := readTime(s)
		if admitted != (err == nil) {
			t.Errorf("%q: the schema admits it %v, a time.Time holds it %v (%v)", s, admitted, err == nil, err)
		}
		// The document's pattern is the one enforced.
		if validDateTime(s) && re.MatchString(s) != admitted {
			t.Errorf("%q: the documented pattern says %v", s, !admitted)
		}
		if validDateTime(s) && !admitted {
			leaps++
		}
	}
	if leaps < 4 {
		t.Errorf("%d leap seconds judged", leaps)
	}
}
