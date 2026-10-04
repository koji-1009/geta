package geta_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// The headers a middleware sends with what it answers: declared with
// Middleware.Header, documented on that status, and exposed by CORS.

// busy answers 503 with a Retry-After when X-Busy is set.
func busy() geta.Middleware {
	return geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Busy") != "" {
				w.Header().Set("Retry-After", "5")
				geta.WriteProblem(w, http.StatusServiceUnavailable, "busy")
				return
			}
			next.ServeHTTP(w, r)
		})
	}).Answers(http.StatusServiceUnavailable, "Busy").Header(http.StatusServiceUnavailable, "Retry-After", "Seconds until it is not busy")
}

// A rate limiter's 429 states its Retry-After, Secure's 401 its
// WWW-Authenticate, and a middleware's own Header its header, each optional,
// on the status it answers; a public operation behind the gate states
// neither 401 nor its challenge.
func TestMiddlewareHeadersAreDocumented(t *testing.T) {
	a := accepts(t, geta.Table{
		Root: geta.Scope{limit(1), busy(), gate()},
		Routes: []geta.Entry{
			{Path: "/x", Route: get(okHandler)},
			{Path: "/open", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{}})}},
		},
	})
	m := doc(t, a)
	responses := at(t, m, "paths", "/x", "get", "responses")
	if got := compact(t, at(t, responses, "429", "headers")); got != `{"Retry-After":{"description":"The whole seconds until the rate limit admits a request","required":false,"schema":{"format":"int64","minimum":1,"type":"integer"}}}` {
		t.Error("429:", got)
	}
	if got := compact(t, at(t, responses, "401", "headers")); got != `{"WWW-Authenticate":{"description":"A challenge for each scheme that refused the request, sent with the gate's 401 (RFC 9110 section 11.6.1)","required":false,"schema":{"type":"string"}}}` {
		t.Error("401:", got)
	}
	if got := compact(t, at(t, responses, "503", "headers")); got != `{"Retry-After":{"description":"Seconds until it is not busy","required":false,"schema":{"type":"string"}}}` {
		t.Error("503:", got)
	}
	// The gate's 503 is a cause of the same status; the header stays optional.
	if got := at(t, responses, "503", "description"); got != "Busy; The credential source is unavailable" {
		t.Error("503 description:", got)
	}
	open := at(t, m, "paths", "/open", "get", "responses").(map[string]any)
	if _, has := open["401"]; has {
		t.Error("a public operation documents 401:", open["401"])
	}
	if _, has := at(t, open, "429").(map[string]any)["headers"]; !has {
		t.Error("the public operation's 429 lacks Retry-After")
	}
	// The options operations run the root scope: its 401 and 429 state them too.
	if _, has := at(t, m, "paths", "/x", "options", "responses", "401", "headers").(map[string]any)["WWW-Authenticate"]; !has {
		t.Error("OPTIONS /x 401 lacks WWW-Authenticate")
	}
	if _, has := at(t, m, "paths", "/x", "options", "responses", "429", "headers").(map[string]any)["Retry-After"]; !has {
		t.Error("OPTIONS /x 429 lacks Retry-After")
	}
}

// A header is refused where the document could not state it: on a status the
// middleware does not answer, and under a name that is not a token.
func TestMiddlewareHeaderMistakesAreRefused(t *testing.T) {
	m := geta.Use(noop).Answers(http.StatusServiceUnavailable, "Busy")
	rejects(t, withRoot(one("/x", get(okHandler)), m.Header(http.StatusTooManyRequests, "Retry-After", "")),
		"root scope: middleware 0 (middleware): header Retry-After: status 429 is not in Answers")
	rejects(t, one("/x", get(okHandler), geta.Scope{m.Header(http.StatusServiceUnavailable, "Retry After", "")}),
		`/x: scope 0: middleware 0 (middleware): header "Retry After" is not a token`)
	// OpenAPI ignores a response header named Content-Type.
	rejects(t, withRoot(one("/x", get(okHandler)), m.Header(http.StatusServiceUnavailable, "content-type", "")),
		"root scope: middleware 0 (middleware): header content-type: Content-Type cannot be a declared response header")
	// Answers may come after Header.
	accepts(t, withRoot(one("/x", get(okHandler)), geta.Use(noop).Header(503, "Retry-After", "").Answers(503, "Busy")))
}

