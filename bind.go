package geta

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// inPlan is how an operation's input is bound from a request, derived once
// from the type's tags.
type inPlan struct {
	params []paramPlan
	body   *bodyPlan // a JSON body
	form   *formPlan // or a form or multipart one
	raw    *rawPlan  // or the bytes of one media type
	// cond is the index of the embedded [Conditional], nil without one;
	// required is set when it is a [RequireConditional]'s.
	cond     []int
	required bool
}

type paramPlan struct {
	in       string // path, query, header, cookie
	name     string // as written in the tag; headers are matched canonically
	index    []int
	c        *codec // the field's codec, a slice codec for repeated params
	optional bool
	use      *schema
	// joined marks a precondition field of [Conditional]: its lines are
	// joined into one list (RFC 9110 §5.3) by conditionField, without the
	// checks of a string parameter.
	joined bool
	desc   string // the parameter's description in the document (doc tag)
	// deep binds a struct query parameter (deepObject) from name[member]=value.
	deep *formPlan
}

type bodyPlan struct {
	index    []int
	c        *codec
	optional bool
	use      *schema
	fast     bool         // a valid body is decoded in one pass (fastDecoder)
	opts     json.Options // how the reference path decodes it
	method   string       // the operation's method (unsupported)
	// defaults are the codecs with a member that declares a default, filled
	// by the reference path (applyDefaults); nil for none.
	defaults map[*codec]bool
	desc     string // the body's description (doc tag)
}

var paramLocations = []string{"path", "query", "header", "cookie", "body"}

func (r *registry) inPlan(t reflect.Type) (*inPlan, error) {
	if err := CheckInputType(t.String(), t.Kind() == reflect.Struct); err != nil {
		return nil, err
	}
	p := &inPlan{}
	seen := map[string]string{}
	var walk func(t reflect.Type, prefix []int, conditional bool) error
	walk = func(t reflect.Type, prefix []int, conditional bool) error {
		for i := range t.NumField() {
			f := t.Field(i)
			index := append(append([]int(nil), prefix...), i)
			// Tags are checked as getavet checks them; the type is checked
			// below, once its codec is known.
			vf := vetField(f)
			embedded, err := CheckInputField(vf, seen)
			if err != nil {
				return fmt.Errorf("%s.%w", t, err)
			}
			if embedded {
				inner := conditional
				switch f.Type {
				case conditionalType:
					// bind sets the request's method through this field.
					if !f.IsExported() {
						return fmt.Errorf("%s.%s: geta.Conditional is embedded under an unexported alias", t, f.Name)
					}
					p.cond, inner = index, true
				case requireConditionalType:
					p.required = true
				}
				if err := walk(f.Type, index, inner); err != nil {
					return err
				}
				continue
			}
			loc, name, _ := inputLocation(vf)
			if loc == "" {
				continue
			}
			if loc == "body" && strings.Contains(name, "/") {
				p.raw = newRawPlan(f, name, index)
				continue
			}
			if loc == "body" && name != "json" {
				fp, err := r.formPlan(f, name, index)
				if err != nil {
					return fmt.Errorf("%s.%w", t, err)
				}
				p.form = fp
				continue
			}
			if loc == "body" {
				b, err := r.bodyPlan(f, index)
				if err != nil {
					return fmt.Errorf("%s.%s: %w", t, f.Name, err)
				}
				p.body = b
				continue
			}
			if dt, ok := deepObjectType(f.Type); ok && loc == "query" {
				pp, err := r.deepPlan(f, name, index, dt)
				if err != nil {
					return fmt.Errorf("%s.%s: %w", t, f.Name, err)
				}
				p.params = append(p.params, pp)
				continue
			}
			pp, err := r.paramPlan(f, loc, name, index)
			if err != nil {
				return fmt.Errorf("%s.%s: %w", t, f.Name, err)
			}
			if conditional {
				pp.joined, pp.desc = true, conditionalDocs[name]
			}
			p.params = append(p.params, pp)
		}
		return nil
	}
	if err := walk(t, nil, false); err != nil {
		return nil, err
	}
	return p, nil
}

