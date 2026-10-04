package getaclient_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaclient"
)

// Rules of getaclient that no other test asserts.

type specOut struct {
	N int `json:"n"`
}

type specIDIn struct {
	ID string `path:"id"`
}

type specMapIn struct {
	M map[string]string `query:"m"`
}

// serve answers every request with status, contentType, and body, and
// counts the requests it is sent.
func serve(t *testing.T, status int, contentType, body string) (*getaclient.Client, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Seen", "yes")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &getaclient.Client{Base: srv.URL, HTTP: srv.Client()}, &n
}

// A success whose body does not decode into Out is an error naming the call,
// not an *Error.
func TestADecodeFailureIsAnError(t *testing.T) {
	c, _ := serve(t, http.StatusOK, "application/json", "not json")
	_, err := getaclient.Call[struct{}, specOut](context.Background(), c, http.MethodGet, "/x", nil)
	if err == nil || !strings.HasPrefix(err.Error(), "getaclient: GET /x: ") {
		t.Fatal(err)
	}
	if _, isErr := errors.AsType[*getaclient.Error](err); isErr {
		t.Fatal("a decode failure is an *Error")
	}
}

type specRaw struct {
	Body []byte `body:"text/csv"`
}

type specLocated struct {
	Location string `header:"Location"`
}

// A success whose body Out reads as JSON is the call's only with a JSON
// media type (application/json or application/*+json, parameters aside);
// another, or none, is an *Error carrying the response. An output that reads
// no JSON takes any Content-Type, and a 3xx, as its success.
func TestASuccessOfAnotherMediaTypeIsAnError(t *testing.T) {
	ctx := context.Background()
	for ct, want := range map[string]bool{
		"application/json":                   true,
		"application/json; charset=utf-8":    true,
		"application/vnd.example+json":       true,
		"application/problem+json":           true,
		"text/plain; charset=utf-8":          false,
		"text/json":                          false,
		"":                                   false,
		"application/json; charset=\"broken": false,
	} {
		c, _ := serve(t, http.StatusOK, ct, `{"n":1}`)
		out, err := getaclient.Call[struct{}, specOut](ctx, c, http.MethodGet, "/x", nil)
		if want {
			if err != nil || out.N != 1 {
				t.Errorf("%q: %v %v", ct, out, err)
			}
			continue
		}
		e, isErr := errors.AsType[*getaclient.Error](err)
		if !isErr || e.Status != http.StatusOK || string(e.Body) != `{"n":1}` || e.Header.Get("X-Seen") != "yes" {
			t.Errorf("%q: %v", ct, err)
		}
	}
	c, _ := serve(t, http.StatusOK, "text/csv", "a,b")
	if raw, err := getaclient.Call[struct{}, specRaw](ctx, c, http.MethodGet, "/x", nil); err != nil || string(raw.Body) != "a,b" {
		t.Fatal(raw, err)
	}
	c, _ = serve(t, http.StatusTemporaryRedirect, "text/html", "<a>moved</a>")
	if loc, err := getaclient.Call[struct{}, specLocated](ctx, c, http.MethodGet, "/x", nil); err != nil || loc == nil {
		t.Fatal(loc, err)
	}
	e, isErr := errors.AsType[*getaclient.Error](getaclient.CallNoBody[struct{}](ctx, c, http.MethodGet, "/x", nil))
	if !isErr || e.Status != http.StatusTemporaryRedirect || string(e.Body) != "<a>moved</a>" {
		t.Fatal(e)
	}
}

// What the call cannot render is an error before anything is sent: a path
// field the template lacks, a template parameter the input does not bind, a
// value of no parameter type.
func TestCallRefusesBeforeSending(t *testing.T) {
	c, n := serve(t, http.StatusOK, "application/json", `{"n":1}`)
	ctx := context.Background()
	_, err := getaclient.Call[specIDIn, specOut](ctx, c, http.MethodGet, "/x", &specIDIn{ID: "1"})
	if err == nil || !strings.Contains(err.Error(), `template "/x" has no {id}`) {
		t.Error("path field the template lacks:", err)
	}
	_, err = getaclient.Call[struct{}, specOut](ctx, c, http.MethodGet, "/x/{id}", nil)
	if err == nil || !strings.Contains(err.Error(), "input binds no value for {id}") {
		t.Error("template parameter the input does not bind:", err)
	}
	_, err = getaclient.Call[specMapIn, specOut](ctx, c, http.MethodGet, "/x", &specMapIn{M: map[string]string{"a": "b"}})
	if err == nil || !strings.Contains(err.Error(), "is not a parameter type") {
		t.Error("a value of no parameter type:", err)
	}
	if got := n.Load(); got != 0 {
		t.Fatalf("%d requests sent", got)
	}
}

