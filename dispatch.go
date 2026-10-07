package geta

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
)

// ServeHTTP matches the URL, then runs the root scope around the dispatch.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rc := &requestContext{Context: r.Context(), app: a, body: r.Body, length: r.ContentLength}
	escaped := escapedPath(r.URL)
	clean := cleanPath(r.Method, escaped)
	var m match
	a.router.lookup(r.Method, clean, &m)
	if o, _ := rc.Context.Value(observeKey{}).(*observed); o != nil && o.rc == nil {
		o.rc = rc
	}
	r = r.WithContext(rc)
	w, r, d := a.drainFor(w, r, rc)
	rc.method, rc.path, rc.rawPath = r.Method, r.URL.Path, r.URL.RawPath
	if rt := m.route; rt != nil {
		rc.match = rt.match // for a redirect, what the clean path will match
		if clean == escaped {
			rc.route = rt
			r = setPathValues(r, rc, rt, &m, strings.IndexByte(escaped, '%') < 0, nil)
		}
	}
	if clean != escaped {
		_, unclean := sentPath(r)
		rc.redirect = unclean
	}
	if a.deprecates {
		// Set before the root scope runs, so every response carries them.
		rc.header = w.Header()
		rc.deprecate(rc.route)
	}
	a.root.ServeHTTP(w, r)
	if d != nil {
		// Nothing was written: net/http writes a 200 once the App returns.
		d.arm()
	}
}

// sentPath returns the cleaned, escaped path and query of the URL the client
// sent (RequestURI, or URL for an in-process request), and whether the path
// was not clean.
//
// The path is escaped before it is cleaned, so Location never carries a byte
// a browser reads differently: /x/../\evil.example/x becomes /%5Cevil.example/x,
// not /\evil.example/x, which a browser resolves to the host evil.example.
func sentPath(r *http.Request) (target string, unclean bool) {
	p, q := escapedPath(r.URL), r.URL.RawQuery
	if uri := r.RequestURI; uri != "" {
		if strings.HasPrefix(uri, "/") {
			p, q, _ = strings.Cut(uri, "?")
		} else if u, err := url.ParseRequestURI(uri); err == nil {
			p, q = escapedPath(u), u.RawQuery // absolute form
		}
	}
	if p == "" {
		p = "/" // an absolute-form target with no path asks for /
	}
	p = escapeSent(p)
	target = cleanPath(r.Method, p)
	unclean = target != p
	if q != "" {
		target += "?" + escapeQuery(q)
	}
	return target, unclean
}

// escapedPath returns u's path escaped as geta matches it: RawPath passed
// through escapeSent when it decodes to Path, else u.EscapedPath().
//
// u.EscapedPath alone drops a RawPath holding a byte such as '|' and
// re-escapes Path, turning a client's %2F into a segment-splitting '/'.
func escapedPath(u *url.URL) string {
	if u.RawPath != "" {
		if p, err := url.PathUnescape(u.RawPath); err == nil && p == u.Path {
			return escapeSent(u.RawPath)
		}
	}
	return u.EscapedPath()
}

// escapeQuery percent-encodes the bytes of query s above 0x7F in lower-case
// hex, as http.Redirect does, and each '#' and each '%' that does not begin
// an escape, which a request-target's query may hold as net/http reads it:
// in Location, '#' would begin a fragment, cutting the query, and a stray
// '%' would make it no URI.
func escapeQuery(s string) string {
	i := 0
	for i < len(s) && !escapedQueryByte(s, i) {
		i++
	}
	if i == len(s) {
		return s
	}
	const hex = "0123456789abcdef"
	b := []byte(s[:i])
	for ; i < len(s); i++ {
		if c := s[i]; escapedQueryByte(s, i) {
			b = append(b, '%', hex[c>>4], hex[c&15])
		} else {
			b = append(b, c)
		}
	}
	return string(b)
}