func (r *registry) paramPlan(f reflect.StructField, loc, name string, index []int) (paramPlan, error) {
	t := f.Type
	optional := false
	if t.Kind() == reflect.Pointer {
		optional, t = true, t.Elem()
	}
	c, err := r.codecFor(t)
	if err != nil {
		return paramPlan{}, err
	}
	if err := paramType(loc, name, t.String(), vetKind(c)); err != nil {
		return paramPlan{}, err
	}
	use, err := r.constrain(f.Tag.Get("schema"), c.use())
	if err != nil {
		return paramPlan{}, err
	}
	// getavet applies the same rule (CheckInputField).
	if use, err = carried(loc, name, use); err != nil {
		return paramPlan{}, err
	}
	// A default or example must also be carriable.
	if err := use.checkValues(c); err != nil {
		return paramPlan{}, err
	}
	return paramPlan{in: loc, name: name, index: index, c: c, optional: optional, use: use, desc: f.Tag.Get("doc")}, nil
}

func (r *registry) bodyPlan(f reflect.StructField, index []int) (*bodyPlan, error) {
	t := f.Type
	optional := false
	if t.Kind() == reflect.Pointer {
		optional, t = true, t.Elem()
	}
	c, err := r.codecFor(t)
	if err != nil {
		return nil, err
	}
	use, err := r.constrain(f.Tag.Get("schema"), c.use())
	if err != nil {
		return nil, err
	}
	// CheckInputField already refused a default on the body.
	if err := use.checkValues(c); err != nil {
		return nil, err
	}
	return &bodyPlan{index: index, c: c, optional: optional, use: use,
		fast: fastEligible(c, map[*codec]bool{}), opts: r.decOpts, defaults: defaultsUnder(c), desc: f.Tag.Get("doc")}, nil
}

// validWildcard reports whether s is a Go identifier, as ServeMux requires
// of a wildcard name.
func validWildcard(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if !unicode.IsLetter(c) && c != '_' && (i == 0 || !unicode.IsDigit(c)) {
			return false
		}
	}
	return true
}

// bindError is a request that does not meet its contract or, with err set,
// one the server failed to take in (a 500).
type bindError struct {
	status int
	detail string
	// logged replaces detail in the log when detail quotes the request; ""
	// logs detail itself.
	logged string
	errs   []Violation
	// omitted counts the violations found past errs (decoder.omitted).
	omitted int
	err     error
}

// write answers the refusal; err must be nil.
func (be *bindError) write(w http.ResponseWriter, r *http.Request) {
	logged := be.detail
	if be.logged != "" {
		logged = be.logged
	}
	writeRefusal(w, r, be.status, be.detail, logged, be.errs, be.omitted)
}

