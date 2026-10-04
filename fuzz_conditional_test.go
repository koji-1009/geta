package geta_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/koji-1009/geta"
)

// refETagc reports whether b may stand inside an entity tag's quotes (RFC
// 9110 §8.8.3: %x21 / %x23-7E / obs-text).
func refETagc(b byte) bool { return b == 0x21 || b >= 0x23 && b <= 0x7E || b >= 0x80 }

// refIsTag reports whether s is one entity tag: an optional W/ and a quoted
// opaque tag.
func refIsTag(s string) bool {
	s = strings.TrimPrefix(s, "W/")
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return false
	}
	for _, b := range []byte(s[1 : len(s)-1]) {
		if !refETagc(b) {
			return false
		}
	}
	return true
}

// refTags reads an #entity-tag list (RFC 9110 §5.6.1, §8.8.3): elements
// separated by commas outside quotes, each trimmed of spaces and tabs, empty
// ones ignored, every other an entity tag.
func refTags(v string) ([]string, bool) {
	var elems []string
	quoted, start := false, 0
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case '"':
			quoted = !quoted
		case ',':
			if !quoted {
				elems = append(elems, v[start:i])
				start = i + 1
			}
		}
	}
	elems = append(elems, v[start:])
	var tags []string
	for _, e := range elems {
		if e = strings.Trim(e, " \t"); e == "" {
			continue
		}
		if !refIsTag(e) {
			return nil, false
		}
		tags = append(tags, e)
	}
	return tags, true
}

// refCondition reads an If-Match or If-None-Match: "*" (with nothing around
// it but spaces and tabs), or a list of one or more entity tags; ok false for
// anything else.
func refCondition(v string) (tags []string, star, ok bool) {
	if strings.Trim(v, " \t") == "*" {
		return nil, true, true
	}
	tags, ok = refTags(v)
	return tags, false, ok && len(tags) > 0
}

const (
	refDayName  = `(Mon|Tue|Wed|Thu|Fri|Sat|Sun)`
	refDayNameL = `(Monday|Tuesday|Wednesday|Thursday|Friday|Saturday|Sunday)`
	refMonth    = `(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)`
	refTime     = `([0-9]{2}):([0-9]{2}):([0-9]{2})`
)

var (
	refIMF     = regexp.MustCompile(`^` + refDayName + `, ([0-9]{2}) ` + refMonth + ` ([0-9]{4}) ` + refTime + ` GMT$`)
	refRFC850  = regexp.MustCompile(`^` + refDayNameL + `, ([0-9]{2})-` + refMonth + `-([0-9]{2}) ` + refTime + ` GMT$`)
	refAsctime = regexp.MustCompile(`^` + refDayName + ` ` + refMonth + ` ([0-9]{2}| [0-9]) ` + refTime + ` ([0-9]{4})$`)
	refMonths  = "JanFebMarAprMayJunJulAugSepOctNovDec"
)

// refHTTPDate reads an HTTP-date (RFC 9110 §5.6.7): an IMF-fixdate, an RFC
// 850 date (its two-digit year the latest that is not more than 50 years
// ahead of now), or an asctime date, a real date with its time of day
// 00:00:00 to 23:59:60, the leap second. Day names, months, and GMT are
// case-sensitive (%s in RFC 9110's grammar).
func refHTTPDate(s string) (time.Time, bool) {
	var day, month, year, hms string
	switch m := (refIMF.FindStringSubmatch(s)); {
	case m != nil:
		day, month, year, hms = m[2], m[3], m[4], m[5]+m[6]+m[7]
	default:
		if m := refRFC850.FindStringSubmatch(s); m != nil {
			now := time.Now().Year()
			yy, _ := strconv.Atoi(m[4])
			y := now - now%100 + yy
			if y > now+50 {
				y -= 100
			}
			day, month, year, hms = m[2], m[3], strconv.Itoa(y), m[5]+m[6]+m[7]
		} else if m := refAsctime.FindStringSubmatch(s); m != nil {
			day, month, year, hms = strings.TrimSpace(m[3]), m[2], m[7], m[4]+m[5]+m[6]
		} else {
			return time.Time{}, false
		}
	}
	n := func(s string) int { v, _ := strconv.Atoi(s); return v }
	y, mo, d := n(year), strings.Index(refMonths, month)/3+1, n(day)
	h, mi, sec := n(hms[0:2]), n(hms[2:4]), n(hms[4:6])
	if d < 1 || d > daysIn(y, mo) || h > 23 || mi > 59 || sec > 60 || sec == 60 && (h != 23 || mi != 59) {
		return time.Time{}, false
	}
	// The leap second has no later second: a modification after 23:59:59
	// is after it.
	return time.Date(y, time.Month(mo), d, h, mi, min(sec, 59), 0, time.UTC), true
}

