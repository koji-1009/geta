package geta_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
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

// The fuzz targets of a request's inputs: a query string, a form body, a
// multipart body, and the typed response headers App.Conforms reads as a
// request's header parameters are read.

// fzFilter is a deepObject's members, and a form body of the same fields.
type fzFilter struct {
	Min    *int       `form:"min" schema:"minimum=0"`
	Status string     `form:"status" schema:"enum=open|closed,default=open"`
	Since  *geta.Date `form:"since"`
	Q      *string    `form:"q" schema:"maxLength=5"`
}

// FzQuery holds a parameter of every kind a query binds: a defaulted and
// bounded integer, scalars, a string held to a pattern, an enum, format
// types, a string of a format, a required and an optional slice. fiForm is
// the same fields as a form's, so one converts to the other.
type FzQuery struct {
	I  int16      `query:"i" schema:"minimum=-5,maximum=500,default=7"`
	U  *uint8     `query:"u" schema:"exclusiveMaximum=200"`
	F  *float64   `query:"f" schema:"multipleOf=0.5"`
	B  *bool      `query:"b"`
	S  *string    `query:"s" schema:"maxLength=8,pattern=^[a-z]*$"`
	E  *string    `query:"e" schema:"enum=red|green"`
	D  *geta.Date `query:"d"`
	T  *time.Time `query:"t"`
	ID *uuid.UUID `query:"id"`
	Sd *string    `query:"sd" schema:"format=date"`
	R  []string   `query:"r" schema:"maxItems=3,items.maxLength=4"`
	P  *[]float32 `query:"p" schema:"uniqueItems=true,items.minimum=0"`
}

type fiForm struct {
	I  int16      `form:"i" schema:"minimum=-5,maximum=500,default=7"`
	U  *uint8     `form:"u" schema:"exclusiveMaximum=200"`
	F  *float64   `form:"f" schema:"multipleOf=0.5"`
	B  *bool      `form:"b"`
	S  *string    `form:"s" schema:"maxLength=8,pattern=^[a-z]*$"`
	E  *string    `form:"e" schema:"enum=red|green"`
	D  *geta.Date `form:"d"`
	T  *time.Time `form:"t"`
	ID *uuid.UUID `form:"id"`
	Sd *string    `form:"sd" schema:"format=date"`
	R  []string   `form:"r" schema:"maxItems=3,items.maxLength=4"`
	P  *[]float32 `form:"p" schema:"uniqueItems=true,items.minimum=0"`
}

type fzQueryIn struct {
	FzQuery
	Filter *fzFilter `query:"filter"`
}

type fiFormIn struct {
	Body fiForm `body:"form"`
}

type fzDeepIn struct {
	Filter *fzFilter `query:"filter"`
}

type fzFilterFormIn struct {
	Body *fzFilter `body:"form"`
}

// fzNames are the names FzQuery and fiForm bind.
var fzNames = []string{"i", "u", "f", "b", "s", "e", "d", "t", "id", "sd", "r", "p"}

// fzSend serves one request: rawQuery is the query string as sent, body nil
// for none, and unknown sends the body with no declared length.
func fzSend(a *geta.App, method, path, rawQuery, ctype string, body []byte, unknown bool) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rd)
	r.URL.RawQuery = rawQuery
	r.RequestURI = ""
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	if unknown && body != nil {
		r.ContentLength = -1
	}
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, r)
	return rec
}

// fzRefusal checks that rec is a 200, or one of the statuses allowed
// answered as a problem whose status is the response's, and returns the
// problem (nil for a 200).
func fzRefusal(t *testing.T, rec *httptest.ResponseRecorder, allowed ...int) *geta.Problem {
	t.Helper()
	if rec.Code == http.StatusOK {
		return nil
	}
	if !slices.Contains(allowed, rec.Code) {
		t.Fatalf("status %d, want 200 or one of %v: %s", rec.Code, allowed, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("status %d with Content-Type %q: %s", rec.Code, ct, rec.Body)
	}
	var p geta.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("status %d: the problem is not JSON: %v: %s", rec.Code, err, rec.Body)
	}
	if p.Status != rec.Code {
		t.Fatalf("status %d, the problem says %d", rec.Code, p.Status)
	}
	return &p
}

// fzValues is the query a bound FzQuery (and filter) is sent as, each value
// written as geta reads it back.
func fzValues(q FzQuery, filter *fzFilter) url.Values {
	v := url.Values{}
	v.Set("i", strconv.Itoa(int(q.I)))
	if q.U != nil {
		v.Set("u", strconv.FormatUint(uint64(*q.U), 10))
	}
	if q.F != nil {
		v.Set("f", strconv.FormatFloat(*q.F, 'g', -1, 64))
	}
	if q.B != nil {
		v.Set("b", strconv.FormatBool(*q.B))
	}
	if q.S != nil {
		v.Set("s", *q.S)
	}
	if q.E != nil {
		v.Set("e", *q.E)
	}
	if q.D != nil {
		v.Set("d", q.D.String())
	}
	if q.T != nil {
		v.Set("t", q.T.Format(time.RFC3339Nano))
	}
	if q.ID != nil {
		v.Set("id", q.ID.String())
	}
	if q.Sd != nil {
		v.Set("sd", *q.Sd)
	}
	for _, r := range q.R {
		v.Add("r", r)
	}
	if q.P != nil {
		for _, p := range *q.P {
			v.Add("p", strconv.FormatFloat(float64(p), 'g', -1, 32))
		}
	}
	if filter != nil {
		for k, vs := range fzFilterValues(*filter) {
			v["filter["+k+"]"] = vs
		}
	}
	return v
}

