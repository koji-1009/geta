package geta_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaclient"
	"github.com/koji-1009/geta/getatest"
)

// Email is an application's own mail address: format email, as the
// application takes one. Mail carries addresses RFC 5321 refuses, such as
// a..b@docomo.ne.jp, so it reads any text of one @ between a local part and
// a domain of at least one dot, with no space or control character.
type Email struct{ local, domain string }

func (Email) SchemaFormat() string { return "email" }

func (e Email) MarshalText() ([]byte, error) { return []byte(e.local + "@" + e.domain), nil }

func (e *Email) UnmarshalText(b []byte) error {
	s := string(b)
	local, domain, ok := strings.Cut(s, "@")
	if !ok || local == "" || strings.Contains(domain, "@") || !strings.Contains(domain, ".") ||
		strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") ||
		strings.ContainsFunc(s, func(r rune) bool { return r <= ' ' || r == 0x7F }) {
		return errors.New("not a mail address this application takes")
	}
	*e = Email{local, domain}
	return nil
}

type signup struct {
	Name  string  `json:"name"`
	Email Email   `json:"email"`
	CC    []Email `json:"cc"`
}

type signupIn struct {
	Invite *Email `query:"invite"`
	From   *Email `header:"X-From"`
	Body   signup `body:"json"`
}

type signupOut struct {
	Contact Email  `header:"X-Contact"`
	Body    signup `body:"json"`
}

func signupApp(got **signupIn) geta.Table {
	h := func(_ context.Context, in *signupIn) (*signupOut, error) {
		*got = in
		return &signupOut{Contact: in.Body.Email, Body: in.Body}, nil
	}
	return one("/signup", geta.Route{Post: geta.Op(http.StatusCreated, h, geta.Doc{})})
}