// bind fills dst from r, collecting every violation. tmp lists the
// temporary files of a multipart body for the caller to remove after the
// handler returns; on failure there are none.
func (p *inPlan) bind(w http.ResponseWriter, r *http.Request, dst reflect.Value, limits Limits) (be *bindError, tmp []string) {
	var query url.Values
	if p.needs("query") {
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			return &bindError{status: http.StatusBadRequest, detail: "the query string is malformed"}, nil
		}
		query = q
	}
	var errs []Violation
	omitted := 0
	for _, pp := range p.params {
		var raw []string
		switch pp.in {
		case "path":
			raw = []string{r.PathValue(pp.name)}
		case "query":
			if pp.deep != nil {
				errs = pp.bindDeep(query, dst.FieldByIndex(pp.index), limits, errs, &omitted)
				continue
			}
			raw = query[pp.name]
		case "header":
			raw = r.Header.Values(pp.name)
			if pp.joined {
				// Not a string parameter (conditionField).
				dst.FieldByIndex(pp.index).Set(reflect.ValueOf(conditionField(raw)))
				continue
			}
			if pp.c.kind == kSlice {
				raw = headerElements(raw)
			}
		case "cookie":
			// A browser may send one name twice, for two paths; the first wins.
			if cs := r.CookiesNamed(pp.name); len(cs) > 0 {
				raw = []string{cs[0].Value}
			}
		}
		field := dst.FieldByIndex(pp.index)
		if len(raw) == 1 && pp.bindFast(raw[0], field, limits) {
			continue
		}
		// Absent, repeated, or refused: the slow path names the violation.
		field.SetZero()
		d := &decoder{limits: limits, in: pp.in}
		pp.bind(d, raw, field)
		errs = append(errs, d.errs...)
		omitted += d.omitted
	}
	if p.cond != nil {
		dst.FieldByIndex(p.cond).Addr().Interface().(*Conditional).method = r.Method
	}
	if p.body != nil {
		if be := p.body.bind(w, r, dst.FieldByIndex(p.body.index), limits); be != nil {
			if be.status != http.StatusBadRequest {
				return be, nil
			}
			errs = append(errs, be.errs...)
			omitted += be.omitted
		}
	}
	if p.form != nil {
		be, files := p.form.bind(w, r, dst.FieldByIndex(p.form.index), limits)
		if be != nil {
			if be.status != http.StatusBadRequest {
				return be, nil
			}
			errs = append(errs, be.errs...)
			omitted += be.omitted
		}
		tmp = files
	}
	if p.raw != nil {
		if be := p.raw.bind(w, r, dst.FieldByIndex(p.raw.index), limits); be != nil {
			if be.status != http.StatusBadRequest {
				return be, nil
			}
			errs = append(errs, be.errs...)
			omitted += be.omitted
		}
	}
	if len(errs) > 0 {
		removeFiles(tmp)
		// Each decoder caps its own violations; listViolations caps the
		// total, counting the rest in omitted.
		return &bindError{status: http.StatusBadRequest, detail: "the request does not match its contract", errs: errs, omitted: omitted}, nil
	}
	return nil, tmp
}

// reads reports whether the input reads a request body.
func (p *inPlan) reads() bool {
	return p.body != nil || p.form != nil || p.raw != nil
}

// contentRefused answers 415 to content sent to an operation that reads no
// body (RFC 9110 §15.5.16), and nil to a request with none. Zero bytes are
// no content. A positive Content-Length is refused before reading, so a
// client waiting for 100 Continue sends nothing; a body of unknown length is
// read up to its first byte, and a failed read counts as no content.
func contentRefused(r *http.Request) *bindError {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	switch n := declaredLength(r); {
	case n == 0:
		return nil
	case n < 0:
		var first [1]byte
		if k, _ := io.ReadFull(r.Body, first[:]); k == 0 {
			return nil
		}
	}
	return &bindError{status: http.StatusUnsupportedMediaType, detail: "the operation takes no request content"}
}

// headerElements splits a list header's lines into elements (RFC 9110
// §5.6.1): split on commas, trim spaces and tabs, drop empty elements.
func headerElements(lines []string) []string {
	var out []string
	for _, line := range lines {
		for e := range strings.SplitSeq(line, ",") {
			if e = strings.Trim(e, " \t"); e != "" {
				out = append(out, e)
			}
		}
	}
	return out
}

func (p *inPlan) needs(loc string) bool {
	for _, pp := range p.params {
		if pp.in == loc {
			return true
		}
	}
	return false
}