// fzFilterValues is the form a bound fzFilter is sent as.
func fzFilterValues(f fzFilter) url.Values {
	v := url.Values{"status": {f.Status}}
	if f.Min != nil {
		v.Set("min", strconv.Itoa(*f.Min))
	}
	if f.Since != nil {
		v.Set("since", f.Since.String())
	}
	if f.Q != nil {
		v.Set("q", *f.Q)
	}
	return v
}

var (
	fzJSONInt  = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
	fzUnsigned = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
	fzLower    = regexp.MustCompile(`^[a-z]*$`)
)

// fzValid reports, of the parameters whose constraints are simple to state,
// whether vals holds a value each takes, judged apart from geta: a JSON
// integer in range, a boolean, a string's length in code points and its
// pattern, an enum, and a slice's length and its elements' lengths.
func fzValid(vals url.Values) map[string]bool {
	one := func(name string, ok func(string) bool) bool {
		vs, present := vals[name]
		return !present || len(vs) == 1 && ok(vs[0])
	}
	r := vals["r"]
	rOK := len(r) >= 1 && len(r) <= 3
	for _, s := range r {
		rOK = rOK && utf8.ValidString(s) && utf8.RuneCountInString(s) <= 4
	}
	return map[string]bool{
		"i": one("i", func(s string) bool {
			n, err := strconv.ParseInt(s, 10, 64)
			return fzJSONInt.MatchString(s) && err == nil && n >= -5 && n <= 500
		}),
		"u": one("u", func(s string) bool {
			n, err := strconv.ParseUint(s, 10, 64)
			return fzUnsigned.MatchString(s) && err == nil && n < 200
		}),
		"b": one("b", func(s string) bool { return s == "true" || s == "false" }),
		"s": one("s", func(s string) bool {
			return utf8.ValidString(s) && utf8.RuneCountInString(s) <= 8 && fzLower.MatchString(s)
		}),
		"e": one("e", func(s string) bool { return s == "red" || s == "green" }),
		"r": rOK,
	}
}

// fzRefused is the set of names the violations of p are at, but those of
// unknown fields, each violation's location checked to be in and trim cut
// from its path.
func fzRefused(t *testing.T, p *geta.Problem, in string, trim string) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, v := range p.Errors {
		if v.In != in {
			t.Fatalf("violation in %q, want %q: %+v", v.In, in, p.Errors)
		}
		if v.Message == "unknown field" {
			continue // a name no field binds, which may hold "["
		}
		name, _, _ := strings.Cut(strings.TrimPrefix(v.Path, trim), "[")
		names[name] = true
	}
	return names
}

// fzAgrees checks that the violations of p name exactly the parameters
// fzValid finds invalid. Where the response omitted violations (past 50, or
// past 16 KiB of them), a missing one is not a disagreement.
func fzAgrees(t *testing.T, vals url.Values, refused map[string]bool, p *geta.Problem) {
	t.Helper()
	for name, ok := range fzValid(vals) {
		switch {
		case ok && refused[name]:
			t.Fatalf("%s=%q refused, which its constraint takes: %+v", name, vals[name], p.Errors)
		case !ok && !refused[name] && (p == nil || p.Omitted == 0):
			t.Fatalf("%s=%q taken, which its constraint refuses", name, vals[name])
		}
	}
}

var fzQuerySeeds = []string{
	"r=a",
	"r=a&i=-5&u=199&f=1.5&b=true&s=abc&e=red&d=2026-02-28&t=2026-07-18T09:30:00Z&id=123e4567-e89b-12d3-a456-426614174000&sd=2026-02-28&p=1&p=2.5",
	"r=a&i=501&u=200&f=0.3&b=TRUE&s=Abc&e=blue&d=2026-02-30&t=1998-12-31T23:59:60Z&id=x&sd=2026-02-30&p=1&p=1&p=-1",
	"r=a&u=-0&i=-0&i=007",
	"r=a&r=b&r=c&r=d", "r=abcde", "r=%ff", "r=a&s=", "r=a&u=", "r=a&f=1e400", "r=a&f=-0", "r=a&f=1e+21&p=3.4028235e38",
	"r=a&t=2026-07-18t09:30:00z&t2=x", "r=a&t=1990-12-31T15:59:59-24:00", "r=a&t=2026-07-18T09:30:00,5Z",
	"r=a&filter[min]=1&filter[since]=2026-05-01", "r=a&filter%5Bstatus%5D=closed&filter=ignored&filterx[min]=1",
	"r=a&filter[min][x]=1", "r=a&filter[bogus]=1", "r=a&filter[min]=1&filter[min]=2", "r=a&filter[]=1&filter[status]=gone",
	"%zz", "r=a;i=1", "=x&r=a", "r=a&&", "&=&r=+", "r=%e3%81%82", "r=a&s=%00", "r=a&i=1&i=2", "r=a&p=NaN",
	"r=a&id=123E4567-E89B-12D3-A456-426614174000", "limit=3&mode=a&n=1.5&n=2", "r=a&filter[min]=-1&filter[q]=abcdef",
}

