package geta_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaclient"
	"github.com/koji-1009/geta/getatest"
)

// quotaError is a store's error: it knows nothing of HTTP.
type quotaError struct {
	Wait      int
	Remaining int
	Secret    string
}

func (e *quotaError) Error() string { return "quota exhausted for " + e.Secret }

type conflictError struct{ With string }

func (e *conflictError) Error() string { return "conflicts with " + e.With }

var errBusy = errors.New("busy")

// quota is how the route describes a quotaError: a Retry-After header and
// two members.
type quota struct {
	RetryAfter int          `header:"Retry-After" doc:"seconds until a request is taken again"`
	Body       quotaMembers `body:"json"`
}

type quotaMembers struct {
	Detail    string `json:"detail"`
	Remaining int    `json:"remaining"`
}

// conflict leaves the row's detail when it has none of its own.
type conflict struct {
	Detail *string `json:"detail,omitzero"`
	With   string  `json:"with"`
}

type failIn struct {
	Kind string `query:"kind"`
}

func describedTable() geta.Table {
	rows := []geta.Failure{
		geta.OnAsProblem(http.StatusTooManyRequests, "quota exceeded", func(e *quotaError) quota {
			return quota{RetryAfter: e.Wait, Body: quotaMembers{Detail: "try again later", Remaining: e.Remaining}}
		}).Type("https://errors.example/quota"),
		geta.On(errBusy, http.StatusTooManyRequests, "busy"),
		geta.OnAsProblem(http.StatusConflict, "conflict", func(e *conflictError) conflict {
			if e.With == "" {
				return conflict{}
			}
			d := "conflicts with " + e.With
			return conflict{Detail: &d, With: e.With}
		}),
	}
	return one("/f", geta.Route{Get: geta.Op(http.StatusOK, func(_ context.Context, in *failIn) (*ok, error) {
		switch in.Kind {
		case "quota":
			return nil, &quotaError{Wait: 30, Remaining: 0, Secret: "s3cret"}
		case "busy":
			return nil, errBusy
		case "conflict":
			return nil, &conflictError{With: "b"}
		case "bare":
			return nil, &conflictError{}
		}
		return &ok{true}, nil
	}, geta.Doc{Failures: rows})})
}

// A row built with OnAsProblem answers with the problem the error describes:
// its members beside the problem's own, the detail its own or the row's,
// its headers; nothing of err.Error(). getaclient and getatest read it back.
func TestFailuresDescribeTheirProblem(t *testing.T) {
	c := getatest.New(t, describedTable())
	res := c.Get("/f?kind=quota")
	if res.Status != 429 || res.Header.Get("Retry-After") != "30" || strings.Contains(res.Text(), "s3cret") {
		t.Fatal(res.Status, res.Header, res.Text())
	}
	if got := res.Text(); got != `{"type":"https://errors.example/quota","title":"Too Many Requests","status":429,"detail":"try again later","remaining":0}` {
		t.Fatal(got)
	}
	if p := res.Problem(); p.Detail != "try again later" || p.Type != "https://errors.example/quota" {
		t.Fatal(p)
	}
	if q := res.ProblemAs[quota](); q.RetryAfter != 30 || q.Body.Detail != "try again later" {
		t.Fatal(q)
	}
	if res := c.Get("/f?kind=busy"); res.Status != 429 || res.Header.Get("Retry-After") != "" || res.Problem().Detail != "busy" {
		t.Fatal(res.Status, res.Header, res.Text())
	}
	if got := c.Get("/f?kind=conflict").Text(); got != `{"type":"about:blank","title":"Conflict","status":409,"detail":"conflicts with b","with":"b"}` {
		t.Fatal(got)
	}
	if got := c.Get("/f?kind=bare").Text(); got != `{"type":"about:blank","title":"Conflict","status":409,"detail":"conflict","with":""}` {
		t.Fatal(got)
	}
	_, err := getaclient.Call[failIn, ok](t.Context(), c.Typed(), http.MethodGet, "/f", &failIn{Kind: "quota"})
	q, found := getaclient.ProblemAs[quota](err)
	if !found || q.RetryAfter != 30 || q.Body.Remaining != 0 || q.Body.Detail != "try again later" {
		t.Fatal(q, found, err)
	}
	_, err = getaclient.Call[failIn, ok](t.Context(), c.Typed(), http.MethodGet, "/f", &failIn{Kind: "conflict"})
	if cf, found := getaclient.ProblemAs[conflict](err); !found || cf.With != "b" {
		t.Fatal(cf, found, err)
	}
}

// The document states a described row with its status: the problem's
// schema with its members, its headers (required only where every cause of
// the status sets them), and any of the problems where the causes differ.
func TestDescribedFailuresAreDocumented(t *testing.T) {
	m := doc(t, accepts(t, describedTable()))
	r := at(t, m, "paths", "/f", "get", "responses", "429").(map[string]any)
	if got := compact(t, at(t, r, "content", "application/problem+json", "schema")); got != `{"anyOf":[{"$ref":"#/components/schemas/Problem"},`+
		`{"allOf":[{"$ref":"#/components/schemas/Problem"},{"properties":{"detail":{"type":"string"},"remaining":{"format":"int64","type":"integer"}},"required":["detail","remaining"],"type":"object"}]}]}` {
		t.Error(got)
	}
	if got := compact(t, at(t, r, "headers", "Retry-After")); got != `{"description":"seconds until a request is taken again","required":false,"schema":{"format":"int64","type":"integer"}}` {
		t.Error(got)
	}
	if got := compact(t, at(t, m, "paths", "/f", "get", "responses", "409", "content", "application/problem+json", "schema")); !strings.HasPrefix(got, `{"allOf":[{"$ref":"#/components/schemas/Problem"},{"properties":{"detail":{"type":"string"},"with":`) {
		t.Error(got)
	}
	// Alone on its status, the header is required.
	alone := one("/f", geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *empty) (*ok, error) { return nil, nil }, geta.Doc{Failures: []geta.Failure{
		geta.OnAsProblem(http.StatusServiceUnavailable, "down", func(e *quotaError) quota { return quota{} }),
	}})})
	if got := at(t, doc(t, accepts(t, alone)), "paths", "/f", "get", "responses", "503", "headers", "Retry-After", "required"); got != true {
		t.Error(got)
	}
}

