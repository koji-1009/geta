package geta_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// An operation whose input embeds no Conditional declares no validator, so
// geta evaluates the request's preconditions itself (RFC 9110 §13.1, §13.2),
// against a current representation with no entity tag and no modification
// date, before the request content is processed and before the handler runs.

type hookIn struct {
	ID string `path:"id" schema:"maxLength=3"`
}

type hookPutIn struct {
	ID   string `path:"id" schema:"maxLength=3"`
	Body hook   `body:"json"`
}

type hook struct {
	URL string `json:"url" schema:"minLength=1"`
}

var errNoHook = errors.New("no such hook")

// hookTable serves /hooks/{id}, where only "a" exists, with no Conditional:
// GET, PUT with a body, and DELETE; /accepted, a GET answering 202. ran
// counts the handlers run.
func hookTable(ran *atomic.Int32) geta.Table {
	notFound := geta.Doc{Failures: []geta.Failure{geta.On(errNoHook, http.StatusNotFound, "no such hook")}}
	find := func(id string) error {
		ran.Add(1)
		if id != "a" {
			return errNoHook
		}
		return nil
	}
	return geta.Table{Routes: []geta.Entry{
		{Path: "/hooks/{id}", Route: geta.Route{
			Get: geta.Op(http.StatusOK, func(_ context.Context, in *hookIn) (*ok, error) {
				if err := find(in.ID); err != nil {
					return nil, err
				}
				return &ok{true}, nil
			}, notFound),
			Put:    geta.OpNoBody(http.StatusNoContent, func(_ context.Context, in *hookPutIn) error { return find(in.ID) }, notFound),
			Delete: geta.OpNoBody(http.StatusNoContent, func(_ context.Context, in *hookIn) error { return find(in.ID) }, notFound),
		}},
		{Path: "/accepted", Route: geta.Route{Get: geta.Op(http.StatusAccepted, func(context.Context, *empty) (*ok, error) {
			ran.Add(1)
			return &ok{true}, nil
		}, geta.Doc{})}},
	}}
}

