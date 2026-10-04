package geta

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Match is the operation a request reached: its template, its method, and
// its documentation. A request that reaches no operation (a 404 or 405) has
// no match. A request redirected to its clean path has the match of the
// operation the clean path reaches. OPTIONS on a served path matches the
// OPTIONS operation geta serves there; OPTIONS * matches nothing.
type Match struct {
	Template string
	Method   string
	Doc      Doc
	// Operation is true for every match geta gives.
	Operation bool
}

// requestContext is geta's per-request state in the request's context: the
// App, the match, and what the match was made from. It is one value, so a
// request costs one allocation for it.
type requestContext struct {
	context.Context
	app   *App
	match *Match
	// route is the operation ServeHTTP matched at the clean path, or nil.
	// Until moved, the request is served by it without matching again.
	route *route
	// The method and path the router matched on.
	method, path, rawPath string
	valued                bool // set by a route's values mux when it matches
	// dispatched is set once dispatch has chosen the operation; the match
	// is fixed from then on.
	dispatched bool
	// moved is set once a root middleware changed the method or the path,
	// pathMoved once it changed the path.
	moved, pathMoved bool
	// redirect is true when the client sent an unclean path.
	redirect bool
	// What the root scope's [Secure] gates checked: gated once one read the
	// match, admitted the match the first checked (nil for none), and mixed
	// once two gates checked different ones.
	gated, mixed bool
	admitted     *Match
	// before is the match whose Doc.BeforeGate the root scope ran.
	before *Match
	// header is the response header as the App received it, and deprecated
	// the deprecation headers set on it; nil when no operation sends them.
	header     http.Header
	deprecated *deprecationPlan
	// body and length are the request's Body and ContentLength as the App
	// received them (declaredLength).
	body   io.ReadCloser
	length int64
	// read is the body boundBody gave a read deadline, if any.
	read *boundedBody
	// drain is the request's drain, if any.
	drain *drain
}

// window returns the time the request's body has to arrive: the serving
// operation's window, or the root scope's for a request that reaches none.
func (c *requestContext) window() time.Duration {
	if c.match != nil && (c.dispatched || !c.redirects()) {
		if op := c.app.byMatch[c.match]; op != nil {
			return op.window
		}
	}
	return c.app.rootWindow
}

// declaredLength returns r's Content-Length, or -1 when none is declared or
// a middleware replaced the body or the length.
func declaredLength(r *http.Request) int64 {
	if rc := requestFrom(r.Context()); rc != nil && (r.Body != rc.body || r.ContentLength != rc.length) {
		return -1
	}
	return r.ContentLength
}

type requestKey struct{}

func (c *requestContext) Value(key any) any {
	if key == (requestKey{}) {
		return c
	}
	return c.Context.Value(key)
}

func requestFrom(ctx context.Context) *requestContext {
	rc, _ := ctx.Value(requestKey{}).(*requestContext)
	return rc
}

// keepRequest returns ctx carrying r's request context, for a context that
// replaces r's but may not derive from it, such as a [Verifier]'s.
func keepRequest(ctx context.Context, r *http.Request) context.Context {
	rc := requestFrom(r.Context())
	if rc == nil || requestFrom(ctx) == rc {
		return ctx
	}
	return &keptRequest{Context: ctx, rc: rc}
}

// keptRequest carries a request context it does not derive from.
type keptRequest struct {
	context.Context
	rc *requestContext
}

func (c *keptRequest) Value(key any) any {
	if key == (requestKey{}) {
		return c.rc
	}
	return c.Context.Value(key)
}

// unchanged reports whether r still has the method and path matched on.
func (c *requestContext) unchanged(r *http.Request) bool {
	return r.Method == c.method && r.URL.Path == c.path && r.URL.RawPath == c.rawPath
}

// rematch matches r again if a root middleware changed its method or path,
// and marks the request moved. After dispatch it does nothing.
func (c *requestContext) rematch(r *http.Request) {
	if c.dispatched || c.unchanged(r) {
		return
	}
	var m match
	c.app.router.lookup(r.Method, cleanPath(r.Method, escapedPath(r.URL)), &m)
	c.match = nil
	if m.route != nil {
		c.match = m.route.match
	}
	c.moved = true
	if r.URL.Path != c.path || r.URL.RawPath != c.rawPath {
		c.pathMoved = true
	}
	c.method, c.path, c.rawPath = r.Method, r.URL.Path, r.URL.RawPath
	if c.redirects() {
		c.deprecate(nil)
	} else {
		c.deprecate(m.route)
	}
}