// escapedQueryByte reports whether q[i] goes out of a query escaped.
func escapedQueryByte(q string, i int) bool {
	switch c := q[i]; c {
	case '#':
		return true
	case '%':
		return i+2 >= len(q) || !isHex(q[i+1]) || !isHex(q[i+2])
	default:
		return c >= 0x80
	}
}

// escapeSent percent-encodes each byte of path p that is not unreserved, a
// sub-delimiter, ':', '@', '/', '[' or ']', and each '%' that does not begin
// an escape. Escapes the client sent, %2F included, are kept.
func escapeSent(p string) string {
	n := 0
	for i := 0; i < len(p); i++ {
		if escapedByte(p, i) {
			n++
		}
	}
	if n == 0 {
		return p
	}
	const hex = "0123456789ABCDEF"
	b := make([]byte, 0, len(p)+2*n)
	for i := 0; i < len(p); i++ {
		if c := p[i]; escapedByte(p, i) {
			b = append(b, '%', hex[c>>4], hex[c&15])
		} else {
			b = append(b, c)
		}
	}
	return string(b)
}

// escapedByte reports whether p[i] goes out of a path escaped.
func escapedByte(p string, i int) bool {
	switch c := p[i]; {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return false
	case c == '%':
		return i+2 >= len(p) || !isHex(p[i+1]) || !isHex(p[i+2])
	default:
		return c >= 0x80 || !strings.ContainsRune("-._~!$&'()*+,;=:@/[]", rune(c))
	}
}

// redirectClean answers 307 to target with Location, Content-Length 0, and no
// body.
func redirectClean(w http.ResponseWriter, target string) {
	h := w.Header()
	h.Set("Location", target)
	h.Set("Content-Length", "0")
	w.WriteHeader(http.StatusTemporaryRedirect)
}

// dispatch runs the matched operation, OPTIONS on a served path included.
// Otherwise: a path the client sent with a dot segment is a 307 to its clean
// form (an empty segment is no dot segment: such a path matches nothing), a
// path served under other methods is a 405 problem with
// Allow, and anything else is a 404 problem. OPTIONS * answers 204 with every
// method served; any other method in asterisk form is a 400.
func (a *App) dispatch(w http.ResponseWriter, r *http.Request) {
	// If the root scope changed the request, the match follows it; the
	// operation served must be the one the root scope's gates checked. A
	// missing request context means a root middleware replaced it, losing
	// the match and the gates' record: a 500 defect.
	rc := requestFrom(r.Context())
	if rc == nil {
		writeDefect(w, r, a.log, "geta: a root middleware replaced the request context",
			errors.New("context does not derive from r.Context()"),
			methodAttr(r), routeAttr(r.Context()))
		return
	}
	rc.rematch(r)
	if rc.route != nil && !rc.moved {
		// Unmoved: every gate read rc.route's match, so nothing to refuse.
		rc.dispatched = true
		rc.route.h.ServeHTTP(w, r)
		return
	}
	var m match
	escaped := escapedPath(r.URL)
	if escaped == "*" && r.Method != http.MethodOptions {
		// The asterisk form is for OPTIONS alone (RFC 9110 §7.1).
		writeProblem(w, r, http.StatusBadRequest, "the asterisk-form request target is only for OPTIONS", nil)
		return
	}
	clean := cleanPath(r.Method, escaped)
	if clean != escaped && rc.redirects() {
		// Redirect to the clean form of the client's own URL, keeping a
		// mount's prefix. A path made unclean by a middleware is not
		// redirected but served as its clean form.
		target, _ := sentPath(r)
		redirectClean(w, target)
		return
	}
	a.router.lookup(r.Method, clean, &m)
	switch {
	case m.route != nil:
		if rc.unchecked(m.route) {
			rc.refuseUnchecked(w, r, m.route)
			return
		}
		if rc.skippedBefore(m.route) {
			rc.refuseSkippedBefore(w, r, m.route)
			return
		}
		var prev *route // whose values ServeHTTP gave the request
		if rc.moved {
			prev = rc.route
		}
		mux := clean == escaped && strings.IndexByte(escaped, '%') < 0
		r = setPathValues(r, rc, m.route, &m, mux, prev)
		rc.match, rc.dispatched = m.route.match, true
		// A root Timeout timed the operation matched before the move.
		retime(r.Context(), m.route.match)
		m.route.h.ServeHTTP(w, r)
	case len(m.allowed) > 0:
		w.Header().Set("Allow", allowHeader(m.allowed))
		writeProblem(w, r, http.StatusMethodNotAllowed, "", nil)
	case asterisk(r.Method, escaped):
		// RFC 9110 §9.3.7. net/http answers OPTIONS * itself unless
		// DisableGeneralOptionsHandler is set.
		w.Header().Set("Allow", a.allowAll)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeProblem(w, r, http.StatusNotFound, "", nil)
	}
}

