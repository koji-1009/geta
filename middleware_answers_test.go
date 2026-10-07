package geta_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// What Middleware.Answers documents depends on the status: a 4xx or 5xx is a
// problem, a success the operation answers lists the middleware's reason
// after the status's text, another 2xx or 3xx is a response with no content,
// and a 1xx or a number outside 100 to 599 is refused.

// httpsOnly redirects a request that did not arrive over TLS to its https
// URL with 308, as http.Redirect writes it (a short text/html body on a GET).
func httpsOnly(location ...geta.HeaderType) geta.Middleware {
	return geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Plain-Http") != "" {
				http.Redirect(w, r, r.Header.Get("X-Plain-Http"), http.StatusPermanentRedirect)
				return
			}
			next.ServeHTTP(w, r)
		})
	}).Answers(http.StatusPermanentRedirect, "The request did not arrive over https").
		Header(http.StatusPermanentRedirect, "Location", "The https URL", location...)
}

// A 2xx or 3xx the operation does not answer is documented with no content,
// never a problem: its description the middleware's reason, or the reasons
// of every middleware that answers it joined, its headers those they
// declare. A 4xx stays a problem, and a 204 is as any other.
func TestMiddlewareAnswersAnotherSuccessWithoutContent(t *testing.T) {
	moved := geta.Use(noop).Answers(http.StatusPermanentRedirect, "The resource moved")
	accepted := geta.Use(noop).Answers(http.StatusCreated, "Queued for creation").Answers(http.StatusNoContent, "Nothing to say")
	teapot := geta.Use(noop).Answers(http.StatusTeapot, "A teapot")
	a := accepts(t, withRoot(one("/x", get(okHandler)), httpsOnly(), moved, accepted, teapot))
	responses := at(t, doc(t, a), "paths", "/x", "get", "responses").(map[string]any)
	for status, want := range map[string]string{
		"308": `{"description":"The request did not arrive over https; The resource moved","headers":{"Location":{"description":"The https URL","required":false,"schema":{"type":"string"}}}}`,
		"201": `{"description":"Queued for creation"}`,
		"204": `{"description":"Nothing to say"}`,
		"418": `{"content":{"application/problem+json":{"schema":{"$ref":"#/components/schemas/GetaProblem"}}},"description":"A teapot"}`,
	} {
		if got := compact(t, at(t, responses, status)); got != want {
			t.Errorf("%s: %s, want %s", status, got, want)
		}
	}
	// The operation's own success is as before.
	if got := at(t, responses, "200", "description"); got != "OK" {
		t.Error("200:", got)
	}
	// So are the options operations, which run the root scope.
	if got := compact(t, at(t, doc(t, a), "paths", "/x", "options", "responses", "308")); !strings.HasPrefix(got, `{"description":"The request did not arrive over https; The resource moved","headers":{"Location":`) {
		t.Error("OPTIONS 308:", got)
	}
}

// A 304 answers a conditional GET or HEAD that would have been answered 200
// (RFC 9110 section 15.4.5): a middleware's is documented, with no content,
// on a GET whose success is 200, as ETag's, and on no other operation.
func TestMiddlewareAnswers304OnlyWhereItCanBe(t *testing.T) {
	cache := geta.Use(noop).Answers(http.StatusNotModified, "The cached representation is current").
		Header(http.StatusNotModified, "ETag", "The current entity tag")
	a := accepts(t, geta.Table{
		Root: geta.Scope{cache},
		Routes: []geta.Entry{
			{Path: "/x", Route: geta.Route{
				Get:  geta.Op(http.StatusOK, okHandler, geta.Doc{}),
				Post: geta.Op(http.StatusOK, okHandler, geta.Doc{}),
			}},
			{Path: "/made", Route: geta.Route{Get: geta.Op(http.StatusCreated, okHandler, geta.Doc{})}},
		},
	})
	m := doc(t, a)
	// geta's own 304, to If-None-Match: * where no validator is declared, is
	// listed after it.
	if got := compact(t, at(t, m, "paths", "/x", "get", "responses", "304")); got != `{"description":"The cached representation is current; If-None-Match: *; the operation declares no validator (its input does not embed geta.Conditional)","headers":{"ETag":{"description":"The current entity tag","required":false,"schema":{"type":"string"}}}}` {
		t.Error("GET 304:", got)
	}
	for _, where := range [][]string{{"/x", "post"}, {"/made", "get"}, {"/x", "options"}} {
		if _, has := at(t, m, "paths", where[0], where[1], "responses").(map[string]any)["304"]; has {
			t.Errorf("%s %s documents 304", where[1], where[0])
		}
	}
}

// getatest checks a middleware's redirect as the document states it: the
// headers it declares with a type, and no body, so http.Redirect's text/html
// one passes. CORS lets a page read the Location.
func TestConformsChecksAMiddlewaresRedirect(t *testing.T) {
	for _, c := range []struct {
		to   string
		want string
	}{
		{"https://example.com/x", ""},
		{"http://example.com/x", "a header does not match the documented schema: Location: "},
	} {
		rec := &recorder{TB: t}
		res := getatest.New(rec, withRoot(one("/x", get(okHandler)), httpsOnly(geta.HeaderOf[string]("pattern=^https://")))).
			With("X-Plain-Http", c.to).Get("/x")
		if res.Status != http.StatusPermanentRedirect {
			t.Fatal(res.Status)
		}
		switch {
		case c.want == "" && len(rec.errs) != 0:
			t.Errorf("%s: %q", c.to, rec.errs)
		case c.want != "" && (len(rec.errs) != 1 || !strings.Contains(rec.errs[0], c.want)):
			t.Errorf("%s: %q, want %q", c.to, rec.errs, c.want)
		}
	}
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.CORS(geta.AllowOrigins("*")), httpsOnly()))
	sameHeaders(t, "GET /x", do(t, a, "GET", "/x").Header().Get("Access-Control-Expose-Headers"), "Location")
}

// A middleware that answers a 1xx, which precedes a response rather than
// being one, or a number that is no HTTP status, is refused in every scope,
// naming the middleware and its position.
func TestMiddlewareAnswersMistakesAreRefused(t *testing.T) {
	early := geta.Use(noop).Answers(http.StatusEarlyHints, "Hints")
	rejects(t, withRoot(one("/x", get(okHandler)), geta.Use(noop), early),
		`root scope: middleware 1 (middleware): Answers(103, "Hints"): 103 is an informational (1xx) status`)
	rejects(t, one("/x", get(okHandler), geta.Scope{geta.Use(noop).Answers(600, "Odd")}),
		`/x: scope 0: middleware 0 (middleware): Answers(600, "Odd"): 600 is not an HTTP status (100 to 599)`)
	rejects(t, one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Scope: geta.Scope{geta.Ordered(geta.OrderAuthorize, noop).Answers(0, "")}})}),
		`GET /x: operation scope: middleware 0 (authorize): Answers(0, ""): 0 is not an HTTP status (100 to 599)`)
	rejects(t, withRoot(one("/x", get(okHandler)), geta.Use(noop).Answers(http.StatusSwitchingProtocols, "Switch")),
		`Answers(101, "Switch"): 101 is an informational (1xx) status`)
	// 200 and 599 are statuses a middleware answers.
	accepts(t, withRoot(one("/x", get(okHandler)), geta.Use(noop).Answers(200, "Cached").Answers(599, "Odd")))
}