// FuzzQueryParameters sends an arbitrary query string to an input holding
// every kind of query parameter and a deepObject. A string net/url cannot
// parse is a 400 naming no violation; any other is a 200 or a 400 problem
// whose violations are at the parameters a check apart from geta finds
// invalid, and only those; and what a 200 binds, sent again as the query
// it is written as, binds the same values.
func FuzzQueryParameters(f *testing.F) {
	for _, s := range fzQuerySeeds {
		f.Add(s)
	}
	var got *fzQueryIn
	h := func(_ context.Context, in *fzQueryIn) (*ok, error) { got = in; return &ok{true}, nil }
	a, err := geta.New(one("/q", get(h)))
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		got = nil
		p := fzRefusal(t, fzSend(a, http.MethodGet, "/q", raw, "", nil, false), http.StatusBadRequest)
		vals, perr := url.ParseQuery(raw)
		if perr != nil {
			if p == nil || p.Detail != "the query string is malformed" || len(p.Errors) != 0 {
				t.Fatalf("%q, which net/url refuses (%v): %+v", raw, perr, p)
			}
			return
		}
		if p == nil {
			fzAgrees(t, vals, nil, nil)
			if got.S != nil && *got.S != vals.Get("s") || !slices.Equal(got.R, vals["r"]) {
				t.Fatalf("%q bound s=%v r=%q", raw, got.S, got.R)
			}
			if _, sent := vals["i"]; !sent && got.I != 7 {
				t.Fatalf("%q bound i=%d, not its default", raw, got.I)
			}
			again := fzValues(got.FzQuery, got.Filter).Encode()
			if p := fzRefusal(t, fzSend(a, http.MethodGet, "/q", again, "", nil, false)); p != nil {
				t.Fatalf("%q bound values that, sent as %q, are refused: %+v", raw, again, p)
			}
			if back := fzValues(got.FzQuery, got.Filter).Encode(); back != again {
				t.Fatalf("%q: %q binds %q", raw, again, back)
			}
			return
		}
		refused := fzRefused(t, p, "query", "")
		for name := range refused {
			if !slices.Contains(fzNames, name) && name != "filter" {
				t.Fatalf("%q: a violation at %q, which no parameter binds: %+v", raw, name, p.Errors)
			}
		}
		fzAgrees(t, vals, refused, p)
	})
}

const fiFormMedia = "application/x-www-form-urlencoded"

// fzSameViolations reports whether two problems name the same violations:
// the same list, or, where either omitted some (its paths, of another
// length, may reach 16 KiB at another count), the same count found and the
// same violations as far as both list.
func fzSameViolations(a, b []string, omittedA, omittedB int) bool {
	if omittedA == 0 && omittedB == 0 {
		return slices.Equal(a, b)
	}
	n := min(len(a), len(b))
	return len(a)+omittedA == len(b)+omittedB && slices.Equal(a[:n], b[:n])
}

// fzListed is p's violations as "path: message", prefix cut from each path.
func fzListed(p *geta.Problem, prefix string) []string {
	out := make([]string, len(p.Errors))
	for i, v := range p.Errors {
		out[i] = strings.TrimPrefix(v.Path, prefix) + ": " + v.Message
	}
	return out
}

// fzCutPath is path as a problem lists it: past 256 bytes, "…" and its end
// from a rune boundary, 256 bytes in all.
func fzCutPath(path string) string {
	if len(path) <= 256 {
		return path
	}
	i := len(path) - (256 - len("…"))
	for i < len(path) && !utf8.RuneStart(path[i]) {
		i++
	}
	return "…" + path[i:]
}

