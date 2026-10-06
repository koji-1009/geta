package geta_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaclient"
	"github.com/koji-1009/geta/getatest"
)

type echoIn struct {
	ID    int64      `path:"id"`
	Tags  []string   `query:"tag"`
	Since *time.Time `query:"since"`
	Trace string     `header:"X-Trace"`
	Sid   *string    `cookie:"sid"`
	Body  *item      `body:"json"`
}

type echoOut struct {
	Echo     string       `header:"X-Echo"`
	Count    *int         `header:"X-Count"`
	Session  *http.Cookie `cookie:"sid"`
	Response item         `body:"json"`
}

// The client sends every location geta binds and reads every one an
// envelope writes, with the handler's own types.
func TestTypedClientCoversEveryLocation(t *testing.T) {
	var got *echoIn
	h := func(ctx context.Context, in *echoIn) (*echoOut, error) {
		got = in
		n := len(in.Tags)
		resp := item{Name: "none"}
		if in.Body != nil {
			resp = *in.Body
		}
		return &echoOut{Echo: in.Trace, Count: &n, Session: &http.Cookie{Value: "s2"}, Response: resp}, nil
	}
	c := getatest.New(t, one("/things/{id}", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})})).Typed()
	since := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	sid := "s1"
	in := &echoIn{ID: 42, Tags: []string{"a", "b c"}, Since: &since, Trace: "t-1", Sid: &sid, Body: &item{Name: "n", Price: 2}}
	out, err := getaclient.Call[echoIn, echoOut](t.Context(), c, http.MethodPost, "/things/{id}", in)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 42 || strings.Join(got.Tags, "|") != "a|b c" || !got.Since.Equal(since) || got.Trace != "t-1" || *got.Sid != "s1" || *got.Body != *in.Body {
		t.Fatalf("sent: %+v", got)
	}
	if out.Echo != "t-1" || *out.Count != 2 || out.Session.Value != "s2" || out.Response != *in.Body {
		t.Fatalf("received: %+v", out)
	}
	// Optional inputs left nil are not sent.
	if _, err := getaclient.Call[echoIn, echoOut](t.Context(), c, http.MethodPost, "/things/{id}", &echoIn{ID: 1, Tags: []string{"z"}}); err != nil {
		t.Fatal(err)
	}
	if got.Since != nil || got.Sid != nil || got.Body != nil {
		t.Fatalf("%+v", got)
	}
}

// A failure is an *getaclient.Error carrying the problem, so its type tells
// rows that share a status apart.
func TestTypedClientReturnsTheProblem(t *testing.T) {
	gone := errors.New("gone")
	h := func(context.Context, *empty) (*ok, error) { return nil, gone }
	r := geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{Failures: []geta.Failure{
		geta.On(gone, http.StatusNotFound, "deleted").Type("https://errors.example/gone"),
	}})}
	c := getatest.New(t, one("/x", r)).Typed()
	_, err := getaclient.Call[empty, ok](t.Context(), c, http.MethodGet, "/x", nil)
	e, isErr := errors.AsType[*getaclient.Error](err)
	if !isErr || e.Status != 404 || e.Problem.Type != "https://errors.example/gone" || e.Problem.Detail != "deleted" {
		t.Fatalf("%v", err)
	}
}

// A call naming no operation, or the wrong types, fails the test before
// anything is sent.
func TestTypedClientChecksTheCallAgainstTheApp(t *testing.T) {
	type other struct {
		X int `json:"x"`
	}
	for name, call := range map[string]func(c *getaclient.Client) error{
		"unknown template": func(c *getaclient.Client) error {
			_, err := getaclient.Call[empty, ok](t.Context(), c, http.MethodGet, "/nope", nil)
			return err
		},
		"wrong method": func(c *getaclient.Client) error {
			_, err := getaclient.Call[empty, ok](t.Context(), c, http.MethodPost, "/x", nil)
			return err
		},
		"wrong output": func(c *getaclient.Client) error {
			_, err := getaclient.Call[empty, other](t.Context(), c, http.MethodGet, "/x", nil)
			return err
		},
		"wrong input": func(c *getaclient.Client) error {
			_, err := getaclient.Call[other, ok](t.Context(), c, http.MethodGet, "/x", nil)
			return err
		},
		"no body expected": func(c *getaclient.Client) error {
			return getaclient.CallNoBody(t.Context(), c, http.MethodGet, "/x", &empty{})
		},
	} {
		rec := &recorder{TB: t}
		c := getatest.New(rec, one("/x", get(okHandler))).Typed()
		if err := call(c); err == nil || len(rec.errs) != 1 || !strings.Contains(rec.errs[0], "getatest:") {
			t.Errorf("%s: %v %q", name, err, rec.errs)
		}
	}
}

type namedIn struct {
	Name string `path:"name"`
}

type namedOut struct {
	Op   string `json:"op"`
	Name string `json:"name"`
}

