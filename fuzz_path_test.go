package geta_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
	"uuid"

	"github.com/koji-1009/geta"
)

type fzIntPath struct {
	N int32 `path:"n"`
}

type fzUUIDPath struct {
	ID uuid.UUID `path:"id"`
}

type fzDatePath struct {
	D geta.Date `path:"d"`
}

type fzStringPath struct {
	S string `path:"s" schema:"maxLength=8"`
}

var refFullDate = regexp.MustCompile(`^([0-9]{4})-([0-9]{2})-([0-9]{2})$`)

// refDate reads an RFC 3339 full-date: a real day of the Gregorian
// calendar, its year 0000 to 9999.
func refDate(s string) bool {
	m := refFullDate.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	y, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	d, _ := strconv.Atoi(m[3])
	return mo >= 1 && mo <= 12 && d >= 1 && d <= daysIn(y, mo)
}

// FuzzPathValues sends a fuzzed segment, as the client escaped it (when it
// is a segment a request-target carries as net/http reads one: no space or
// control byte, any byte past ASCII, any escape) or as url.PathEscape
// escapes it,
// to a path parameter of an int32, a uuid.UUID, a geta.Date, and a string
// of at most 8 code points, and holds the answer to the rules: "." and ".."
// are redirected (307) to the clean path on the request's host; an empty
// segment matches nothing (404); any other, its escapes decoded (%2F stays
// in the segment, %2E is no dot segment), binds the reference's value (an
// integer as JSON writes one in range, a 36-character uuid, a real
// full-date, a UTF-8 string within its maxLength) or is a 400 problem whose
// one violation is the parameter's, in path. The segment is cut to 128
// bytes (fzCut), past the longest value any parameter takes.
func FuzzPathValues(f *testing.F) {
	var got string
	echo := func(v fmt.Stringer) { got = v.String() }
	app, err := geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/i/{n}", Route: get(func(_ context.Context, in *fzIntPath) (*ok, error) {
			got = strconv.FormatInt(int64(in.N), 10)
			return &ok{true}, nil
		})},
		{Path: "/u/{id}", Route: get(func(_ context.Context, in *fzUUIDPath) (*ok, error) { echo(in.ID); return &ok{true}, nil })},
		{Path: "/d/{d}", Route: get(func(_ context.Context, in *fzDatePath) (*ok, error) { echo(in.D); return &ok{true}, nil })},
		{Path: "/s/{s}", Route: get(func(_ context.Context, in *fzStringPath) (*ok, error) {
			got = in.S
			return &ok{true}, nil
		})},
	}})
	if err != nil {
		f.Fatal(err)
	}
	for _, s := range []string{"7", "-0", "007", "+7", "2147483648", "-2147483648", "1e3", "%37", "%2D1",
		"123e4567-e89b-12d3-a456-426614174000", "123E4567-E89B-12D3-A456-426614174000", "%7B123e4567-e89b-12d3-a456-426614174000%7D",
		"2024-02-29", "2023-02-29", "0000-02-29", "2024-1-01", "2024-01-01T00:00:00Z",
		"a%2Fb", "a/b", "%2E", "%2E%2E", ".", "..", "", "%", "%zz", "%C3%A9", "%FF", "%00", "a%20b", "12345678", "123456789",
		"%E2%82%AC%E2%82%AC%E2%82%AC%E2%82%AC%E2%82%AC%E2%82%AC%E2%82%AC%E2%82%AC", "\\evil.example", "?q", "#f",
		"%2F\"", "a%2Fb{", "a%2Fb|", "a%2Fb\xc3\xa9", "%2F%2F\"", "\xc3\xa9"} {
		for route := range uint8(4) {
			f.Add(route, s, true)
			f.Add(route, s, false)
		}
	}
	f.Fuzz(func(t *testing.T, route uint8, seg string, raw bool) {
		seg = fzCut(seg, 128)
		prefix := []string{"/i/", "/u/", "/d/", "/s/"}[route%4]
		escaped := seg
		if !raw || strings.ContainsAny(seg, "/?#") || strings.ContainsFunc(seg, func(r rune) bool { return r <= 0x20 || r == 0x7F }) {
			escaped = url.PathEscape(seg)
		}
		value, err := url.PathUnescape(escaped)
		if err != nil {
			escaped = url.PathEscape(seg)
			value = seg
		}
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.URL.Path, req.URL.RawPath, req.RequestURI = prefix+value, prefix+escaped, prefix+escaped
		where := fmt.Sprintf("GET %s (%q)", req.RequestURI, req.URL.Path)
		if escaped == "." || escaped == ".." {
			redirectStaysOnHost(t, app, req)
			return
		}
		got = ""
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if escaped == "" {
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s: %d, want 404: %s", where, rec.Code, rec.Body)
			}
			return
		}
		var want string
		var ok bool
		switch prefix {
		case "/i/":
			var n int64
			n, _, ok = refInt(value, 32, false)
			want = strconv.FormatInt(n, 10)
		case "/u/":
			want, ok = strings.ToLower(value), refUUID.MatchString(value)
		case "/d/":
			want, ok = value, refDate(value)
		case "/s/":
			want, ok = value, utf8.ValidString(value) && utf8.RuneCountInString(value) <= 8
		}
		if !ok {
			if rec.Code != http.StatusBadRequest || got != "" {
				t.Fatalf("%s: %d (bound %q), want 400: %s", where, rec.Code, got, rec.Body)
			}
			p := fuzzProblem(t, where, rec.Code, rec.Header(), rec.Body.Bytes())
			name := map[string]string{"/i/": "n", "/u/": "id", "/d/": "d", "/s/": "s"}[prefix]
			if len(p.Errors) != 1 || p.Errors[0].In != "path" || p.Errors[0].Path != name {
				t.Fatalf("%s: violations %+v, want one at path %s", where, p.Errors, name)
			}
			return
		}
		if rec.Code != http.StatusOK || got != want {
			t.Fatalf("%s: %d, bound %q, want 200 binding %q: %s", where, rec.Code, got, want, rec.Body)
		}
	})
}