func (pp *paramPlan) bind(d *decoder, raw []string, dst reflect.Value) {
	if len(raw) == 0 {
		// Only a non-pointer can have a default.
		if pp.use.Default != nil {
			setScalar(pp.c, *pp.use.Default, dst)
			return
		}
		if !pp.optional {
			what := "parameter"
			if pp.in == "body" {
				what = "field" // a form's
			}
			d.fail(pp.name, "missing required %s", what)
		}
		return
	}
	var v any
	if pp.c.kind == kSlice {
		a := make([]any, len(raw))
		for i, s := range raw {
			a[i] = paramValue(pp.c.elem, s)
		}
		v = a
	} else {
		if len(raw) > 1 {
			d.fail(pp.name, "given %d times, but takes one value", len(raw))
			return
		}
		v = paramValue(pp.c, raw[0])
	}
	before := len(d.errs)
	d.check(pp.c, pp.use, v, pp.name)
	if len(d.errs) > before {
		return
	}
	target := dst
	if pp.optional {
		ptr := reflect.New(pp.c.t)
		dst.Set(ptr)
		target = ptr.Elem()
	}
	if pp.c.kind == kSlice {
		out := reflect.MakeSlice(pp.c.t, len(raw), len(raw))
		for i, s := range raw {
			setScalar(pp.c.elem, s, out.Index(i))
		}
		target.Set(out)
		return
	}
	setScalar(pp.c, raw[0], target)
}

// bindDeep binds a deepObject from the query's name[member] keys and
// returns errs with its violations appended, adding those it leaves out to
// omitted. With no such key, the parameter is absent.
func (pp *paramPlan) bindDeep(query url.Values, dst reflect.Value, limits Limits, errs []Violation, omitted *int) []Violation {
	fv := &formValues{values: url.Values{}}
	prefix := pp.name + "["
	for k, vs := range query {
		if member, ok := strings.CutPrefix(k, prefix); ok && strings.HasSuffix(member, "]") {
			fv.values[member[:len(member)-1]] = vs
		}
	}
	if len(fv.values) == 0 {
		if !pp.optional {
			errs = append(errs, Violation{In: "query", Path: pp.name, Message: "missing required parameter"})
		}
		return errs
	}
	if be := pp.deep.fill(dst, fv, limits); be != nil {
		errs = append(errs, be.errs...)
		*omitted += be.omitted
	}
	return errs
}

// bindFast binds a single scalar value without allocating, exactly as bind
// would, and reports whether it did. On false, bind takes over; dst may be
// partly set.
func (pp *paramPlan) bindFast(s string, dst reflect.Value, limits Limits) bool {
	target := func() reflect.Value {
		if !pp.optional {
			return dst
		}
		ptr := reflect.New(pp.c.t)
		dst.Set(ptr)
		return ptr.Elem()
	}
	use := pp.use
	switch pp.c.kind {
	case kString:
		// Unlike JSON text, parameter text may not be UTF-8.
		if !utf8.ValidString(s) || !strOK(use, limits, s) {
			return false
		}
		target().SetString(s)
	case kBool:
		if s != "true" && s != "false" {
			return false
		}
		target().SetBool(s == "true")
	case kInt:
		if !isJSONInteger(s) {
			return false
		}
		n, err := strconv.ParseInt(s, 10, pp.c.t.Bits())
		if err != nil || !numOK(use, s, float64(n)) {
			return false
		}
		target().SetInt(n)
	case kUint:
		if !isJSONInteger(s) {
			return false
		}
		n, err := strconv.ParseUint(s, 10, pp.c.t.Bits())
		if err != nil || !numOK(use, s, float64(n)) {
			return false
		}
		target().SetUint(n)
	case kFloat:
		if !jsonNumberSyntax.MatchString(s) {
			return false
		}
		f, near, err := parseFloat(s, pp.c.t.Bits())
		if err != nil || !numOK(use, s, near) {
			return false
		}
		target().SetFloat(f)
	case kText:
		if !utf8.ValidString(s) || !strOK(use, limits, s) {
			return false
		}
		if unmarshalText(target().Addr().Interface(), []byte(s)) != nil {
			return false
		}
	default:
		return false
	}
	return true
}

