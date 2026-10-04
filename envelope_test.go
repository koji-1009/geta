package geta_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Output envelopes: what they may declare, and how embedded fields are
// written and documented.

type listItem struct {
	Name string `json:"name"`
}

// A schema tag on an output envelope's body would be neither documented nor
// checked, so it is refused.
func TestRejectsSchemaTagOnOutputBody(t *testing.T) {
	type out struct {
		Body []listItem `body:"json" schema:"minItems=1"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*out, error) { return nil, nil })),
		"out.Body", "an output body takes no schema tag")
}

// Set-Cookie is written by cookie fields, whose document entry would replace
// a header field's, so an envelope cannot name it as a header.
func TestRejectsSetCookieHeaderField(t *testing.T) {
	type out struct {
		Raw     string       `header:"set-cookie"`
		Session *http.Cookie `cookie:"sid"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*out, error) { return nil, nil })),
		"out.Raw", `header "set-cookie" is reserved; use a *http.Cookie field`)
	type alone struct {
		Raw string `header:"Set-Cookie"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*alone, error) { return nil, nil })),
		"alone.Raw", `header "Set-Cookie" is reserved`)
}

// An envelope's header names are checked at New: a name net/http would not
// send, a header geta sets for the body, and a schema tag are refused.
func TestEnvelopeHeaderNamesAreChecked(t *testing.T) {
	type badName struct {
		X string `header:"X Bad"`
		B ok     `body:"json"`
	}
	rejects(t, one("/h", get(func(context.Context, *empty) (*badName, error) { return nil, nil })),
		`"X Bad" is not a valid header name`)
	type badChar struct {
		X string `header:"X-Ü"`
	}
	rejects(t, one("/h", get(func(context.Context, *empty) (*badChar, error) { return nil, nil })),
		`"X-Ü" is not a valid header name`)
	type contentType struct {
		CT string `header:"Content-Type"`
		B  ok     `body:"json"`
	}
	rejects(t, one("/h", get(func(context.Context, *empty) (*contentType, error) { return nil, nil })),
		`header "Content-Type" is reserved for the body geta writes`)
	type contentLength struct {
		N int `header:"content-length"`
	}
	rejects(t, one("/h", get(func(context.Context, *empty) (*contentLength, error) { return nil, nil })),
		`header "content-length" is reserved for the body geta writes`)
	type nosniff struct {
		N string `header:"X-Content-Type-Options"`
	}
	rejects(t, one("/h", get(func(context.Context, *empty) (*nosniff, error) { return nil, nil })),
		`header "X-Content-Type-Options" is reserved for the body geta writes`)
	type headerSchema struct {
		N int `header:"X-N" schema:"minimum=1"`
	}
	rejects(t, one("/h", get(func(context.Context, *empty) (*headerSchema, error) { return nil, nil })),
		"an output header takes no schema tag")
	type cookieSchema struct {
		C *http.Cookie `cookie:"sid" schema:"minLength=1"`
	}
	rejects(t, one("/h", get(func(context.Context, *empty) (*cookieSchema, error) { return nil, nil })),
		"a cookie field takes no schema tag")

	// Every tchar is accepted.
	type tchars struct {
		X string `header:"X-!#$%&'*+.^_|~09az"`
	}
	accepts(t, one("/h", get(func(context.Context, *empty) (*tchars, error) { return &tchars{X: "v"}, nil })))
}

// An envelope's body is always written: a pointer body, which could be
// absent although the document lists content for the status, is refused.
func TestOptionalEnvelopeBodyIsRefused(t *testing.T) {
	type optional struct {
		Location string `header:"Location"`
		B        *ok    `body:"json"`
	}
	rejects(t, one("/o", get(func(context.Context, *empty) (*optional, error) { return nil, nil })),
		"the body field is a pointer; use geta_test.ok")
}

type envMeta struct {
	Req     string       `header:"X-Req"`
	Session *http.Cookie `cookie:"sid"`
}

// stampText promotes time.Time's methods: it is a text type.
type stampText struct {
	time.Time
}

// An envelope's untagged embedded struct has its fields written and
// documented by their own tags, as an input's are bound, exported or not;
// an envelope whose tags are all in an embedded struct is an envelope.
func TestEnvelopeWritesEmbeddedFields(t *testing.T) {
	type env struct {
		envMeta
		Body ok `body:"json"`
	}
	a := accepts(t, one("/x", get(func(context.Context, *empty) (*env, error) {
		return &env{envMeta{"r1", &http.Cookie{Value: "v"}}, ok{true}}, nil
	})))
	rec := do(t, a, http.MethodGet, "/x")
	if rec.Code != 200 || rec.Header().Get("X-Req") != "r1" || !strings.HasPrefix(rec.Header().Get("Set-Cookie"), "sid=v") ||
		strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	headers := at(t, doc(t, a), "paths", "/x", "get", "responses", "200", "headers").(map[string]any)
	if _, ok := headers["X-Req"]; !ok {
		t.Errorf("X-Req is not documented: %v", headers)
	}
	if _, ok := headers["Set-Cookie"]; !ok {
		t.Errorf("Set-Cookie is not documented: %v", headers)
	}

	type headersOnly struct {
		envMeta
	}
	a = accepts(t, one("/x", get(func(context.Context, *empty) (*headersOnly, error) {
		return &headersOnly{envMeta{Req: "r2"}}, nil
	})))
	rec = do(t, a, http.MethodGet, "/x")
	if rec.Code != 200 || rec.Header().Get("X-Req") != "r2" || rec.Body.Len() != 0 {
		t.Fatalf("%d %v %q", rec.Code, rec.Header(), rec.Body)
	}
}

// What an envelope cannot take from an embedded field is refused, with the
// text CheckEnvelopeField gives, which getavet reports.
func TestEnvelopeEmbeddedRefusals(t *testing.T) {
	type pointer struct {
		*envMeta
		Body ok `body:"json"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*pointer, error) { return nil, nil })),
		"pointer.envMeta: embedded pointer types are not supported in an envelope; embed geta_test.envMeta")
	type text struct {
		stampText
		Body ok `body:"json"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*text, error) { return nil, nil })),
		"text.stampText: embedded geta_test.stampText is a text type with no fields; tag it header")
	type schema struct {
		envMeta `schema:"minLength=1"`
		Body    ok `body:"json"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*schema, error) { return nil, nil })),
		"schema.envMeta: an embedded struct takes no schema tag")
	type repeated struct {
		envMeta
		Again string `header:"x-req"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*repeated, error) { return nil, nil })),
		`repeated.Again: empty or repeated header name "x-req"`)
	type badInner struct {
		Raw string `header:"Content-Type"`
	}
	type inner struct {
		badInner
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*inner, error) { return nil, nil })),
		`badInner.Raw: header "Content-Type" is reserved for the body geta writes`)
}
