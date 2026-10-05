package geta_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaclient"
	"github.com/koji-1009/geta/getatest"
)

// A 3xx an operation declares is its success: the typed client answers its
// output, its Location read, and follows nothing, whatever its http.Client
// would do; a 304 is no success.

type seeOther struct {
	Location string `header:"Location"`
}

type chosen struct {
	Status   int    `status:"200|303"`
	Location string `header:"Location"`
	Body     ok     `body:"json"`
}

type chooseIn struct {
	Move bool `query:"move"`
}

func redirectsApp(t *testing.T) *getatest.Client {
	return getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/go", Route: geta.Route{Post: geta.Op(http.StatusSeeOther, func(context.Context, *empty) (*seeOther, error) {
			return &seeOther{Location: "/there"}, nil
		}, geta.Doc{})}},
		{Path: "/choose", Route: geta.Route{Get: geta.Op(http.StatusOK, func(_ context.Context, in *chooseIn) (*chosen, error) {
			if in.Move {
				return &chosen{Status: http.StatusSeeOther, Location: "/there", Body: ok{true}}, nil
			}
			return &chosen{Body: ok{true}}, nil
		}, geta.Doc{})}},
		{Path: "/there", Route: get(textHandler("there"))},
		{Path: "/tagged", Route: geta.Route{Get: geta.Op(http.StatusOK, func(_ context.Context, in *condIn) (*ok, error) {
			if err := in.Check(`"v1"`, time.Time{}); err != nil {
				return nil, err
			}
			return &ok{true}, nil
		}, geta.Doc{})}},
	}})
}

func TestDeclaredRedirectsAreSuccesses(t *testing.T) {
	c := redirectsApp(t)
	ctx := t.Context()
	// Through getatest's client, which follows nothing.
	out, err := getaclient.Call[empty, seeOther](ctx, c.Typed(), http.MethodPost, "/go", nil)
	if err != nil || out.Location != "/there" {
		t.Fatal(out, err)
	}
	ch, err := getaclient.Call[chooseIn, chosen](ctx, c.Typed(), http.MethodGet, "/choose", &chooseIn{Move: true})
	if err != nil || ch.Status != http.StatusSeeOther || ch.Location != "/there" || !ch.Body.OK {
		t.Fatal(ch, err)
	}
	// Through an http.Client that follows redirects: the call still answers
	// the 303, not what its Location points to.
	follows := &getaclient.Client{Base: c.URL(), HTTP: &http.Client{Transport: c.HTTP().Transport}}
	out, err = getaclient.Call[empty, seeOther](ctx, follows, http.MethodPost, "/go", nil)
	if err != nil || out.Location != "/there" {
		t.Fatal(out, err)
	}
	// A 304 is no success: it says the caller's copy is current.
	tag := `"v1"`
	_, err = getaclient.Call[condIn, ok](ctx, c.Typed(), http.MethodGet, "/tagged", &condIn{geta.Conditional{IfNoneMatch: &tag}})
	if e, isErr := errors.AsType[*getaclient.Error](err); !isErr || e.Status != http.StatusNotModified {
		t.Fatal(err)
	}
}

// A 2xx or 3xx a middleware answers in front of the operation is no answer of
// the operation: http.Redirect's 308 (a text/html body on a GET, none on a
// DELETE) and a 202 with a text/plain body are an *Error carrying the
// status, the header (the Location), and the body, for an output that reads
// JSON and for CallNoBody; an envelope that reads no body takes a 3xx's
// Location. An operation's own 3xx with a plain output stays a success.

type movedHeaders struct {
	Status   int    `status:"200|308"`
	Location string `header:"Location"`
}

// queueing answers 202 with a text/plain body when the request asks for it.
var queueing = geta.Use(func(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Queue") != "" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte("queued"))
			return
		}
		next.ServeHTTP(w, r)
	})
}).Answers(http.StatusAccepted, "Queued for later")