// setScalar stores a parameter's text, already checked, in dst.
func setScalar(c *codec, s string, dst reflect.Value) {
	switch c.kind {
	case kString:
		dst.SetString(s)
	case kBool:
		dst.SetBool(s == "true")
	case kInt:
		n, _ := strconv.ParseInt(s, 10, c.t.Bits())
		dst.SetInt(n)
	case kUint:
		n, _ := strconv.ParseUint(s, 10, c.t.Bits())
		dst.SetUint(n)
	case kFloat:
		f, _ := strconv.ParseFloat(s, c.t.Bits())
		dst.SetFloat(f)
	case kText:
		unmarshalText(dst.Addr().Interface(), []byte(s))
	}
}

// paramValue converts a parameter's text to the JSON value it stands for, so
// it is validated as a body is. A number must use JSON syntax.
func paramValue(c *codec, s string) any {
	switch c.kind {
	case kInt, kUint:
		if isJSONInteger(s) {
			return number(s)
		}
	case kFloat:
		if jsonNumberSyntax.MatchString(s) {
			return number(s)
		}
	case kBool:
		switch s {
		case "true":
			return true
		case "false":
			return false
		}
	default:
		return s
	}
	return s // a type violation
}

func (b *bodyPlan) bind(w http.ResponseWriter, r *http.Request, dst reflect.Value, limits Limits) *bindError {
	bad := func(msg string) *bindError {
		return &bindError{status: http.StatusBadRequest, errs: []Violation{{In: "body", Path: "$", Message: msg}}}
	}
	// Judge declared content before reading, so a client waiting for 100
	// Continue sends nothing.
	n := declaredLength(r)
	if n > 0 {
		if be := b.judge(w, r); be != nil {
			return be
		}
		if n > limits.MaxBodyBytes {
			return tooLong(limits.MaxBodyBytes)
		}
	}
	f := getFastDecoder(limits)
	defer f.release()
	f.opts = b.opts
	body := http.MaxBytesReader(w, r.Body, limits.MaxBodyBytes)
	// Otherwise read one byte to tell an absent body from content, and judge
	// it before reading the rest. MaxBodyBytes is at least 1.
	f.body.Grow(1)
	first := f.body.AvailableBuffer()[:1]
	if _, err := io.ReadFull(body, first); err == io.EOF {
		if !b.optional {
			return bad("missing required request body")
		}
		return nil
	} else if err != nil {
		return readFailure(err, limits)
	}
	f.body.Write(first)
	if n <= 0 {
		if be := b.judge(w, r); be != nil {
			return be
		}
	}
	if _, err := f.body.ReadFrom(body); err != nil {
		return readFailure(err, limits)
	}
	data := f.body.Bytes()
	if b.fast {
		target := dst
		if b.optional {
			ptr := reflect.New(b.c.t)
			dst.Set(ptr)
			target = ptr.Elem()
		}
		if f.decode(b.c, b.use, target) {
			return nil
		}
		// Start over on the reference path, which names every violation.
		dst.SetZero()
	}
	if errs, omitted := b.reference(f.reread(data), data, dst, limits); len(errs) > 0 {
		dst.SetZero()
		return &bindError{status: http.StatusBadRequest, errs: errs, omitted: omitted}
	}
	return nil
}

// judge answers 415 to content in a content coding or not JSON, and nil
// otherwise.
func (b *bodyPlan) judge(w http.ResponseWriter, r *http.Request) *bindError {
	if be := codingRefused(w, r); be != nil {
		return be
	}
	if ct := r.Header.Get("Content-Type"); !isJSONMediaType(ct) {
		return unsupported(w, ct, bodyMediaTypes["json"], b.method)
	}
	return nil
}

// tooLong returns the 413 for a body longer than limit.
func tooLong(limit int64) *bindError {
	return &bindError{status: http.StatusRequestEntityTooLarge,
		detail: fmt.Sprintf("the request body exceeds %d bytes", limit)}
}