// A header declared with a type (HeaderOf) is documented with that type's
// schema and its constraints, as an envelope's header field is, a text type's
// format included; one declared without stays a string. CORS exposes it as
// any other.
func TestMiddlewareHeaderTypesAreDocumented(t *testing.T) {
	typed := geta.Use(noop).Answers(http.StatusTooManyRequests, "Limited").
		Header(http.StatusTooManyRequests, "Retry-After", "Seconds", geta.HeaderOf[int]("minimum=1")).
		Header(http.StatusTooManyRequests, "X-RateLimit-Remaining", "", geta.HeaderOf[uint32]("")).
		Header(http.StatusTooManyRequests, "X-RateLimit-Reset", "", geta.HeaderOf[time.Time]("")).
		Header(http.StatusTooManyRequests, "X-RateLimit-Policy", "", geta.HeaderOf[string]("enum=burst|steady")).
		Header(http.StatusTooManyRequests, "X-Plain", "")
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.CORS(geta.AllowOrigins("*")), typed))
	headers := at(t, doc(t, a), "paths", "/x", "get", "responses", "429", "headers")
	for name, want := range map[string]string{
		"Retry-After":           `{"description":"Seconds","required":false,"schema":{"format":"int64","minimum":1,"type":"integer"}}`,
		"X-RateLimit-Remaining": `{"required":false,"schema":{"maximum":4294967295,"minimum":0,"type":"integer"}}`,
		"X-RateLimit-Reset":     `{"required":false,"schema":{"format":"date-time","pattern":"^[0-9]{4}-[0-9]{2}-[0-9]{2}[Tt][0-9]{2}:[0-9]{2}:[0-5]","type":"string"}}`,
		"X-RateLimit-Policy":    `{"required":false,"schema":{"enum":["burst","steady"],"type":"string"}}`,
		"X-Plain":               `{"required":false,"schema":{"type":"string"}}`,
	} {
		if got := compact(t, at(t, headers, name)); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
	sameHeaders(t, "GET /x", do(t, a, "GET", "/x").Header().Get("Access-Control-Expose-Headers"),
		"Retry-After", "X-RateLimit-Remaining", "X-RateLimit-Reset", "X-RateLimit-Policy", "X-Plain")
}

// limiter answers every request 429, setting Retry-After to retry unless it
// is "" and X-Plain to a value of no type, as a problem, or as plain text
// when plain is set.
func limiter(retry string, plain bool) geta.Middleware {
	return geta.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if retry != "" {
				w.Header().Set("Retry-After", retry)
			}
			w.Header().Set("X-Plain", "  any, value at all; even this ")
			if plain {
				http.Error(w, "slow down", http.StatusTooManyRequests)
				return
			}
			geta.WriteProblem(w, http.StatusTooManyRequests, "rate limit exceeded")
		})
	}).Answers(http.StatusTooManyRequests, "Limited").
		Header(http.StatusTooManyRequests, "Retry-After", "Seconds", geta.HeaderOf[int]("minimum=1")).
		Header(http.StatusTooManyRequests, "X-Plain", "")
}

// getatest holds what a middleware sends to the type its Header declares
// (App.Conforms), whatever the body: a Retry-After of 0 or "soon" against
// HeaderOf[int]("minimum=1") fails the test that provoked it, one of 1, or
// none, passes, and a header declared without a type takes any value.
func TestConformsHoldsAMiddlewaresHeaderToItsType(t *testing.T) {
	for _, c := range []struct {
		retry string
		plain bool
		want  string
	}{
		{"0", false, "a header does not match the documented schema: Retry-After: 0 is less than minimum 1"},
		{"0", true, "a header does not match the documented schema: Retry-After: 0 is less than minimum 1"},
		{"soon", false, "a header does not match the documented schema: Retry-After: expected integer, got string"},
		{"soon", true, "a header does not match the documented schema: Retry-After: expected integer, got string"},
		{"1", false, ""},
		{"120", true, ""},
		{"", false, ""},
	} {
		rec := &recorder{TB: t}
		res := getatest.New(rec, withRoot(one("/x", get(okHandler)), limiter(c.retry, c.plain))).Get("/x")
		if res.Status != http.StatusTooManyRequests {
			t.Fatal(res.Status)
		}
		switch {
		case c.want == "" && len(rec.errs) != 0:
			t.Errorf("%q: %q", c.retry, rec.errs)
		case c.want != "" && (len(rec.errs) != 1 || !strings.Contains(rec.errs[0], c.want)):
			t.Errorf("%q plain=%v: %q, want %q", c.retry, c.plain, rec.errs, c.want)
		}
	}
}

