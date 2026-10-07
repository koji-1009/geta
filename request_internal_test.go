package geta

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// A body plan that does not read in one pass (fast false) binds on the
// reference path, which fills defaults (applyDefaults) as the single pass
// does: both accept the same bodies, build the same value, optional body
// included, and name the same violations.
func TestTheReferencePathBindsAsTheSinglePass(t *testing.T) {
	type in struct {
		Body *fastBody `body:"json"`
	}
	p, err := newRegistry().inPlan(reflect.TypeFor[in]())
	if err != nil {
		t.Fatal(err)
	}
	bodies := append([]string(nil), fastSeeds...)
	for s := range keywordSeeds {
		bodies = append(bodies, s)
	}
	accepted := 0
	for _, s := range bodies {
		var got [2]in
		var errs [2]string
		for i, fast := range []bool{true, false} {
			p.body.fast = fast
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(s))
			r.Header.Set("Content-Type", "application/json")
			if be := p.body.bind(httptest.NewRecorder(), r, reflect.ValueOf(&got[i]).Elem().FieldByIndex(p.body.index), DefaultLimits, nil); be != nil {
				errs[i] = "refused"
				for _, v := range be.errs {
					errs[i] += " " + v.Path + ": " + v.Message
				}
			}
		}
		if errs[0] != errs[1] || !reflect.DeepEqual(got[0], got[1]) {
			t.Fatalf("%s:\nsingle pass %q %#v\nreference   %q %#v", s, errs[0], got[0].Body, errs[1], got[1].Body)
		}
		if errs[0] == "" && s != "" {
			accepted++
		}
	}
	if accepted < 20 {
		t.Fatalf("only %d bodies accepted", accepted)
	}
	// The defaults are the values bound, in a slice's elements, a map's
	// values under a named key, a Nullable's value, and behind a pointer.
	var dst in
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
		`{"leaf":{"s":"abc","ds":[{}],"dk":{"x":{}},"dnl":{},"dp":{},"dnn":[null,{}]},"list":[{"s":"a"}]}`))
	r.Header.Set("Content-Type", "application/json")
	if be := p.body.bind(httptest.NewRecorder(), r, reflect.ValueOf(&dst).Elem().FieldByIndex(p.body.index), DefaultLimits, nil); be != nil {
		t.Fatal(be.errs)
	}
	l := dst.Body.Leaf
	nl, _ := l.Dnl.Get()
	nn, _ := (*l.Dnn)[1].Get()
	if l.Dn != 7 || l.Dlv != 1 || (*l.Ds)[0].A != "zz" || (*l.Dk)["x"].A != "zz" || nl.A != "zz" || l.Dp.A != "zz" || !(*l.Dnn)[0].IsNull() || nn.A != "zz" {
		t.Fatalf("%+v", l)
	}
}

// An integer parameter's text is a JSON integer: no leading zero, no plus.
func TestIsJSONInteger(t *testing.T) {
	for s, want := range map[string]bool{"0": true, "-0": true, "7": true, "-7": true, "10": true, "9007199254740993": true,
		"": false, "-": false, "007": false, "-07": false, "00": false, "+7": false, "1.0": false, "1e2": false, " 1": false} {
		if isJSONInteger(s) != want {
			t.Errorf("%q: want %v", s, want)
		}
	}
}

// A header list's elements: comma-separated, trimmed, empty ones dropped;
// its carrier refuses what no element is.
func TestHeaderElements(t *testing.T) {
	if got := headerElements([]string{" a ,b", "", ",\t,c,"}); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatal(got)
	}
	if got := headerElements([]string{",", ""}); got != nil {
		t.Fatal(got)
	}
	for s, want := range map[string]bool{"a": true, "a b": true, "é": true, "": false, "a,b": false, " a": false, "a\t": false, "a\x01": false, "a\x7f": false} {
		if headerElementCarries(s) != want {
			t.Errorf("%q: want %v", s, want)
		}
	}
	// It passes exactly the strings its documented pattern matches.
	re := regexp.MustCompile(headerElement.pattern)
	for _, s := range carryStrings() {
		if got, want := headerElement.holds(s), re.MatchString(s); got != want {
			t.Errorf("%q: holds %v, the pattern %v", s, got, want)
		}
	}
}