// maybeQuota sets Retry-After only when it knows the wait.
type maybeQuota struct {
	RetryAfter *int         `header:"Retry-After"`
	Body       quotaMembers `body:"json"`
}

// A description's pointer header counts as not set: alone on its
// status, or beside a row of the same P, it is not required, since a nil
// one is not sent; beside a row that always sets it, neither.
func TestAPointerHeaderOfADescriptionIsNotRequired(t *testing.T) {
	wait := 0
	route := func(rows ...geta.Failure) geta.Table {
		return one("/f", geta.Route{Get: geta.Op(http.StatusOK, func(_ context.Context, in *failIn) (*ok, error) {
			if in.Kind == "conflict" {
				return nil, &conflictError{}
			}
			return nil, &quotaError{Wait: wait}
		}, geta.Doc{Failures: rows})})
	}
	maybe := geta.OnAsProblem(http.StatusServiceUnavailable, "down", func(e *quotaError) maybeQuota {
		if e.Wait == 0 {
			return maybeQuota{}
		}
		return maybeQuota{RetryAfter: &e.Wait}
	})
	for name, tbl := range map[string]geta.Table{
		"alone": route(maybe),
		"beside a row that sets it": route(maybe, geta.OnAsProblem(http.StatusServiceUnavailable, "conflict", func(*conflictError) quota {
			return quota{RetryAfter: 1}
		})),
	} {
		c := getatest.New(t, tbl)
		if got := compact(t, at(t, doc(t, c.App()), "paths", "/f", "get", "responses", "503", "headers", "Retry-After")); !strings.Contains(got, `"required":false`) {
			t.Errorf("%s: %s", name, got)
		}
		// getatest holds each response to the document: a 503 without the
		// header, and one with it, conform.
		wait = 0
		if res := c.Get("/f?kind=quota"); res.Status != 503 || res.Header.Values("Retry-After") != nil {
			t.Fatal(name, res.Status, res.Header)
		}
		wait = 7
		if res := c.Get("/f?kind=quota"); res.Status != 503 || res.Header.Get("Retry-After") != "7" {
			t.Fatal(name, res.Status, res.Header)
		}
	}
	// A plain int header alone is required, as TestDescribedFailuresAreDocumented has it.
	sets := route(geta.OnAsProblem(http.StatusServiceUnavailable, "down", func(*quotaError) quota { return quota{RetryAfter: 1} }))
	if got := at(t, doc(t, accepts(t, sets)), "paths", "/f", "get", "responses", "503", "headers", "Retry-After", "required"); got != true {
		t.Error(got)
	}
}

// What a description could state falsely is refused: a P that is not a
// struct, a member that is the problem's own, a detail that is no string, a
// cookie, status, or raw body field, one header by two schemas on a status,
// and no function.
func TestFailureDescriptionMistakesAreRefused(t *testing.T) {
	type ownStatus struct {
		Status int `json:"status"`
	}
	type numberDetail struct {
		Detail int `json:"detail"`
	}
	type withCookie struct {
		Session *http.Cookie `cookie:"sid"`
	}
	type withStatus struct {
		Status int `status:"200"`
	}
	type rawBody struct {
		Body []byte `body:"text/plain"`
	}
	type listBody struct {
		Body []string `body:"json"`
	}
	type textRetry struct {
		RetryAfter string `header:"Retry-After"`
	}
	type intRetry struct {
		RetryAfter int `header:"Retry-After"`
	}
	table := func(rows ...geta.Failure) geta.Table {
		return one("/f", geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *empty) (*ok, error) { return nil, nil }, geta.Doc{Failures: rows})})
	}
	for want, tbl := range map[string]geta.Table{
		"geta.OnAsProblem: int is not a struct":                          table(geta.OnAsProblem(429, "", func(*quotaError) int { return 0 })),
		`member "status" is reserved`:                                    table(geta.OnAsProblem(429, "", func(*quotaError) ownStatus { return ownStatus{} })),
		`member "detail" has kind int, not string`:                       table(geta.OnAsProblem(429, "", func(*quotaError) numberDetail { return numberDetail{} })),
		"Session: a problem's envelope takes no cookie field":            table(geta.OnAsProblem(429, "", func(*quotaError) withCookie { return withCookie{} })),
		"Status: a problem's envelope takes no status field":             table(geta.OnAsProblem(429, "", func(*quotaError) withStatus { return withStatus{} })),
		`Body: a problem's body is not body:"json"`:                      table(geta.OnAsProblem(429, "", func(*quotaError) rawBody { return rawBody{} })),
		"geta.OnAsProblem: []string is not a struct":                     table(geta.OnAsProblem(429, "", func(*quotaError) listBody { return listBody{} })),
		"failure rows on status 429 give header Retry-After two schemas": table(geta.OnAsProblem(429, "", func(*quotaError) textRetry { return textRetry{} }), geta.OnAsProblem(429, "", func(*conflictError) intRetry { return intRetry{} })),
		"geta.OnAsProblem with a nil describe function":                  table(geta.OnAsProblem[*quotaError, quota](429, "", nil)),
	} {
		_, err := geta.New(tbl)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v\nwant %q", err, want)
		}
	}
}
