package geta_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// echo answers with whatever it was given, formatted.
type echoed struct {
	Got string `json:"got"`
}

func echo[In any](path string) geta.Table {
	h := func(ctx context.Context, in *In) (*echoed, error) {
		b, err := json.Marshal(in)
		return &echoed{Got: string(b)}, err
	}
	return one(path, geta.Route{
		Get:  geta.Op(http.StatusOK, h, geta.Doc{}),
		Post: geta.Op(http.StatusOK, h, geta.Doc{}),
	})
}

// echoBody is echo for an input with a body, which a GET does not take: it
// answers POST alone.
func echoBody[In any](path string) geta.Table {
	h := func(ctx context.Context, in *In) (*echoed, error) {
		b, err := json.Marshal(in)
		return &echoed{Got: string(b)}, err
	}
	return one(path, geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})})
}

func got(t *testing.T, res *getatest.Response) string {
	t.Helper()
	if res.Status != 200 {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
	return res.JSON[echoed]().Got
}

func violations(t *testing.T, res *getatest.Response) []string {
	t.Helper()
	if res.Status != 400 {
		t.Fatalf("want 400, got %d %s", res.Status, res.Body)
	}
	var out []string
	for _, v := range res.Problem().Errors {
		out = append(out, v.In+" "+v.Path+": "+v.Message)
	}
	return out
}

func same(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

type queryIn struct {
	Limit  *int      `query:"limit" schema:"minimum=1,maximum=100"`
	Q      string    `query:"q"`
	Tags   *[]string `query:"tag" schema:"maxItems=2"`
	Strict *bool     `query:"strict"`
	Ratio  *float64  `query:"ratio"`
}

func TestQueryBinding(t *testing.T) {
	c := getatest.New(t, echo[queryIn]("/x"))
	if g := got(t, c.Get("/x?q=hi&limit=5&tag=a&tag=b&strict=true&ratio=0.5")); g != `{"Limit":5,"Q":"hi","Tags":["a","b"],"Strict":true,"Ratio":0.5}` {
		t.Fatalf("%s", g)
	}
	if g := got(t, c.Get("/x?q=hi")); g != `{"Limit":null,"Q":"hi","Tags":null,"Strict":null,"Ratio":null}` {
		t.Fatalf("%s", g)
	}
	same(t, violations(t, c.Get("/x")), "query q: missing required parameter")
	same(t, violations(t, c.Get("/x?q=a&limit=0")), "query limit: 0 is less than minimum 1")
	same(t, violations(t, c.Get("/x?q=a&limit=x")), "query limit: expected integer, got string")
	same(t, violations(t, c.Get("/x?q=a&limit=1.0")), "query limit: expected integer, got string")
	same(t, violations(t, c.Get("/x?q=a&q=b")), "query q: given 2 times, but takes one value")
	same(t, violations(t, c.Get("/x?q=a&tag=1&tag=2&tag=3")), "query tag: array length 3 exceeds maxItems 2")
	same(t, violations(t, c.Get("/x?q=a&strict=yes")), "query strict: expected boolean, got string")
	same(t, violations(t, c.Get("/x?q=a&ratio=NaN")), "query ratio: expected number, got string")
	// Every violation is reported at once.
	same(t, violations(t, c.Get("/x?limit=500&strict=1")),
		"query limit: 500 is greater than maximum 100",
		"query q: missing required parameter",
		"query strict: expected boolean, got string")
	if res := c.Get("/x?q=%zz"); res.Status != 400 || res.Problem().Detail != "the query string is malformed" {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
}

type headerIn struct {
	Request uuid.UUID  `header:"x-request-id"`
	Since   *time.Time `header:"If-Modified-Since-Iso"`
	Session *string    `cookie:"sid"`
}

func TestHeaderAndCookieBinding(t *testing.T) {
	c := getatest.New(t, echo[headerIn]("/x"))
	id := "123e4567-e89b-12d3-a456-426614174000"
	g := got(t, c.With("X-Request-Id", id).With("Cookie", "a=1; sid=s1; sid=s2").Get("/x"))
	if g != `{"Request":"`+id+`","Since":null,"Session":"s1"}` {
		t.Fatalf("%s", g)
	}
	same(t, violations(t, c.Get("/x")), "header x-request-id: missing required parameter")
	same(t, violations(t, c.With("X-Request-Id", "nope").Get("/x")), `header x-request-id: "nope" is not a valid uuid`)
	same(t, violations(t, c.With("X-Request-Id", id).With("If-Modified-Since-Iso", "yesterday").Get("/x")),
		`header If-Modified-Since-Iso: "yesterday" is not a valid date-time`)
}

// level is an application type with its own text form: the parsing lives in
// one place, UnmarshalText.
type level int

func (l level) MarshalText() ([]byte, error) { return []byte([]string{"low", "high"}[l]), nil }
func (l *level) UnmarshalText(b []byte) error {
	switch string(b) {
	case "low":
		*l = 0
	case "high":
		*l = 1
	default:
		return fmt.Errorf("bad level")
	}
	return nil
}

type pathIn struct {
	ID    uuid.UUID `path:"id"`
	Level level     `path:"level"`
}

func TestPathBindingWithTextTypes(t *testing.T) {
	c := getatest.New(t, echo[pathIn]("/things/{id}/{level}"))
	got(t, c.Get("/things/123e4567-e89b-12d3-a456-426614174000/high"))
	same(t, violations(t, c.Get("/things/123e4567-e89b-12d3-a456-426614174000/mid")), `path level: "mid" is not a valid level`)
	same(t, violations(t, c.Get("/things/x/low")), `path id: "x" is not a valid uuid`)
}

type item struct {
	Name  string `json:"name" schema:"minLength=1"`
	Price int    `json:"price" schema:"minimum=0"`
}

type bodyIn struct {
	Body item `body:"json"`
}

type optionalBodyIn struct {
	Body *item `body:"json"`
}

func TestBodyBinding(t *testing.T) {
	c := getatest.New(t, echoBody[bodyIn]("/x"))
	got(t, c.Post("/x", `{"name":"a","price":1}`))
	same(t, violations(t, c.Post("/x", "")), "body $: missing required request body")
	same(t, violations(t, c.Post("/x", `{"name":"","price":-1}`)),
		"body $.name: string length 0 is shorter than minLength 1",
		"body $.price: -1 is less than minimum 0")
	same(t, violations(t, c.Post("/x", `{"name":"a","price":1,"extra":true}`)), "body $.extra: unknown member")
	same(t, violations(t, c.Post("/x", `{"name":"a","price":1,"name":"b"}`)), "body $.name: duplicate object key")
	same(t, violations(t, c.Post("/x", `{"name":"a"`)), "body $: unexpected end of JSON input")
	same(t, violations(t, c.Post("/x", `nope`)), "body $: malformed JSON at byte 1: invalid character 'o' in literal null (expecting 'u')")
	got(t, c.With("Content-Type", "application/merge-patch+json").Post("/x", `{"name":"a","price":1}`))

	o := getatest.New(t, echoBody[optionalBodyIn]("/x"))
	if g := got(t, o.Post("/x", "")); g != `{"Body":null}` {
		t.Fatalf("%s", g)
	}
}

func TestBodyLimitIs413AtTheExactBoundary(t *testing.T) {
	limits := geta.DefaultLimits
	limits.MaxBodyBytes = 64
	app, err := geta.New(echoBody[bodyIn]("/x"), geta.WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	c := getatest.Serve(t, app)
	body := func(n int) string {
		s := `{"name":"","price":1}`
		return strings.Replace(s, `""`, `"`+strings.Repeat("a", n-len(s))+`"`, 1)
	}
	if b := body(64); len(b) != 64 {
		t.Fatal(len(b))
	}
	got(t, c.Post("/x", body(64)))
	res := c.Post("/x", body(65))
	if res.Status != 413 || !strings.Contains(res.Problem().Detail, "exceeds 64 bytes") {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
}

// A Limits built field by field leaves the caps it does not name zero; New
// refuses each rather than serve an app that refuses every body.
func TestWithLimitsRefusesAZeroCap(t *testing.T) {
	_, err := geta.New(echoBody[bodyIn]("/x"), geta.WithLimits(geta.Limits{MaxBodyBytes: 64 << 10}))
	if err == nil {
		t.Fatal("New accepted Limits with zero caps")
	}
	for _, f := range []string{"MaxStringLength is 0", "MaxItems is 0", "MaxDepth is 0"} {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("%v: no %q", err, f)
		}
	}
	if strings.Contains(err.Error(), "MaxBodyBytes") || strings.Contains(err.Error(), "MaxResponseBuffer") {
		t.Errorf("%v", err)
	}
	lim := geta.DefaultLimits
	lim.MaxBodyBytes = -1
	if _, err := geta.New(echoBody[bodyIn]("/x"), geta.WithLimits(lim)); err == nil || !strings.Contains(err.Error(), "MaxBodyBytes is -1") {
		t.Errorf("%v", err)
	}
	lim = geta.DefaultLimits
	lim.MaxMultipartMemory, lim.MaxResponseBuffer = 0, 0
	if _, err := geta.New(echoBody[bodyIn]("/x"), geta.WithLimits(lim)); err != nil {
		t.Error(err)
	}
}

// An envelope carries headers and cookies beside its body.
type created struct {
	Location string       `header:"Location"`
	Version  *int         `header:"X-Version"`
	Session  *http.Cookie `cookie:"sid"`
	Theme    *http.Cookie `cookie:"theme"`
	Body     item         `body:"json"`
}

func TestEnvelope(t *testing.T) {
	h := func(ctx context.Context, _ *empty) (*created, error) {
		sid := &http.Cookie{Value: "abc", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 3600}
		return &created{Location: "/items/1", Session: sid, Body: item{Name: "a", Price: 1}}, nil
	}
	c := getatest.New(t, one("/x", geta.Route{Post: geta.Op(http.StatusCreated, h, geta.Doc{})}))
	res := c.Post("/x", nil)
	if res.Status != 201 || res.Header.Get("Location") != "/items/1" || res.Header.Get("X-Version") != "" {
		t.Fatalf("%d %v", res.Status, res.Header)
	}
	if sc := res.Header.Values("Set-Cookie"); len(sc) != 1 || sc[0] != "sid=abc; Path=/; Max-Age=3600; HttpOnly; SameSite=Lax" {
		t.Fatalf("%q", sc)
	}
	if res.Text() != `{"name":"a","price":1}` {
		t.Fatalf("%s", res.Body)
	}
}

// A cookie net/http's Cookie.Valid refuses is never written: the response
// is a 500, so no attribute can be smuggled into the header.
func TestInvalidCookieIsADefect(t *testing.T) {
	type out struct {
		C *http.Cookie `cookie:"sid"`
	}
	for _, ck := range []*http.Cookie{
		{Value: "b; Path=/evil"},
		{Value: "b\r\nSet-Cookie: c=d"},
		{Value: `b\c`},
		{Value: "v", Domain: "x;y"},
		{Value: "v", Path: "/x\r\n"},
		{Value: "v", Partitioned: true},
		{Name: "other", Value: "v"},
	} {
		h := func(ctx context.Context, _ *empty) (*out, error) { return &out{C: ck}, nil }
		res := getatest.New(t, one("/x", geta.Route{Post: geta.Op(http.StatusNoContent, h, geta.Doc{})})).Post("/x", nil)
		if res.Status != 500 || res.Header.Get("Set-Cookie") != "" {
			t.Errorf("%+v: %d %q", ck, res.Status, res.Header.Values("Set-Cookie"))
		}
	}
}

func TestRejectsInvalidCookieField(t *testing.T) {
	type badName struct {
		C *http.Cookie `cookie:"a b"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*badName, error) { return nil, nil })), `"a b" is not a valid cookie name`)
	type notPointer struct {
		C http.Cookie `cookie:"sid"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*notPointer, error) { return nil, nil })), "a cookie field has type *http.Cookie")
}

func TestCookieAttributesRender(t *testing.T) {
	type out struct {
		C *http.Cookie `cookie:"sid"`
	}
	exp := time.Date(2021, 6, 9, 10, 18, 14, 0, time.UTC)
	h := func(ctx context.Context, _ *empty) (*out, error) {
		return &out{C: &http.Cookie{Name: "sid", Value: "abc", MaxAge: 3600, Expires: exp, Domain: "example.com",
			Path: "/app", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}}, nil
	}
	res := getatest.New(t, one("/x", geta.Route{Post: geta.Op(http.StatusNoContent, h, geta.Doc{})})).Post("/x", nil)
	want := "sid=abc; Path=/app; Domain=example.com; Expires=Wed, 09 Jun 2021 10:18:14 GMT; Max-Age=3600; HttpOnly; Secure; SameSite=Strict"
	if res.Header.Get("Set-Cookie") != want {
		t.Fatalf("%q", res.Header.Get("Set-Cookie"))
	}
}

// An input's untagged embedded pointer is refused, exported or not: binding
// its fields would allocate it, and a skipped one would leave its tags
// unread and the document without its parameters.
func TestRejectsEmbeddedPointerInInput(t *testing.T) {
	type hidden struct {
		*vetAuth
	}
	rejects(t, one("/x", get(func(context.Context, *hidden) (*ok, error) { return nil, nil })),
		"hidden.vetAuth", "embedded pointer types are not supported in an input; embed geta_test.vetAuth")
	type shown struct {
		*VetPaging
	}
	rejects(t, one("/x", get(func(context.Context, *shown) (*ok, error) { return nil, nil })),
		"shown.VetPaging", "embedded pointer types are not supported in an input; embed geta_test.VetPaging")
	// A tagged one is a parameter like any other.
	type tagged struct {
		*VetToken `header:"X-Token"`
	}
	accepts(t, one("/x", get(func(context.Context, *tagged) (*ok, error) { return nil, nil })))
}

// An input's untagged embedded text type has no fields to bind; it is
// refused, exported or not, rather than skipped.
func TestRejectsEmbeddedTextTypeInInput(t *testing.T) {
	type exported struct {
		time.Time
		ID string `query:"id"`
	}
	rejects(t, one("/x", get(func(context.Context, *exported) (*ok, error) { return nil, nil })),
		"exported.Time: embedded time.Time is a text type with no fields; tag it path, query, header, or cookie")
	type hidden struct {
		stampText
	}
	rejects(t, one("/x", get(func(context.Context, *hidden) (*ok, error) { return nil, nil })),
		"hidden.stampText: embedded geta_test.stampText is a text type with no fields")
	// Tagged, it is a parameter.
	type tagged struct {
		time.Time `query:"at"`
	}
	accepts(t, one("/x", get(func(context.Context, *tagged) (*ok, error) { return nil, nil })))
}

// net/http reads no cookie under a name that is not a token, so an input's
// cookie name is checked as an output's is.
func TestRejectsInvalidInputCookieName(t *testing.T) {
	type in struct {
		S string `cookie:"a b"`
	}
	rejects(t, one("/x", get(func(context.Context, *in) (*ok, error) { return nil, nil })), `in.S: "a b" is not a valid cookie name`)
	type fine struct {
		S string `cookie:"a_b"`
	}
	accepts(t, one("/x", get(func(context.Context, *fine) (*ok, error) { return nil, nil })))
}

// No request carries a header field under a name that is not a token, so an
// input's header name is checked as an output's is.
func TestRejectsInvalidInputHeaderName(t *testing.T) {
	for _, name := range []string{"X Name", "X:Name", "Ä-Name", "X(Name)"} {
		_, err := geta.CheckInputField(geta.VetField{Name: "S", Exported: true, Type: "string", Kind: "string", Tag: reflect.StructTag(`header:"` + name + `"`)}, map[string]string{})
		want := fmt.Sprintf(`S: %q is not a valid header name`, name)
		if err == nil || err.Error() != want {
			t.Errorf("%s: CheckInputField: %v", name, err)
		}
	}
	type in struct {
		S string `header:"X Name"`
	}
	rejects(t, one("/x", get(func(context.Context, *in) (*ok, error) { return nil, nil })),
		`in.S: "X Name" is not a valid header name`)
	type fine struct {
		S string `header:"X-Name_1.~"`
	}
	accepts(t, one("/x", get(func(context.Context, *fine) (*ok, error) { return nil, nil })))
}