// FuzzFormBody sends an arbitrary form body, with a declared length or
// none, to an input whose form holds every kind of field. A body past
// MaxBodyBytes is a 413, an empty one a missing body, one net/url cannot
// parse one violation at $; any other is a 200 or a 400 whose violations
// are at the fields a check apart from geta finds invalid and at each name
// the form does not hold. What a 200 binds, sent again as the form it is
// written as, binds the same values. And a form field binds as a query
// parameter does: the same text sent as the query of the same fields binds
// the same values or is refused with the same violations.
func FuzzFormBody(f *testing.F) {
	for _, s := range fzQuerySeeds {
		f.Add(s, false)
	}
	f.Add("", false)
	f.Add("r=a+b&s=a%20", true)
	f.Add(strings.Repeat("r=a&", 130), true)
	f.Add(strings.Repeat("r=a&", 130), false)
	limits := geta.DefaultLimits
	limits.MaxBodyBytes = 512
	var form *fiFormIn
	var query *fzQueryIn
	hf := func(_ context.Context, in *fiFormIn) (*ok, error) { form = in; return &ok{true}, nil }
	hq := func(_ context.Context, in *fzQueryIn) (*ok, error) { query = in; return &ok{true}, nil }
	a, err := geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/f", Route: geta.Route{Post: geta.Op(http.StatusOK, hf, geta.Doc{})}},
		{Path: "/q", Route: get(hq)},
	}}, geta.WithLimits(limits))
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, body string, unknown bool) {
		form, query = nil, nil
		p := fzRefusal(t, fzSend(a, http.MethodPost, "/f", "", fiFormMedia, []byte(body), unknown),
			http.StatusBadRequest, http.StatusRequestEntityTooLarge)
		switch {
		case int64(len(body)) > limits.MaxBodyBytes:
			if p == nil || p.Status != http.StatusRequestEntityTooLarge {
				t.Fatalf("%d bytes past the limit: %+v", len(body), p)
			}
			return
		case p != nil && p.Status == http.StatusRequestEntityTooLarge:
			t.Fatalf("%d bytes within the limit: 413", len(body))
		case body == "":
			if p == nil || !slices.Equal(fzListed(p, ""), []string{"$: missing required request body"}) {
				t.Fatalf("no body: %+v", p)
			}
			return
		}
		vals, perr := url.ParseQuery(body)
		if perr != nil {
			if p == nil || len(p.Errors) != 1 || p.Errors[0].Path != "$" || p.Errors[0].Message != "the form body is malformed: "+perr.Error() {
				t.Fatalf("%q, which net/url refuses (%v): %+v", body, perr, p)
			}
			return
		}
		var unknownNames []string
		for k := range vals {
			if !slices.Contains(fzNames, k) {
				unknownNames = append(unknownNames, k)
			}
		}
		if p == nil {
			if len(unknownNames) > 0 {
				t.Fatalf("%q: names %q taken, which the form does not hold", body, unknownNames)
			}
			fzAgrees(t, vals, nil, nil)
			again := fzValues(FzQuery(form.Body), nil).Encode()
			if int64(len(again)) <= limits.MaxBodyBytes {
				if p := fzRefusal(t, fzSend(a, http.MethodPost, "/f", "", fiFormMedia, []byte(again), false)); p != nil {
					t.Fatalf("%q bound values that, sent as %q, are refused: %+v", body, again, p)
				}
				if back := fzValues(FzQuery(form.Body), nil).Encode(); back != again {
					t.Fatalf("%q: %q binds %q", body, again, back)
				}
			}
		} else {
			listed := fzListed(p, "")
			for _, k := range unknownNames {
				if want := fzCutPath("$."+strings.ToValidUTF8(k, "�")) + ": unknown field"; !slices.Contains(listed, want) && p.Omitted == 0 {
					t.Fatalf("%q: no %q in %q", body, want, listed)
				}
			}
			fzAgrees(t, vals, fzRefused(t, p, "body", "$."), p)
		}
		if len(unknownNames) > 0 {
			return
		}
		q := fzRefusal(t, fzSend(a, http.MethodGet, "/q", body, "", nil, false), http.StatusBadRequest)
		switch {
		case (p == nil) != (q == nil):
			t.Fatalf("%q: as a form %+v, as a query %+v", body, p, q)
		case p == nil:
			if fv, qv := fzValues(FzQuery(form.Body), nil).Encode(), fzValues(query.FzQuery, nil).Encode(); fv != qv {
				t.Fatalf("%q: as a form %q, as a query %q", body, fv, qv)
			}
		default:
			// A form's field and a query's parameter are each missing by its
			// own name.
			fv, qv := fzListed(p, "$."), fzListed(q, "")
			for i := range fv {
				fv[i] = strings.Replace(fv[i], ": missing required field", ": missing required parameter", 1)
			}
			if !fzSameViolations(fv, qv, p.Omitted, q.Omitted) {
				t.Fatalf("%q: as a form %q, as a query %q", body, fv, qv)
			}
		}
	})
}

// FuzzDeepObjectAgreesWithForm sends arbitrary members as a deepObject's
// query keys (filter[m]) and as a form body of the same struct: a deepObject's
// members are bound as a form's fields are, so both bind the same values, or
// both are refused with the same violations, at filter[m] and at $.m.
func FuzzDeepObjectAgreesWithForm(f *testing.F) {
	for _, s := range []string{"min=1&since=2026-05-01", "status=closed", "min=-1&q=abcdef", "bogus=1", "min=1&min=2",
		"status=gone", "min][x=1", "=1", "since=2026-02-30", "min=007", "q=%ff", "%ff=1", "status=", "min=1e2", "q=+",
		"since=2026-02-28&status=open&q=abcde&min=0", "]=1", "[=1&[]=2"} {
		f.Add(s)
	}
	var deep, form *fzFilter
	hd := func(_ context.Context, in *fzDeepIn) (*ok, error) { deep = in.Filter; return &ok{true}, nil }
	hf := func(_ context.Context, in *fzFilterFormIn) (*ok, error) { form = in.Body; return &ok{true}, nil }
	a, err := geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/d", Route: get(hd)},
		{Path: "/f", Route: geta.Route{Post: geta.Op(http.StatusOK, hf, geta.Doc{})}},
	}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		vals, err := url.ParseQuery(raw)
		if err != nil || len(vals) == 0 {
			return
		}
		q := url.Values{}
		for k, vs := range vals {
			q["filter["+k+"]"] = vs
		}
		deep, form = nil, nil
		dp := fzRefusal(t, fzSend(a, http.MethodGet, "/d", q.Encode(), "", nil, false), http.StatusBadRequest)
		fp := fzRefusal(t, fzSend(a, http.MethodPost, "/f", "", fiFormMedia, []byte(raw), false), http.StatusBadRequest)
		switch {
		case (dp == nil) != (fp == nil):
			t.Fatalf("%q: as a deepObject %+v, as a form %+v", raw, dp, fp)
		case dp == nil:
			if deep == nil || form == nil {
				t.Fatalf("%q: bound %v and %v", raw, deep, form)
			}
			if dv, fv := fzFilterValues(*deep).Encode(), fzFilterValues(*form).Encode(); dv != fv {
				t.Fatalf("%q: as a deepObject %q, as a form %q", raw, dv, fv)
			}
			return
		}
		dl := fzListed(dp, "filter[")
		for i, v := range dp.Errors {
			if v.In != "query" || !strings.HasPrefix(v.Path, "filter[") {
				t.Fatalf("%q: %+v", raw, dp.Errors)
			}
			// filter[m]: message, as $.m: message.
			path, _ := strings.CutSuffix(strings.TrimPrefix(v.Path, "filter["), "]")
			dl[i] = path + ": " + v.Message
		}
		fl := fzListed(fp, "$.")
		for _, v := range fp.Errors {
			if v.In != "body" {
				t.Fatalf("%q: %+v", raw, fp.Errors)
			}
		}
		if !fzSameViolations(dl, fl, dp.Omitted, fp.Omitted) {
			t.Fatalf("%q: as a deepObject %q, as a form %q", raw, dl, fl)
		}
	})
}

