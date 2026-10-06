package geta

import (
	"encoding/json/v2"
	"fmt"
	"maps"
	"math"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// problemPlan is how a failure row built with [OnAsProblem] writes its
// description: an envelope's headers (out) and the body's members (body).
type problemPlan struct {
	t       reflect.Type // P
	out     *outPlan     // an envelope's headers; nil when P is the body
	body    *codec       // the members; nil for an envelope without a body
	bodyIdx []int        // the envelope's body field
	detail  int          // the index of body's member "detail", or -1
	opts    json.Options
	unions  bool
}

// problemPlan plans the description type t of a row, refusing a type the
// document cannot state.
func (r *registry) problemPlan(t reflect.Type) (*problemPlan, error) {
	if err := checkProblemType(t.String(), t.Kind() == reflect.Struct); err != nil {
		return nil, err
	}
	pp := &problemPlan{t: t, detail: -1, opts: r.encOpts}
	body := (*codec)(nil)
	if isEnvelope(t) {
		var walk func(t reflect.Type) error
		walk = func(t reflect.Type) error {
			for i := range t.NumField() {
				vf := vetField(t.Field(i))
				if envelopeEmbedded(vf) {
					if err := walk(t.Field(i).Type); err != nil {
						return err
					}
					continue
				}
				if err := checkProblemField(vf); err != nil {
					return fmt.Errorf("%s.%w", t, err)
				}
			}
			return nil
		}
		if err := walk(t); err != nil {
			return nil, err
		}
		p, err := r.envelope(t)
		if err != nil {
			return nil, err
		}
		pp.out, body, pp.bodyIdx = p, p.body, p.bodyIdx
	} else {
		c, err := r.codecFor(t)
		if err != nil {
			return nil, err
		}
		body = c
	}
	if body != nil {
		if body.kind != kStruct {
			return nil, checkProblemType(body.t.String(), false)
		}
		for i, f := range body.fields {
			if err := checkProblemMember(f.json, vetKind(f.c)); err != nil {
				return nil, fmt.Errorf("%s: %w", body.t, err)
			}
			if f.json == "detail" {
				pp.detail = i
			}
		}
		pp.body, pp.unions = body, markUnions(body)
	}
	return pp, nil
}

// write answers failure f for err: the problem's members merged with the
// description's, and the description's headers. A description that cannot
// be written is a defect and answers 500.
func (pp *problemPlan) write(w http.ResponseWriter, r *http.Request, c *compiledOp, f Failure, err error) {
	v := f.describe.fn(err)
	p := &Problem{Type: f.problemType(), Title: http.StatusText(f.status), Status: f.status, Detail: f.detail}
	var members []byte
	if pp.body != nil {
		bv := v
		if pp.bodyIdx != nil {
			bv = v.FieldByIndex(pp.bodyIdx)
		}
		if pp.unions {
			if err := nilUnion(pp.body, bv, "$"); err != nil {
				c.writeDefect(w, r, "geta: a failure's description could not be written", err)
				return
			}
		}
		if pp.detail >= 0 {
			// The description's detail replaces the row's unless it is nil.
			fc := pp.body.fields[pp.detail]
			if dv := bv.FieldByIndex(fc.index); !fc.optional || !dv.IsNil() {
				p.Detail = ""
			}
		}
		b, err := json.Marshal(bv.Addr().Interface(), pp.opts)
		if err != nil {
			c.writeDefect(w, r, "geta: a failure's description could not be written", err)
			return
		}
		members = b
	}
	h := w.Header()
	if pp.out != nil {
		if err := pp.out.setHeaders(h, v); err != nil {
			c.writeDefect(w, r, "geta: a failure's description could not be written", err)
			return
		}
	}
	body, _ := json.Marshal(p, marshalOptions)
	if len(members) > 2 {
		// Splice two objects whose names are disjoint (checkProblemMember).
		body = append(append(body[:len(body)-1], ','), members[1:]...)
	}
	h["Content-Type"] = []string{ProblemContentType}
	h["Content-Length"] = []string{strconv.Itoa(len(body))}
	h["X-Content-Type-Options"] = []string{"nosniff"}
	w.WriteHeader(f.status)
	if _, err := w.Write(body); err != nil {
		abortUntaken(r, err)
	}
}

// schema returns Problem's schema combined (allOf) with an open object of
// the description's members.
func (pp *problemPlan) schema() map[string]any {
	ref := map[string]any{"$ref": componentRef + "Problem"}
	if pp.body == nil {
		return ref
	}
	members := pp.body.schema.document(nil)
	delete(members, "additionalProperties")
	return map[string]any{"allOf": []any{ref, members}}
}

// failureResponse is what the document states of one failure status: its
// content's schema and its headers.
type failureResponse struct {
	schemas []any                // one per distinct cause
	plans   []*problemPlan       // the descriptions those schemas state, in order
	plain   bool                 // a cause answers a plain problem
	headers map[string]any       // the descriptions' headers
	outs    map[string]headerOut // what states each of headers, for Conforms
	setters map[string]int       // how many causes set each header, always
	causes  int
}

// failureResponses gathers, per failure status, the schemas and headers of
// the rows that describe their problem. plain counts, per status, the
// causes that answer a plain problem.
func (c *compiledOp) failureResponses(plain map[int]int) map[int]*failureResponse {
	out := map[int]*failureResponse{}
	seen := map[int]map[reflect.Type]bool{}
	for i, f := range c.op.doc.Failures {
		pp := c.rows[i]
		if pp == nil {
			continue
		}
		fr := out[f.status]
		if fr == nil {
			fr = &failureResponse{headers: map[string]any{}, outs: map[string]headerOut{}, setters: map[string]int{}}
			out[f.status], seen[f.status] = fr, map[reflect.Type]bool{}
		}
		fr.causes++
		if seen[f.status][pp.t] {
			for _, h := range pp.headers() {
				if !h.optional {
					fr.setters[headerKey(fr.headers, h.name)]++
				}
			}
			continue
		}
		seen[f.status][pp.t] = true
		fr.schemas = append(fr.schemas, pp.schema())
		fr.plans = append(fr.plans, pp)
		for _, h := range pp.headers() {
			// Rows may spell a name differently; the first spelling wins.
			name := headerKey(fr.headers, h.name)
			header := map[string]any{"schema": h.use.document(nil)}
			if h.desc != "" {
				header["description"] = h.desc
			}
			fr.headers[name] = header
			fr.outs[name] = h
			if !h.optional {
				fr.setters[name]++
			}
		}
	}
	for status, fr := range out {
		fr.causes += plain[status]
		fr.plain = plain[status] > 0
		for _, name := range slices.Sorted(maps.Keys(fr.headers)) {
			// A header is required where every cause of the status sets it.
			fr.headers[name].(map[string]any)["required"] = fr.setters[name] == fr.causes
		}
	}
	return out
}

// headerKey returns the key of m that matches name case-insensitively
// (RFC 9110 §5.1), or name if none does.
func headerKey[V any](m map[string]V, name string) string {
	for k := range m {
		if strings.EqualFold(k, name) {
			return k
		}
	}
	return name
}

func (pp *problemPlan) headers() []headerOut {
	if pp.out == nil {
		return nil
	}
	return pp.out.headers
}

// content returns the status's content: the plain or described problem, or
// anyOf them when causes differ.
func (fr *failureResponse) content() map[string]any {
	schemas := fr.schemas
	if fr.plain {
		schemas = append([]any{map[string]any{"$ref": componentRef + "Problem"}}, schemas...)
	}
	s := schemas[0].(map[string]any)
	if len(schemas) > 1 {
		s = map[string]any{"anyOf": schemas}
	}
	return map[string]any{ProblemContentType: map[string]any{"schema": s}}
}

// conforms checks a problem body against content: Problem's members, and
// the members of one of the status's descriptions. If any cause answers a
// plain problem, any problem conforms. Unknown members are allowed.
func (fr *failureResponse) conforms(body []byte) error {
	v, err := parseJSON(body, math.MaxInt)
	if err != nil {
		return fmt.Errorf("the body is not JSON: %w", err)
	}
	d := &decoder{limits: unbounded, in: "response", written: true}
	m, isObject := v.(map[string]any)
	if !isObject {
		d.fail("$", "expected object, got %s", jsonType(v))
		return d.mismatch("the body")
	}
	checkProblem(d, m)
	if len(d.errs) > 0 || fr.plain {
		return d.mismatch("the body")
	}
	for i, pp := range fr.plans {
		if pp.body == nil {
			return nil // headers only: Problem's members are the whole body
		}
		pd := &decoder{limits: unbounded, in: "response", written: true}
		members := make(map[string]any, len(pp.body.fields))
		for _, f := range pp.body.fields {
			if mv, ok := m[f.json]; ok {
				members[f.json] = mv
			}
		}
		pd.check(pp.body, pp.body.use(), members, "$")
		if len(pd.errs) == 0 {
			return nil
		}
		if i == 0 {
			d.errs, d.omitted = pd.errs, pd.omitted
		}
	}
	return d.mismatch("the body")
}

// checkProblem checks m against [Problem]'s schema (problemSchema).
func checkProblem(d *decoder, m map[string]any) {
	for _, name := range []string{"type", "title", "status"} {
		if _, ok := m[name]; !ok {
			d.fail("$."+name, "missing required member")
		}
	}
	for _, f := range []struct{ name, want string }{
		{"type", "string"}, {"title", "string"}, {"status", "integer"},
		{"detail", "string"}, {"instance", "string"}, {"errors", "array"}, {"omitted", "integer"},
	} {
		if v, ok := m[f.name]; ok {
			if got := jsonType(v); got != f.want {
				d.fail("$."+f.name, "expected %s, got %s", f.want, got)
			}
		}
	}
	if n, ok := m["omitted"].(number); ok {
		if f, _ := strconv.ParseFloat(string(n), 64); cmpNum(string(n), f, 1) < 0 {
			d.fail("$.omitted", "%s is less than minimum 1", n)
		}
	}
	errs, _ := m["errors"].([]any)
	for i, el := range errs {
		path := fmt.Sprintf("$.errors[%d]", i)
		e, ok := el.(map[string]any)
		if !ok {
			d.fail(path, "expected object, got %s", jsonType(el))
			continue
		}
		for _, name := range []string{"in", "path", "message"} {
			switch v, ok := e[name]; {
			case !ok:
				d.fail(path+"."+name, "missing required member")
			case jsonType(v) != "string":
				d.fail(path+"."+name, "expected string, got %s", jsonType(v))
			}
		}
	}
}

// checkRowHeaders refuses two rows of one status that give one header
// different schemas.
func (c *compiledOp) checkRowHeaders() error {
	byStatus := map[int]map[string]headerOut{}
	for i, f := range c.op.doc.Failures {
		pp := c.rows[i]
		if pp == nil {
			continue
		}
		if byStatus[f.status] == nil {
			byStatus[f.status] = map[string]headerOut{}
		}
		for _, h := range pp.headers() {
			key := http.CanonicalHeaderKey(h.name)
			if prev, ok := byStatus[f.status][key]; ok && !reflect.DeepEqual(prev.use.document(nil), h.use.document(nil)) {
				return fmt.Errorf("failure rows on status %d give header %s two schemas", f.status, h.name)
			}
			byStatus[f.status][key] = h
		}
	}
	return nil
}

// checkMiddlewareHeaders refuses a typed middleware header ([HeaderOf])
// whose schema differs from that of the same header, in any spelling, on the
// same status in a failure row, the output, or another middleware. An
// untyped declaration admits any string and never conflicts. A gate is
// skipped when the operation requires no security.
func (c *compiledOp) checkMiddlewareHeaders() error {
	type stated struct {
		name, by string
		schema   any
	}
	byStatus := map[int]map[string]stated{}
	state := func(status int, name, by string, use *schema) error {
		if byStatus[status] == nil {
			byStatus[status] = map[string]stated{}
		}
		key := http.CanonicalHeaderKey(name)
		doc := use.document(nil)
		prev, ok := byStatus[status][key]
		if !ok {
			byStatus[status][key] = stated{name: name, by: by, schema: doc}
			return nil
		}
		if reflect.DeepEqual(prev.schema, doc) {
			return nil
		}
		was, _ := json.Marshal(prev.schema, json.Deterministic(true))
		is, _ := json.Marshal(doc, json.Deterministic(true))
		return fmt.Errorf("status %d: %s states header %s with schema %s, but %s states %s with schema %s",
			status, prev.by, prev.name, was, by, name, is)
	}
	for i, f := range c.op.doc.Failures {
		if pp := c.rows[i]; pp != nil {
			for _, h := range pp.headers() {
				// Rows of one status agree (checkRowHeaders).
				if err := state(f.status, h.name, fmt.Sprintf("failure row %d (%s)", i, f.label), h.use); err != nil {
					return err
				}
			}
		}
	}
	if c.out != nil && c.out.kind == outEnvelope {
		for _, s := range c.successes() {
			for _, h := range c.out.headers {
				if err := state(s, h.name, "the output", h.use); err != nil {
					return err
				}
			}
		}
	}
	for i, m := range c.chain {
		if m.gate != nil && len(c.security) == 0 {
			continue
		}
		for _, h := range m.headers {
			if h.schema == nil {
				continue
			}
			if err := state(h.status, h.name, fmt.Sprintf("middleware %d (%s)", i, m.name), h.schema); err != nil {
				return err
			}
		}
	}
	return nil
}
