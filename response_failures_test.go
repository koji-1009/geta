package geta_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// The rules of response.md on failures, Recover, and Conforms that no other
// test asserts.

// panicAfter is a middleware that panics once the operation has answered.
var panicAfter = geta.Use(func(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		panic("after the response")
	})
})

// serveRecovering serves one GET /x and returns what ServeHTTP panicked with.
func serveRecovering(a *geta.App, rec *httptest.ResponseRecorder) (p any) {
	defer func() { p = recover() }()
	a.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	return nil
}

// A panic after the response started, a streamed body among them, cannot
// become a 500: Recover logs it and aborts the connection. http.ErrAbortHandler
// itself passes Recover unchanged, writing nothing.
func TestRecoverAfterTheResponseStartedAborts(t *testing.T) {
	limits := geta.DefaultLimits
	limits.MaxResponseBuffer = 64
	for name, h := range map[string]func(context.Context, *empty) (*largeList, error){
		"buffered": func(context.Context, *empty) (*largeList, error) { return largeOf(16, false), nil },
		"streamed": func(context.Context, *empty) (*largeList, error) { return largeOf(4096, false), nil },
	} {
		log, buf := logger()
		a, err := geta.New(withRoot(one("/x", get(h)), geta.Recover(log), panicAfter), geta.WithLimits(limits), geta.WithLogger(quietLogger()))
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		if p := serveRecovering(a, rec); p != http.ErrAbortHandler {
			t.Fatalf("%s: %v", name, p)
		}
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s: %d %v", name, rec.Code, rec.Header())
		}
		if l := buf.String(); !strings.Contains(l, "geta: panic") || !strings.Contains(l, "after the response") || !strings.Contains(l, "stack=") {
			t.Errorf("%s: %s", name, l)
		}
	}

	log, buf := logger()
	abort := func(context.Context, *empty) (*ok, error) { panic(http.ErrAbortHandler) }
	a := accepts(t, withRoot(one("/x", get(abort)), geta.Recover(log)))
	rec := httptest.NewRecorder()
	if p := serveRecovering(a, rec); p != http.ErrAbortHandler {
		t.Fatal(p)
	}
	if rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" || buf.String() != "" {
		t.Fatal(rec.Body, rec.Header(), buf)
	}
}

type badRetry struct {
	RetryAfter string `header:"Retry-After"`
}

type sealedProblem struct {
	Main shape `json:"main"`
}

type brokenProblem struct {
	V broken `json:"v"`
}

// A description that cannot be written (a header value no header carries, a
// required sealed member left nil, a member that does not encode) is a
// defect: a logged 500 with an instance, carrying none of the description's
// headers and keeping the middleware's.
func TestADescriptionThatCannotBeWrittenIsADefect(t *testing.T) {
	mark := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Middleware", "1")
			next.ServeHTTP(w, r)
		})
	})
	h := func(context.Context, *empty) (*ok, error) { return nil, &quotaError{} }
	for name, row := range map[string]geta.Failure{
		"header": geta.OnAsProblem(429, "", func(*quotaError) badRetry { return badRetry{RetryAfter: "1\r\nX-Evil: 1"} }),
		"sealed": geta.OnAsProblem(429, "", func(*quotaError) sealedProblem { return sealedProblem{} }),
		"encode": geta.OnAsProblem(429, "", func(*quotaError) brokenProblem { return brokenProblem{} }),
	} {
		log, buf := logger()
		tbl := withRoot(one("/x", geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{Failures: []geta.Failure{row}})}), mark)
		a, err := geta.New(tbl, geta.WithUnion(shapes), geta.WithLogger(log))
		if err != nil {
			t.Fatal(name, err)
		}
		res := getatest.Serve(t, a).Get("/x")
		p := res.Problem()
		if p.Status != 500 || !strings.HasPrefix(p.Instance, "urn:uuid:") || res.Header.Get("Retry-After") != "" ||
			res.Header.Get("X-Evil") != "" || res.Header.Get("X-Middleware") != "1" {
			t.Errorf("%s: %v %v", name, p, res.Header)
		}
		if l := buf.String(); !strings.Contains(l, "a failure's description could not be written") || !strings.Contains(l, p.Instance) {
			t.Errorf("%s: %s", name, l)
		}
	}
}

// CORS exposes the headers a described row sets, so a page reads them.
func TestCORSExposesDescribedHeaders(t *testing.T) {
	a := accepts(t, withRoot(describedTable(), geta.CORS(geta.AllowOrigins("*"))))
	r := do(t, a, "GET", "/f?kind=quota", "Origin", "https://a.example")
	if r.Code != 429 || r.Header().Get("Retry-After") != "30" ||
		!strings.Contains(strings.ToLower(r.Header().Get("Access-Control-Expose-Headers")), "retry-after") {
		t.Fatal(r.Code, r.Header())
	}
}