// Any status but a 2xx or a 3xx other than 304 is an *Error carrying the
// status, the body, and the header; its Problem is set when the
// Content-Type is exactly application/problem+json and the body decodes.
func TestErrorCarriesTheResponse(t *testing.T) {
	ctx := context.Background()
	problem := `{"type":"about:blank","title":"Not Found","status":404,"detail":"gone"}`
	for ct, wantProblem := range map[string]bool{
		geta.ProblemContentType:                     true,
		geta.ProblemContentType + "; charset=utf-8": false,
		"application/json":                          false,
	} {
		c, _ := serve(t, http.StatusNotFound, ct, problem)
		_, err := getaclient.Call[struct{}, specOut](ctx, c, http.MethodGet, "/x", nil)
		e, isErr := errors.AsType[*getaclient.Error](err)
		if !isErr || e.Status != http.StatusNotFound || string(e.Body) != problem || e.Header.Get("X-Seen") != "yes" {
			t.Fatalf("%s: %v", ct, err)
		}
		if (e.Problem != nil) != wantProblem || wantProblem && e.Problem.Detail != "gone" {
			t.Errorf("%s: Problem %+v", ct, e.Problem)
		}
	}
	c, _ := serve(t, http.StatusNotFound, geta.ProblemContentType, "not json")
	_, err := getaclient.Call[struct{}, specOut](ctx, c, http.MethodGet, "/x", nil)
	if e, isErr := errors.AsType[*getaclient.Error](err); !isErr || e.Problem != nil {
		t.Fatalf("a body that is no problem: %v", err)
	}
	// An Error's text leaves out a type that is empty, as it does about:blank.
	e := &getaclient.Error{Status: http.StatusConflict, Problem: &geta.Problem{Title: "Conflict", Detail: "taken"}}
	if got := e.Error(); got != "409 Conflict: taken" {
		t.Error(got)
	}
}

var errSpecGone = errors.New("gone")

type specDepIn struct {
	Gone bool `query:"gone"`
}

