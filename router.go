package geta

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
)

// router matches a request path to an operation segment by segment, trying
// a literal before a parameter and backtracking out of a dead-end literal.
// So /users/by-role/{role} and /users/{id}/name both serve:
// /users/by-role/name reaches the first, /users/7/name the second.
type router struct {
	root routeNode
}

type routeNode struct {
	literals map[string]*routeNode
	param    *routeNode
	routes   map[string]*route // by method
	template string            // the template that ends here, if any
}

// route is one operation as the router serves it.
type route struct {
	h       http.Handler // the operation inside its directory scopes
	match   *Match
	params  []string // parameter names, in path order
	op      *compiledOp
	pattern string // "GET /users/{id}", as r.Pattern carries it
	// values is a ServeMux holding this route's pattern alone, used to set
	// path values as ServeMux does (setPathValues); nil without parameters.
	values *http.ServeMux
	// rootGated is true when a root-scope gate has a scheme to check for
	// this operation.
	rootGated bool
}

// maxInline is how many parameter values a request carries without
// allocating.
const maxInline = 8

// add registers template for method. It refuses a template that differs
// from a registered one only in parameter names. New guarantees a method is
// never registered twice for one template.
func (rt *router) add(method, template string, r *route) error {
	n := &rt.root
	if template != "/" {
		for seg := range strings.SplitSeq(template[1:], "/") {
			if strings.HasPrefix(seg, "{") {
				if n.param == nil {
					n.param = &routeNode{}
				}
				n = n.param
				continue
			}
			if n.literals == nil {
				n.literals = map[string]*routeNode{}
			}
			next, ok := n.literals[seg]
			if !ok {
				next = &routeNode{}
				n.literals[seg] = next
			}
			n = next
		}
	}
	if n.template != "" && n.template != template {
		return fmt.Errorf("%s and %s differ only in parameter names", n.template, template)
	}
	if n.routes == nil {
		n.routes = map[string]*route{}
	}
	n.template = template
	n.routes[method] = r
	return nil
}

// match is the outcome of a lookup.
type match struct {
	route   *route
	vals    [maxInline]string
	more    []string // values past maxInline
	n       int
	allowed map[string]bool // methods served at the path, when route is nil
}

func (m *match) push(v string) {
	if m.n < maxInline {
		m.vals[m.n] = v
	} else {
		m.more = append(m.more, v)
	}
	m.n++
}

func (m *match) pop() {
	m.n--
	if m.n >= maxInline {
		m.more = m.more[:len(m.more)-1]
	}
}

func (m *match) value(i int) string {
	if i < maxInline {
		return m.vals[i]
	}
	return m.more[i-maxInline]
}

// lookup matches an escaped path ("/users/a%2Fb") and method into m. Each
// segment is unescaped after splitting, so %2F stays inside its segment, as
// in ServeMux. GET also answers HEAD. A path not starting with "/" matches
// nothing.
func (rt *router) lookup(method, escaped string, m *match) {
	if !strings.HasPrefix(escaped, "/") {
		return
	}
	rest := ""
	if escaped != "/" {
		rest = escaped[1:]
	}
	rt.walk(&rt.root, escaped == "/", rest, method, m)
}

// walk matches rest below n. done is true once every segment is consumed.
func (rt *router) walk(n *routeNode, done bool, rest, method string, m *match) bool {
	if done {
		if len(n.routes) == 0 {
			return false
		}
		r := n.routes[method]
		if r == nil && method == http.MethodHead {
			r = n.routes[http.MethodGet]
		}
		if r != nil {
			m.route = r
			return true
		}
		if m.allowed == nil {
			m.allowed = map[string]bool{}
		}
		for meth := range n.routes {
			m.allowed[meth] = true
			if meth == http.MethodGet {
				m.allowed[http.MethodHead] = true
			}
		}
		return false
	}
	seg, after, more := strings.Cut(rest, "/")
	val := seg
	if strings.IndexByte(seg, '%') >= 0 {
		u, err := url.PathUnescape(seg)
		if err != nil {
			return false
		}
		val = u
	}
	if next := n.literals[val]; next != nil && rt.walk(next, !more, after, method, m) {
		return true
	}
	if n.param != nil && val != "" {
		m.push(val)
		if rt.walk(n.param, !more, after, method, m) {
			return true
		}
		m.pop()
	}
	return false
}