type fzUpload struct {
	Title  string       `form:"title" schema:"maxLength=16"`
	Count  *int8        `form:"count"`
	Tags   *[]string    `form:"tag" schema:"maxItems=3"`
	Avatar geta.File    `form:"avatar"`
	Extra  *[]geta.File `form:"extra" schema:"maxItems=2"`
}

type fzUploadIn struct {
	Body fzUpload `body:"multipart"`
}

// fzFile is what a handler read of a file it received.
type fzFile struct {
	name, ctype string
	size        int64
	data        []byte
}

func fzRead(f geta.File) fzFile {
	got := fzFile{name: f.Filename(), ctype: f.ContentType(), size: f.Size()}
	rc, err := f.Open()
	if err != nil {
		got.name = "open failed: " + err.Error()
		return got
	}
	defer rc.Close()
	if got.data, err = io.ReadAll(rc); err != nil {
		got.name = "read failed: " + err.Error()
	}
	return got
}

// fzReceived is what a handler received of a multipart body.
type fzReceived struct {
	title  string
	count  *int8
	tags   []string
	avatar fzFile
	extra  []fzFile
}

const (
	fzUploadMax    = 2048 // the body limit of fzUploadApp
	fzUploadMemory = 16   // the bytes of files it holds in memory
)

// fzUploadApp takes fzUpload under a body limit of fzUploadMax bytes, its
// files past fzUploadMemory bytes held in temporary files; its handler
// stores in *got what it received, its files read while it runs.
func fzUploadApp(f *testing.F, got **fzReceived) *geta.App {
	limits := geta.DefaultLimits
	limits.MaxBodyBytes = fzUploadMax
	limits.MaxMultipartMemory = fzUploadMemory
	h := func(_ context.Context, in *fzUploadIn) (*ok, error) {
		r := &fzReceived{title: in.Body.Title, count: in.Body.Count, avatar: fzRead(in.Body.Avatar)}
		if in.Body.Tags != nil {
			r.tags = *in.Body.Tags
		}
		if in.Body.Extra != nil {
			for _, x := range *in.Body.Extra {
				r.extra = append(r.extra, fzRead(x))
			}
		}
		*got = r
		return &ok{true}, nil
	}
	a, err := geta.New(one("/m", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}), geta.WithLimits(limits))
	if err != nil {
		f.Fatal(err)
	}
	return a
}

// fzTempDir has temporary files made in a directory of the fuzz target's
// own and returns a check that the directory is empty, as a request leaves
// it once answered.
func fzTempDir(f *testing.F) func(t *testing.T) {
	dir := f.TempDir()
	f.Setenv("TMPDIR", dir)
	return func(t *testing.T) {
		t.Helper()
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("%d temporary files left behind", len(entries))
		}
	}
}

// fzTaken checks what a handler took of a multipart body against fzUpload's
// constraints, apart from geta: the title's length in code points, the
// count of tags and of extra files, text that is UTF-8, and each file's
// size as the length of what it reads.
func fzTaken(t *testing.T, r *fzReceived) {
	t.Helper()
	if r == nil {
		t.Fatal("a 200 with no handler run")
	}
	if !utf8.ValidString(r.title) || utf8.RuneCountInString(r.title) > 16 {
		t.Fatalf("title %q taken", r.title)
	}
	if len(r.tags) > 3 {
		t.Fatalf("%d tags taken", len(r.tags))
	}
	for _, s := range r.tags {
		if !utf8.ValidString(s) {
			t.Fatalf("tag %q taken", s)
		}
	}
	if len(r.extra) > 2 {
		t.Fatalf("%d extra files taken", len(r.extra))
	}
	for _, f := range append([]fzFile{r.avatar}, r.extra...) {
		if int64(len(f.data)) != f.size {
			t.Fatalf("file %q: size %d, read %d bytes", f.name, f.size, len(f.data))
		}
	}
}