// Conforms reads each header the document states with a declared schema as
// a request's header parameter is read, and holds it to that schema: a
// middleware's HeaderOf, a described row's header, and an output's header
// field, absent where it is not required, and on one line. A nil header
// checks none.
func TestConformsChecksDocumentedHeaders(t *testing.T) {
	typed := geta.Use(noop).Answers(http.StatusTooManyRequests, "Limited").
		Header(http.StatusTooManyRequests, "Retry-After", "Seconds", geta.HeaderOf[int]("minimum=1")).
		Header(http.StatusTooManyRequests, "X-RateLimit-Remaining", "", geta.HeaderOf[uint32]("")).
		Header(http.StatusTooManyRequests, "X-RateLimit-Reset", "", geta.HeaderOf[time.Time]("")).
		Header(http.StatusTooManyRequests, "X-RateLimit-Policy", "", geta.HeaderOf[string]("enum=burst|steady")).
		Header(http.StatusTooManyRequests, "X-Plain", "")
	a := accepts(t, withRoot(one("/x", get(okHandler)), typed))
	x := geta.Match{Template: "/x", Method: http.MethodGet, Operation: true}
	const limited = http.StatusTooManyRequests
	for _, c := range []struct {
		status int
		h      http.Header
		want   string
	}{
		{limited, nil, ""},
		{limited, http.Header{}, ""},
		{limited, http.Header{"Retry-After": {"1"}, "X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"2026-10-04T00:00:00Z"}, "X-Ratelimit-Policy": {"burst"}, "X-Plain": {"x, y"}}, ""},
		{limited, http.Header{"Retry-After": {"1", "2"}}, "Retry-After: sent on 2 lines, but takes one value"},
		{limited, http.Header{"Retry-After": {"1.5"}}, "Retry-After: expected integer, got string"},
		{limited, http.Header{"X-Ratelimit-Remaining": {"-1"}}, "X-RateLimit-Remaining: -1 is out of range for uint32"},
		{limited, http.Header{"X-Ratelimit-Reset": {"tomorrow"}}, "X-RateLimit-Reset: "},
		{limited, http.Header{"X-Ratelimit-Policy": {"wild"}}, "X-RateLimit-Policy: "},
		// Another status states none of them.
		{http.StatusOK, http.Header{"Retry-After": {"soon"}}, ""},
	} {
		err := a.Conforms(x, c.status, c.h, nil)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%d %v: %v; want %q", c.status, c.h, err, c.want)
		}
	}

	// A described row's header, optional where a plain cause shares its status.
	d := accepts(t, describedTable())
	f := geta.Match{Template: "/f", Method: http.MethodGet, Operation: true}
	if err := d.Conforms(f, http.StatusTooManyRequests, http.Header{"Retry-After": {"soon"}}, nil); err == nil || !strings.Contains(err.Error(), "Retry-After: expected integer, got string") {
		t.Error("row:", err)
	}
	if err := d.Conforms(f, http.StatusTooManyRequests, http.Header{}, nil); err != nil {
		t.Error("row absent:", err)
	}

	// An output's header field, required unless it is a pointer; the body is
	// checked beside it.
	e := accepts(t, one("/e", get(func(context.Context, *struct{}) (*headedOut, error) { return &headedOut{Count: 1}, nil })))
	m := geta.Match{Template: "/e", Method: http.MethodGet, Operation: true}
	for _, c := range []struct {
		h    http.Header
		body string
		want []string
	}{
		{http.Header{"X-Count": {"3"}}, `{"ok":true}`, nil},
		{http.Header{"X-Count": {"3"}, "X-Note": {"n"}}, "", nil},
		{http.Header{}, "", []string{"X-Count: missing required header"}},
		{http.Header{"X-Count": {"three"}}, `{"ok":"yes"}`, []string{"X-Count: expected integer, got string", "the body does not match"}},
	} {
		err := e.Conforms(m, http.StatusOK, c.h, []byte(c.body))
		if c.want == nil && err != nil || c.want != nil && err == nil {
			t.Errorf("%v %s: %v", c.h, c.body, err)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%v %s: %v; want %q", c.h, c.body, err, w)
			}
		}
	}
}