// A status a row and a BeforeGate middleware both answer lists both causes,
// the row's first.
func TestARowBesideABeforeGateAnswerIsDocumentedWithBoth(t *testing.T) {
	h := func(context.Context, *empty) (*ok, error) { return nil, errBusy }
	a := accepts(t, one("/x", geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{
		Failures:   []geta.Failure{geta.On(errBusy, http.StatusTooManyRequests, "slow down")},
		BeforeGate: geta.Scope{limit(100)},
	})}))
	if got := at(t, doc(t, a), "paths", "/x", "get", "responses", "429", "description"); got != "slow down; Rate limit exceeded" {
		t.Fatal(got)
	}
	if r := do(t, a, "GET", "/x"); r.Code != 429 || !strings.Contains(r.Body.String(), "slow down") {
		t.Fatal(r.Code, r.Body)
	}
}

// RequireConditional.CheckAbsent evaluates a request that carries a
// precondition as CheckAbsent does: If-None-Match: * holds, If-Match fails.
func TestRequireConditionalCheckAbsentEvaluates(t *testing.T) {
	if err := (&geta.RequireConditional{Conditional: geta.Conditional{IfNoneMatch: new("*")}}).CheckAbsent(); err != nil {
		t.Fatal(err)
	}
	err := (&geta.RequireConditional{Conditional: geta.Conditional{IfMatch: new(`"a"`)}}).CheckAbsent()
	if pe, ok := errors.AsType[*geta.PreconditionError](err); !ok || pe.Status() != http.StatusPreconditionFailed {
		t.Fatal(err)
	}
}

type countProblem struct {
	N int `json:"n"`
}

// A status several descriptions share admits a problem any of them states;
// a description of headers alone states Problem's members only; Problem's
// own members are held to its schema, its errors among them.
func TestConformsReadsEveryDescriptionOfAStatus(t *testing.T) {
	h := func(context.Context, *empty) (*ok, error) { return nil, errBusy }
	a := accepts(t, one("/x", geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{Failures: []geta.Failure{
		geta.OnAsProblem(http.StatusConflict, "", func(*conflictError) conflict { return conflict{} }),
		geta.OnAsProblem(http.StatusConflict, "", func(*quotaError) countProblem { return countProblem{} }),
		geta.OnAsProblem(http.StatusServiceUnavailable, "", func(*quotaError) badRetry { return badRetry{} }),
	}})}))
	x := geta.Match{Template: "/x", Method: http.MethodGet}
	const head = `{"type":"about:blank","title":"t","status":409`
	for _, c := range []struct {
		status int
		body   string
		want   []string
	}{
		{409, head + `,"with":"b"}`, nil},
		{409, head + `,"n":1}`, nil},
		{409, head + `}`, []string{"$.with: missing required member"}},
		{503, `{"type":"about:blank","title":"t","status":503,"extra":1}`, nil},
		{503, `{"type":"about:blank","title":"t","status":503,"errors":[1,{"in":"query","path":1}]}`, []string{
			"$.errors[0]: expected object, got integer", "$.errors[1].path: expected string, got integer", "$.errors[1].message: missing required member",
		}},
	} {
		err := a.Conforms(x, c.status, nil, []byte(c.body))
		if c.want == nil && err != nil || c.want != nil && err == nil {
			t.Errorf("%d %s: %v", c.status, c.body, err)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%d %s: %v; want %q", c.status, c.body, err, w)
			}
		}
	}
}

type rawReport struct {
	Body []byte `body:"text/plain"`
}

// Conforms reports a success body that is not JSON, and checks nothing of a
// status the document gives no schema of its own (a plain problem), an
// empty body, a raw body, or an upgrade.
func TestConformsChecksOnlyWhatTheDocumentStates(t *testing.T) {
	h := func(context.Context, *empty) (*ok, error) { return nil, errBusy }
	a := accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/x", Route: geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{Failures: []geta.Failure{geta.On(errBusy, http.StatusConflict, "busy")}})}},
		{Path: "/raw", Route: get(func(context.Context, *empty) (*rawReport, error) { return &rawReport{}, nil })},
		{Path: "/ws", Route: geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, echoUpgrade, geta.Doc{})}},
	}})
	x := geta.Match{Template: "/x", Method: http.MethodGet}
	if err := a.Conforms(x, http.StatusOK, nil, []byte("not json")); err == nil || !strings.Contains(err.Error(), "the body is not JSON") {
		t.Fatal(err)
	}
	for _, c := range []struct {
		m      geta.Match
		status int
		body   string
	}{
		{x, http.StatusConflict, "not json"},
		{x, http.StatusInternalServerError, "not json"},
		{x, http.StatusOK, ""},
		{geta.Match{Template: "/raw", Method: http.MethodGet}, http.StatusOK, "not json"},
		{geta.Match{Template: "/ws", Method: http.MethodGet}, http.StatusSwitchingProtocols, "not json"},
		{geta.Match{}, http.StatusOK, "not json"},
	} {
		if err := a.Conforms(c.m, c.status, nil, []byte(c.body)); err != nil {
			t.Errorf("%v %d %q: %v", c.m, c.status, c.body, err)
		}
	}
	// Through getatest, a raw body is not judged as JSON either.
	if res := getatest.Serve(t, a).Get("/raw"); res.Status != 200 {
		t.Fatal(res.Status)
	}
}
