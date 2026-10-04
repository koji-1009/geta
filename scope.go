package geta

import (
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"time"
)

// Order places a middleware in the chain. Ranks must not descend along any
// route's composed chain; [New] rejects a chain where they do, naming both.
// Equal ranks are unordered.
//
// An application may add its own stages between geta's, such as
// Order{Rank: OrderAuthenticate.Rank + 500, Name: "shed-principal"}.
type Order struct {
	Rank int
	Name string
}

func (o Order) String() string { return fmt.Sprintf("%s(%d)", o.Name, o.Rank) }

// OrderSpacing is the distance between two adjacent stages of geta's
// vocabulary, leaving room for an application's own.
const OrderSpacing = 1000

// geta's vocabulary, outermost first.
var (
	OrderObserve      = Order{1 * OrderSpacing, "observe"}
	OrderCrossOrigin  = Order{2 * OrderSpacing, "cross-origin"}
	OrderRecover      = Order{3 * OrderSpacing, "recover"}
	OrderShed         = Order{4 * OrderSpacing, "shed"}
	OrderDeadline     = Order{5 * OrderSpacing, "deadline"}
	OrderNegotiate    = Order{6 * OrderSpacing, "negotiate"}
	OrderValidate     = Order{7 * OrderSpacing, "validate"}
	OrderAuthenticate = Order{8 * OrderSpacing, "authenticate"}
	OrderAuthorize    = Order{9 * OrderSpacing, "authorize"}
)

// Middleware is a standard net/http middleware with an optional order and
// the statuses it can answer with. Build one with [Use], [Ordered], or one of
// geta's own; the zero value is rejected by [New].
type Middleware struct {
	fn      func(http.Handler) http.Handler
	order   *Order
	name    string
	answers []answer      // statuses this middleware may write, for the document
	headers []mwHeader    // headers it sends with those statuses
	exposes []string      // response headers it may set, exposed by CORS
	scopes  []mwScopes    // scopes it requires of a scheme, for the document
	gate    *Policy       // set by Secure
	root    string        // why it runs only in the root scope, if it does
	timeout bool          // set by Timeout
	length  time.Duration // a Timeout's own length
	bad     error         // a construction mistake, reported by New
}

type answer struct {
	status   int
	reason   string
	getOnly  bool // only a GET with a 200 success can receive it (a 304)
	bodyOnly bool // only an operation that reads a body can receive it (408)
}

// mwHeader is a response header a middleware sends with a status it answers.
type mwHeader struct {
	status      int
	name        string
	description string
	schema      *schema // nil: a string
	c           *codec  // HeaderOf's type, read by Conforms; nil: a string
	bad         error   // reported by New
}

// HeaderType is the type a middleware's header is documented as, made by
// [HeaderOf] and given to [Middleware.Header]. The zero value is refused by
// [New].
type HeaderType struct {
	t           reflect.Type
	constraints string
}

// HeaderOf returns a header type T whose schema is T's with constraints, a
// schema tag such as "minimum=1" ("" for none). A rate limiter that writes
// delay-seconds declares its Retry-After with HeaderOf[int]("minimum=1").
//
// [New] refuses T if it is a pointer or not a string, number, boolean, or
// encoding.TextMarshaler, and refuses constraints that are malformed, do not
// apply to T, admit no value, or hold an enum member, default, or example a
// header cannot carry. geta checks the header the middleware sends only at
// test time, in [App.Conforms], never at runtime.
func HeaderOf[T any](constraints string) HeaderType {
	return HeaderType{t: reflect.TypeFor[T](), constraints: constraints}
}

// headerSchema returns the schema and codec of a middleware's header name of
// type ht.
func headerSchema(name string, ht HeaderType) (*schema, *codec, error) {
	if ht.t == nil {
		return nil, nil, fmt.Errorf("header %s: the zero geta.HeaderType; build it with geta.HeaderOf", name)
	}
	if ht.t.Kind() == reflect.Pointer {
		return nil, nil, fmt.Errorf("header %s: geta.HeaderOf[%s] is a pointer; use geta.HeaderOf[%s]", name, ht.t, ht.t.Elem())
	}
	c, err := newRegistry().codecFor(ht.t)
	if err != nil {
		return nil, nil, fmt.Errorf("header %s: %w", name, err)
	}
	if err := headerType(name, ht.t.String(), vetKind(c)); err != nil {
		return nil, nil, err
	}
	// Checked as a header parameter's schema tag is.
	use, err := parseConstraints(ht.constraints, c.use())
	if err != nil {
		return nil, nil, fmt.Errorf("header %s: %w", name, err)
	}
	if use, err = carriedAt("header", "header "+name, use); err != nil {
		return nil, nil, err
	}
	if err := use.checkValues(c); err != nil {
		return nil, nil, fmt.Errorf("header %s: %w", name, err)
	}
	return use, c, nil
}

// Use adapts an ordinary net/http middleware. It carries no order, so it
// constrains nothing and is constrained by nothing.
func Use(fn func(http.Handler) http.Handler) Middleware {
	return Middleware{fn: fn, name: "middleware"}
}

// Ordered adapts an ordinary net/http middleware and places it at order.
func Ordered(order Order, fn func(http.Handler) http.Handler) Middleware {
	o := order
	return Middleware{fn: fn, order: &o, name: order.Name}
}