// ResponseHeader hands the caller the header of the response the call
// receives: a success's, a deprecated operation's Deprecation and Sunset
// among it, which no output field can hold, for Call and CallNoBody alike;
// and a failure's, the one the *Error carries. Deprecation reads the two
// moments back as the Doc gives them.
func TestResponseHeaderReadsADeprecatedSuccess(t *testing.T) {
	deprecatedAt := time.Date(2023, 6, 30, 23, 59, 59, 0, time.UTC)
	sunsetAt := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	doc := geta.Doc{Deprecated: true, Deprecation: deprecatedAt, Sunset: sunsetAt,
		Failures: []geta.Failure{geta.On(errSpecGone, http.StatusGone, "gone")}}
	a, err := geta.New(geta.Table{Routes: []geta.Entry{{Path: "/old", Route: geta.Route{
		Get: geta.Op(http.StatusOK, func(_ context.Context, in *specDepIn) (*specOut, error) {
			if in.Gone {
				return nil, errSpecGone
			}
			return &specOut{N: 1}, nil
		}, doc),
		Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *struct{}) error { return nil }, doc),
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	c := &getaclient.Client{Base: srv.URL, HTTP: srv.Client()}
	ctx := context.Background()

	var h http.Header
	out, err := getaclient.Call[specDepIn, specOut](ctx, c, http.MethodGet, "/old", nil, getaclient.ResponseHeader(&h))
	if err != nil || out.N != 1 {
		t.Fatal(out, err)
	}
	if h.Get("Deprecation") != "@1688169599" || h.Get("Sunset") != "Fri, 01 Jan 2027 00:00:00 GMT" {
		t.Errorf("Call: %v", h)
	}
	if dep, sunset := getaclient.Deprecation(h); !dep.Equal(deprecatedAt) || !sunset.Equal(sunsetAt) {
		t.Errorf("Deprecation: %v %v", dep, sunset)
	}

	h = nil
	if err := getaclient.CallNoBody[struct{}](ctx, c, http.MethodDelete, "/old", nil, getaclient.ResponseHeader(&h)); err != nil {
		t.Fatal(err)
	}
	if dep, sunset := getaclient.Deprecation(h); !dep.Equal(deprecatedAt) || !sunset.Equal(sunsetAt) {
		t.Errorf("CallNoBody: %v", h)
	}

	h = nil
	_, err = getaclient.Call[specDepIn, specOut](ctx, c, http.MethodGet, "/old", &specDepIn{Gone: true}, getaclient.ResponseHeader(&h))
	e, isErr := errors.AsType[*getaclient.Error](err)
	if !isErr || e.Status != http.StatusGone || h.Get("Deprecation") != "@1688169599" || h.Get("Content-Type") != e.Header.Get("Content-Type") {
		t.Fatalf("failure: %v %v", err, h)
	}
	if dep, _ := getaclient.Deprecation(e.Header); !dep.Equal(deprecatedAt) {
		t.Errorf("Error.Header: %v", e.Header)
	}
}

// A ResponseHeader given a nil pointer is an error before anything is sent;
// a call that receives no response leaves the header as it was.
func TestResponseHeaderRefusesANilPointer(t *testing.T) {
	c, n := serve(t, http.StatusOK, "application/json", `{"n":1}`)
	_, err := getaclient.Call[struct{}, specOut](context.Background(), c, http.MethodGet, "/x", nil, getaclient.ResponseHeader(nil))
	if err == nil || !strings.Contains(err.Error(), "nil ResponseHeader") || n.Load() != 0 {
		t.Fatal(err, n.Load())
	}
	h := http.Header{"Kept": {"yes"}}
	_, err = getaclient.Call[specIDIn, specOut](context.Background(), c, http.MethodGet, "/x", &specIDIn{ID: "1"}, getaclient.ResponseHeader(&h))
	if err == nil || h.Get("Kept") != "yes" {
		t.Fatal(err, h)
	}
}

// Deprecation reads a structured-field Date and an HTTP-date, and gives the
// zero time for a header absent, repeated, or of another form.
func TestDeprecationReadsTheHeadersForms(t *testing.T) {
	cases := []struct {
		h           http.Header
		dep, sunset time.Time
	}{
		{http.Header{}, time.Time{}, time.Time{}},
		{http.Header{"Deprecation": {"@-1"}, "Sunset": {"Sunday, 06-Nov-94 08:49:37 GMT"}},
			time.Unix(-1, 0).UTC(), time.Date(1994, 11, 6, 8, 49, 37, 0, time.UTC)},
		{http.Header{"Deprecation": {"1688169599"}, "Sunset": {"2027-01-01"}}, time.Time{}, time.Time{}},
		{http.Header{"Deprecation": {"@"}}, time.Time{}, time.Time{}},
		{http.Header{"Deprecation": {"@1.5"}}, time.Time{}, time.Time{}},
		{http.Header{"Deprecation": {"@+1"}}, time.Time{}, time.Time{}},
		{http.Header{"Deprecation": {"@1234567890123456"}}, time.Time{}, time.Time{}},
		{http.Header{"Deprecation": {"@1", "@2"}, "Sunset": {"Fri, 01 Jan 2027 00:00:00 GMT", "Fri, 01 Jan 2027 00:00:00 GMT"}}, time.Time{}, time.Time{}},
	}
	for _, c := range cases {
		dep, sunset := getaclient.Deprecation(c.h)
		if !dep.Equal(c.dep) || !sunset.Equal(c.sunset) || dep.IsZero() != c.dep.IsZero() {
			t.Errorf("%v: %v %v", c.h, dep, sunset)
		}
	}
}

type specMembers struct {
	With string `json:"with"`
}

// ProblemAs reports false for an error that is no *Error, an *Error with no
// problem, and a problem that does not decode as P.
func TestProblemAsReportsWhatItCannotRead(t *testing.T) {
	if _, ok := getaclient.ProblemAs[specMembers](errors.New("plain")); ok {
		t.Error("a plain error")
	}
	if _, ok := getaclient.ProblemAs[specMembers](&getaclient.Error{Status: 502, Body: []byte("bad gateway")}); ok {
		t.Error("no problem")
	}
	body := []byte(`{"type":"about:blank","title":"Conflict","status":409,"with":7}`)
	if _, ok := getaclient.ProblemAs[specMembers](&getaclient.Error{Status: 409, Problem: &geta.Problem{Status: 409}, Body: body}); ok {
		t.Error("a member of another type")
	}
	body = []byte(`{"type":"about:blank","title":"Conflict","status":409,"with":"b"}`)
	if p, ok := getaclient.ProblemAs[specMembers](&getaclient.Error{Status: 409, Problem: &geta.Problem{Status: 409}, Body: body}); !ok || p.With != "b" {
		t.Error("a problem that decodes:", p, ok)
	}
}