// A temporary file geta made but cannot write (storeWriter) is the
// server's failure, not the request's: a 500 with no violations, the handler
// not run, and every temporary file of the request removed, the ones written
// before it included.
func TestAFileGetaCannotWriteIsA500(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	made := 0
	createTemp = func(dir, pattern string) (*os.File, error) {
		f, err := os.CreateTemp(dir, pattern)
		if err != nil {
			return nil, err
		}
		made++
		if made == 2 {
			f.Close() // every write to it fails
		}
		return f, nil
	}
	t.Cleanup(func() { createTemp = os.CreateTemp })
	type body struct {
		Files []File `form:"files"`
	}
	type in struct {
		Body body `body:"multipart"`
	}
	type out struct {
		N int `json:"n"`
	}
	ran := false
	lim := DefaultLimits
	lim.MaxMultipartMemory = 0
	app, err := New(Table{Routes: []Entry{{Path: "/m", Route: Route{Post: Op(http.StatusOK, func(context.Context, *in) (*out, error) {
		ran = true
		return &out{}, nil
	}, Doc{})}}}}, WithLimits(lim), WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for _, name := range []string{"a", "b", "c"} {
		w, _ := mw.CreateFormFile("files", name)
		w.Write([]byte("content"))
	}
	mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/m", &b)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, r)
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || rec.Code != 500 || len(p.Errors) != 0 || ran || made != 2 {
		t.Fatalf("%d %s ran %v made %d", rec.Code, rec.Body, ran, made)
	}
	if left, err := os.ReadDir(dir); err != nil || len(left) != 0 {
		t.Fatalf("%v left %v", err, left)
	}
}

// writtenStringLen returns the bytes of s as sendProblem writes it in a
// string, quotes aside.
func writtenStringLen(t testing.TB, s string) int {
	b, err := jsonv2.Marshal(s, problemOptions)
	if err != nil {
		t.Fatal(err)
	}
	return len(b) - len(`""`)
}

// jsonStringLen is the length sendProblem writes, exactly: every byte, every
// rune (U+2028 and U+2029 included), invalid UTF-8 of each kind (a
// surrogate, an overlong form, a sequence cut short), and mixes of them.
func TestJSONStringLenIsTheWrittenLength(t *testing.T) {
	var cases []string
	for c := range 256 {
		cases = append(cases, string([]byte{byte(c)}), "a"+string([]byte{byte(c)})+"é<")
	}
	for r := rune(0); r <= utf8.MaxRune; r++ {
		if utf8.ValidRune(r) {
			cases = append(cases, string(r))
		}
	}
	cases = append(cases, "\xed\xa0\x80", "\xc0\x80", "\xe2\x80", "\xf0\x9f\x98", "\xff\xfe",
		"a\b\f\n\r\t\"\\/<>&\x00\x1f\x7f"+string(rune(0x2028))+string(rune(0x2029))+"\xe2\x80"+string(utf8.RuneError))
	for _, s := range cases {
		if got, want := jsonStringLen(s), writtenStringLen(t, s); got != want {
			t.Fatalf("%q: %d, written %d", s, got, want)
		}
	}
}

func FuzzJSONStringLen(f *testing.F) {
	for _, s := range []string{"", "a", "<&>", "\n\t\x01", "\xe2\x80\xa8\xe2\x80\xa9", "\xed\xa0\x80\xff", "é\"\\"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if got, want := jsonStringLen(s), writtenStringLen(t, s); got != want {
			t.Fatalf("%q: %d, written %d", s, got, want)
		}
	})
}

// The listed violations' errors array is at most 16 KiB, exactly: a list
// that writes 16384 bytes is whole, and one that writes 16385 loses its
// last violation to omitted.
func TestListViolationsBoundsTheWrittenArray(t *testing.T) {
	// Each message mixes the bytes JSON writes at two, three, and six bytes.
	unit := "\n" + string(rune(0x2028)) + "<"
	for _, extra := range []int{0, 1} {
		errs := []Violation{
			{In: "body", Path: "$.a", Message: strings.Repeat(unit, 700)},
			{In: "body", Path: "$.b", Message: strings.Repeat(unit, 700)},
		}
		written := func(errs []Violation) int {
			b, err := jsonv2.Marshal(errs, problemOptions)
			if err != nil {
				t.Fatal(err)
			}
			return len(b)
		}
		// Pad the second message to 16 KiB in all, and extra past it.
		errs[1].Message += strings.Repeat("a", 16<<10-written(errs)+extra)
		if n := written(errs); n != 16<<10+extra {
			t.Fatal(n)
		}
		listed, omitted := listViolations(slices.Clone(errs), 0)
		if want := 2 - extra; len(listed) != want || omitted != extra {
			t.Fatalf("%d bytes: %d listed, %d omitted", written(errs), len(listed), omitted)
		}
	}
}