// Invalid returns a middleware [New] refuses, naming err. A constructor
// outside geta returns one for bad arguments, so the mistake surfaces at
// assembly like geta's own. It has no function; New refuses it before
// wrapping anything.
func Invalid(name string, err error) Middleware {
	return Middleware{name: name, bad: err}
}

// Answers declares that the middleware may itself respond with status, so
// the OpenAPI document of every operation behind it lists that status. An
// authorization middleware answering 403 declares it here.
//
// A 4xx or 5xx is documented as a problem, with reason among its causes. A
// 2xx or 3xx equal to the operation's success adds reason to its
// description. Any other 2xx or 3xx is documented as a response with no
// content, described by reason, with the headers declared by
// [Middleware.Header]. A 304 is listed only on a GET operation whose success
// is 200 (RFC 9110 §15.4.5). [New] refuses a 1xx and a status outside 100 to
// 599.
func (m Middleware) Answers(status int, reason string) Middleware {
	an := answer{status: status, reason: reason, getOnly: status == http.StatusNotModified}
	m.answers = append(append([]answer(nil), m.answers...), an)
	return m
}

// Header declares a response header the middleware sends when it answers
// status, declared with [Middleware.Answers]. The document states the header
// as optional on that status of every operation behind the middleware, and
// [CORS] exposes it. A rate limiter answering 429 declares its Retry-After
// here. The schema is a string, or that of typ ([HeaderOf]).
//
// [New] refuses a name that is not a token, a status the middleware does not
// answer, Content-Type, more than one typ, and a typ HeaderOf refuses. It
// also refuses a typ whose schema differs from another declaration of the
// same header (any spelling) on that status: a failure row's, the output's,
// or another typed Header in the chain. A Header without typ defers to any
// other declaration.
func (m Middleware) Header(status int, name, description string, typ ...HeaderType) Middleware {
	h := mwHeader{status: status, name: name, description: description}
	switch len(typ) {
	case 0:
	case 1:
		h.schema, h.c, h.bad = headerSchema(name, typ[0])
	default:
		h.bad = fmt.Errorf("header %s: Header takes one geta.HeaderType, not %d", name, len(typ))
	}
	m.headers = append(append([]mwHeader(nil), m.headers...), h)
	return m
}

// mwScopes are scopes a middleware requires of the scheme that admitted a
// request.
type mwScopes struct {
	scheme Scheme
	scopes []string
}

// Scopes declares that the middleware lets a request through only when
// scheme admitted it and its credentials hold every one of scopes, as a
// middleware checking a bearer token's scopes does. The document states the
// scopes in scheme's requirement on every operation behind the middleware,
// and drops alternatives without scheme. [New] refuses no scope, a scope that
// is not an RFC 6749 scope-token, a scope an oauth2 scheme's flows do not
// define, and scopes on an operation with no gate requiring scheme before
// the middleware.
func (m Middleware) Scopes(scheme Scheme, scopes ...string) Middleware {
	m.scopes = append(append([]mwScopes(nil), m.scopes...), mwScopes{scheme: scheme, scopes: slices.Clone(scopes)})
	return m
}

// checkHeaders returns an error for a status, header, or scope m declares
// that the document could not state.
func (m Middleware) checkHeaders() error {
	for _, an := range m.answers {
		switch {
		case an.status < 100 || an.status > 599:
			return fmt.Errorf("Answers(%d, %q): %d is not an HTTP status (100 to 599)", an.status, an.reason, an.status)
		case an.status < 200:
			return fmt.Errorf("Answers(%d, %q): %d is an informational (1xx) status", an.status, an.reason, an.status)
		}
	}
	for _, h := range m.headers {
		if !validHeaderName(h.name) {
			return fmt.Errorf("header %q is not a token", h.name)
		}
		if http.CanonicalHeaderKey(h.name) == "Content-Type" {
			// OpenAPI ignores a response header named Content-Type.
			return fmt.Errorf("header %s: Content-Type cannot be a declared response header", h.name)
		}
		if !slices.ContainsFunc(m.answers, func(a answer) bool { return a.status == h.status }) {
			return fmt.Errorf("header %s: status %d is not in Answers", h.name, h.status)
		}
		if h.bad != nil {
			return h.bad
		}
	}
	for _, sc := range m.scopes {
		if sc.scheme.Name == "" {
			return fmt.Errorf("Scopes names a scheme with no Name")
		}
		if len(sc.scopes) == 0 {
			return fmt.Errorf("Scopes of scheme %q names no scope", sc.scheme.Name)
		}
		for _, s := range sc.scopes {
			if !scopeToken(s) {
				return fmt.Errorf("Scopes of scheme %q: %q is not a scope-token", sc.scheme.Name, s)
			}
		}
	}
	return nil
}

// Order reports the middleware's order, if it has one.
func (m Middleware) Order() (Order, bool) {
	if m.order == nil {
		return Order{}, false
	}
	return *m.order, true
}

// Scope is the middleware a directory applies to every route beneath it, in
// run order. The root scope wraps the whole dispatch, 404 and 405 included.
// [Doc.Scope] is one operation's own, run inside its URL's.
type Scope []Middleware

// checkOrder rejects a chain whose ordered entries descend.
func checkOrder(chain []Middleware, where string) error {
	var prev *Middleware
	for i := range chain {
		m := &chain[i]
		if m.order == nil {
			continue
		}
		if prev != nil && m.order.Rank < prev.order.Rank {
			return fmt.Errorf("%s: middleware %s runs after %s but has a lower order", where, m.order, prev.order)
		}
		prev = m
	}
	return nil
}