// FuzzMultipartBody sends arbitrary bytes under an arbitrary Content-Type,
// with a declared length or none, to an input of files and text fields. It
// is a 200, or a 400, 413, or 415 problem; a declared length past the limit
// is never taken; what a 200 hands the handler holds to the constraints;
// and no temporary file is left once the request is answered.
func FuzzMultipartBody(f *testing.F) {
	const ct = "multipart/form-data; boundary=b"
	part := func(disposition, ctype, content string) string {
		s := "--b\r\nContent-Disposition: " + disposition + "\r\n"
		if ctype != "" {
			s += "Content-Type: " + ctype + "\r\n"
		}
		return s + "\r\n" + content + "\r\n"
	}
	title := part(`form-data; name="title"`, "", "hi")
	avatar := part(`form-data; name="avatar"; filename="me.png"`, "image/png", "PNG data past sixteen bytes")
	whole := title + avatar + "--b--\r\n"
	for _, s := range []struct {
		ctype, body string
		unknown     bool
	}{
		{ct, whole, false},
		{ct, whole, true},
		{ct, whole[:len(whole)/2], true},
		{ct, whole + strings.Repeat("epilogue", 300), true},
		{ct, title + part(`form-data; name="avatar"`, "", "") + part(`form-data; name="extra"; filename="../x"`, "", "1") + "--b--", false},
		{ct, part("attachment", "", "x") + "--b--\r\n", false},
		{ct, title + title + avatar + part(`form-data; name="zzz"`, "", "z") + "--b--\r\n", false},
		{ct, part(`form-data; name="title"`, "", "\xff") + avatar + "--b--\r\n", false},
		{ct, "--b\r\nContent-Disposition: form-data; name=\"title\"\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\na=3Db=\r\n\r\n" + avatar + "--b--\r\n", false},
		{ct, part(`form-data; name="count"`, "", "007") + part(`form-data; name="tag"`, "", "a") + title + avatar + "--b--\r\n", false},
		{ct, "not multipart", false},
		{ct, "", false},
		{"multipart/form-data", whole, false},
		{`multipart/form-data; boundary="b"; charset=utf-8`, whole, false},
		{"multipart/form-data; boundary=", whole, false},
		{"text/plain", whole, false},
		{"", whole, true},
		{ct, "--b\r\nContent-Disposition: form-data; name*=utf-8''%74itle\r\n\r\nx\r\n" + avatar + "--b--", false},
	} {
		f.Add(s.ctype, []byte(s.body), s.unknown)
	}
	check := fzTempDir(f)
	var got *fzReceived
	a := fzUploadApp(f, &got)
	f.Fuzz(func(t *testing.T, ctype string, body []byte, unknown bool) {
		got = nil
		p := fzRefusal(t, fzSend(a, http.MethodPost, "/m", "", ctype, body, unknown),
			http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType)
		check(t)
		if !unknown && len(body) > fzUploadMax && (p == nil || p.Status == http.StatusBadRequest) {
			t.Fatalf("%d bytes declared past the limit: %+v", len(body), p)
		}
		if p == nil {
			fzTaken(t, got)
			// Taken whole: its close delimiter went by (RFC 2046 section 5.1.1).
			if _, params, err := mime.ParseMediaType(ctype); err != nil || !bytes.Contains(body, []byte("--"+params["boundary"]+"--")) {
				t.Fatalf("taken without a close delimiter: %q %q", ctype, body)
			}
		} else if got != nil {
			t.Fatalf("%d, after the handler ran", p.Status)
		}
	})
}

// fzHeaderText is s as a part's header can carry it: printable ASCII and
// spaces, with none at either end, as textproto reads a header back.
func fzHeaderText(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < ' ' || r > '~' {
			return -1
		}
		return r
	}, s))
}

// Bits of FuzzMultipartParts' flags.
const (
	fzNoTitle      = 1 << iota // leave the title out
	fzTwoAvatars               // send avatar twice
	fzUnknownPart              // send a part no field names
	fzNamelessPart             // send a part with no form-data name
	fzNoCount                  // leave the count out
	fzUnknownLen               // send the body with no declared length
	fzTruncate                 // cut the body at cut
	fzNoAvatar                 // leave the avatar out
)

