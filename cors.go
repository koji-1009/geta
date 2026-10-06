package geta

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// CORSOption configures [CORS].
type CORSOption func(*corsConfig)

type corsConfig struct {
	origins     []string
	methods     []string
	headers     []string
	expose      []string
	credentials bool
	maxAge      time.Duration
	maxAgeSet   bool
}

// AllowOrigins lists the origins allowed; "*" allows any.
func AllowOrigins(origins ...string) CORSOption {
	return func(c *corsConfig) { c.origins = append(c.origins, origins...) }
}

// AllowMethods lists methods a preflight allows beyond those [CORS] derives
// from the path asked about, such as one a middleware serves.
func AllowMethods(methods ...string) CORSOption {
	return func(c *corsConfig) { c.methods = append(c.methods, methods...) }
}

// AllowHeaders lists request headers a preflight allows beyond those [CORS]
// derives from the operation asked about, such as one a middleware reads.
func AllowHeaders(headers ...string) CORSOption {
	return func(c *corsConfig) { c.headers = append(c.headers, headers...) }
}

// ExposeHeaders lists response headers a page may read beyond those [CORS]
// derives from the operation that answers.
func ExposeHeaders(headers ...string) CORSOption {
	return func(c *corsConfig) { c.expose = append(c.expose, headers...) }
}

// AllowCredentials lets a page send cookies. It cannot be combined with "*".
func AllowCredentials() CORSOption { return func(c *corsConfig) { c.credentials = true } }

// PreflightMaxAge sets how long a browser may cache a preflight, sent as
// Access-Control-Max-Age. [New] refuses a duration that is not a whole
// positive number of seconds. Without PreflightMaxAge the header is not sent.
func PreflightMaxAge(d time.Duration) CORSOption {
	return func(c *corsConfig) { c.maxAge, c.maxAgeSet = d, true }
}

// CORS answers preflights and marks responses for the allowed origins. It
// sits outside Recover and Timeout, so a 500 or 504 still carries its
// headers. Unless origins include "*", every response varies on Origin.
//
// What a page may send and read derives from the operations. A preflight
// allows the methods the path serves, less HEAD and OPTIONS, plus
// [AllowMethods]; a path nothing serves gets only the listed ones. It allows
// the request headers of the operation serving Access-Control-Request-Method,
// plus [AllowHeaders]: its header parameters, the header its security schemes
// read (Authorization, or an apiKey's header), and Content-Type when it takes
// a body.
//
// A response exposes the answering operation's response headers, plus
// [ExposeHeaders]: the headers its success declares, those its failure rows
// set ([OnAsProblem]), ETag behind [ETag], and those its middleware declare
// ([Middleware.Header]), such as Retry-After or WWW-Authenticate. Set-Cookie
// is never listed. A request that reaches no operation exposes only the
// listed ones.
//
// [New] refuses CORS outside the root scope, since OPTIONS runs the root
// scope alone. It also refuses no origin, "*" with [AllowCredentials], and a
// bad [PreflightMaxAge].
func CORS(opts ...CORSOption) Middleware {
	cfg := corsConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	wildcard := slices.Contains(cfg.origins, "*")
	var bad error
	switch {
	case len(cfg.origins) == 0:
		bad = errors.New("geta.CORS allows no origin; pass geta.AllowOrigins")
	case wildcard && cfg.credentials:
		bad = errors.New(`geta.CORS: "*" with credentials is forbidden by the Fetch standard`)
	case cfg.maxAgeSet && (cfg.maxAge <= 0 || cfg.maxAge%time.Second != 0):
		bad = fmt.Errorf("geta.CORS: PreflightMaxAge %v is not a positive whole number of seconds", cfg.maxAge)
	}
	m := Ordered(OrderCrossOrigin, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			origin := r.Header.Get("Origin")
			allowed := ""
			switch {
			case wildcard:
				allowed = "*"
			case origin != "" && slices.Contains(cfg.origins, origin):
				allowed = origin
			}
			if !wildcard {
				addVary(h, "Origin")
			}
			if allowed != "" {
				h.Set("Access-Control-Allow-Origin", allowed)
				if cfg.credentials {
					h.Set("Access-Control-Allow-Credentials", "true")
				}
			}
			if method := r.Header.Get("Access-Control-Request-Method"); r.Method == http.MethodOptions && method != "" {
				if allowed != "" {
					served, op := preflight(r, method)
					if methods := headerList(served, cfg.methods); methods != "" {
						h.Set("Access-Control-Allow-Methods", methods)
					}
					var derived []string
					if op != nil {
						derived = op.corsAllow
					}
					if headers := headerList(derived, cfg.headers); headers != "" {
						h.Set("Access-Control-Allow-Headers", headers)
					}
					if cfg.maxAge > 0 {
						h.Set("Access-Control-Max-Age", strconv.Itoa(int(cfg.maxAge/time.Second)))
					}
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			cw := &corsWriter{ResponseWriter: w, r: r, origin: !wildcard, allowed: allowed != "", expose: cfg.expose}
			next.ServeHTTP(cw, r)
			// Complete a response nothing was written to.
			cw.complete()
		})
	})
	m.name = "cors"
	m.bad = bad
	m.root = "it answers preflights"
	return m
}