func TestMiddlewareSuccessesAreNoOutput(t *testing.T) {
	c := getatest.New(t, withRoot(geta.Table{Routes: []geta.Entry{
		{Path: "/plain", Route: geta.Route{
			Get:    geta.Op(http.StatusOK, okHandler, geta.Doc{}),
			Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error { return nil }, geta.Doc{}),
		}},
		{Path: "/chosen", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *chooseIn) (*chosen, error) {
			return &chosen{Body: ok{true}}, nil
		}, geta.Doc{})}},
		{Path: "/headers", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *empty) (*movedHeaders, error) {
			return &movedHeaders{}, nil
		}, geta.Doc{})}},
		// 300, the one 3xx a plain output may answer: it needs no Location.
		{Path: "/choices", Route: geta.Route{Post: geta.Op(http.StatusMultipleChoices, okHandler, geta.Doc{})}},
	}}, httpsOnly(), queueing))
	ctx := t.Context()
	const to = "https://example.com/x"
	redirected := c.With("X-Plain-Http", to).Typed()
	isRedirect := func(what string, err error, body bool) {
		t.Helper()
		e, isErr := errors.AsType[*getaclient.Error](err)
		if !isErr || e.Status != http.StatusPermanentRedirect || e.Header.Get("Location") != to || e.Problem != nil || body != (len(e.Body) > 0) {
			t.Fatalf("%s: %v", what, err)
		}
	}
	// A plain output: http.Redirect's text/html body is not decoded.
	out, err := getaclient.Call[empty, ok](ctx, redirected, http.MethodGet, "/plain", nil)
	isRedirect("plain output", err, true)
	if out != nil {
		t.Fatal(out)
	}
	if got := err.Error(); got != `308 Permanent Redirect, not the call's output (Content-Type "text/html; charset=utf-8", Location "https://example.com/x")` {
		t.Errorf("%q", got)
	}
	// An envelope with a JSON body: the same.
	_, err = getaclient.Call[chooseIn, chosen](ctx, redirected, http.MethodGet, "/chosen", nil)
	isRedirect("envelope with a body", err, true)
	// CallNoBody: a 3xx is no success; http.Redirect writes no body on a DELETE.
	isRedirect("CallNoBody", getaclient.CallNoBody[empty](ctx, redirected, http.MethodDelete, "/plain", nil), false)
	// ResponseHeader reads the same header as the Error.
	var h http.Header
	_, err = getaclient.Call[empty, ok](ctx, redirected, http.MethodGet, "/plain", nil, getaclient.ResponseHeader(&h))
	isRedirect("with ResponseHeader", err, true)
	if h.Get("Location") != to {
		t.Fatal(h)
	}
	// An envelope that reads no body takes the 3xx, its Location and status.
	mh, err := getaclient.Call[empty, movedHeaders](ctx, redirected, http.MethodGet, "/headers", nil)
	if err != nil || mh.Status != http.StatusPermanentRedirect || mh.Location != to {
		t.Fatal(mh, err)
	}
	// A middleware's 202 with a text/plain body against a JSON output.
	_, err = getaclient.Call[empty, ok](ctx, c.With("X-Queue", "1").Typed(), http.MethodGet, "/plain", nil)
	e, isErr := errors.AsType[*getaclient.Error](err)
	if !isErr || e.Status != http.StatusAccepted || string(e.Body) != "queued" || e.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatal(err)
	}
	// Without the middlewares answering, each call is the operation's.
	if o, err := getaclient.Call[empty, ok](ctx, c.Typed(), http.MethodGet, "/plain", nil); err != nil || !o.OK {
		t.Fatal(o, err)
	}
	if err := getaclient.CallNoBody[empty](ctx, c.Typed(), http.MethodDelete, "/plain", nil); err != nil {
		t.Fatal(err)
	}
	// An operation's own 3xx with a plain output is its success: geta writes
	// it as JSON.
	if o, err := getaclient.Call[empty, ok](ctx, c.Typed(), http.MethodPost, "/choices", nil); err != nil || !o.OK {
		t.Fatal(o, err)
	}
}

// ProblemAs decodes with the client's JSON options, so a description that
// holds a sealed type is read back, by getaclient and getatest alike.

type shapeProblem struct {
	Shape shape `json:"shape"`
}

type shapeError struct{ radius float64 }

func (*shapeError) Error() string { return "bad shape" }

func TestProblemAsReadsSealedTypes(t *testing.T) {
	c := getatest.New(t, one("/s", geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *empty) (*ok, error) {
		return nil, &shapeError{radius: 2}
	}, geta.Doc{Failures: []geta.Failure{
		geta.OnAsProblem(http.StatusUnprocessableEntity, "bad shape", func(e *shapeError) shapeProblem {
			return shapeProblem{Shape: circle{Kind: "circle", Radius: e.radius}}
		}),
	}})}), geta.WithUnion(shapes))
	_, err := getaclient.Call[empty, ok](t.Context(), c.Typed(), http.MethodGet, "/s", nil)
	p, found := getaclient.ProblemAs[shapeProblem](err)
	if !found {
		t.Fatal(err)
	}
	if ci, isCircle := p.Shape.(circle); !isCircle || ci.Radius != 2 {
		t.Fatalf("%#v", p.Shape)
	}
	if got := c.Get("/s").ProblemAs[shapeProblem](); got.Shape.(circle).Radius != 2 {
		t.Fatalf("%#v", got)
	}
}