type lowerRetry struct {
	RetryAfter int `header:"retry-after"`
}

type upperRetry struct {
	RetryAfter int `header:"Retry-After"`
}

// A header is one header however its declarations spell its name (RFC 9110
// section 5.1): a status's response states it once, under the first row's
// spelling, required where every cause sets it, and a middleware's of the
// name and the same schema gives way to the row's, as it does under the same
// spelling.
func TestAHeaderIsStatedOnceWhateverItsCase(t *testing.T) {
	m := geta.Use(noop).Answers(http.StatusTooManyRequests, "Limited").
		Header(http.StatusTooManyRequests, "Retry-After", "", geta.HeaderOf[int](""))
	a := accepts(t, withRoot(one("/f", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Failures: []geta.Failure{
		geta.OnAsProblem(http.StatusTooManyRequests, "q", func(*quotaError) lowerRetry { return lowerRetry{} }),
	}})}), m))
	if got := compact(t, at(t, doc(t, a), "paths", "/f", "get", "responses", "429", "headers")); got != `{"retry-after":{"required":false,"schema":{"format":"int64","type":"integer"}}}` {
		t.Error("row and middleware:", got)
	}
	b := accepts(t, one("/f", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Failures: []geta.Failure{
		geta.OnAsProblem(http.StatusTooManyRequests, "q", func(*quotaError) lowerRetry { return lowerRetry{} }),
		geta.OnAsProblem(http.StatusTooManyRequests, "c", func(*conflictError) upperRetry { return upperRetry{} }),
	}})}))
	if got := compact(t, at(t, doc(t, b), "paths", "/f", "get", "responses", "429", "headers")); got != `{"retry-after":{"required":true,"schema":{"format":"int64","type":"integer"}}}` {
		t.Error("two rows:", got)
	}
	f := geta.Match{Template: "/f", Method: http.MethodGet, Operation: true}
	if err := b.Conforms(f, http.StatusTooManyRequests, http.Header{}, nil); err == nil || !strings.Contains(err.Error(), "retry-after: missing required header") {
		t.Error("Conforms:", err)
	}
}

