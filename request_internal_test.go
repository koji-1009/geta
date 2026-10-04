package geta

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
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
			if be := p.body.bind(httptest.NewRecorder(), r, reflect.ValueOf(&got[i]).Elem().FieldByIndex(p.body.index), DefaultLimits); be != nil {
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
	if be := p.body.bind(httptest.NewRecorder(), r, reflect.ValueOf(&dst).Elem().FieldByIndex(p.body.index), DefaultLimits); be != nil {
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