// An output header is read into the field's type: a bool, an unsigned
// integer, a float, through a pointer too; the first value of a repeated
// header.

type typedHeaders struct {
	Flag bool     `header:"X-Flag"`
	N    uint16   `header:"X-N"`
	F    float64  `header:"X-F"`
	P    *float32 `header:"X-P"`
	S    string   `header:"X-S"`
}

func TestClientReadsHeadersOfEveryType(t *testing.T) {
	half := float32(0.5)
	c := getatest.New(t, one("/h", get(func(context.Context, *empty) (*typedHeaders, error) {
		return &typedHeaders{Flag: true, N: 65535, F: 1.25, P: &half, S: "s"}, nil
	})))
	h, err := getaclient.Call[empty, typedHeaders](t.Context(), c.Typed(), http.MethodGet, "/h", nil)
	if err != nil || !h.Flag || h.N != 65535 || h.F != 1.25 || h.P == nil || *h.P != 0.5 || h.S != "s" {
		t.Fatal(h, err)
	}
	// A header that repeats is read from its first value.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for k, vs := range map[string][]string{"X-Flag": {"true", "false"}, "X-N": {"7", "8"}, "X-F": {"1.5", "x"},
			"X-P": {"0.25", "9"}, "X-S": {"first", "second"}} {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	h, err = getaclient.Call[empty, typedHeaders](t.Context(), &getaclient.Client{Base: srv.URL, HTTP: srv.Client()}, http.MethodGet, "/h", nil)
	if err != nil || !h.Flag || h.N != 7 || h.F != 1.5 || h.P == nil || *h.P != 0.25 || h.S != "first" {
		t.Fatal(h, err)
	}
}

// An Error reads as its status, the problem's title, its type when it has
// one, its detail, and each violation; without a problem, as the status and
// its reason phrase.
func TestClientErrorText(t *testing.T) {
	gone := errors.New("gone")
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/g", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *empty) (*ok, error) { return nil, gone },
			geta.Doc{Failures: []geta.Failure{geta.On(gone, http.StatusGone, "it went away").Type("https://errors.example/gone")}})}},
		{Path: "/n/{id}", Route: get(idHandler)},
	}})
	_, err := getaclient.Call[empty, ok](t.Context(), c.Typed(), http.MethodGet, "/g", nil)
	if got := err.Error(); got != "410 Gone (https://errors.example/gone): it went away" {
		t.Errorf("%q", got)
	}
	_, err = getaclient.Call[struct {
		ID string `path:"id"`
	}, ok](t.Context(), &getaclient.Client{Base: c.URL(), HTTP: c.HTTP()}, http.MethodGet, "/n/{id}", &struct {
		ID string `path:"id"`
	}{ID: "x"})
	if got := err.Error(); got != `400 Bad Request: the request does not match its contract; path id: expected integer, got string` {
		t.Errorf("%q", got)
	}
	if got := (&getaclient.Error{Status: http.StatusBadGateway}).Error(); got != "502 Bad Gateway" {
		t.Errorf("%q", got)
	}
}

// An Error's Problem carries omitted, the count of violations not listed.
func TestClientErrorCarriesOmitted(t *testing.T) {
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/j", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *rqManyIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}},
	}})
	// Sent as a map: no a, and 60 members the body does not know.
	type in struct {
		P    int            `query:"p"`
		Q    int            `query:"q"`
		Body map[string]int `body:"json"`
	}
	body := map[string]int{}
	for i := range 60 {
		body[fmt.Sprintf("m%02d", i)] = 1
	}
	_, err := getaclient.Call[in, ok](t.Context(), &getaclient.Client{Base: c.URL(), HTTP: c.HTTP()}, http.MethodPost, "/j", &in{P: 1, Q: 2, Body: body})
	e, isError := errors.AsType[*getaclient.Error](err)
	if !isError || e.Problem == nil || len(e.Problem.Errors) != 50 || e.Problem.Omitted != 11 {
		t.Fatalf("%v", err)
	}
}