// RFC 9110 field by field, on an operation that declares no validator. Every
// status is one the document lists: getatest fails any other.
func TestUndeclaredPreconditions(t *testing.T) {
	var ran atomic.Int32
	c := getatest.New(t, hookTable(&ran))
	valid := map[string]any{"url": "https://example.com"}
	invalid := map[string]any{"url": ""}
	for _, tc := range []struct {
		name    string
		headers []string
		method  string
		path    string
		body    any
		want    int
		runs    bool
	}{
		// §13.1.1: no listed tag matches a representation that has none.
		{"If-Match a tag", header("If-Match", `"not-the-tag"`), "DELETE", "/hooks/a", nil, 412, false},
		{"If-Match a list", header("If-Match", `"x", "y"`), "DELETE", "/hooks/a", nil, 412, false},
		{"If-Match neither * nor a tag", header("If-Match", "x"), "DELETE", "/hooks/a", nil, 412, false},
		{"If-Match a tag on GET", header("If-Match", `"x"`), "GET", "/hooks/a", nil, 412, false},
		{"If-Match a tag on PUT", header("If-Match", `"x"`), "PUT", "/hooks/a", valid, 412, false},
		// "*" holds while the target exists; a missing one is the handler's
		// 404, which takes precedence (§13.2.1).
		{"If-Match *", header("If-Match", "*"), "DELETE", "/hooks/a", nil, 204, true},
		{"If-Match * on a missing target", header("If-Match", "*"), "DELETE", "/hooks/b", nil, 404, true},
		// §13.1.2: a list holds, "*" fails: 304 on GET or HEAD, else 412.
		{"If-None-Match a tag", header("If-None-Match", `"x"`), "DELETE", "/hooks/a", nil, 204, true},
		{"If-None-Match a tag on GET", header("If-None-Match", `"x"`), "GET", "/hooks/a", nil, 200, true},
		{"If-None-Match *", header("If-None-Match", "*"), "DELETE", "/hooks/a", nil, 412, false},
		{"If-None-Match * on PUT", header("If-None-Match", "*"), "PUT", "/hooks/a", valid, 412, false},
		{"If-None-Match * on GET", header("If-None-Match", "*"), "GET", "/hooks/a", nil, 304, false},
		{"If-None-Match * on HEAD", header("If-None-Match", "*"), "HEAD", "/hooks/a", nil, 304, false},
		// A 304 answers only where a 200 would (§15.4.5).
		{"If-None-Match * on a GET answering 202", header("If-None-Match", "*"), "GET", "/accepted", nil, 202, true},
		// §13.1.3, §13.1.4: no modification date, so the dates are ignored.
		{"If-Unmodified-Since", header("If-Unmodified-Since", dateEarly), "DELETE", "/hooks/a", nil, 204, true},
		{"If-Modified-Since", header("If-Modified-Since", dateLate), "GET", "/hooks/a", nil, 200, true},
		// §13.2.1: what is refused before the content is processed is
		// answered whatever the preconditions; the content is processed
		// after them.
		{"a parameter refused", header("If-Match", `"x"`), "DELETE", "/hooks/abcd", nil, 400, false},
		{"a body of another media type", []string{"If-Match", `"x"`, "Content-Type", "text/plain"}, "PUT", "/hooks/a", "url", 415, false},
		{"a required body missing", header("If-Match", `"x"`), "PUT", "/hooks/a", nil, 400, false},
		{"an invalid body", header("If-Match", `"x"`), "PUT", "/hooks/a", invalid, 412, false},
		{"an invalid body, no precondition", nil, "PUT", "/hooks/a", invalid, 400, false},
		// §13.2.1: OPTIONS ignores them.
		{"OPTIONS", header("If-Match", `"x"`), "OPTIONS", "/hooks/a", nil, 204, false},
	} {
		cl := c
		for i := 0; i+1 < len(tc.headers); i += 2 {
			cl = cl.With(tc.headers[i], tc.headers[i+1])
		}
		before := ran.Load()
		res := cl.Do(tc.method, tc.path, tc.body)
		if res.Status != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, res.Status, res.Text(), tc.want)
		}
		if runs := ran.Load() > before; runs != tc.runs {
			t.Errorf("%s: the handler ran: %v", tc.name, runs)
		}
	}
}