// preflight returns the methods r's path serves, less HEAD and OPTIONS, and
// the operation it serves under method. Both are empty outside an [App] or
// for a path nothing serves.
func preflight(r *http.Request, method string) (methods []string, op *compiledOp) {
	rc := requestFrom(r.Context())
	if rc == nil {
		return nil, nil
	}
	path := cleanPath(method, escapedPath(r.URL))
	if allow := rc.app.router.allow(path); allow != "" {
		for m := range strings.SplitSeq(allow, ", ") {
			if m != http.MethodHead && m != http.MethodOptions {
				methods = append(methods, m)
			}
		}
	}
	var m match
	rc.app.router.lookup(method, path, &m)
	if m.route != nil {
		op = m.route.op
	}
	return methods, op
}

// headerList joins header names, dropping case-insensitive duplicates.
func headerList(lists ...[]string) string {
	var names []string
	for _, l := range lists {
		for _, n := range l {
			if !slices.ContainsFunc(names, func(s string) bool { return strings.EqualFold(s, n) }) {
				names = append(names, n)
			}
		}
	}
	return strings.Join(names, ", ")
}

// corsWriter completes a cross-origin response as it goes out: it restores
// Vary: Origin if the handler replaced Vary, and, for an allowed origin,
// sets Access-Control-Expose-Headers.
type corsWriter struct {
	http.ResponseWriter
	r       *http.Request
	origin  bool // a specific-origin configuration
	allowed bool
	expose  []string
	done    bool
}

func (c *corsWriter) WriteHeader(code int) {
	if code >= 200 {
		c.complete()
	}
	c.ResponseWriter.WriteHeader(code)
}

// complete sets Vary and Access-Control-Expose-Headers, once.
func (c *corsWriter) complete() {
	if c.done {
		return
	}
	c.done = true
	h := c.Header()
	if c.origin {
		addVary(h, "Origin")
	}
	if c.allowed {
		var derived []string
		if rc := requestFrom(c.r.Context()); rc != nil && rc.match != nil {
			if op := rc.app.byMatch[rc.match]; op != nil {
				derived = op.corsExpose
			}
		}
		if expose := headerList(derived, c.expose); expose != "" {
			h.Set("Access-Control-Expose-Headers", expose)
		}
	}
}

func (c *corsWriter) Write(b []byte) (int, error) {
	if !c.done {
		c.WriteHeader(http.StatusOK)
	}
	return c.ResponseWriter.Write(b)
}

func (c *corsWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// corsHeaders derives c.corsAllow, the request headers [CORS] allows, and
// c.corsExpose, the response headers it exposes, from what c declares.
func (c *compiledOp) corsHeaders() {
	var allow, expose []string
	add := func(list *[]string, name string) {
		if !slices.ContainsFunc(*list, func(s string) bool { return strings.EqualFold(s, name) }) {
			*list = append(*list, name)
		}
	}
	for _, p := range c.in.params {
		if p.in == "header" {
			add(&allow, p.name)
		}
	}
	for _, s := range c.security {
		switch s.Type {
		case "http", "oauth2", "openIdConnect":
			add(&allow, "Authorization")
		case "apiKey":
			if s.In == "header" {
				add(&allow, s.Param)
			}
		}
	}
	if mt := c.bodyMediaType(); mt != "" {
		add(&allow, "Content-Type")
		// geta's 415 names the media type it takes (unsupported).
		add(&expose, "Accept")
		if name := acceptFor(c.method); name != "" {
			add(&expose, name) // Accept-Patch, Accept-Query
		}
	}
	if headers, ok := c.success(c.op.status)["headers"].(map[string]any); ok {
		for _, name := range slices.Sorted(maps.Keys(headers)) {
			// A page never reads Set-Cookie: Fetch forbids it.
			if !strings.EqualFold(name, "Set-Cookie") {
				add(&expose, name)
			}
		}
	}
	for _, pp := range c.rows {
		if pp != nil {
			for _, h := range pp.headers() {
				add(&expose, h.name)
			}
		}
	}
	for _, m := range c.chain {
		for _, name := range m.exposes {
			add(&expose, name)
		}
		if m.gate != nil && len(c.security) == 0 {
			// A gate that requires nothing of this operation answers nothing.
			continue
		}
		for _, h := range m.headers {
			if !strings.EqualFold(h.name, "Set-Cookie") {
				add(&expose, h.name)
			}
		}
	}
	c.corsAllow, c.corsExpose = allow, expose
}
