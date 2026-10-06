package geta

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// New assembles t into an [App]. Production and getatest both call it, so
// every check runs in any test that builds the app. It returns an error
// naming the place for struct tags that disagree with the URL, invalid
// schemas and constraints, descending middleware order, invalid failure
// rows, schemes without a verifier or a gate, duplicate operation IDs and
// schema names, and a body on a status that has none.
func New(t Table, opts ...Option) (*App, error) {
	cfg := config{log: slog.Default(), limits: DefaultLimits, info: Info{Title: "API", Version: "0.0.0"}, oas: OpenAPI31}
	for _, o := range opts {
		o(&cfg)
	}
	a := &App{
		log:    cfg.log,
		limits: cfg.limits,
		oas:    cfg.oas,
	}
	var errs []error
	fail := func(err error) { errs = append(errs, err) }
	if cfg.log == nil {
		// A nil logger would turn every defect's 500 into a panic.
		fail(errors.New("WithLogger: nil logger"))
		a.log = slog.Default()
	}

	if f, ok := openAPIVersions[cfg.oas]; ok {
		a.features = f
	} else {
		fail(fmt.Errorf("WithOpenAPI: unsupported version %q", string(cfg.oas)))
	}

	if err := cfg.limits.check(); err != nil {
		fail(err)
	}
	if err := checkScope(t.Root, "root scope"); err != nil {
		fail(err)
	}
	if err := checkOrder(t.Root, "root scope"); err != nil {
		fail(err)
	}
	// Root gates check their defaults on every request, even one that
	// reaches no operation, so check them even with no routes.
	if _, err := checkSecurity(nil, t.Root); err != nil {
		fail(fmt.Errorf("root scope: %w", err))
	}
	reg := newRegistry()
	reg.limits = &a.limits
	for _, d := range cfg.schemas {
		if err := reg.declare(d); err != nil {
			fail(err)
		}
	}
	for _, u := range cfg.unions {
		if err := reg.declareUnion(u); err != nil {
			fail(err)
		}
	}
	reg.sealDeclarations()
	a.jsonOpts = json.JoinOptions(reg.encOpts, reg.decOpts)
	paths := map[string]bool{}
	opIDs := map[string]string{}
	for _, e := range t.Routes {
		where := e.Path
		params, err := parsePath(e.Path)
		if err != nil {
			fail(err)
			continue
		}
		if paths[e.Path] {
			fail(fmt.Errorf("%s: two entries denote this path", where))
			continue
		}
		paths[e.Path] = true
		chain := slices.Clone(t.Root)
		for i, s := range e.Scopes {
			if err := checkScope(s, fmt.Sprintf("%s: scope %d", where, i)); err != nil {
				fail(err)
			}
			if err := checkBelowRoot(s, fmt.Sprintf("%s: scope %d", where, i)); err != nil {
				fail(err)
			}
			chain = append(chain, s...)
		}
		methods := e.Route.methods()
		if len(methods) == 0 {
			fail(fmt.Errorf("%s: the route serves no method", where))
			continue
		}
		for _, m := range methods {
			m.op.op = m.op.op.snapshot()
			// The operation's own scope runs innermost, for that method only.
			opChain := chain
			if s := m.op.op.doc.Scope; len(s) > 0 {
				if err := checkScope(s, fmt.Sprintf("%s %s: operation scope", m.method, where)); err != nil {
					fail(err)
					continue
				}
				if err := checkBelowRoot(s, fmt.Sprintf("%s %s: operation scope", m.method, where)); err != nil {
					fail(err)
					continue
				}
				opChain = slices.Concat(chain, s)
			}
			// BeforeGate goes just before the first gate: in the root scope
			// via the slot (rootSlot), otherwise in the route's own chain.
			routeChain := opChain[len(t.Root):]
			before := m.op.op.doc.BeforeGate
			slotted := false
			if len(before) > 0 {
				if err := checkBeforeGate(before, fmt.Sprintf("%s %s: Doc.BeforeGate", m.method, where)); err != nil {
					fail(err)
					continue
				}
				if g := firstGate(t.Root); g >= 0 {
					opChain = slices.Concat(t.Root[:g], before, t.Root[g:], routeChain)
					slotted = true
				} else {
					k := max(firstGate(routeChain), 0)
					routeChain = slices.Concat(routeChain[:k], before, routeChain[k:])
					opChain = slices.Concat(t.Root, routeChain)
				}
			}
			op, err := a.compile(reg, e.Path, params, m, opChain)
			if err != nil {
				fail(err)
				continue
			}
			op.routeChain = routeChain
			if slotted {
				op.before = before
			}
			if prev, dup := opIDs[op.opID]; dup {
				fail(fmt.Errorf("%s %s: operationId %q is also used by %s", m.method, e.Path, op.opID, prev))
				continue
			}
			opIDs[op.opID] = m.method + " " + e.Path
			a.ops = append(a.ops, op)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	everywhere := map[string]bool{}
	for _, e := range t.Routes {
		methods := e.Route.methods()
		allowed(everywhere, methods)
		op, err := a.optionsOp(e.Path, t.Root)
		if err != nil {
			fail(err)
			continue
		}
		if prev, dup := opIDs[op.opID]; dup {
			fail(fmt.Errorf("OPTIONS %s: operationId %q is also used by %s", e.Path, op.opID, prev))
			continue
		}
		opIDs[op.opID] = http.MethodOptions + " " + e.Path
		a.ops = append(a.ops, op)
	}
	// OPTIONS * is always served, even with no routes.
	everywhere[http.MethodOptions] = true
	a.allowAll = allowHeader(everywhere)
	a.listed = map[*compiledOp]map[int]bool{}
	a.byMatch = map[*Match]*compiledOp{}
	// Report a refused path once, not again for each of its methods.
	refused := map[string]bool{}
	var options []*route
	for _, op := range a.ops {
		if refused[op.path] {
			continue
		}
		params, _ := parsePath(op.path)
		rt := &route{
			h:         wrap(op.routeChain, op),
			match:     &Match{Template: op.path, Method: op.method, Doc: op.op.snapshot().doc, Operation: true},
			params:    params,
			op:        op,
			pattern:   op.method + " " + op.path,
			rootGated: rootGated(t.Root, op.op.doc.Security),
		}
		if len(params) > 0 {
			rt.values = valuesMux(rt.pattern)
		}
		a.byMatch[rt.match] = op
		if op.window > 0 {
			a.drains = true
		}
		op.corsHeaders()
		if err := a.router.add(op.method, op.path, rt); err != nil {
			fail(err)
			refused[op.path] = true
		}
		if op.options {
			options = append(options, rt)
		}
		listed := map[int]bool{}
		for status := range op.responses() {
			n, _ := strconv.Atoi(status)
			listed[n] = true
		}
		a.listed[op] = listed
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	// Allow depends on every template, so compute it once all are added.
	for _, rt := range options {
		rt.op.allow = a.router.allowValues(rt)
	}
	// A request that reaches no operation (404, 405, redirect, OPTIONS *)
	// runs the root scope alone, including its Timeouts.
	a.rootWindow = chainWindow(t.Root, 0)
	if a.rootWindow > 0 {
		a.drains = true
	}
	a.root = wrap(a.rootChain(t.Root), http.HandlerFunc(a.dispatch))
	doc, err := a.openAPI(cfg.info, reg)
	if err != nil {
		return nil, err
	}
	a.doc = doc
	return a, nil
}

func checkScope(s Scope, where string) error {
	for i, m := range s {
		if m.bad != nil {
			return fmt.Errorf("%s: middleware %d (%s): %w", where, i, m.name, m.bad)
		}
		if m.fn == nil {
			return fmt.Errorf("%s: middleware %d is the zero geta.Middleware", where, i)
		}
		if err := m.checkHeaders(); err != nil {
			return fmt.Errorf("%s: middleware %d (%s): %w", where, i, m.name, err)
		}
	}
	return nil
}

// checkBelowRoot refuses a root-only middleware in a scope below the root
// (a directory's, a Doc.Scope, a Doc.BeforeGate).
func checkBelowRoot(s Scope, where string) error {
	for i, m := range s {
		if m.root != "" {
			return fmt.Errorf("%s: middleware %d (%s) works only in the root scope: %s", where, i, m.name, m.root)
		}
	}
	return nil
}

// rootGated reports whether a gate in root has a scheme to check for an
// operation: the declared security, or the gate's default if none is
// declared.
func rootGated(root Scope, security []Scheme) bool {
	for _, m := range root {
		switch {
		case m.gate == nil:
		case security != nil:
			if len(security) > 0 {
				return true
			}
		case len(m.gate.Default) > 0:
			return true
		}
	}
	return false
}

// chainWindow returns how long a request through chain has for its body to
// arrive: the shortest of chain's Timeouts, each replaced by op (a
// Doc.Timeout) when op is positive, or 0 if chain has no Timeout.
func chainWindow(chain []Middleware, op time.Duration) time.Duration {
	var w time.Duration
	for _, m := range chain {
		if !m.timeout {
			continue
		}
		l := m.length
		if op > 0 {
			l = op
		}
		if w == 0 || l < w {
			w = l
		}
	}
	return w
}

func wrap(chain []Middleware, h http.Handler) http.Handler {
	for i := len(chain) - 1; i >= 0; i-- {
		h = chain[i].fn(h)
	}
	return h
}

// parsePath validates a table path and returns its parameter names.
func parsePath(p string) ([]string, error) {
	if !strings.HasPrefix(p, "/") {
		return nil, fmt.Errorf("path %q does not start with /", p)
	}
	if p == "/" {
		return nil, nil
	}
	var params []string
	for seg := range strings.SplitSeq(p[1:], "/") {
		if seg == "" {
			return nil, fmt.Errorf("path %q has an empty segment", p)
		}
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			name := seg[1 : len(seg)-1]
			if !validWildcard(name) {
				return nil, fmt.Errorf("path %q: parameter %q is not a Go identifier", p, name)
			}
			if slices.Contains(params, name) {
				return nil, fmt.Errorf("path %q repeats parameter %q", p, name)
			}
			params = append(params, name)
			continue
		}
		if strings.ContainsAny(seg, "{}") {
			return nil, fmt.Errorf("path %q: segment %q is neither literal nor a whole parameter", p, seg)
		}
		// Segments are matched decoded, so a literal is written unescaped:
		// "café", not "caf%C3%A9".
		if strings.Contains(seg, "%") {
			return nil, fmt.Errorf("path %q: segment %q contains %%", p, seg)
		}
		// Clients remove dot segments (RFC 3986 §5.2.4), so none could
		// reach such a path.
		if seg == "." || seg == ".." {
			return nil, fmt.Errorf("path %q: segment %q is a dot segment", p, seg)
		}
		// ? and # end a path; spaces and control characters are not URI
		// characters.
		if i := strings.IndexFunc(seg, func(r rune) bool { return r == '?' || r == '#' || r == ' ' || unicode.IsControl(r) }); i >= 0 {
			r, _ := utf8.DecodeRuneInString(seg[i:])
			return nil, fmt.Errorf("path %q: segment %q contains %q", p, seg, r)
		}
	}
	return params, nil
}

func (a *App) compile(reg *registry, path string, params []string, m methodOp, chain []Middleware) (*compiledOp, error) {
	op := m.op.op
	where := fmt.Sprintf("%s %s (%s)", m.method, path, op.site)
	c := &compiledOp{method: m.method, path: path, op: op, chain: chain, app: a, limits: a.limits}
	if err := op.doc.checkText(); err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	if op.doc.Limits != nil {
		c.limits = op.doc.Limits(a.limits)
		if err := c.limits.check(); err != nil {
			return nil, fmt.Errorf("%s: Doc.Limits: %w", where, err)
		}
	}
	// Plan the input under the operation's own limits.
	appLimits := reg.limits
	reg.limits = &c.limits
	defer func() { reg.limits = appLimits }()
	// A 3.1 document has no field for QUERY.
	if _, known := openAPIVersions[a.oas]; known && m.method == MethodQuery && !a.features.queryOperation {
		return nil, fmt.Errorf("%s: OpenAPI %s has no QUERY operation; use geta.OpenAPI32", where, a.oas)
	}
	in, err := reg.inPlan(op.in)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	c.in = in
	if in.body != nil || in.form != nil || in.raw != nil {
		if err := CheckMethodBody(m.method); err != nil {
			return nil, fmt.Errorf("%s: %w", where, err)
		}
	}
	if in.body != nil {
		in.body.method = m.method
		// Types in a request body must be readable, not only writable.
		if err := reg.checkRead(in.body.c); err != nil {
			return nil, fmt.Errorf("%s: %w", where, err)
		}
	}
	if in.form != nil {
		in.form.method = m.method
	}
	if in.raw != nil {
		in.raw.method = m.method
	}
	bound := map[string]bool{}
	for _, p := range in.params {
		if p.in != "path" {
			continue
		}
		if !slices.Contains(params, p.name) {
			return nil, fmt.Errorf("%s: input binds path parameter %q not in the URL", where, p.name)
		}
		bound[p.name] = true
	}
	for _, p := range params {
		if !bound[p] {
			return nil, fmt.Errorf("%s: input does not bind path parameter {%s}", where, p)
		}
	}
	var outType reflect.Type
	if op.out != nil {
		outType = op.out
	}
	out, err := reg.outPlan(outType)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	c.out = out
	if err := checkStatus(op.status, out, outType); err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	c.rows = make([]*problemPlan, len(op.doc.Failures))
	for i, f := range op.doc.Failures {
		if err := f.check(); err != nil {
			return nil, fmt.Errorf("%s: failure row %d: %w", where, i, err)
		}
		if f.describe != nil {
			pp, err := reg.problemPlan(f.describe.t)
			if err != nil {
				return nil, fmt.Errorf("%s: failure row %d (%s): %w", where, i, f.label, err)
			}
			c.rows[i] = pp
		}
	}
	if err := c.checkRowHeaders(); err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	if err := checkOrder(chain, m.method+" "+path); err != nil {
		return nil, err
	}
	if err := CheckDocTimeout(op.doc.Timeout); err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	switch t := op.doc.Timeout; {
	case t > 0 && !slices.ContainsFunc(chain, func(m Middleware) bool { return m.timeout }):
		return nil, fmt.Errorf("%s: Doc.Timeout %v, but the chain has no geta.Timeout", where, t)
	}
	c.window = chainWindow(chain, op.doc.Timeout)
	reqs, err := checkSecurity(op.doc.Security, chain)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	c.require(reqs)
	// After require, so a gate that requires nothing declares nothing.
	if err := c.checkMiddlewareHeaders(); err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	if err := c.planScopes(op.doc.Security); err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	if err := c.planDeprecation(); err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	if c.deprecated != nil {
		a.deprecates = true
	}
	c.opID = op.doc.OperationID
	if c.opID == "" {
		c.opID = operationID(m.method, path)
	}
	return c, nil
}

// require sets what c requires: reqs, and every scheme some requirement
// names.
func (c *compiledOp) require(reqs [][]Scheme) {
	c.requirements = reqs
	for _, req := range reqs {
		for _, s := range req {
			if !containsScheme(c.security, s) {
				c.security = append(c.security, s)
			}
		}
	}
}

// planScopes gathers the scopes the chain's middleware require
// (Middleware.Scopes) and the requirements the document lists. It refuses
// scopes nothing could meet: on a scheme no earlier gate requires, on a
// scheme defined differently from the gate's, outside an oauth2 scheme's
// flows, or when no requirement holds every scheme the chain asks scopes of.
func (c *compiledOp) planScopes(declared []Scheme) error {
	c.documented = c.requirements
	var gates []*Policy // the gates before the middleware at hand
	for i, m := range c.chain {
		if m.gate != nil {
			gates = append(gates, m.gate)
		}
		for _, sc := range m.scopes {
			var required *Scheme
			for _, g := range gates {
				schemes := g.Default
				if declared != nil {
					schemes = declared
				}
				for j := range schemes {
					if schemes[j].Name == sc.scheme.Name {
						required = &schemes[j]
					}
				}
			}
			if required == nil {
				return fmt.Errorf("middleware %d (%s) requires scopes of scheme %q, which no earlier geta.Secure gate requires",
					i, m.name, sc.scheme.Name)
			}
			if !required.equal(sc.scheme) {
				return fmt.Errorf("middleware %d (%s) defines scheme %q as %+v, but the gate defines it as %+v",
					i, m.name, sc.scheme.Name, sc.scheme, *required)
			}
			if required.Type == "oauth2" {
				for _, s := range sc.scopes {
					if !required.defines(s) {
						return fmt.Errorf("middleware %d (%s) requires scope %q, which no flow of oauth2 scheme %q defines",
							i, m.name, s, sc.scheme.Name)
					}
				}
			}
			if c.scopes == nil {
				c.scopes = map[string][]string{}
			}
			for _, s := range sc.scopes {
				if !slices.Contains(c.scopes[sc.scheme.Name], s) {
					c.scopes[sc.scheme.Name] = append(c.scopes[sc.scheme.Name], s)
				}
			}
		}
	}
	if c.scopes == nil {
		return nil
	}
	c.documented = slices.DeleteFunc(slices.Clone(c.requirements), func(req []Scheme) bool {
		for name := range c.scopes {
			if !slices.ContainsFunc(req, func(s Scheme) bool { return s.Name == name }) {
				return true
			}
		}
		return false
	})
	if len(c.documented) == 0 {
		names := slices.Sorted(maps.Keys(c.scopes))
		return fmt.Errorf("no security requirement holds schemes %s, whose scopes its middleware require", strings.Join(names, " and "))
	}
	return nil
}

// optionsOp returns the OPTIONS operation geta serves on a template: 204
// with Allow listing the path's methods, HEAD beside GET, and OPTIONS (RFC
// 9110 §9.3.7). A route cannot declare its own.
//
// It gives the same Allow as a 405, so it runs where a 405 does: in the
// root scope alone, with root gates checking their defaults.
func (a *App) optionsOp(path string, root Scope) (*compiledOp, error) {
	c := &compiledOp{
		method: http.MethodOptions,
		path:   path,
		op: &operation{
			status: http.StatusNoContent,
			doc:    Doc{Summary: "The methods this URL serves"},
			in:     reflect.TypeFor[struct{}](),
			site:   "geta",
		},
		in:      &inPlan{},
		out:     &outPlan{kind: outNone},
		chain:   slices.Clone(root),
		opID:    operationID(http.MethodOptions, path),
		app:     a,
		limits:  a.limits,
		options: true,
		window:  chainWindow(root, 0),
	}
	// New already checked the root scope.
	reqs, _ := checkSecurity(nil, c.chain)
	c.require(reqs)
	if err := c.planScopes(nil); err != nil {
		return nil, fmt.Errorf("OPTIONS %s: %w", path, err)
	}
	return c, nil
}

func checkStatus(status int, out *outPlan, outType reflect.Type) error {
	if sp, ok := out.special.(interface{ status() int }); ok && out.kind == outSpecial {
		if status != sp.status() {
			return fmt.Errorf("success status %d, but this output answers %d", status, sp.status())
		}
		return nil
	}
	// The same rule CheckSuccessStatus applies for getavet.
	output := ""
	if outType != nil {
		output = outType.String()
	}
	if err := checkSuccess(status, output, out.statuses, out.locationField()); err != nil {
		return err
	}
	statuses := []int{status}
	if out.statuses != nil {
		// A zero status field means the operation's status, so the field
		// must declare it.
		if err := statusDeclared(status, out.statuses); err != nil {
			return err
		}
		statuses = out.statuses
	}
	for _, s := range statuses {
		if out.hasBody() && (s == http.StatusNoContent || s == http.StatusResetContent) {
			return fmt.Errorf("success status %d takes no body, but the output has one", s)
		}
	}
	return nil
}

// successes returns the success statuses in order: the operation's own, or
// those its output's status field declares.
func (c *compiledOp) successes() []int {
	if c.out.statuses == nil {
		return []int{c.op.status}
	}
	return slices.Sorted(slices.Values(c.out.statuses))
}

// checkSecurity resolves what an operation requires, as OpenAPI security
// requirement objects, and rejects a requirement nothing can enforce.
//
// Every gate in the chain must admit the request. With declared schemes,
// each gate checks those, so there is one object per scheme. Otherwise each
// gate checks its default, so there is one object per combination of one
// scheme from each non-empty default. A combination that contains another
// is dropped.
func checkSecurity(declared []Scheme, chain []Middleware) ([][]Scheme, error) {
	var gates []*Policy
	for _, m := range chain {
		if m.gate != nil {
			gates = append(gates, m.gate)
		}
	}
	if declared != nil {
		if len(declared) > 0 && len(gates) == 0 {
			names := make([]string, len(declared))
			for i, s := range declared {
				names[i] = s.Name
			}
			return nil, fmt.Errorf("requires %s, but no geta.Secure gate is in its chain", strings.Join(names, " or "))
		}
		var reqs [][]Scheme
		for _, s := range declared {
			if err := checkScheme(s, gates); err != nil {
				return nil, err
			}
			reqs = addRequirement(reqs, []Scheme{s})
		}
		return reqs, nil
	}
	reqs := [][]Scheme{nil}
	for _, g := range gates {
		if len(g.Default) == 0 {
			continue
		}
		for _, s := range g.Default {
			if err := checkScheme(s, []*Policy{g}); err != nil {
				return nil, err
			}
		}
		var next [][]Scheme
		for _, req := range reqs {
			for _, s := range g.Default {
				r := req
				if !containsScheme(r, s) {
					r = append(slices.Clone(r), s)
				}
				next = addRequirement(next, r)
			}
		}
		reqs = next
	}
	if len(reqs) == 1 && len(reqs[0]) == 0 {
		return nil, nil
	}
	return reqs, nil
}

// checkScheme rejects a scheme that is incomplete or that one of gates
// cannot verify.
func checkScheme(s Scheme, gates []*Policy) error {
	if s.Name == "" || s.Type == "" {
		return fmt.Errorf("security scheme %+v has no Name or Type", s)
	}
	if err := s.check(); err != nil {
		return fmt.Errorf("security scheme %q: %w", s.Name, err)
	}
	for _, g := range gates {
		if g.Verifiers[s.Name] == nil {
			return fmt.Errorf("requires scheme %q, but the gate's policy has no verifier for it", s.Name)
		}
	}
	return nil
}

// addRequirement adds req to reqs unless a subset of it is already there,
// and drops any supersets of it.
func addRequirement(reqs [][]Scheme, req []Scheme) [][]Scheme {
	within := func(small, big []Scheme) bool {
		for _, s := range small {
			if !containsScheme(big, s) {
				return false
			}
		}
		return true
	}
	for _, r := range reqs {
		if within(r, req) {
			return reqs
		}
	}
	reqs = slices.DeleteFunc(reqs, func(r []Scheme) bool { return within(req, r) })
	return append(reqs, req)
}

// operationID derives an identifier from the method and path:
// GET /users/{id}/posts is getUsersIdPosts.
func operationID(method, path string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(method))
	if path == "/" {
		b.WriteString("Root")
	}
	for _, r := range strings.FieldsFunc(path, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		rs := []rune(r)
		rs[0] = unicode.ToUpper(rs[0])
		b.WriteString(string(rs))
	}
	return b.String()
}