// refConditional is the reference evaluation of a request's preconditions
// (RFC 9110 §13.2.2) for a target whose representation has the tag etag
// and the modification modified (exists false: none), on method. It returns
// the status and the header that decided it, or 0 when the method is to be
// performed. values are the four fields as bound; a nil one is absent.
func refConditional(method string, exists, required bool, etag string, modified time.Time, im, inm, ims, ius *string) (int, string) {
	safe := method == http.MethodGet || method == http.MethodHead
	date := func(v *string) (time.Time, bool) {
		if v == nil {
			return time.Time{}, false
		}
		return refHTTPDate(*v)
	}
	if !modified.IsZero() {
		modified = modified.Truncate(time.Second)
	}
	if required {
		_, unmodified := date(ius)
		_, modifiedSince := date(ims)
		if im == nil && inm == nil && !unmodified && !(safe && modifiedSince) {
			return http.StatusPreconditionRequired, "If-Match"
		}
	}
	if im != nil {
		tags, star, ok := refCondition(*im)
		switch {
		case !ok:
			return http.StatusBadRequest, "If-Match"
		case !exists:
			return http.StatusPreconditionFailed, "If-Match"
		case star:
		case etag == "" || strings.HasPrefix(etag, "W/") || !slices.Contains(tags, etag):
			return http.StatusPreconditionFailed, "If-Match"
		}
	} else if t, ok := date(ius); ok && exists && !modified.IsZero() && modified.After(t) {
		return http.StatusPreconditionFailed, "If-Unmodified-Since"
	}
	if inm != nil {
		tags, star, ok := refCondition(*inm)
		if !ok {
			return http.StatusBadRequest, "If-None-Match"
		}
		match := star
		for _, tg := range tags {
			match = match || etag != "" && strings.TrimPrefix(tg, "W/") == strings.TrimPrefix(etag, "W/")
		}
		if exists && match {
			if safe {
				return http.StatusNotModified, "If-None-Match"
			}
			return http.StatusPreconditionFailed, "If-None-Match"
		}
	} else if t, ok := date(ims); ok && safe && exists && !modified.IsZero() && !modified.After(t) {
		return http.StatusNotModified, "If-Modified-Since"
	}
	return 0, ""
}

// refCondField is what an input embedding Conditional binds of a header's
// lines, and what geta.ETag reads of If-None-Match's: absent with none; one
// line as it is; several as the list of their non-empty lines joined by ", ".
// Whatever the bytes (obs-text, which is no UTF-8, included) and the length,
// it is bound: a precondition is no string parameter.
func refCondField(lines []string) *string {
	switch len(lines) {
	case 0:
		return nil
	case 1:
		return &lines[0]
	}
	j := strings.Join(slices.DeleteFunc(slices.Clone(lines), func(s string) bool { return s == "" }), ", ")
	return &j
}

type fzTagged struct {
	ETag *string `header:"ETag"`
	Body ok      `body:"json"`
}

var fzCondHeaders = []string{"If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"}

