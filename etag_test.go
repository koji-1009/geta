package geta_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

func TestETag(t *testing.T) {
	a := accepts(t, withRoot(geta.Table{Routes: []geta.Entry{
		{Path: "/x", Route: geta.Route{
			Get:  geta.Op(http.StatusOK, textHandler("hi"), geta.Doc{}),
			Post: geta.Op(http.StatusOK, textHandler("hi"), geta.Doc{}),
			Put:  geta.Op(http.StatusCreated, textHandler("hi"), geta.Doc{}),
		}},
	}}, geta.ETag()))
	first := do(t, a, "GET", "/x")
	tag := first.Header().Get("ETag")
	if first.Code != 200 || !strings.HasPrefix(tag, `"`) {
		t.Fatal(first.Code, first.Header())
	}
	for _, inm := range []string{tag, "*", `"x", ` + tag + `, "y"`, "W/" + tag} {
		r := do(t, a, "GET", "/x", "If-None-Match", inm)
		if r.Code != 304 || r.Body.Len() != 0 || r.Header().Get("ETag") != tag || r.Header().Get("Content-Type") != "" {
			t.Errorf("%q: %d %v %q", inm, r.Code, r.Header(), r.Body)
		}
	}
	if r := do(t, a, "GET", "/x", "If-None-Match", `"nope"`); r.Code != 200 || r.Header().Get("ETag") != tag {
		t.Fatal(r.Code)
	}
	if r := do(t, a, "POST", "/x", "If-None-Match", tag); r.Code != 200 || r.Header().Get("ETag") != "" {
		t.Fatal("POST:", r.Code, r.Header())
	}
	if r := do(t, a, "PUT", "/x"); r.Code != 201 || r.Header().Get("ETag") != "" {
		t.Fatal("a non-200 was tagged")
	}
}

func TestETagKeepsTheHandlersTag(t *testing.T) {
	type tagged struct {
		ETag string `header:"ETag"`
		Body text   `body:"json"`
	}
	h := func(context.Context, *empty) (*tagged, error) { return &tagged{ETag: `"app"`, Body: text{"hi"}}, nil }
	a := accepts(t, withRoot(one("/x", get(h)), geta.ETag()))
	if r := do(t, a, "GET", "/x", "If-None-Match", `"app"`); r.Code != 304 || r.Header().Get("ETag") != `"app"` {
		t.Fatal(r.Code, r.Header())
	}
}

// ETag tags only GET and HEAD.
func TestETagSkipsPost(t *testing.T) {
	app, err := geta.New(geta.Table{
		Root:   geta.Scope{geta.ETag()},
		Routes: []geta.Entry{{Path: "/x", Route: geta.Route{Post: geta.Op(http.StatusOK, okHandler, geta.Doc{})}}},
	}, geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
	if rec.Code != 200 || rec.Header().Get("ETag") != "" {
		t.Fatalf("POST: %d ETag %q; want 200 without a tag", rec.Code, rec.Header().Get("ETag"))
	}
}

// An entity tag may hold a comma inside its quotes; If-None-Match is split
// only between tags. A field that is not "*" or a list of entity tags is a
// 400, as Conditional.Check answers it, carrying none of the handler's
// headers; its lines are read as one list.
func TestETagListWithCommas(t *testing.T) {
	type tagged struct {
		ETag string `header:"ETag"`
		Body text   `body:"json"`
	}
	h := func(context.Context, *empty) (*tagged, error) { return &tagged{ETag: `"a,b"`, Body: text{"hi"}}, nil }
	a := accepts(t, withRoot(one("/x", get(h)), geta.ETag()))
	for _, inm := range []string{`"a,b"`, `"x", "a,b"`, `W/"a,b"`, `"x","a,b" , "y"`, ` , ,"a,b"`, `*`} {
		if r := do(t, a, "GET", "/x", "If-None-Match", inm); r.Code != 304 || r.Header().Get("ETag") != `"a,b"` {
			t.Errorf("%q: %d %v; want 304", inm, r.Code, r.Header())
		}
	}
	for _, inm := range []string{`"a"`, `"b"`, `"a", "b"`} {
		if r := do(t, a, "GET", "/x", "If-None-Match", inm); r.Code != 200 {
			t.Errorf("%q: %d; want 200", inm, r.Code)
		}
	}
	for _, inm := range []string{`a,b`, `"a,b`, `x"a,b"`, `"a,b"x`, `"x" junk, "a,b"`, `"a,b", junk`, `"a", *`, `w/"a,b"`, "\"a,\x01b\"", ``, ` , `} {
		r := do(t, a, "GET", "/x", "If-None-Match", inm)
		if r.Code != 400 || r.Header().Get("ETag") != "" || !strings.Contains(r.Body.String(), "If-None-Match") {
			t.Errorf("%q: %d %v %s; want 400", inm, r.Code, r.Header(), r.Body)
		}
	}
	// Two lines are one list: `*` beside a tag is no list Check reads either.
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Add("If-None-Match", `"a,b"`)
	req.Header.Add("If-None-Match", `*`)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("two lines: %d; want 400", rec.Code)
	}
}