// FuzzMultipartParts builds a multipart body of arbitrary parts: a title, a
// count, tags, an avatar with an arbitrary filename, Content-Type, and
// content, extra files growing past MaxMultipartMemory and past
// MaxBodyBytes, a part no field names, a part with no name, a missing
// field, a second file, and a body cut short. A body past the limit is a
// 413; a whole body is a 200 exactly when its parts meet the constraints,
// judged apart from geta, and the handler then receives each value and
// file as sent: a file's filename without its directory,
// its Content-Type, and its content; a body cut short is never taken
// without its close delimiter; and no temporary file is left once the
// request is answered.
func FuzzMultipartParts(f *testing.F) {
	f.Add("hi", "7", "t", uint8(2), []byte("PNG"), "me.png", "image/png", uint8(1), uint16(20), uint8(0), uint16(0))
	f.Add("héllo wörld", "-128", "é", uint8(3), []byte("long avatar data past memory"), "dir/a b\".png", " text/plain ", uint8(2), uint16(0), uint8(fzUnknownLen), uint16(0))
	f.Add("", "", "", uint8(0), []byte(nil), "", "", uint8(0), uint16(0), uint8(fzNoCount), uint16(0))
	f.Add("seventeen chars!!", "128", "\xff", uint8(4), []byte("x"), "名前.txt", "", uint8(3), uint16(2500), uint8(0), uint16(0))
	f.Add("t", "007", "a", uint8(1), []byte("--fzB0undary"), "\x00\r\n", "a\r\nb", uint8(0), uint16(0), uint8(fzUnknownPart|fzNamelessPart), uint16(0))
	f.Add("t", "1", "a", uint8(1), []byte("data"), "a", "b", uint8(2), uint16(2100), uint8(fzUnknownLen), uint16(0))
	f.Add("t", "1", "a", uint8(1), []byte("data"), "a", "b", uint8(1), uint16(100), uint8(fzTruncate), uint16(300))
	f.Add("t", "1", "a", uint8(1), []byte("data"), "a", "b", uint8(1), uint16(100), uint8(fzTwoAvatars|fzNoTitle), uint16(0))
	f.Add("t", "1", "a", uint8(1), []byte("data"), "/", "b", uint8(1), uint16(100), uint8(fzNoAvatar), uint16(0))
	check := fzTempDir(f)
	var got *fzReceived
	a := fzUploadApp(f, &got)
	f.Fuzz(func(t *testing.T, title, count, tag string, ntags uint8, avatar []byte, fname, ctype string, nextra uint8, grow uint16, flags uint8, cut uint16) {
		const boundary = "fzB0undary"
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		if err := w.SetBoundary(boundary); err != nil {
			t.Fatal(err)
		}
		write := func(h textproto.MIMEHeader, content []byte) {
			pw, err := w.CreatePart(h)
			if err != nil {
				t.Fatal(err)
			}
			pw.Write(content)
		}
		field := func(name, value string) {
			write(textproto.MIMEHeader{"Content-Disposition": {mime.FormatMediaType("form-data", map[string]string{"name": name})}}, []byte(value))
		}
		ctype = fzHeaderText(ctype)
		file := func(name string, content []byte) {
			params := map[string]string{"name": name}
			if fname != "" {
				params["filename"] = fname
			}
			h := textproto.MIMEHeader{"Content-Disposition": {mime.FormatMediaType("form-data", params)}}
			if ctype != "" {
				h.Set("Content-Type", ctype)
			}
			write(h, content)
		}
		if flags&fzNoTitle == 0 {
			field("title", title)
		}
		if flags&fzNoCount == 0 {
			field("count", count)
		}
		ntags %= 5
		for range ntags {
			field("tag", tag)
		}
		avatars := 1
		switch {
		case flags&fzNoAvatar != 0:
			avatars = 0
		case flags&fzTwoAvatars != 0:
			avatars = 2
		}
		for range avatars {
			file("avatar", avatar)
		}
		nextra %= 4
		extras := make([][]byte, nextra)
		for i := range extras {
			extras[i] = avatar
			if i == 0 {
				extras[i] = bytes.Repeat([]byte("x"), int(grow%3000))
			}
			file("extra", extras[i])
		}
		if flags&fzUnknownPart != 0 {
			field("zzz", "z")
		}
		if flags&fzNamelessPart != 0 {
			write(textproto.MIMEHeader{"Content-Disposition": {"attachment"}}, []byte("x"))
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		body := buf.Bytes()
		truncated := flags&fzTruncate != 0 && int(cut) < len(body)
		if truncated {
			body = body[:cut]
		}

		got = nil
		p := fzRefusal(t, fzSend(a, http.MethodPost, "/m", "", w.FormDataContentType(), body, flags&fzUnknownLen != 0),
			http.StatusBadRequest, http.StatusRequestEntityTooLarge)
		check(t)
		if p == nil {
			fzTaken(t, got)
		}
		delimiter := []byte("--" + boundary)
		switch {
		case truncated:
			if p == nil && !bytes.Contains(body, []byte("--"+boundary+"--")) {
				t.Fatalf("a body cut at %d of %d bytes, before its close delimiter, taken: %q received %+v", cut, buf.Len(), body, got)
			}
			return
		case len(body) > fzUploadMax:
			if p == nil || p.Status != http.StatusRequestEntityTooLarge {
				t.Fatalf("%d bytes past the limit: %+v", len(body), p)
			}
			return
		case p != nil && p.Status == http.StatusRequestEntityTooLarge:
			t.Fatalf("%d bytes within the limit: 413", len(body))
		case bytes.Contains(avatar, delimiter) || strings.Contains(title+count+tag, "--"+boundary):
			return // content that holds the delimiter is another body
		}
		n, err := strconv.ParseInt(count, 10, 8)
		valid := flags&fzNoTitle == 0 && utf8.ValidString(title) && utf8.RuneCountInString(title) <= 16 &&
			(flags&fzNoCount != 0 || fzJSONInt.MatchString(count) && err == nil) &&
			ntags <= 3 && (ntags == 0 || utf8.ValidString(tag)) &&
			avatars == 1 && nextra <= 2 && flags&(fzUnknownPart|fzNamelessPart) == 0
		switch {
		case !valid && p == nil:
			t.Fatalf("taken, which the constraints refuse: %+v", got)
		case !valid:
			return
		case p != nil:
			t.Fatalf("refused, which the constraints take: %+v", p.Errors)
		}
		name := ""
		if fname != "" {
			name = filepath.Base(fname)
		}
		sent := func(content []byte) fzFile {
			return fzFile{name: name, ctype: ctype, size: int64(len(content)), data: content}
		}
		same := func(a, b fzFile) bool {
			return a.name == b.name && a.ctype == b.ctype && a.size == b.size && bytes.Equal(a.data, b.data)
		}
		if got.title != title || (got.count == nil) != (flags&fzNoCount != 0) || got.count != nil && int64(*got.count) != n {
			t.Fatalf("title %q count %v received, %q %q sent", got.title, got.count, title, count)
		}
		if len(got.tags) != int(ntags) || ntags > 0 && slices.ContainsFunc(got.tags, func(s string) bool { return s != tag }) {
			t.Fatalf("tags %q received, %d of %q sent", got.tags, ntags, tag)
		}
		if !same(got.avatar, sent(avatar)) {
			t.Fatalf("avatar %+v received, %+v sent", got.avatar, sent(avatar))
		}
		if len(got.extra) != len(extras) {
			t.Fatalf("%d extra files received, %d sent", len(got.extra), len(extras))
		}
		for i, x := range extras {
			if !same(got.extra[i], sent(x)) {
				t.Fatalf("extra %d: %+v received, %+v sent", i, got.extra[i], sent(x))
			}
		}
	})
}

// fiHeaderIn binds, as request header parameters, the types and constraints
// fzHeaderTypes declares of a middleware's response headers.
type fiHeaderIn struct {
	A *int       `header:"X-A" schema:"minimum=1,maximum=3600"`
	B *uint32    `header:"X-B"`
	C *bool      `header:"X-C"`
	D *time.Time `header:"X-D"`
	E *string    `header:"X-E" schema:"enum=burst|steady"`
	P *string    `header:"X-P" schema:"pattern=^[a-z0-9]{1,8}$"`
}

var fzHeaderTypes = []struct {
	name string
	typ  geta.HeaderType
}{
	{"X-A", geta.HeaderOf[int]("minimum=1,maximum=3600")},
	{"X-B", geta.HeaderOf[uint32]("")},
	{"X-C", geta.HeaderOf[bool]("")},
	{"X-D", geta.HeaderOf[time.Time]("")},
	{"X-E", geta.HeaderOf[string]("enum=burst|steady")},
	{"X-P", geta.HeaderOf[string]("pattern=^[a-z0-9]{1,8}$")},
}

// FuzzConformsHeaderAgreesWithRequest holds an arbitrary header value to a
// middleware's HeaderOf declarations of an int, a uint32, a bool, a
// time.Time, a string enum, and a string pattern in App.Conforms, which reads
// it as a request's header parameter of the same type and constraints is
// read: Conforms takes the value exactly when a request carrying it in that
// header parameter is bound (a 200), and refuses it exactly when the
// request is a 400 naming that header. Values are kept below the request's
// MaxStringLength backstop, which a response's schema does not state.
func FuzzConformsHeaderAgreesWithRequest(f *testing.F) {
	for _, s := range []string{"1", "0", "3600", "3601", "-1", "007", "+7", "1.5", "1e2", "soon", "", " 1", "1 ", "1\t",
		"4294967295", "4294967296", "true", "false", "TRUE", "2026-10-04T00:00:00Z", "2026-10-04t00:00:00z",
		"1998-12-31T23:59:60Z", "2026-02-30T00:00:00Z", "2026-10-04T00:00:00+24:00", "2026-10-04T00:00:00,5Z",
		"burst", "steady", "wild", "abc", "abcdefghi", "a\x00", "a\r\nb", "\xff", "é", "a b", "x\t"} {
		f.Add(s)
	}
	m := geta.Use(noop).Answers(http.StatusTooManyRequests, "Limited")
	for _, h := range fzHeaderTypes {
		m = m.Header(http.StatusTooManyRequests, h.name, "", h.typ)
	}
	h := func(context.Context, *fiHeaderIn) (*ok, error) { return &ok{true}, nil }
	a, err := geta.New(withRoot(one("/h", get(h)), m))
	if err != nil {
		f.Fatal(err)
	}
	match := geta.Match{Template: "/h", Method: http.MethodGet, Operation: true}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 4000 {
			return
		}
		for _, h := range fzHeaderTypes {
			r := httptest.NewRequest(http.MethodGet, "/h", nil)
			r.Header[h.name] = []string{s}
			rec := httptest.NewRecorder()
			a.ServeHTTP(rec, r)
			p := fzRefusal(t, rec, http.StatusBadRequest)
			if p != nil && (len(p.Errors) == 0 || p.Errors[0].In != "header" || p.Errors[0].Path != h.name) {
				t.Fatalf("%s: %q: %+v", h.name, s, p)
			}
			cerr := a.Conforms(match, http.StatusTooManyRequests, http.Header{h.name: {s}}, nil)
			if (p == nil) != (cerr == nil) {
				t.Fatalf("%s: %q: as a request %+v, as a response %v", h.name, s, p, cerr)
			}
		}
	})
}