// redirects reports whether dispatch redirects the request to the clean form
// of the URL the client sent.
func (c *requestContext) redirects() bool {
	return c.redirect && !c.pathMoved
}

// matchedFor is [Matched] for r as it is now. Gates read the match this way.
// Before dispatch, the match read is recorded so dispatch can refuse an
// operation the gates did not check.
func matchedFor(r *http.Request) (Match, bool) {
	rc := requestFrom(r.Context())
	if rc == nil {
		return Match{}, false
	}
	rc.rematch(r)
	switch {
	case rc.dispatched:
	case !rc.gated:
		rc.gated, rc.admitted = true, rc.match
	case rc.admitted != rc.match:
		rc.mixed = true
	}
	if rc.match == nil {
		return Match{}, false
	}
	return *rc.match, true
}

// unchecked reports whether serving rt would skip a root gate's check: the
// gates read a match other than rt's, and rt has something for them to check.
func (c *requestContext) unchecked(rt *route) bool {
	return c.gated && rt.rootGated && (c.mixed || c.admitted != rt.match)
}

// refuseUnchecked answers 500 for an operation the gates did not check.
func (c *requestContext) refuseUnchecked(w http.ResponseWriter, r *http.Request, rt *route) {
	checked := "no operation"
	if c.admitted != nil {
		checked = c.admitted.Method + " " + c.admitted.Template
	}
	if c.mixed {
		checked = "other operations"
	}
	rt.op.writeDefect(w, r, "geta: a root middleware rewrote the request after a Secure gate",
		fmt.Errorf("gates checked %s but the request reached %s; move the rewrite before geta.Secure",
			checked, rt.pattern))
}

// Matched returns what the request matched. Matching happens before the root
// scope runs, so every middleware can read it. ok is false for a URL that
// matches no route.
//
// When a root middleware changes the method or path, the match follows from
// the next [Secure] gate on, and from dispatch on at the latest. Once
// dispatch has chosen the operation, the match is fixed: changes in the
// operation's own scopes affect neither what is served nor what its gates
// check. A root middleware that passes on a context not derived from the
// request's loses the match, and dispatch answers 500 as a defect.
func Matched(ctx context.Context) (Match, bool) {
	rc := requestFrom(ctx)
	if rc == nil || rc.match == nil {
		return Match{}, false
	}
	return *rc.match, true
}

// Observe returns ctx carrying a record of how an [App] serves the request
// made with it, and a function that reads the record: the method the request
// is served as and the match of the operation that serves it. ok is false
// while no operation serves it: for a 404, a 405, a redirect to the clean
// path, and before the App has begun. The record follows a root
// middleware's changes as [Matched] does. A handler wrapping an App uses it
// to learn what the App served:
//
//	ctx, read := geta.Observe(r.Context())
//	app.ServeHTTP(w, r.WithContext(ctx))
//	method, m, ok := read()
//
// Called inside the App, Observe returns ctx unchanged and reads the current
// request.
func Observe(ctx context.Context) (context.Context, func() (method string, m Match, ok bool)) {
	if rc := requestFrom(ctx); rc != nil {
		return ctx, rc.served
	}
	o := &observed{}
	return context.WithValue(ctx, observeKey{}, o), func() (string, Match, bool) {
		if o.rc == nil {
			return "", Match{}, false
		}
		return o.rc.served()
	}
}

type observeKey struct{}

// observed receives the request context of a request made with an [Observe]
// context.
type observed struct{ rc *requestContext }

// served is what [Observe] reads. A request dispatch will redirect is not
// served.
func (c *requestContext) served() (string, Match, bool) {
	if c.match == nil || !c.dispatched && c.redirects() {
		return c.method, Match{}, false
	}
	return c.method, *c.match, true
}

func loggerFrom(ctx context.Context) *slog.Logger {
	if rc := requestFrom(ctx); rc != nil && rc.app != nil && rc.app.log != nil {
		return rc.app.log
	}
	return slog.Default()
}