// reference reads a body the single pass refused or cannot read: parse
// (parseJSONFrom), check against the schema, decode (decodeJSON), and fill
// defaults (applyDefaults). It names every violation, up to maxViolations,
// and counts the rest. The single pass must accept exactly what this
// accepts (fastdecode_test.go).
func (b *bodyPlan) reference(dec *jsontext.Decoder, data []byte, dst reflect.Value, limits Limits) (errs []Violation, omitted int) {
	// dec's offsets index data (spellings).
	var sp *spellings
	if b.defaults != nil {
		sp = &spellings{data: data}
	}
	tree, err := parseJSONFrom(dec, limits.MaxDepth, sp)
	if err != nil {
		if de, ok := err.(*duplicateKeyError); ok {
			return []Violation{{In: "body", Path: capPath(de.path), Message: "duplicate object key"}}, 0
		}
		return []Violation{{In: "body", Path: "$", Message: err.Error()}}, 0
	}
	d := &decoder{limits: limits, in: "body"}
	d.check(b.c, b.use, tree, "$")
	if len(d.errs) > 0 {
		return d.errs, d.omitted
	}
	target := dst
	if b.optional {
		ptr := reflect.New(b.c.t)
		dst.Set(ptr)
		target = ptr.Elem()
	}
	d.decodeJSON(data, target, b.opts)
	if len(d.errs) == 0 && b.defaults != nil {
		applyDefaults(b.c, tree, target, b.defaults, b.opts, sp)
	}
	return d.errs, d.omitted
}

var jsonNumberSyntax = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

func isJSONMediaType(ct string) bool {
	if ct == "application/json" {
		return true // fast path
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return mt == "application/json" || (strings.HasPrefix(mt, "application/") && strings.HasSuffix(mt, "+json"))
}

// unsupported answers 415 to content whose Content-Type ct is not mt (RFC
// 9110 §15.5.16), naming mt in Accept and, per acceptFor, in Accept-Patch or
// Accept-Query. Content with no Content-Type is refused (RFC 9110 §8.3). The
// detail quotes ct cut by clip to maxValueBytes, as a violation quotes a
// value: a header is as long as MaxHeaderBytes allows.
func unsupported(w http.ResponseWriter, ct, mt, method string) *bindError {
	h := w.Header()
	h.Set("Accept", mt)
	if name := acceptFor(method); name != "" {
		h.Set(name, mt)
	}
	if ct == "" {
		return &bindError{status: http.StatusUnsupportedMediaType, detail: "missing Content-Type"}
	}
	return &bindError{status: http.StatusUnsupportedMediaType, detail: fmt.Sprintf("Content-Type %q is not %s", clip(ct, maxValueBytes), mt),
		logged: "the Content-Type is not " + mt}
}

// acceptFor returns the header a 415 on method sets beside Accept:
// Accept-Patch for PATCH (RFC 5789 §3.1), Accept-Query for QUERY
// (draft-ietf-httpbis-safe-method-w-body §3), or "".
func acceptFor(method string) string {
	switch method {
	case http.MethodPatch:
		return "Accept-Patch"
	case MethodQuery:
		return "Accept-Query"
	}
	return ""
}

// codingRefused answers 415 with Accept-Encoding: identity to content in a
// content coding (RFC 9110 §8.4, §12.5.3), and nil otherwise. geta decodes
// no coding; "identity" counts as none. The detail quotes the codings cut by
// clip to maxValueBytes, as unsupported quotes a Content-Type.
func codingRefused(w http.ResponseWriter, r *http.Request) *bindError {
	vs := r.Header.Values("Content-Encoding")
	if len(vs) == 0 {
		return nil
	}
	var codings []string
	for _, v := range vs {
		for c := range strings.SplitSeq(v, ",") {
			// Trim only OWS (RFC 9110 §5.6.1).
			if c = strings.Trim(c, " \t"); c != "" && !strings.EqualFold(c, "identity") {
				codings = append(codings, c)
			}
		}
	}
	if len(codings) == 0 {
		return nil
	}
	w.Header().Set("Accept-Encoding", "identity")
	return &bindError{status: http.StatusUnsupportedMediaType,
		detail: fmt.Sprintf("Content-Encoding %q is not supported", clip(strings.Join(codings, ", "), maxValueBytes)),
		logged: "unsupported Content-Encoding"}
}