// setPathValues sets r's pattern and path values as ServeMux would, and
// returns the request to serve.
//
// When mux is true, r is served through the route's values mux, which stores
// the values in a slice rather than the map SetPathValue allocates for names
// r's pattern lacks. mux is false for a path with an escape or one that is
// not clean; then, or if the values mux does not match, the values are set
// one by one.
//
// r is cloned first when an outer pattern names parameters, since
// SetPathValue writes into a slice shallow copies share. prev is the route
// whose values r carried before a root middleware moved it, or nil; its
// names rt lacks are set to "".
func setPathValues(r *http.Request, rc *requestContext, rt *route, m *match, mux bool, prev *route) *http.Request {
	outer := prev == nil && strings.Contains(r.Pattern, "{")
	if rt.values != nil && rc != nil && mux && !outer && prev == nil {
		rc.valued = false
		rt.values.ServeHTTP(discard{}, r)
		if rc.valued {
			r.Pattern = rt.pattern
			return r
		}
	}
	if outer || prev != nil {
		r = r.Clone(r.Context())
	}
	if prev != nil {
		for _, name := range prev.params {
			if !slices.Contains(rt.params, name) {
				r.SetPathValue(name, "")
			}
		}
	}
	r.Pattern = rt.pattern
	for i, name := range rt.params {
		r.SetPathValue(name, m.value(i))
	}
	return r
}

// valuesMux returns a ServeMux holding pattern alone, or nil if ServeMux
// panics on it. New refuses such templates first, so nil is only a fallback.
func valuesMux(pattern string) (mux *http.ServeMux) {
	defer func() {
		if recover() != nil {
			mux = nil
		}
	}()
	mux = http.NewServeMux()
	mux.Handle(pattern, http.HandlerFunc(markValued))
	return mux
}

// markValued is what a values mux serves when it matches.
func markValued(_ http.ResponseWriter, r *http.Request) {
	if rc := requestFrom(r.Context()); rc != nil {
		rc.valued = true
	}
}

// discard is the ResponseWriter given to a values mux. It is not written to
// in practice: the mux only sees paths its pattern matches.
type discard struct{}

func (discard) Header() http.Header         { return http.Header{} }
func (discard) Write(b []byte) (int, error) { return len(b), nil }
func (discard) WriteHeader(int)             {}

