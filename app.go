package geta

import (
	"cmp"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"
)

// Table is the route table geta sync writes: the root scope and one entry
// per route directory. It holds only values already built from the
// application's Env, never the Env itself.
type Table struct {
	// Root wraps the whole dispatch, 404 and 405 included, and is the only
	// scope OPTIONS runs.
	Root Scope
	// Routes are the URLs, each with the scopes of the directories above it,
	// outermost first.
	Routes []Entry
}

// Entry is one URL of the table.
type Entry struct {
	Path   string
	Route  Route
	Scopes []Scope
}

// App is an assembled application. It is an http.Handler.
type App struct {
	router   router
	root     http.Handler
	log      *slog.Logger
	ops      []*compiledOp                // in table order
	byMatch  map[*Match]*compiledOp       // the operation each route's match names
	listed   map[*compiledOp]map[int]bool // the statuses the document lists
	doc      []byte
	limits   Limits
	jsonOpts json.Options // the sealed types' marshalers and unmarshalers
	// allowAll is the Allow value of OPTIONS *: every method some path serves.
	allowAll string
	// oas is the version the document follows, and features what it can say.
	oas      OpenAPIVersion
	features oasFeatures
	// deprecates is set when some operation sends deprecation headers.
	deprecates bool
	// rootWindow is the body window of a request that reaches no operation,
	// 0 for none. drains is set when some request has a window, so ServeHTTP
	// installs a drain.
	rootWindow time.Duration
	drains     bool
}

// compiledOp is one operation, assembled.
type compiledOp struct {
	method string
	path   string
	op     *operation
	in     *inPlan
	out    *outPlan
	chain  []Middleware // root first, then the entry's scopes, then the operation's
	// routeChain is the part of chain past the root scope. before is the
	// Doc.BeforeGate the root scope's slot runs, or nil.
	routeChain []Middleware
	before     Scope
	security   []Scheme // every scheme some requirement names
	// requirements are the OpenAPI security requirements: the request must
	// satisfy every scheme of one of them. Empty when nothing is required.
	requirements [][]Scheme
	// scopes maps a scheme name to the scopes the chain requires of it
	// (Middleware.Scopes). documented are the requirements the document
	// lists: those holding every scheme scopes names.
	scopes     map[string][]string
	documented [][]Scheme
	opID       string
	app        *App
	// rows are the plans of OnAsProblem failure rows, in row order; nil for
	// the others.
	rows []*problemPlan
	// problems maps a failure status to its documented problem.
	problems map[int]*failureResponse
	// headers maps a status to the response headers Conforms checks, in
	// report order.
	headers map[int][]responseHeader
	// limits are the App's, or the operation's own (Doc.Limits).
	limits Limits
	// corsAllow and corsExpose are the headers [CORS] allows and exposes.
	corsAllow, corsExpose []string
	// options is set on the OPTIONS operation geta serves itself, and allow
	// lists the Allow values it answers with.
	options bool
	allow   []string
	// deprecated is what Doc.Deprecation and Doc.Sunset send, nil for neither.
	deprecated *deprecationPlan
	// window is the time the request body has to arrive: the shortest
	// Timeout length in chain, 0 for none.
	window time.Duration
}

// operation returns the operation m names, or nil if none of a's does.
func (a *App) operation(m Match) *compiledOp {
	for _, op := range a.ops {
		if op.method == m.Method && op.path == m.Template {
			return op
		}
	}
	return nil
}

// Documented reports whether the OpenAPI document lists status among the
// responses of the operation m names, as [Observe] reports it. matched is
// false when m names no operation of this app (a 404, a 405, a preflight, a
// redirect). getatest calls it on every response, so a status a middleware
// sends without declaring it ([Middleware.Answers]) fails the test.
func (a *App) Documented(m Match, status int) (documented, matched bool) {
	op := a.operation(m)
	if op == nil {
		return false, false
	}
	return a.listed[op][status], true
}

// Conforms checks a response's header fields and body against what the
// document states for the operation m names, as [Observe] reports it. It
// returns nil when m names no operation. getatest calls it on every response
// and every event; geta itself checks neither at runtime.
//
// Each header the document states on status with a declared schema is
// checked: a middleware's ([Middleware.Header] with [HeaderOf]), a failure
// row's, and an output's header field. Each is parsed as a request header
// parameter of its type and held to its schema. A missing required header
// and a header sent on more than one line fail. Plain-string headers and
// geta's own (Allow, Accept, Deprecation, Sunset, Set-Cookie) are not
// checked. A nil header checks none, as for a stream's events.
//
// The body is checked when it is a JSON body of a success status, an event
// stream (each event's data against the event type), or the problem of a
// failure status an [OnAsProblem] row answers. An empty body, a raw body, an
// upgrade, and a plain problem are not checked. [Limits] do not apply: a
// response string past MaxStringLength conforms unless a declared maxLength
// refuses it. A response that fails on both is reported for both, headers
// first.
func (a *App) Conforms(m Match, status int, header http.Header, body []byte) error {
	op := a.operation(m)
	if op == nil {
		return nil
	}
	var herr error
	if header != nil {
		herr = op.conformsHeaders(status, header)
	}
	return errors.Join(herr, op.conformsBody(status, body))
}