// An application's own type carries the format email: the document names it,
// and the type's UnmarshalText, lenient as the application is, holds every
// request to it, in a body and as a parameter. geta does not hold it to an
// RFC of its own.
func TestAnApplicationTypeCarriesTheFormatEmail(t *testing.T) {
	var got *signupIn
	app := accepts(t, signupApp(&got))
	d := doc(t, app)
	params := map[string]string{}
	for _, p := range at(t, d, "paths", "/signup", "post", "parameters").([]any) {
		params[p.(map[string]any)["name"].(string)] = compact(t, p.(map[string]any)["schema"])
	}
	for name, want := range map[string]string{
		"invite": `{"format":"email","maxLength":4096,"type":"string"}`,
		// geta does not know the application's grammar, so a header states
		// what a header field value carries, as any text type's does.
		"X-From": `{"format":"email","maxLength":4096,"pattern":"^(?:[^\\x00-\\x20\\x7F](?:[^\\x00-\\x08\\x0A-\\x1F\\x7F]*[^\\x00-\\x20\\x7F])?)?$","type":"string"}`,
	} {
		if params[name] != want {
			t.Errorf("parameter %s: %s, want %s", name, params[name], want)
		}
	}
	// signup is read and written: signup-Input is the request's schema.
	if g := compact(t, at(t, d, "components", "schemas", "signup", "properties", "email")); g != `{"format":"email","type":"string"}` {
		t.Error(g)
	}
	props := at(t, d, "components", "schemas", "signup-Input", "properties").(map[string]any)
	if g := compact(t, props["email"]); g != `{"format":"email","maxLength":4096,"type":"string"}` {
		t.Error(g)
	}
	if g := compact(t, props["cc"]); g != `{"items":{"format":"email","maxLength":4096,"type":"string"},"maxItems":8192,"type":"array"}` {
		t.Error(g)
	}

	send := func(q url.Values, from, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/signup?"+q.Encode(), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if from != "" {
			req.Header.Set("X-From", from)
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec
	}
	rec := send(url.Values{"invite": {"a..b@docomo.ne.jp"}}, ".x@example.com",
		`{"name":"n","email":"a..b@docomo.ne.jp","cc":["\"q\"@example.com"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got.Invite.local != "a..b" || got.From.local != ".x" || got.Body.Email.domain != "docomo.ne.jp" || got.Body.CC[0].local != `"q"` {
		t.Fatalf("%+v", got)
	}
	if rec.Header().Get("X-Contact") != "a..b@docomo.ne.jp" {
		t.Fatal(rec.Header())
	}

	rec = send(url.Values{"invite": {"nobody"}}, "a@b", `{"name":"n","email":"a@@b.c","cc":["a@b.c","a b@c.d"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, want := range []string{
		`"path":"invite","message":"\"nobody\" is not a valid email"`,
		`"path":"X-From","message":"\"a@b\" is not a valid email"`,
		`"path":"$.email","message":"\"a@@b.c\" is not a valid email"`,
		`"path":"$.cc[1]","message":"\"a b@c.d\" is not a valid email"`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("missing %s in %s", want, rec.Body)
		}
	}
}

// getaclient sends the type as the server reads it and reads it back, and
// getatest finds every response as the document states it.
func TestAnApplicationEmailRoundTrips(t *testing.T) {
	var got *signupIn
	c := getatest.New(t, signupApp(&got))
	var mail, invite Email
	if err := mail.UnmarshalText([]byte("a..b@docomo.ne.jp")); err != nil {
		t.Fatal(err)
	}
	if err := invite.UnmarshalText([]byte("x.@example.com")); err != nil {
		t.Fatal(err)
	}
	in := &signupIn{Invite: &invite, From: &mail, Body: signup{Name: "n", Email: mail, CC: []Email{invite}}}
	out, err := getaclient.Call[signupIn, signupOut](t.Context(), c.Typed(), http.MethodPost, "/signup", in)
	if err != nil {
		t.Fatal(err)
	}
	if *got.Invite != invite || *got.From != mail || got.Body.Email != mail || out.Contact != mail || out.Body.CC[0] != invite {
		t.Fatalf("sent %+v, server read %+v, client read %+v", in, got, out)
	}
	if r := c.Post("/signup?invite=a%40b", `{"name":"n","email":"a@b.c","cc":[]}`); r.Status != http.StatusBadRequest ||
		!strings.Contains(r.Text(), `"path":"invite","message":"\"a@b\" is not a valid email"`) {
		t.Fatal(r.Status, r.Text())
	}
}

// day is an application's own date: geta's format date, which the
// application reads its own way, "today" among its dates.
type day struct{ s string }

func (day) SchemaFormat() string { return "date" }

func (d day) MarshalText() ([]byte, error) { return []byte(d.s), nil }

func (d *day) UnmarshalText(b []byte) error {
	if _, err := geta.ParseDate(string(b)); err != nil && string(b) != "today" {
		return err
	}
	d.s = string(b)
	return nil
}

type dayIn struct {
	On   day     `query:"on"`
	Body *dayOut `body:"json"`
}

type dayOut struct {
	On day `json:"on"`
}

// An application's type may carry a format geta's own types carry too: its
// UnmarshalText alone holds a request to it, not geta's check of the format.
func TestAnApplicationTypeMayCarryAFormatGetaChecks(t *testing.T) {
	var got *dayIn
	app := accepts(t, one("/d", geta.Route{Post: geta.Op(http.StatusOK, func(_ context.Context, in *dayIn) (*dayOut, error) {
		got = in
		return &dayOut{On: in.On}, nil
	}, geta.Doc{})}))
	if g := compact(t, at(t, doc(t, app), "components", "schemas", "dayOut-Input", "properties", "on")); g != `{"format":"date","maxLength":4096,"type":"string"}` {
		t.Error(g)
	}
	r := do(t, app, "POST", "/d?on=today")
	if r.Code != http.StatusOK || got.On.s != "today" {
		t.Fatal(r.Code, r.Body)
	}
	req := httptest.NewRequest(http.MethodPost, "/d?on=2024-02-29", strings.NewReader(`{"on":"today"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || got.Body.On.s != "today" {
		t.Fatal(rec.Code, rec.Body)
	}
	r = do(t, app, "POST", "/d?on=2023-02-29")
	if r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), `"path":"on","message":"\"2023-02-29\" is not a valid date"`) {
		t.Fatal(r.Code, r.Body)
	}
}

// stringFormat names a format but is no text type: nothing would hold a
// request to it.
type stringFormat string

func (stringFormat) SchemaFormat() string { return "email" }

// jsonFormat names a format but reads itself by its JSON methods, past
// UnmarshalText.
type jsonFormat struct{ Email }

func (jsonFormat) MarshalJSON() ([]byte, error) { return []byte(`""`), nil }
func (*jsonFormat) UnmarshalJSON([]byte) error  { return nil }

// noFormat names no format.
type noFormat struct{ Email }

func (noFormat) SchemaFormat() string { return "" }

// A type whose format no request would be held to is refused, as is a
// schema tag's format on a type that has its own, and a schema tag naming a
// format geta does not check: that is an application type's to carry.
func TestAFormatTypeTheDocumentCannotTrustIsRefused(t *testing.T) {
	type stringMember struct {
		S stringFormat `json:"s"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*stringMember, error) { return nil, nil })),
		"type geta_test.stringFormat has a SchemaFormat method but is not a text type")
	type stringParam struct {
		S stringFormat `query:"s"`
	}
	rejects(t, one("/x", get(func(context.Context, *stringParam) (*ok, error) { return nil, nil })),
		"type geta_test.stringFormat has a SchemaFormat method but is not a text type")
	type jsonMember struct {
		J jsonFormat `json:"j"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*jsonMember, error) { return nil, nil })),
		"type geta_test.jsonFormat has both a SchemaFormat method and its own JSON methods")
	type noMember struct {
		N noFormat `json:"n"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*noMember, error) { return nil, nil })),
		"type geta_test.noFormat: SchemaFormat names no format")
	type tagged struct {
		E Email `query:"e" schema:"format=date"`
	}
	rejects(t, one("/x", get(func(context.Context, *tagged) (*ok, error) { return nil, nil })),
		`E: schema keyword format: the type has its own format`)
	// getavet, which sees SchemaFormat but not what it returns, gets the
	// same verdict and text for the kind it names such a type by; the type
	// takes the string keywords a text type takes.
	if err := geta.CheckSchemaTag("format=date", "format"); err == nil ||
		err.Error() != "schema keyword format: the type has its own format" {
		t.Error(err)
	}
	if err := geta.CheckSchemaTag("minLength=3,pattern=@", "format"); err != nil {
		t.Error(err)
	}
	type stringEmail struct {
		E string `json:"e" schema:"format=email"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*stringEmail, error) { return nil, nil })),
		`schema keyword format: unknown format "email"`, "geta checks date, date-time")
}