// FuzzConditional sends fuzzed preconditions (each of the four fields
// absent, or its lines, split at NUL) to operations that check them against
// a fuzzed current representation (a strong, weak, or no tag; a
// modification or none), dates fuzzed as raw text or written near the
// modification in each of the three HTTP-date forms, and holds the answer to
// refConditional on GET, HEAD, PUT, and DELETE: Check on /r, CheckAbsent on
// /a, RequireConditional's Check on /q. Each field is bound whatever its
// bytes and length (an entity tag may hold obs-text, which is no UTF-8).
// Behind geta.ETag on /e, If-None-Match alone decides, read as Check reads
// it, against the response's tag. A 304 has no body and
// carries the validator; every 4xx is a problem of its status, a 412's
// detail beginning with the field that decided it.
func FuzzConditional(f *testing.F) {
	var cur string
	var mod time.Time
	check := func(_ context.Context, in *condIn) (*ok, error) {
		if err := in.Check(cur, mod); err != nil {
			return nil, err
		}
		return &ok{true}, nil
	}
	write := func(_ context.Context, in *condIn) error { return in.Check(cur, mod) }
	tagged := func(context.Context, *empty) (*fzTagged, error) {
		out := &fzTagged{Body: ok{true}}
		if cur != "" {
			out.ETag = &cur
		}
		return out, nil
	}
	app, err := geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/r", Route: geta.Route{
			Get:    geta.Op(http.StatusOK, check, geta.Doc{}),
			Put:    geta.OpNoBody(http.StatusNoContent, write, geta.Doc{}),
			Delete: geta.OpNoBody(http.StatusNoContent, write, geta.Doc{}),
		}},
		{Path: "/a", Route: geta.Route{
			Put: geta.OpNoBody(http.StatusNoContent, func(_ context.Context, in *condIn) error { return in.CheckAbsent() }, geta.Doc{}),
		}},
		{Path: "/q", Route: geta.Route{
			Get: geta.Op(http.StatusOK, func(_ context.Context, in *requiredIn) (*ok, error) {
				if err := in.Check(cur, mod); err != nil {
					return nil, err
				}
				return &ok{true}, nil
			}, geta.Doc{}),
			Put: geta.OpNoBody(http.StatusNoContent, func(_ context.Context, in *requiredIn) error { return in.Check(cur, mod) }, geta.Doc{}),
		}},
		{Path: "/e", Route: geta.Route{Get: geta.Op(http.StatusOK, tagged, geta.Doc{})}, Scopes: []geta.Scope{{geta.ETag()}}},
	}})
	if err != nil {
		f.Fatal(err)
	}
	// The tag geta.ETag computes for /e's body, which is always the same.
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/e", nil))
	computed := rec.Header().Get("ETag")
	if rec.Code != 200 || computed == "" {
		f.Fatal(rec.Code, rec.Header())
	}
	base := time.Date(2024, 1, 2, 3, 4, 5, 500_000_000, time.UTC)

	// sel: bits 0-1 the route (r, a, q, e), 2-3 the method, 4-5 the tag
	// (none, strong, weak, strong), 6 no modification. present: bits 0-3
	// the fields sent. forms: per date field two bits (raw, IMF, RFC 850,
	// asctime), If-Modified-Since's first. deltas: per date field a byte,
	// the seconds from the modification a written date names (mod 3).
	f.Add(`"v2"`, `"v2"`, "", "", uint8(0b010001), uint8(0b0001), uint8(0), uint16(0), "v2", int64(0))
	f.Add(`"v1", "v2"`, `W/"v2"`, "", "", uint8(0b010000), uint8(0b0010), uint8(0), uint16(0), "v2", int64(0))
	f.Add(`, "v1" ,, "v2",`, "*", "", "", uint8(0b011000), uint8(0b0011), uint8(0), uint16(0), "v2", int64(0))
	f.Add(`"v,2"`, `"v1", *`, "", "", uint8(0b101000), uint8(0b0011), uint8(0), uint16(0), "w", int64(0))
	f.Add("\"v1\"\x00\"v2\"", "\x00", "", "", uint8(0b011000), uint8(0b0011), uint8(0), uint16(0), "v2", int64(0))
	f.Add("*\x00*", " , ", "", "", uint8(0b011000), uint8(0b0011), uint8(0), uint16(0), "v2", int64(0))
	f.Add("v2", `"v2`, "", "", uint8(0b011000), uint8(0b0011), uint8(0), uint16(0), "v2", int64(0))
	f.Add("", "", "now", "yesterday", uint8(0b011010), uint8(0b1100), uint8(0b0101), uint16(0x0102), "v2", int64(0))
	f.Add("", "", "", "", uint8(0b011000), uint8(0b1100), uint8(0b1010), uint16(0x00FF), "v2", int64(1))
	f.Add("", "", "", "", uint8(0b011000), uint8(0b1100), uint8(0b1111), uint16(0x0101), "v2", int64(-86400))
	f.Add("", "", "Tue, 02 Jan 2024 03:04:04 UTC", "Tuesday, 02-Jan-24 03:04:04 PST", uint8(0b011000), uint8(0b1100), uint8(0), uint16(0), "v2", int64(0))
	f.Add("", "", "Tue Jan  2 03:04:04 2024", "Wednesday, 01-Jan-70 00:00:00 GMT", uint8(0b011000), uint8(0b1100), uint8(0), uint16(0), "v2", int64(0))
	// Not HTTP-dates, which time.Parse takes; and the leap second, which it
	// refuses. Each decides a PUT unless ignored.
	for _, d := range []string{"Tue, 02 Jan 2024 3:04:04 GMT", "Tue, 02 Jan 2024 03:04:04.9 GMT", "Tue Jan 2 03:04:04 2024",
		"Tue,  02 Jan 2024 03:04:04 GMT", "Tue, 02 JAN 2024 03:04:04 GMT", "tue, 02 Jan 2024 03:04:04 GMT", "Tuesday, 02-Jan-24 3:04:04 GMT",
		"Sun, 31 Dec 2023 23:59:60 GMT", "Mon, 01 Jan 2024 23:59:60 GMT", "Tue, 02 Jan 2024 03:04:60 GMT", "Wed, 02 Jan 2024 03:04:04 GMT"} {
		f.Add("", "", "", d, uint8(0b011000), uint8(0b1000), uint8(0), uint16(0), "v2", int64(0))
	}
	f.Add("", `"x"`, "Tue, 02 Jan 2024 03:04:05 GMT", "", uint8(0b1000000), uint8(0b0110), uint8(0), uint16(0), "", int64(0))
	f.Add("", "*", "", "", uint8(0b011100), uint8(0b0010), uint8(0), uint16(0), "v2", int64(0))
	f.Add("", "", "", "", uint8(0b011110), uint8(0b0000), uint8(0b0101), uint16(0), "v2", int64(0))
	f.Add("", "*\xc2\xa0", "", "", uint8(0b010011), uint8(0b0010), uint8(0), uint16(0), "v2", int64(0))
	f.Add("*\xc2\x85", "*\xc2\xa0", "", "", uint8(0b011000), uint8(0b0011), uint8(0), uint16(0), "v2", int64(0))
	f.Add("", "\"\xff\"", "", "", uint8(0b010011), uint8(0b0010), uint8(0), uint16(0), "\xff", int64(0))
	f.Add("", " *", "", "", uint8(0b010000), uint8(0b0010), uint8(0), uint16(0), "v2", int64(0))
	// obs-text in a tag, which is no UTF-8, is a tag to Check as to ETag.
	f.Add("\"\xff\"", "", "", "", uint8(0b011000), uint8(0b0001), uint8(0), uint16(0), "\xff", int64(0))
	f.Add("", "W/\"\xff\"", "", "", uint8(0b010000), uint8(0b0010), uint8(0), uint16(0), "\xff", int64(0))
	f.Add("\"\xfe\"", "", "", "", uint8(0b011000), uint8(0b0001), uint8(0), uint16(0), "\xff", int64(0))
	// A date of no UTF-8, or with a control character, is no HTTP-date: ignored.
	f.Add("", "", "", "Tue, 02 Jan 2024 03:04:04 GMT\xff", uint8(0b011000), uint8(0b1000), uint8(0), uint16(0), "v2", int64(0))
	f.Add("", "", "Tue, 02 Jan 2024 03:04:06 GMT\x01", "", uint8(0b010000), uint8(0b0100), uint8(0), uint16(0), "v2", int64(0))
	// An empty line beside "*" is an empty list element: the value is "*".
	f.Add("", "\x00*", "", "", uint8(0b010000), uint8(0b0010), uint8(0), uint16(0), "v2", int64(0))
	f.Add("", "\x00*", "", "", uint8(0b010011), uint8(0b0010), uint8(0), uint16(0), "v2", int64(0))
	// A list past MaxStringLength is read as any other.
	f.Add(strings.Repeat(`"x", `, 1000)+`"v2"`, "", "", "", uint8(0b011000), uint8(0b0001), uint8(0), uint16(0), "v2", int64(0))
	f.Add("", strings.Repeat(`"x", `, 1000)+`"v2"`, "", "", uint8(0b010011), uint8(0b0010), uint8(0), uint16(0), "v2", int64(0))
	f.Fuzz(func(t *testing.T, im, inm, ims, ius string, sel, present, forms uint8, deltas uint16, opaque string, modSecs int64) {
		route := "raqe"[sel&3 : sel&3+1]
		method := map[string][4]string{
			"r": {"GET", "HEAD", "PUT", "DELETE"},
			"a": {"PUT", "PUT", "PUT", "PUT"},
			"q": {"GET", "HEAD", "PUT", "PUT"},
			"e": {"GET", "HEAD", "GET", "HEAD"},
		}[route][sel>>2&3]
		op := []byte(nil)
		for _, b := range []byte(opaque) {
			if refETagc(b) {
				op = append(op, b)
			}
		}
		switch sel >> 4 & 3 {
		case 0:
			cur = ""
		case 2:
			cur = `W/"` + string(op) + `"`
		default:
			cur = `"` + string(op) + `"`
		}
		mod = time.Time{}
		if sel&0x40 == 0 {
			mod = base.Add(time.Duration(modSecs%(20*365*86400)) * time.Second)
		}
		at := mod
		if at.IsZero() {
			at = base
		}
		values := []string{im, inm, ims, ius}
		for i, form := range []uint8{forms & 3, forms >> 2 & 3} {
			d := at.Truncate(time.Second).Add(time.Duration(int8(deltas>>(8*i))%3) * time.Second)
			switch form {
			case 1:
				values[2+i] = d.Format(http.TimeFormat)
			case 2:
				values[2+i] = d.Format("Monday, 02-Jan-06 15:04:05 GMT")
			case 3:
				values[2+i] = d.Format(time.ANSIC)
			}
		}
		req := httptest.NewRequest(method, "/"+route, nil)
		lines := make([][]string, 4)
		for i, name := range fzCondHeaders {
			if present>>i&1 == 1 {
				lines[i] = strings.Split(values[i], "\x00")
				req.Header[name] = lines[i]
			}
		}
		where := fmt.Sprintf("%s /%s, tag %q, modified %v, %q", method, route, cur, mod, req.Header)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)

		var status int
		var decider string
		var bad []string
		success := http.StatusOK
		if method == "PUT" || method == "DELETE" {
			success = http.StatusNoContent
		}
		tag := cur
		if route == "e" {
			if tag == "" {
				tag = computed
			}
			if lines[1] != nil {
				tags, star, ok := refCondition(*refCondField(lines[1]))
				switch {
				case !ok:
					status, decider, bad = http.StatusBadRequest, "If-None-Match", []string{"If-None-Match"}
				case star || slices.ContainsFunc(tags, func(tg string) bool { return strings.TrimPrefix(tg, "W/") == strings.TrimPrefix(tag, "W/") }):
					status = http.StatusNotModified
				}
			}
		} else {
			var v [4]*string
			for i := range fzCondHeaders {
				v[i] = refCondField(lines[i])
			}
			status, decider = refConditional(method, route != "a", route == "q", cur, mod, v[0], v[1], v[2], v[3])
			if status == http.StatusBadRequest {
				bad = []string{decider}
			}
		}
		if status == 0 {
			status = success
		}
		if rec.Code != status {
			t.Fatalf("%s: %d, want %d (%s): %s", where, rec.Code, status, decider, rec.Body)
		}
		switch {
		case status == http.StatusNotModified:
			if rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
				t.Fatalf("%s: a 304 with a body: %q", where, rec.Body)
			}
			switch {
			case tag != "":
				if got := rec.Header().Get("ETag"); got != tag {
					t.Fatalf("%s: a 304 with ETag %q, want %q", where, got, tag)
				}
			case !mod.IsZero():
				if got, want := rec.Header().Get("Last-Modified"), mod.UTC().Format(http.TimeFormat); got != want {
					t.Fatalf("%s: a 304 with Last-Modified %q, want %q", where, got, want)
				}
			}
		case status >= 400:
			p := fuzzProblem(t, where, status, rec.Header(), rec.Body.Bytes())
			switch status {
			case http.StatusBadRequest:
				var named []string
				for _, e := range p.Errors {
					if e.In == "header" {
						named = append(named, e.Path)
					}
				}
				if !slices.Equal(named, bad) || len(p.Errors) != len(bad) {
					t.Fatalf("%s: violations %+v, want %q", where, p.Errors, bad)
				}
			case http.StatusPreconditionFailed:
				if !strings.HasPrefix(p.Detail, decider+" ") {
					t.Fatalf("%s: a 412 decided by %s says %q", where, decider, p.Detail)
				}
			case http.StatusPreconditionRequired:
				if !strings.Contains(p.Detail, "If-Match") {
					t.Fatalf("%s: a 428 says %q", where, p.Detail)
				}
			}
		}
	})
}