// geta's own refusals: a 412 problem naming the field, and a 304 with no
// body.
func TestUndeclaredPreconditionAnswers(t *testing.T) {
	var ran atomic.Int32
	a := accepts(t, hookTable(&ran))
	rec := do(t, a, "DELETE", "/hooks/a", "If-Match", `"not-the-tag"`)
	if rec.Code != 412 || rec.Header().Get("Content-Type") != geta.ProblemContentType ||
		!strings.Contains(rec.Body.String(), `"detail":"If-Match failed: the operation declares no entity tag"`) {
		t.Errorf("412: %d %s %s", rec.Code, rec.Header(), rec.Body)
	}
	rec = do(t, a, "DELETE", "/hooks/a", "If-None-Match", "*")
	if rec.Code != 412 || !strings.Contains(rec.Body.String(), "If-None-Match") {
		t.Errorf("If-None-Match 412: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, a, "GET", "/hooks/a", "If-None-Match", "*")
	if rec.Code != 304 || rec.Body.Len() != 0 || rec.Header().Get("ETag") != "" {
		t.Errorf("304: %d %s %s", rec.Code, rec.Header(), rec.Body)
	}
	// Several lines are one list: "*" twice is no "*".
	if rec := do(t, a, "DELETE", "/hooks/a", "If-Match", "*", "If-Match", "*"); rec.Code != 412 {
		t.Errorf("If-Match * on two lines: %d", rec.Code)
	}
	if ran.Load() != 0 {
		t.Errorf("a handler ran %d times", ran.Load())
	}
}

// On a GET geta.ETag tags, If-None-Match is the middleware's to read once the
// handler has answered: "*" is a 304 where the target exists, and the
// handler's 404 where it does not.
func TestUndeclaredNoneMatchBesideTheETagMiddleware(t *testing.T) {
	var ran atomic.Int32
	c := getatest.New(t, withRoot(hookTable(&ran), geta.ETag()))
	if res := c.With("If-None-Match", "*").Get("/hooks/a"); res.Status != 304 || res.Header.Get("ETag") == "" {
		t.Errorf("existing: %d %s", res.Status, res.Header)
	}
	if res := c.With("If-None-Match", "*").Get("/hooks/b"); res.Status != 404 {
		t.Errorf("missing: %d", res.Status)
	}
	if res := c.With("If-Match", `"x"`).Get("/hooks/a"); res.Status != 412 {
		t.Errorf("If-Match: %d", res.Status)
	}
}

// The document lists geta's own answers on every operation that declares no
// validator: 412 everywhere, and 304 on a GET that answers 200 and that
// geta.ETag does not tag. OPTIONS lists neither.
func TestUndeclaredPreconditionsAreDocumented(t *testing.T) {
	var ran atomic.Int32
	m := doc(t, accepts(t, hookTable(&ran)))
	const declares = "; the operation declares no validator (its input does not embed geta.Conditional)"
	for _, c := range []struct {
		path, method string
		want         map[string]string
		not          []string
	}{
		{"/hooks/{id}", "delete", map[string]string{"412": "A precondition failed: If-Match other than *, or If-None-Match: *" + declares}, []string{"304"}},
		{"/hooks/{id}", "put", map[string]string{"412": "A precondition failed: If-Match other than *, or If-None-Match: *" + declares}, []string{"304"}},
		{"/hooks/{id}", "get", map[string]string{"304": "If-None-Match: *" + declares, "412": "A precondition failed: If-Match other than *" + declares}, nil},
		{"/accepted", "get", map[string]string{"412": "A precondition failed: If-Match other than *" + declares}, []string{"304"}},
		{"/hooks/{id}", "options", nil, []string{"304", "412"}},
	} {
		responses := at(t, m, "paths", c.path, c.method, "responses").(map[string]any)
		for s, want := range c.want {
			if got := at(t, responses, s, "description"); got != want {
				t.Errorf("%s %s %s: %q, want %q", c.method, c.path, s, got, want)
			}
		}
		for _, s := range c.not {
			if _, has := responses[s]; has {
				t.Errorf("%s %s lists %s", c.method, c.path, s)
			}
		}
		// The fields are not parameters: no input binds them.
		if strings.Contains(compact(t, at(t, m, "paths", c.path, c.method)), `"name":"If-Match"`) {
			t.Errorf("%s %s documents If-Match as a parameter", c.method, c.path)
		}
	}
	if got := compact(t, at(t, m, "paths", "/hooks/{id}", "get", "responses", "304")); got != `{"description":"If-None-Match: *`+declares+`"}` {
		t.Errorf("304: %s", got)
	}
	tagged := doc(t, accepts(t, withRoot(hookTable(&ran), geta.ETag())))
	if got := at(t, tagged, "paths", "/hooks/{id}", "get", "responses", "304", "description"); got != "The representation has not changed" {
		t.Errorf("a tagged GET's 304: %q", got)
	}
}

// A body declared geta.Deferred is read when the handler asks, so the
// handler's own checks answer first, in RFC 9110 §13.2.1's order: the target
// (404), then the preconditions (428, 412), then the content (400).

type deferredPutIn struct {
	geta.RequireConditional
	ID   string              `path:"id" schema:"maxLength=3"`
	Body geta.Deferred[hook] `body:"json"`
}

type plainPutIn struct {
	geta.RequireConditional
	ID   string `path:"id"`
	Body hook   `body:"json"`
}

// deferredTable serves PUT /d/{id} with a Deferred body and PUT /p/{id} with
// a plain one, both where only "a" exists, tagged "v2". got receives the
// body the handler read.
func deferredTable(got *hook, limits func(geta.Limits) geta.Limits) geta.Table {
	notFound := []geta.Failure{geta.On(errNoHook, http.StatusNotFound, "no such hook")}
	return geta.Table{Routes: []geta.Entry{
		{Path: "/d/{id}", Route: geta.Route{Put: geta.OpNoBody(http.StatusNoContent, func(_ context.Context, in *deferredPutIn) error {
			if in.ID != "a" {
				return errNoHook
			}
			if err := in.Check(current, time.Time{}); err != nil {
				return err
			}
			h, err := in.Body.Value()
			if err != nil {
				return fmt.Errorf("reading the hook: %w", err) // answered as is, wrapped or not
			}
			*got = h
			return nil
		}, geta.Doc{Failures: notFound, Limits: limits})}},
		{Path: "/p/{id}", Route: geta.Route{Put: geta.OpNoBody(http.StatusNoContent, func(_ context.Context, in *plainPutIn) error {
			if in.ID != "a" {
				return errNoHook
			}
			return in.Check(current, time.Time{})
		}, geta.Doc{Failures: notFound})}},
	}}
}

func TestDeferredBodyIsReadAfterTheHandlersChecks(t *testing.T) {
	var got hook
	c := getatest.New(t, deferredTable(&got, nil))
	valid := map[string]any{"url": "https://example.com"}
	invalid := map[string]any{"url": "", "x": 1}
	for _, tc := range []struct {
		name, ifMatch, path string
		body                any
		want                int
	}{
		{"a missing target", current, "/d/b", invalid, 404},
		{"a stale precondition", `"v1"`, "/d/a", invalid, 412},
		{"no precondition", "", "/d/a", invalid, 428},
		{"an invalid body", current, "/d/a", invalid, 400},
		{"a valid body", current, "/d/a", valid, 204},
		// A plain body is bound before the handler runs.
		{"a plain body, missing target", current, "/p/b", invalid, 400},
		{"a plain body, stale precondition", `"v1"`, "/p/a", invalid, 400},
	} {
		cl := c
		if tc.ifMatch != "" {
			cl = c.With("If-Match", tc.ifMatch)
		}
		res := cl.Put(tc.path, tc.body)
		if res.Status != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, res.Status, res.Text(), tc.want)
		}
	}
	if got.URL != "https://example.com" {
		t.Errorf("the handler read %+v", got)
	}
	// Its 400 lists every violation of the body, as one bound before.
	p := c.With("If-Match", current).Put("/d/a", invalid).Problem()
	if p.Detail != "the request does not match its contract" || len(p.Errors) != 2 || p.Errors[0].In != "body" {
		t.Errorf("400: %+v", p)
	}
}