// A middleware's typed header and another declaration of the name on its
// status — a described row's, the output's on a success status the
// middleware answers, another middleware's, or its own — state one header,
// so New refuses two schemas of it, however the name is spelled; one schema
// is one header. A middleware's header without a type admits any string,
// which the other narrows: it gives way, whichever comes first, and Conforms
// holds the header to the other's schema.
func TestMiddlewareHeaderSchemasAgree(t *testing.T) {
	const limited = http.StatusTooManyRequests
	typed := func(name, constraints string) geta.Middleware {
		return geta.Use(noop).Answers(limited, "Limited").Header(limited, name, "", geta.HeaderOf[int](constraints))
	}
	untyped := geta.Use(noop).Answers(limited, "Limited").Header(limited, "Retry-After", "Any")
	rowed := func(root ...geta.Middleware) geta.Table {
		return withRoot(one("/f", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Failures: []geta.Failure{
			geta.OnAsProblem(limited, "q", func(*quotaError) lowerRetry { return lowerRetry{} }),
		}})}), root...)
	}
	f := geta.Match{Template: "/f", Method: http.MethodGet, Operation: true}

	// A row's and a middleware's.
	rejects(t, rowed(typed("Retry-After", "minimum=1")),
		"GET /f (", `): status 429: failure row 0 (errors.As *geta_test.quotaError, described by geta_test.lowerRetry) states header retry-after with schema {"format":"int64","type":"integer"}, `+
			`but middleware 0 (middleware) states Retry-After with schema {"format":"int64","minimum":1,"type":"integer"}`)
	rejects(t, rowed(geta.Use(noop).Answers(limited, "Limited").Header(limited, "Retry-After", "", geta.HeaderOf[string](""))),
		`failure row 0 (errors.As *geta_test.quotaError, described by geta_test.lowerRetry) states header retry-after`, `states Retry-After with schema {"pattern":`)
	// Without a type, the middleware's gives way to the row's, and Conforms
	// holds what is sent to the row's.
	a := accepts(t, rowed(untyped))
	if got := compact(t, at(t, doc(t, a), "paths", "/f", "get", "responses", "429", "headers")); got != `{"retry-after":{"required":false,"schema":{"format":"int64","type":"integer"}}}` {
		t.Error("row and untyped middleware:", got)
	}
	if err := a.Conforms(f, limited, http.Header{"Retry-After": {"soon"}}, nil); err == nil || !strings.Contains(err.Error(), "retry-after: expected integer, got string") {
		t.Error("row and untyped middleware, Conforms:", err)
	}

	// Two middleware's, or one's twice, however spelled.
	x := one("/x", get(okHandler))
	rejects(t, withRoot(x, typed("Retry-After", "minimum=1"), typed("retry-after", "minimum=2")),
		`status 429: middleware 0 (middleware) states header Retry-After with schema {"format":"int64","minimum":1,"type":"integer"}, `+
			`but middleware 1 (middleware) states retry-after with schema {"format":"int64","minimum":2,"type":"integer"}`)
	rejects(t, one("/x", get(okHandler), geta.Scope{typed("Retry-After", "minimum=1").Header(limited, "Retry-After", "", geta.HeaderOf[string](""))}),
		`middleware 0 (middleware) states header Retry-After with schema {"format":"int64","minimum":1,"type":"integer"}, `+
			`but middleware 0 (middleware) states Retry-After with schema {"pattern":`)
	b := accepts(t, withRoot(x, typed("Retry-After", "minimum=1"), typed("RETRY-AFTER", "minimum=1")))
	if got := compact(t, at(t, doc(t, b), "paths", "/x", "get", "responses", "429", "headers")); got != `{"Retry-After":{"required":false,"schema":{"format":"int64","minimum":1,"type":"integer"}}}` {
		t.Error("one schema:", got)
	}
	// One without a type gives way to one with, which comes after it.
	b = accepts(t, withRoot(x, untyped, typed("Retry-After", "minimum=1")))
	if got := compact(t, at(t, doc(t, b), "paths", "/x", "get", "responses", "429", "headers")); got != `{"Retry-After":{"required":false,"schema":{"format":"int64","minimum":1,"type":"integer"}}}` {
		t.Error("untyped then typed:", got)
	}
	xm := geta.Match{Template: "/x", Method: http.MethodGet, Operation: true}
	if err := b.Conforms(xm, limited, http.Header{"Retry-After": {"0"}}, nil); err == nil || !strings.Contains(err.Error(), "Retry-After: 0 is less than minimum 1") {
		t.Error("untyped then typed, Conforms:", err)
	}

	// The output's and a middleware's that answers the success status itself.
	cached := func(typ ...geta.HeaderType) geta.Middleware {
		return geta.Use(noop).Answers(http.StatusOK, "Served from the cache").Header(http.StatusOK, "x-count", "", typ...)
	}
	e := func(m geta.Middleware) geta.Table {
		return withRoot(one("/e", get(func(context.Context, *struct{}) (*headedOut, error) { return &headedOut{Count: 1}, nil })), m)
	}
	rejects(t, e(cached(geta.HeaderOf[int]("minimum=5"))),
		`GET /e (`, `): status 200: the output states header X-Count with schema {"format":"int64","type":"integer"}, `+
			`but middleware 0 (middleware) states x-count with schema {"format":"int64","minimum":5,"type":"integer"}`)
	em := geta.Match{Template: "/e", Method: http.MethodGet, Operation: true}
	for _, typ := range [][]geta.HeaderType{{geta.HeaderOf[int]("")}, nil} {
		c := accepts(t, e(cached(typ...)))
		ok200 := at(t, doc(t, c), "paths", "/e", "get", "responses", "200")
		if got := compact(t, at(t, ok200, "headers", "X-Count")); got != `{"required":true,"schema":{"format":"int64","type":"integer"}}` {
			t.Errorf("%v: output and middleware: %s", typ, got)
		}
		if _, has := at(t, ok200, "headers").(map[string]any)["x-count"]; has {
			t.Errorf("%v: the header is stated twice", typ)
		}
		if got := at(t, ok200, "description"); got != "OK; Served from the cache" {
			t.Errorf("%v: description %v", typ, got)
		}
		if err := c.Conforms(em, http.StatusOK, http.Header{"X-Count": {"1"}}, []byte(`{"ok":true}`)); err != nil {
			t.Errorf("%v: Conforms: %v", typ, err)
		}
	}
	// A middleware's header on the success status it answers is stated
	// there, as on any status it answers.
	c := accepts(t, e(geta.Use(noop).Answers(http.StatusOK, "Served from the cache").Header(http.StatusOK, "Age", "Seconds in the cache", geta.HeaderOf[int]("minimum=0"))))
	if got := compact(t, at(t, doc(t, c), "paths", "/e", "get", "responses", "200", "headers", "Age")); got != `{"description":"Seconds in the cache","required":false,"schema":{"format":"int64","minimum":0,"type":"integer"}}` {
		t.Error("Age:", got)
	}
	if err := c.Conforms(em, http.StatusOK, http.Header{"X-Count": {"1"}, "Age": {"-1"}}, nil); err == nil || !strings.Contains(err.Error(), "Age: -1 is less than minimum 0") {
		t.Error("Age, Conforms:", err)
	}
}