// ServeHTTP runs one operation: bind, call, answer.
func (c *compiledOp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !c.in.reads() {
		// Content sent to an operation that reads none is a 415.
		r = boundBody(w, r)
		if be := contentRefused(r); be != nil {
			closeUnread(w, r)
			be.write(w, r)
			return
		}
	}
	if c.options {
		// Allow lists the methods of every template matching the path.
		w.Header().Set("Allow", c.app.router.allow(escapedPath(r.URL)))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	in := reflect.New(c.op.in)
	if c.in.reads() {
		r = boundBody(w, r)
	}
	be, tmp := c.in.bind(w, r, in.Elem(), c.limits)
	if be != nil {
		closeUnread(w, r)
		if be.err != nil {
			c.writeDefect(w, r, "geta: the request could not be taken in", be.err)
			return
		}
		be.write(w, r)
		return
	}
	if tmp != nil {
		// Multipart files on disk live until the output is written.
		defer removeFiles(tmp)
	}
	out, err := c.op.call(r.Context(), in.Interface())
	if err != nil {
		c.fail(w, r, err)
		return
	}
	if c.out.kind == outSpecial {
		c.out.special.write(w, r, c.app, c, out)
		return
	}
	if err := c.out.write(w, c.op.status, out, c.limits.MaxResponseBuffer); err != nil {
		if ce, ok := err.(*committedError); ok {
			// The response has started: abort so the client cannot take a
			// truncated body for a complete one.
			if ce.write {
				c.app.log.LogAttrs(r.Context(), slog.LevelInfo, "geta: the response could not be written",
					slog.String("method", c.method), slog.String("route", c.path), slog.Any("error", ce.err))
			} else {
				c.defect(r, "geta: output failed after the response started", ce.err)
			}
			panic(http.ErrAbortHandler)
		}
		c.writeDefect(w, r, "geta: output could not be written", err)
	}
}

// fail turns a handler's error into a response: defects and context errors
// first, then a [Conditional]'s verdict, then the failure table top to
// bottom, then 500.
//
// context.Canceled whose request context ended by a [Timeout] deadline is a
// 504, like DeadlineExceeded; any other Canceled is a client that went away.
func (c *compiledOp) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case err == errNilOutput:
		// A defect no row answers, a catch-all OnAs[error] included.
		c.writeDefect(w, r, "geta: handler returned a nil output", err)
		return
	case errors.Is(err, errBadETag):
		c.writeDefect(w, r, "geta: Conditional.Check was given an etag that is not one entity tag", err)
		return
	case errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, context.Canceled) && errors.Is(context.Cause(r.Context()), context.DeadlineExceeded):
		writeProblem(w, r, http.StatusGatewayTimeout, "", nil)
		return
	case errors.Is(err, context.Canceled) && r.Context().Err() != nil:
		c.app.log.LogAttrs(r.Context(), slog.LevelInfo, "geta: client went away",
			slog.String("method", c.method), slog.String("route", c.path))
		return
	}
	// A raw io.Reader body that could not be read is answered as any body:
	// 413 past MaxBodyBytes, 408 past the body's window, else 400, whatever
	// row the error would meet.
	if re, ok := errors.AsType[*bodyReadError](err); ok {
		closeUnread(w, r)
		readFailure(re.err, c.limits).write(w, r)
		return
	}
	// A PreconditionError is answered only by an operation whose input embeds
	// a Conditional; anywhere else it is a 500 defect, whatever the rows.
	if pe, ok := errors.AsType[*PreconditionError](err); ok {
		if c.in.cond != nil && c.app.listed[c][pe.status] {
			pe.write(w, r)
			return
		}
		c.writeDefect(w, r, "geta: a PreconditionError from an operation that does not answer it", err)
		return
	}
	for i, f := range c.op.doc.Failures {
		if f.match(err) {
			if pp := c.rows[i]; pp != nil {
				pp.write(w, r, c, f, err)
				return
			}
			if err := sendProblem(w, &Problem{Type: f.problemType(), Title: http.StatusText(f.status), Status: f.status, Detail: f.detail}); err != nil {
				abortUntaken(r, err)
			}
			return
		}
	}
	c.writeDefect(w, r, "geta: unmatched error", err)
}

// defect records a defect that can no longer change the response, such as
// one in a stream already under way.
func (c *compiledOp) defect(r *http.Request, msg string, err error) {
	c.app.log.LogAttrs(r.Context(), slog.LevelError, msg,
		slog.String("method", c.method), slog.String("route", c.path), slog.Any("error", err))
}

// writeDefect records a defect and answers 500 with its occurrence id.
func (c *compiledOp) writeDefect(w http.ResponseWriter, r *http.Request, msg string, err error) {
	writeDefect(w, r, c.app.log, msg, err, slog.String("method", c.method), slog.String("route", c.path))
}
