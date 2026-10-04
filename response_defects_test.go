package geta_test

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// A row matching context.DeadlineExceeded would never answer: geta answers
// the deadline 504 before it reads the failure table, so the document would
// list a status and a cause the operation never sends. New refuses it, the
// target wrapped included. A row for context.Canceled stays: a handler's own
// cancellation, with the request alive, reaches the table.
func TestARowForTheDeadlineIsRefused(t *testing.T) {
	table := func(f geta.Failure) geta.Table {
		return one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Failures: []geta.Failure{f}})})
	}
	rejects(t, table(geta.On(context.DeadlineExceeded, http.StatusServiceUnavailable, "the store is slow")),
		"matches context.DeadlineExceeded, which geta answers 504")
	rejects(t, table(geta.On(fmt.Errorf("store: %w", context.DeadlineExceeded), http.StatusGatewayTimeout, "slow")),
		"matches context.DeadlineExceeded, which geta answers 504")

	h := func(context.Context, *empty) (*ok, error) { return nil, context.Canceled }
	c := getatest.New(t, one("/x", geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{Failures: []geta.Failure{
		geta.On(context.Canceled, http.StatusServiceUnavailable, "the work was called off"),
	}})}))
	if p := c.Get("/x").Problem(); p.Status != 503 || p.Detail != "the work was called off" {
		t.Fatal(p)
	}
}

// A handler that returns a nil output and a nil error is geta's defect to
// report, not an error a catch-all row answers: it stays a logged 500 with
// an instance.
func TestACatchAllRowDoesNotSwallowANilOutput(t *testing.T) {
	log, buf := logger()
	h := func(context.Context, *empty) (*ok, error) { return nil, nil }
	a, err := geta.New(one("/x", geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{Failures: []geta.Failure{
		geta.OnAs[error](http.StatusServiceUnavailable, "down"),
	}})}), geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	p := getatest.Serve(t, a).Get("/x").Problem()
	if p.Status != 500 || !strings.HasPrefix(p.Instance, "urn:uuid:") || p.Detail != "" {
		t.Fatal(p)
	}
	if l := buf.String(); !strings.Contains(l, "nil output") || !strings.Contains(l, p.Instance) {
		t.Fatal(l)
	}
}

type floatHeader struct {
	F    float64 `header:"X-F"`
	Body ok      `body:"json"`
}

type float32Header struct {
	F    float32 `header:"X-F"`
	Body ok      `body:"json"`
}

// A number header holding NaN or an infinity is a value its schema (type
// number) refuses, as the same value in a JSON body is: a 500, the header
// not sent. A float32 header is written in the shortest form that reads back
// as that float32, as its JSON is.
func TestNonFiniteNumberHeadersAreDefects(t *testing.T) {
	for _, f := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		h := func(context.Context, *empty) (*floatHeader, error) { return &floatHeader{F: f}, nil }
		res := getatest.New(t, one("/x", get(h))).Get("/x")
		if res.Status != 500 || res.Header.Get("X-F") != "" {
			t.Errorf("%v: %d %v", f, res.Status, res.Header)
		}
	}
	h := func(context.Context, *empty) (*float32Header, error) { return &float32Header{F: 1.1}, nil }
	if res := getatest.New(t, one("/x", get(h))).Get("/x"); res.Status != 200 || res.Header.Get("X-F") != "1.1" {
		t.Errorf("%d %q", res.Status, res.Header.Get("X-F"))
	}
}

// liarProblem describes a failure with a member whose declared schema
// (string, by WithSchema) its type does not write.
type liarProblem struct {
	V liar `json:"v"`
}

type liarEvent struct {
	V liar `json:"v"`
}

// App.Conforms, and so getatest, holds a described failure's body to the
// problem schema its document states, as it holds a success body: the
// problem's own members and the description's. Where the status has a plain
// cause too, the document admits any problem (anyOf), and so does Conforms.
func TestConformsChecksDescribedFailures(t *testing.T) {
	h := func(_ context.Context, in *failIn) (*ok, error) {
		if in.Kind == "busy" {
			return nil, errBusy
		}
		return nil, &conflictError{With: in.Kind}
	}
	described := func(rows ...geta.Failure) geta.Table {
		return one("/f", geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{Failures: rows})})
	}
	row := geta.OnAsProblem(http.StatusConflict, "conflict", func(*conflictError) liarProblem { return liarProblem{} })
	rec := &recorder{TB: t}
	c := getatest.New(rec, described(row), geta.WithSchema[liar]("string", ""))
	if res := c.Get("/f?kind=a"); res.Status != 409 || res.Text() != `{"type":"about:blank","title":"Conflict","status":409,"detail":"conflict","v":7}` {
		t.Fatal(res.Status, res.Text())
	}
	if len(rec.errs) != 1 || !strings.Contains(rec.errs[0], "$.v: expected string, got integer") {
		t.Fatalf("%q", rec.errs)
	}

	get := geta.Match{Template: "/f", Method: http.MethodGet, Operation: true}
	app := c.App()
	for body, want := range map[string]string{
		`{"type":"about:blank","title":"Conflict","status":409,"v":"x"}`:   "",
		`{"type":"about:blank","title":"Conflict","status":409,"v":7}`:     "$.v: expected string, got integer",
		`{"type":"about:blank","title":"Conflict","status":409}`:           "$.v: missing required member",
		`{"type":"about:blank","title":"Conflict","status":"409","v":"x"}`: "$.status: expected integer, got string",
		`{"title":"Conflict","status":409,"v":"x"}`:                        "$.type: missing required member",
		`[1]`:      "expected object, got array",
		`not json`: "the body is not JSON",
	} {
		err := app.Conforms(get, http.StatusConflict, nil, []byte(body))
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v; want %q", body, err, want)
		}
	}

	// Beside a plain cause, any problem is what the document admits.
	rec = &recorder{TB: t}
	c = getatest.New(rec, described(row, geta.On(errBusy, http.StatusConflict, "busy")), geta.WithSchema[liar]("string", ""))
	c.Get("/f?kind=a")
	c.Get("/f?kind=busy")
	if len(rec.errs) != 0 {
		t.Fatalf("%q", rec.errs)
	}
}