// headedOut is an output with a required header and an optional one.
type headedOut struct {
	Count int     `header:"X-Count"`
	Note  *string `header:"X-Note"`
	Body  ok      `body:"json"`
}

// A header's type is refused where an envelope's header field's would be,
// and its constraints where a response's schema tag would be, or where a
// header could not carry what they admit.
func TestMiddlewareHeaderTypeMistakesAreRefused(t *testing.T) {
	m := geta.Use(noop).Answers(http.StatusTooManyRequests, "Limited")
	where := "root scope: middleware 0 (middleware): "
	for _, c := range []struct {
		typ  []geta.HeaderType
		want string
	}{
		{[]geta.HeaderType{geta.HeaderOf[[]string]("")}, `header "H" has unsupported type []string`},
		{[]geta.HeaderType{geta.HeaderOf[ok]("")}, `header "H" has unsupported type geta_test.ok`},
		{[]geta.HeaderType{geta.HeaderOf[map[string]int]("")}, `header "H" has unsupported type map[string]int`},
		{[]geta.HeaderType{geta.HeaderOf[time.Duration]("")}, `header H: type time.Duration has no JSON form`},
		{[]geta.HeaderType{geta.HeaderOf[*int]("")}, `header H: geta.HeaderOf[*int] is a pointer; use geta.HeaderOf[int]`},
		{[]geta.HeaderType{{}}, `header H: the zero geta.HeaderType; build it with geta.HeaderOf`},
		{[]geta.HeaderType{geta.HeaderOf[int](""), geta.HeaderOf[int]("")}, `header H: Header takes one geta.HeaderType, not 2`},
		{[]geta.HeaderType{geta.HeaderOf[int]("minLength=1")}, `header H: schema keyword minLength applies to string, not integer`},
		{[]geta.HeaderType{geta.HeaderOf[int]("minimum=5,maximum=1")}, `header H: minimum 5 exceeds maximum 1`},
		{[]geta.HeaderType{geta.HeaderOf[int]("minimum")}, `header H: schema tag entry "minimum" is not key=value`},
		{[]geta.HeaderType{geta.HeaderOf[string]("enum= a|b")}, `header H: enum member " a" is not a valid header field value`},
		{[]geta.HeaderType{geta.HeaderOf[string]("default=x\x01")}, `header H: default "x\x01": "x\x01" is not a valid header field value`},
	} {
		rejects(t, withRoot(one("/x", get(okHandler)), m.Header(http.StatusTooManyRequests, "H", "", c.typ...)), where+c.want)
	}
}