// allowHeader returns the Allow value for a 405 or an OPTIONS, sorted as
// ServeMux sorts it.
func allowHeader(allowed map[string]bool) string {
	ms := make([]string, 0, len(allowed))
	for meth := range allowed {
		ms = append(ms, meth)
	}
	slices.Sort(ms)
	return strings.Join(ms, ", ")
}

// allow returns the Allow value of an escaped, clean path: the methods of
// every template matching it, HEAD beside GET, and OPTIONS. Looking up the
// method "", which nothing serves, visits every matching template.
func (rt *router) allow(escaped string) string {
	var m match
	rt.lookup("", escaped, &m)
	return allowHeader(m.allowed)
}

// allowValues returns, sorted, every Allow value OPTIONS answers on paths r
// serves; overlapping templates (/a/b beside /a/{x}) make it vary by path.
// Each parameter segment of r's template is tried as each literal a
// still-matching template has there, and as a value no literal is.
//
// The frontier is never empty: it always holds r's own node at that depth.
func (rt *router) allowValues(r *route) []string {
	var segs []string
	if r.op.path != "/" {
		segs = strings.Split(r.op.path[1:], "/")
	}
	var values []string
	var try func(i int, frontier []*routeNode, path string)
	try = func(i int, frontier []*routeNode, path string) {
		if i == len(segs) {
			if path == "" {
				path = "/"
			}
			var m match
			rt.lookup(http.MethodOptions, path, &m)
			if v := rt.allow(path); m.route == r && !slices.Contains(values, v) {
				values = append(values, v)
			}
			return
		}
		next := func(seg string) {
			var reached []*routeNode
			for _, n := range frontier {
				if l := n.literals[seg]; l != nil {
					reached = append(reached, l)
				}
				if n.param != nil {
					reached = append(reached, n.param)
				}
			}
			try(i+1, reached, path+"/"+seg)
		}
		if !strings.HasPrefix(segs[i], "{") {
			next(segs[i])
			return
		}
		var lits []string
		for _, n := range frontier {
			for l := range n.literals {
				if !slices.Contains(lits, l) {
					lits = append(lits, l)
				}
			}
		}
		for _, l := range lits {
			next(l)
		}
		// No literal has a brace (parsePath), so "{}" matches only a
		// parameter.
		next("{}")
	}
	try(0, []*routeNode{&rt.root}, "")
	slices.Sort(values)
	return values
}

// allowed adds methods to set, with HEAD beside GET, and OPTIONS.
func allowed(set map[string]bool, methods []methodOp) {
	for _, m := range methods {
		set[m.method] = true
		if m.method == http.MethodGet {
			set[http.MethodHead] = true
		}
	}
	set[http.MethodOptions] = true
}

// asterisk reports whether a request is "OPTIONS *" (RFC 9110 §9.3.7).
func asterisk(method, escaped string) bool {
	return method == http.MethodOptions && escaped == "*"
}

// cleanPath returns the path ServeMux redirects to: . and .. resolved,
// doubled slashes collapsed, a trailing slash kept. CONNECT and the asterisk
// form are returned unchanged.
func cleanPath(method, escaped string) string {
	if method == http.MethodConnect || escaped == "*" {
		return escaped
	}
	if escaped == "" {
		return "/"
	}
	p := escaped
	if p[0] != '/' {
		p = "/" + p
	}
	np := path.Clean(p)
	if p[len(p)-1] == '/' && np != "/" {
		np += "/"
	}
	return np
}