// A path value of "." or ".." reaches the operation the template names as
// that value, not as a dot segment the server cleans into another route;
// an empty value, which geta never matches, is refused before sending.
func TestTypedClientSendsDotValuesAsValues(t *testing.T) {
	calls := 0
	meta := func(_ context.Context, in *namedIn) (*namedOut, error) {
		calls++
		return &namedOut{"meta", in.Name}, nil
	}
	file := func(_ context.Context, in *namedIn) (*namedOut, error) {
		calls++
		return &namedOut{"file", in.Name}, nil
	}
	gc := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/files/{name}/meta", Route: get(meta)},
		{Path: "/files/{name}", Route: get(file)},
	}})
	// A production client follows redirects.
	hc := *gc.HTTP()
	hc.CheckRedirect = nil
	c := &getaclient.Client{Base: gc.URL(), HTTP: &hc}
	for _, name := range []string{".", "..", "a/b", "x y"} {
		out, err := getaclient.Call[namedIn, namedOut](t.Context(), c, http.MethodGet, "/files/{name}/meta", &namedIn{Name: name})
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		if out.Op != "meta" || out.Name != name {
			t.Errorf("%q: %+v", name, out)
		}
	}
	calls = 0
	_, err := getaclient.Call[namedIn, namedOut](t.Context(), c, http.MethodGet, "/files/{name}/meta", &namedIn{})
	if err == nil || !strings.Contains(err.Error(), `path parameter "name" is empty`) {
		t.Fatalf("empty value: %v", err)
	}
	if calls != 0 {
		t.Fatalf("an empty value reached %d handlers", calls)
	}
}

// A literal segment is escaped as a path segment (here its ';' and ','),
// and the server matches it decoded. A literal a path cannot hold as written
// ('?', '#', whitespace, a control character) is refused by geta.New.
func TestTypedClientEscapesLiteralSegments(t *testing.T) {
	h := func(context.Context, *empty) (*ok, error) { return &ok{true}, nil }
	c := getatest.New(t, one("/what;a,b", get(h))).Typed()
	out, err := getaclient.Call[empty, ok](t.Context(), c, http.MethodGet, "/what;a,b", nil)
	if err != nil || !out.OK {
		t.Fatalf("%+v %v", out, err)
	}
}

type clientMeta struct {
	Req     string       `header:"X-Req"`
	Session *http.Cookie `cookie:"sid"`
}

type clientInner struct {
	Body ok `body:"json"`
}

type clientOuter struct {
	clientInner
}

type clientEnv struct {
	clientMeta
	clientOuter
	Count *int `header:"X-Count"`
}

type clientHeadersOnly struct {
	clientMeta
}

// getaclient reads an envelope's embedded fields by the rule geta writes
// them by (vet.EnvelopeEmbedded): headers, cookies, and the body declared
// in an embedded struct, at any depth, and an envelope whose tags are all
// in an embedded struct.
func TestClientReadsEnvelopeEmbeddedFields(t *testing.T) {
	n := 3
	tbl := geta.Table{Routes: []geta.Entry{
		{Path: "/env", Route: get(func(context.Context, *empty) (*clientEnv, error) {
			return &clientEnv{clientMeta{"r1", &http.Cookie{Value: "v"}}, clientOuter{clientInner{ok{true}}}, &n}, nil
		})},
		{Path: "/headers", Route: get(func(context.Context, *empty) (*clientHeadersOnly, error) {
			return &clientHeadersOnly{clientMeta{Req: "r2"}}, nil
		})},
	}}
	c := getatest.New(t, tbl).Typed()
	out, err := getaclient.Call[empty, clientEnv](t.Context(), c, http.MethodGet, "/env", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Req != "r1" || out.Session == nil || out.Session.Value != "v" || !out.Body.OK || out.Count == nil || *out.Count != 3 {
		t.Fatalf("received: %+v", out)
	}
	h, err := getaclient.Call[empty, clientHeadersOnly](t.Context(), c, http.MethodGet, "/headers", nil)
	if err != nil {
		t.Fatal(err)
	}
	if h.Req != "r2" || h.Session != nil {
		t.Fatalf("received: %+v", h)
	}
}

type clientAuthIn struct {
	Auth []string `header:"Authorization"`
	Body ok       `body:"json"`
}

type clientAuthOut struct {
	Auth string `json:"auth"`
}

// A header the call sets itself, from its input or for its body, wins over
// the client's default of the same name; a default fills only what the call
// does not set.
func TestClientCallHeaderWinsOverDefault(t *testing.T) {
	h := func(_ context.Context, in *clientAuthIn) (*clientAuthOut, error) {
		return &clientAuthOut{strings.Join(in.Auth, "|")}, nil
	}
	c := getatest.New(t, one("/x", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})})).Typed()
	c.Header = http.Header{"Authorization": {"Bearer default"}, "Content-Type": {"text/plain"}}
	out, err := getaclient.Call[clientAuthIn, clientAuthOut](t.Context(), c, http.MethodPost, "/x",
		&clientAuthIn{Auth: []string{"Bearer mine"}, Body: ok{true}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Auth != "Bearer mine" {
		t.Fatalf("the server read Authorization %q; want the call's own", out.Auth)
	}
	out, err = getaclient.Call[clientAuthIn, clientAuthOut](t.Context(), c, http.MethodPost, "/x", &clientAuthIn{Body: ok{true}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Auth != "Bearer default" {
		t.Fatalf("the server read Authorization %q; want the client's default", out.Auth)
	}
}