// App.Conforms, and getatest's EventStream, hold each event's data to the
// event type's schema.
func TestConformsChecksStreamEvents(t *testing.T) {
	h := func(context.Context, *empty) (*geta.Stream[liarEvent], error) {
		return &geta.Stream[liarEvent]{Events: func(yield func(liarEvent) bool) { yield(liarEvent{}) }}, nil
	}
	rec := &recorder{TB: t}
	c := getatest.New(rec, one("/e", get(h)), geta.WithSchema[liar]("string", ""))
	s := c.Stream("/e")
	if e, ok := s.Next(); !ok || e.Data != `{"v":7}` {
		t.Fatal(e, ok)
	}
	if len(rec.errs) != 1 || !strings.Contains(rec.errs[0], "$.v: expected string, got integer") {
		t.Fatalf("%q", rec.errs)
	}
	get := geta.Match{Template: "/e", Method: http.MethodGet, Operation: true}
	app := c.App()
	for body, want := range map[string]string{
		"data: {\"v\":\"x\"}\n\n":                    "",
		"event: a\nid: 1\ndata: {\"v\":\"x\"}\n\n":   "",
		": keep-alive\n\ndata: {\"v\":\"x\"}\n\n":    "",
		"data: {\"v\":\"x\"}\n\ndata: {\"v\":7}\n\n": "event 2: the data does not match the documented schema: $.v: expected string, got integer",
		"data: {\"v\":\n\n":                          "event 1: the data is not JSON",
		"data:{\"v\":\"x\"}\r\n\r\n":                 "",
		"data: {\"v\":\ndata: \"x\"}\n\n":            "",
		"data: {}\n\n":                               "event 1: the data does not match the documented schema: $.v: missing required member",
	} {
		err := app.Conforms(get, http.StatusOK, nil, []byte(body))
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%q: %v; want %q", body, err, want)
		}
	}
}

// getatest's Stream and Upgrade hold a refusal's body to the document as
// Send does: a described problem off its schema fails the test.
func TestStreamAndUpgradeRefusalsAreCheckedAgainstTheDocument(t *testing.T) {
	refuse := func(context.Context, *empty) error { return &conflictError{With: "a"} }
	row := geta.OnAsProblem(http.StatusConflict, "conflict", func(*conflictError) liarProblem { return liarProblem{} })
	stream := func(ctx context.Context, in *empty) (*geta.Stream[liarEvent], error) { return nil, refuse(ctx, in) }
	upgrade := func(ctx context.Context, in *empty) (*geta.Upgrade, error) { return nil, refuse(ctx, in) }
	tbl := geta.Table{Routes: []geta.Entry{
		{Path: "/e", Route: geta.Route{Get: geta.Op(http.StatusOK, stream, geta.Doc{Failures: []geta.Failure{row}})}},
		{Path: "/u", Route: geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, upgrade, geta.Doc{Failures: []geta.Failure{row}})}},
	}}
	rec := &recorder{TB: t}
	c := getatest.New(rec, tbl, geta.WithSchema[liar]("string", ""))
	if s := c.Stream("/e"); s.Response.Status != 409 {
		t.Fatal(s.Response.Status)
	}
	if u := c.Upgrade("/u", "echo"); u.Switched || u.Response.Status != 409 {
		t.Fatal(u.Response.Status)
	}
	if len(rec.errs) != 2 || !strings.Contains(rec.errs[0], "$.v: expected string, got integer") || !strings.Contains(rec.errs[1], "$.v: expected string, got integer") {
		t.Fatalf("%q", rec.errs)
	}
}

// Upgrade and Connection on the 101 are geta's for Serve: a Header naming
// either would send a second protocol beside Protocol, so it is a defect,
// a 500 before anything switches.
func TestAnUpgradeHeaderCannotNameTheProtocolAgain(t *testing.T) {
	for _, hdr := range []http.Header{{"Upgrade": {"other"}}, {"connection": {"close"}}} {
		h := func(context.Context, *empty) (*geta.Upgrade, error) {
			return &geta.Upgrade{Protocol: "echo", Header: hdr, Serve: func(net.Conn, *bufio.ReadWriter) {}}, nil
		}
		log, buf := logger()
		a, err := geta.New(one("/ws", geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, h, geta.Doc{})}), geta.WithLogger(log))
		if err != nil {
			t.Fatal(err)
		}
		u := getatest.Serve(t, a).Upgrade("/ws", "echo")
		if u.Switched || u.Response.Status != 500 || !strings.Contains(buf.String(), "Upgrade.Header") {
			t.Errorf("%v: %v %d %s", hdr, u.Switched, u.Response.Status, buf)
		}
	}
}