// CORS lets a page read what a middleware sends with what it answers, the
// headers a described failure row sets, and never Set-Cookie.
func TestCORSExposesMiddlewareAndDescribedHeaders(t *testing.T) {
	setCookie := geta.Use(noop).Answers(http.StatusTeapot, "Teapot").Header(http.StatusTeapot, "Set-Cookie", "")
	origin := header("Origin", "https://a.example")
	cors := geta.CORS(geta.AllowOrigins("https://a.example"))
	// The described row's 429 carries its own Retry-After, which the page
	// reads; so does any response of the operation.
	a := accepts(t, withRoot(describedTable(), cors))
	r := do(t, a, "GET", "/f?kind=quota", origin...)
	if r.Code != 429 || r.Header().Get("Retry-After") != "30" {
		t.Fatal(r.Code, r.Header())
	}
	sameHeaders(t, "GET /f 429", r.Header().Get("Access-Control-Expose-Headers"), "Retry-After")
	sameHeaders(t, "GET /f", do(t, a, "GET", "/f", origin...).Header().Get("Access-Control-Expose-Headers"), "Retry-After")
	// A rate limiter's 429 exposes its Retry-After, as every response behind
	// it does.
	a = accepts(t, withRoot(one("/x", get(okHandler)), cors, limit(1)))
	sameHeaders(t, "GET /x", do(t, a, "GET", "/x", origin...).Header().Get("Access-Control-Expose-Headers"), "Retry-After")
	if r = do(t, a, "GET", "/x", origin...); r.Code != 429 || r.Header().Get("Retry-After") == "" {
		t.Fatal(r.Code, r.Header())
	}
	sameHeaders(t, "GET /x limited", r.Header().Get("Access-Control-Expose-Headers"), "Retry-After")
	// A middleware's own header is exposed; a cookie an envelope sets, and a
	// Set-Cookie a middleware declares, never are.
	type session struct {
		Cookie *http.Cookie `cookie:"sid"`
		Body   ok           `body:"json"`
	}
	a = accepts(t, withRoot(one("/session", geta.Route{Post: geta.Op(http.StatusOK,
		func(context.Context, *empty) (*session, error) {
			return &session{Cookie: &http.Cookie{Name: "sid", Value: "v"}, Body: ok{true}}, nil
		}, geta.Doc{})}), cors, busy(), setCookie))
	r = do(t, a, "POST", "/session", origin...)
	if r.Code != 200 || r.Header().Get("Set-Cookie") == "" {
		t.Fatal(r.Code, r.Header())
	}
	sameHeaders(t, "POST /session", r.Header().Get("Access-Control-Expose-Headers"), "Retry-After")
	r = do(t, a, "POST", "/session", "Origin", "https://a.example", "X-Busy", "1")
	if r.Code != 503 || r.Header().Get("Retry-After") != "5" {
		t.Fatal(r.Code, r.Header())
	}
	sameHeaders(t, "POST /session busy", r.Header().Get("Access-Control-Expose-Headers"), "Retry-After")
}

// A rate limiter in an operation's Doc.BeforeGate makes that operation expose
// Retry-After, and no other.
func TestCORSExposesABeforeGateRetryAfter(t *testing.T) {
	a := accepts(t, limitedTable(geta.Scope{geta.CORS(geta.AllowOrigins("*")), gate()}))
	sameHeaders(t, "POST /login", do(t, a, "POST", "/login").Header().Get("Access-Control-Expose-Headers"), "Retry-After")
	sameHeaders(t, "GET /me", do(t, a, "GET", "/me", "Authorization", "Bearer ok").Header().Get("Access-Control-Expose-Headers"), "WWW-Authenticate")
}

// On a URL that reaches no operation, a gate's 401 carries its challenge,
// and the page may read only ExposeHeaders.
func TestCORSUnmatched401ExposesOnlyTheListed(t *testing.T) {
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.CORS(geta.AllowOrigins("*"), geta.ExposeHeaders("X-Request-Id")), gate()))
	r := do(t, a, "GET", "/nope")
	if r.Code != 401 || r.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatal(r.Code, r.Header())
	}
	sameHeaders(t, "GET /nope", r.Header().Get("Access-Control-Expose-Headers"), "X-Request-Id")
}

// A middleware that ends the response without writing it (an empty 200 net/http
// sends once the handler returns) still gets its ExposeHeaders and its Vary.
func TestCORSCompletesAResponseNothingWrote(t *testing.T) {
	silent := geta.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	})
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.CORS(geta.AllowOrigins("https://a.example"), geta.ExposeHeaders("X-Request-Id")), silent))
	r := do(t, a, "GET", "/x", "Origin", "https://a.example")
	if r.Code != 200 || r.Body.Len() != 0 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r.Header().Get("Access-Control-Allow-Origin") != "https://a.example" || r.Header().Get("Vary") != "Origin" {
		t.Fatal(r.Header())
	}
	if got := r.Header().Get("Access-Control-Expose-Headers"); !strings.EqualFold(got, "X-Request-Id") {
		t.Fatalf("Access-Control-Expose-Headers %q", got)
	}
}