// What the headers and the first byte show is still answered before the
// handler: a content coding or another media type (415), a declared length
// past MaxBodyBytes (413), a missing body (400). So is a request whose
// parameters are refused, its 400 listing the body's violations too.
func TestDeferredBodyIsJudgedBeforeTheHandler(t *testing.T) {
	var got hook
	small := func(l geta.Limits) geta.Limits { l.MaxBodyBytes = 16; return l }
	c := getatest.New(t, deferredTable(&got, small)).With("If-Match", `"v1"`)
	if res := c.Content(http.MethodPut, "/d/a", "text/plain", []byte("x")); res.Status != 415 {
		t.Errorf("another media type: %d", res.Status)
	}
	if res := c.With("Content-Encoding", "gzip").Put("/d/a", map[string]any{"url": "u"}); res.Status != 415 {
		t.Errorf("a content coding: %d", res.Status)
	}
	if res := c.Put("/d/a", map[string]any{"url": strings.Repeat("u", 32)}); res.Status != 413 {
		t.Errorf("past MaxBodyBytes: %d", res.Status)
	}
	if res := c.Put("/d/a", nil); res.Status != 400 {
		t.Errorf("no body: %d", res.Status)
	}
	res := c.Put("/d/abcd", map[string]any{"url": ""})
	if p := res.Problem(); res.Status != 400 || len(p.Errors) != 2 || p.Errors[0].In != "path" || p.Errors[1].In != "body" {
		t.Errorf("a refused parameter: %d %+v", res.Status, p)
	}
}