// responseHeader is a documented response header with a declared schema,
// which Conforms checks.
type responseHeader struct {
	name     string // as the document states it
	c        *codec
	use      *schema
	required bool
}

// conformsHeaders checks h, the header of a response of op with status, for
// Conforms. Values are parsed as request header parameters are, with no
// limits.
func (op *compiledOp) conformsHeaders(status int, h http.Header) error {
	d := &decoder{limits: unbounded, in: "header", written: true}
	for _, rh := range op.headers[status] {
		raw := h.Values(rh.name)
		switch {
		case len(raw) == 0:
			if rh.required {
				d.fail(rh.name, "missing required header")
			}
		case len(raw) > 1:
			d.fail(rh.name, "sent on %d lines, but takes one value", len(raw))
		default:
			d.check(rh.c, rh.use, paramValue(rh.c, raw[0]), rh.name)
		}
	}
	return d.mismatch("a header")
}

// conformsBody checks body, a response of op with status, for Conforms.
func (op *compiledOp) conformsBody(status int, body []byte) error {
	if len(body) == 0 {
		return nil
	}
	if fr := op.problems[status]; fr != nil {
		return fr.conforms(body)
	}
	if !slices.Contains(op.successes(), status) {
		return nil
	}
	switch {
	case op.out.body != nil && (op.out.kind == outJSON || op.out.kind == outEnvelope):
		return conformsTo(op.out.body, op.out.bodyUse, body, "the body")
	case op.out.kind == outSpecial:
		if ep, ok := op.out.special.(interface{ eventCodec() *codec }); ok {
			return conformsEvents(ep.eventCodec(), body)
		}
	}
	return nil
}

// conformsTo checks the JSON value body, called what, against s and c.
func conformsTo(c *codec, s *schema, body []byte, what string) error {
	v, err := parseJSON(body, math.MaxInt)
	if err != nil {
		return fmt.Errorf("%s is not JSON: %w", what, err)
	}
	// A response's schema states no backstop, so none applies.
	d := &decoder{limits: unbounded, in: "response", written: true}
	d.check(c, s, v, "$")
	return d.mismatch(what)
}

// conformsEvents checks each event's data in body, a text/event-stream,
// against c.
func conformsEvents(c *codec, body []byte) error {
	text := strings.ReplaceAll(strings.ReplaceAll(string(body), "\r\n", "\n"), "\r", "\n")
	var data []string
	n := 0
	for line := range strings.SplitSeq(text, "\n") {
		if line != "" {
			if field, value, _ := strings.Cut(line, ":"); field == "data" {
				data = append(data, strings.TrimPrefix(value, " "))
			}
			continue
		}
		if data == nil {
			continue
		}
		n++
		if err := conformsTo(c, c.use(), []byte(strings.Join(data, "\n")), "the data"); err != nil {
			return fmt.Errorf("event %d: %w", n, err)
		}
		data = nil
	}
	return nil
}

// mismatch returns d's violations as one error, or nil.
func (d *decoder) mismatch(what string) error {
	if len(d.errs) == 0 {
		return nil
	}
	msgs := make([]string, len(d.errs))
	for i, e := range d.errs {
		msgs[i] = e.Path + ": " + e.Message
	}
	if d.omitted > 0 {
		msgs = append(msgs, fmt.Sprintf("and %d more not listed", d.omitted))
	}
	return fmt.Errorf("%s does not match the documented schema: %s", what, strings.Join(msgs, "; "))
}

// JSONOptions returns the encoding/json/v2 options the app reads and writes
// bodies with, including its sealed types' marshalers and unmarshalers. A
// client decoding into the same types needs them.
func (a *App) JSONOptions() json.Options { return a.jsonOpts }

// Types returns the input and output types of the operation that answers
// method on template, as written in the table ("/users/{id}"); out is nil
// for an operation built with [OpNoBody]. ok is false when there is no such
// operation.
func (a *App) Types(method, template string) (in, out reflect.Type, ok bool) {
	for _, op := range a.ops {
		if op.method == method && op.path == template {
			return op.op.in, op.op.out, true
		}
	}
	return nil, nil, false
}

// OpenAPI returns the OpenAPI document, 3.1 unless [WithOpenAPI] selected
// another version. New builds it from the same values the runtime reads, and
// it is byte-for-byte deterministic.
func (a *App) OpenAPI() []byte { return slices.Clone(a.doc) }

// Operations returns the assembled operations as "METHOD /template", sorted.
func (a *App) Operations() []string {
	out := make([]string, len(a.ops))
	for i, op := range a.ops {
		out[i] = op.method + " " + op.path
	}
	slices.SortFunc(out, func(x, y string) int { return cmp.Compare(x, y) })
	return out
}