// The ETag middleware and Conditional.Check read one If-None-Match alike: a
// list whose first tag matches but which then holds no tag is a 400 from
// both, never a 304 from one and a 400 from the other.
func TestETagAndCheckReadIfNoneMatchAlike(t *testing.T) {
	type in struct{ geta.Conditional }
	check := func(_ context.Context, i *in) (*text, error) {
		if err := i.Check(`"a"`, time.Time{}); err != nil {
			return nil, err
		}
		return &text{"hi"}, nil
	}
	type tagged struct {
		ETag string `header:"ETag"`
		Body text   `body:"json"`
	}
	plain := func(context.Context, *empty) (*tagged, error) { return &tagged{ETag: `"a"`, Body: text{"hi"}}, nil }
	a := accepts(t, withRoot(geta.Table{Routes: []geta.Entry{
		{Path: "/check", Route: get(check)},
		{Path: "/etag", Route: get(plain)},
	}}, geta.ETag()))
	for _, inm := range []string{`"a", junk`, `"a",`, `"a"`} {
		c, e := do(t, a, "GET", "/check", "If-None-Match", inm).Code, do(t, a, "GET", "/etag", "If-None-Match", inm).Code
		if c != e {
			t.Errorf("%q: Check %d, ETag %d", inm, c, e)
		}
	}
}

// ETag's 304 is documented for GET operations that answer 200, and only
// there.
func TestETagDeclaresItsNotModified(t *testing.T) {
	tbl := withRoot(geta.Table{Routes: []geta.Entry{{Path: "/x", Route: geta.Route{
		Get:  geta.Op(http.StatusOK, okHandler, geta.Doc{}),
		Post: geta.Op(http.StatusOK, okHandler, geta.Doc{}),
	}}}}, geta.ETag())
	rec := &recorder{TB: t}
	c := getatest.New(rec, tbl)
	tag := c.Get("/x").Header.Get("ETag")
	if res := c.With("If-None-Match", tag).Get("/x"); res.Status != 304 {
		t.Fatal(res.Status)
	}
	// Its 400 for a malformed If-None-Match is documented beside the 304.
	if res := c.With("If-None-Match", tag+", junk").Get("/x"); res.Status != 400 {
		t.Fatal(res.Status)
	}
	if len(rec.errs) != 0 {
		t.Fatalf("%q", rec.errs)
	}
	m := doc(t, c.App())
	if got := compact(t, at(t, m, "paths", "/x", "get", "responses", "304")); got != `{"description":"The representation has not changed"}` {
		t.Fatal(got)
	}
	if got := at(t, m, "paths", "/x", "get", "responses", "400", "description"); got != `If-None-Match is neither "*" nor a list of one or more entity tags` {
		t.Fatal(got)
	}
	for _, s := range []string{"304", "400"} {
		if _, has := at(t, m, "paths", "/x", "post", "responses").(map[string]any)[s]; has {
			t.Fatal("a POST documents a " + s)
		}
	}
}