type optionalDeferredIn struct {
	Body geta.Deferred[*hook] `body:"json"`
}

// An optional body is a Deferred of a pointer: nil when none is sent. Value
// reads once and answers the same after; a Deferred built by hand holds no
// body.
func TestDeferredValue(t *testing.T) {
	var calls []string
	c := getatest.New(t, one("/o", geta.Route{Post: geta.OpNoBody(http.StatusNoContent, func(_ context.Context, in *optionalDeferredIn) error {
		h, err := in.Body.Value()
		again, err2 := in.Body.Value()
		if h != again || err != err2 {
			return errors.New("a second Value differs")
		}
		if err != nil {
			return err
		}
		if h == nil {
			calls = append(calls, "none")
		} else {
			calls = append(calls, h.URL)
		}
		return nil
	}, geta.Doc{})}))
	if res := c.Post("/o", nil); res.Status != 204 {
		t.Errorf("no body: %d %s", res.Status, res.Text())
	}
	if res := c.Post("/o", map[string]any{"url": "u"}); res.Status != 204 {
		t.Errorf("a body: %d %s", res.Status, res.Text())
	}
	if res := c.Post("/o", "{"); res.Status != 400 {
		t.Errorf("malformed: %d", res.Status)
	}
	if strings.Join(calls, ",") != "none,u" {
		t.Errorf("read %v", calls)
	}
	var d geta.Deferred[hook]
	if v, err := d.Value(); v != (hook{}) || err != nil {
		t.Errorf("by hand: %+v %v", v, err)
	}
}

// The document cannot tell a Deferred body from a plain one.
func TestDeferredBodyIsDocumentedAsItsType(t *testing.T) {
	type plainIn struct {
		Body hook `body:"json" doc:"the hook"`
	}
	type laterIn struct {
		Body geta.Deferred[hook] `body:"json" doc:"the hook"`
	}
	h := func(context.Context, *plainIn) error { return nil }
	l := func(context.Context, *laterIn) error { return nil }
	plain := doc(t, accepts(t, one("/x", geta.Route{Put: geta.OpNoBody(http.StatusNoContent, h, geta.Doc{OperationID: "x"})})))
	later := doc(t, accepts(t, one("/x", geta.Route{Put: geta.OpNoBody(http.StatusNoContent, l, geta.Doc{OperationID: "x"})})))
	if a, b := compact(t, plain), compact(t, later); a != b {
		t.Errorf("plain:\n%s\nDeferred:\n%s", a, b)
	}
}

type deferredQueryIn struct {
	Q geta.Deferred[string] `query:"q"`
}

type deferredFormIn struct {
	Body geta.Deferred[hook] `body:"form"`
}

type deferredPointerIn struct {
	Body *geta.Deferred[hook] `body:"json"`
}

type deferredEmbeddedIn struct {
	geta.Deferred[hook]
}

// A Deferred is a body:"json" field and nothing else.
func TestDeferredOnlyAsAJSONBody(t *testing.T) {
	post := func(r geta.Route) geta.Table { return one("/x", r) }
	rejects(t, post(geta.Route{Post: geta.OpNoBody(http.StatusNoContent, func(context.Context, *deferredQueryIn) error { return nil }, geta.Doc{})}),
		`Q: a geta.Deferred is a body:"json" field`)
	rejects(t, post(geta.Route{Post: geta.OpNoBody(http.StatusNoContent, func(context.Context, *deferredFormIn) error { return nil }, geta.Doc{})}),
		`Body: a geta.Deferred is a body:"json" field`)
	rejects(t, post(geta.Route{Post: geta.OpNoBody(http.StatusNoContent, func(context.Context, *deferredPointerIn) error { return nil }, geta.Doc{})}),
		`Body: the body is a pointer to a geta.Deferred`)
	rejects(t, post(geta.Route{Post: geta.OpNoBody(http.StatusNoContent, func(context.Context, *deferredEmbeddedIn) error { return nil }, geta.Doc{})}),
		`Deferred: a geta.Deferred is a body:"json" field`)
}