// A problem lists maxViolations violations whole, and from one more on counts
// the rest as omitted.
func TestListViolationsBoundsTheCount(t *testing.T) {
	for n, want := range map[int]int{maxViolations: 0, maxViolations + 1: 1} {
		errs := make([]Violation, n)
		for i := range errs {
			errs[i] = Violation{In: "query", Path: "q", Message: "m"}
		}
		listed, omitted := listViolations(errs, 0)
		if len(listed) != maxViolations || omitted != want {
			t.Errorf("%d violations: %d listed, %d omitted", n, len(listed), omitted)
		}
	}
}

// A request's value of maxValueBytes is quoted whole, and one byte more is
// cut; a response's value is whole whatever its length.
func TestARequestsValueIsQuotedUpToTheBound(t *testing.T) {
	at := strings.Repeat("a", maxValueBytes)
	if got := (&decoder{}).value(at); got != at {
		t.Errorf("%d bytes: %q", len(at), got)
	}
	if got := (&decoder{}).value(at + "b"); got != at+pathMark {
		t.Errorf("%d bytes: %q", len(at)+1, got)
	}
	if got := (&decoder{written: true}).value(at + "b"); got != at+"b" {
		t.Errorf("a response's: %q", got)
	}
}

// longErr is a type whose UnmarshalJSON fails with longErrMsg.
type longErr struct{}

var longErrMsg string

func (*longErr) UnmarshalJSON([]byte) error { return errors.New(longErrMsg) }

// A type's own decoding error is quoted whole up to maxErrorBytes, and cut
// one byte past it.
func TestADecodeErrorIsQuotedUpToTheBound(t *testing.T) {
	var v struct {
		X longErr `json:"x"`
	}
	data := []byte(`{"x":1}`)
	longErrMsg = ""
	base := len(jsonv2.Unmarshal(data, &v).Error())
	for _, n := range []int{maxErrorBytes, maxErrorBytes + 1} {
		longErrMsg = strings.Repeat("e", n-base)
		full := jsonv2.Unmarshal(data, &v).Error()
		if len(full) != n {
			t.Fatalf("the error is %d bytes, want %d", len(full), n)
		}
		want := full
		if n > maxErrorBytes {
			want = full[:maxErrorBytes] + pathMark
		}
		d := &decoder{}
		d.decodeJSON(data, reflect.ValueOf(&v).Elem(), jsonv2.JoinOptions())
		if len(d.errs) != 1 || d.errs[0].Message != want {
			t.Fatalf("%d bytes: %q", n, d.errs)
		}
	}
}

// refClip is clip's reference: the longest prefix of s, in n bytes or
// fewer, that splits no rune, a byte of invalid UTF-8 counting as one of its
// own, and "…"; s itself if it fits.
func refClip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := 0
	for i < len(s) {
		_, size := utf8.DecodeRuneInString(s[i:])
		if i+size > n {
			break
		}
		i += size
	}
	return s[:i] + "…"
}

// clip cuts a value as refClip does: at a rune boundary in valid UTF-8, and
// at n bytes through invalid UTF-8, which a header value may hold, rather
// than dropping it to the last rune start before it.
func TestClipSplitsNoRune(t *testing.T) {
	cases := map[string]string{
		strings.Repeat("a", 4):              "aaa…",
		"a" + strings.Repeat("é", 3):        "aé…",
		"aa" + strings.Repeat("é", 3):       "aa…",
		"a€b":                               "a…",
		strings.Repeat("\x80", 5):           "\x80\x80\x80…",
		"a\xe2\x80b":                        "a\xe2\x80…",
		"ab\xe2\x82\xac":                    "ab…",
		"abc":                               "abc",
		"\xed\xa0\x80x":                     "\xed\xa0\x80…",
		"ab" + string(utf8.RuneError) + "x": "ab…",
	}
	for s, want := range cases {
		if got := clip(s, 3); got != want || got != refClip(s, 3) {
			t.Errorf("clip(%q, 3) = %q, want %q (reference %q)", s, got, want, refClip(s, 3))
		}
	}
}

func FuzzClip(f *testing.F) {
	for _, s := range []string{"", "abc", "a€b", "\x80\x80\x80\x80", "\xed\xa0\x80x", "é\xc3"} {
		f.Add(s, uint8(3))
	}
	f.Fuzz(func(t *testing.T, s string, n uint8) {
		if got, want := clip(s, int(n)), refClip(s, int(n)); got != want {
			t.Fatalf("clip(%q, %d) = %q, want %q", s, n, got, want)
		}
	})
}
