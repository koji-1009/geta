package geta

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

// formPlan is how a body:"form" (application/x-www-form-urlencoded) or
// body:"multipart" (multipart/form-data) struct is bound. Each field binds
// one form field as a query parameter is bound, or, in multipart, a [File].
// The form is closed: an unknown field is a violation.
type formPlan struct {
	enc      string // "form", "multipart", or deepObject
	param    string // a deepObject's parameter name
	index    []int
	t        reflect.Type // the struct
	optional bool
	fields   []formField
	byName   map[string]*formField
	method   string // the operation's method (unsupported)
	desc     string // the body's description (doc tag)
}

// formField is one field of a form. pp binds its value under the path
// "$.name"; a file field's pp has no codec.
type formField struct {
	name        string
	pp          paramPlan
	file, slice bool
}

func (r *registry) formPlan(f reflect.StructField, enc string, index []int) (*formPlan, error) {
	t := f.Type
	optional := false
	if t.Kind() == reflect.Pointer {
		optional, t = true, t.Elem()
	}
	kind := "struct"
	if t.Kind() != reflect.Struct || v2Reads(t) || t == fileType {
		kind = "other"
	}
	if err := formBody(f.Name, enc, t.String(), kind); err != nil {
		return nil, err
	}
	p := &formPlan{enc: enc, index: index, t: t, optional: optional, byName: map[string]*formField{}, desc: f.Tag.Get("doc")}
	if err := r.formFields(p); err != nil {
		return nil, err
	}
	return p, nil
}

// deepPlan plans a struct query parameter as a deepObject (explode true):
// each field tagged form is a member sent as name[member]=value.
func (r *registry) deepPlan(f reflect.StructField, name string, index []int, t reflect.Type) (paramPlan, error) {
	// A struct takes no constraint other than deprecated.
	s, err := parseConstraints(f.Tag.Get("schema"), &schema{Ref: "deepObject"})
	if err != nil {
		return paramPlan{}, err
	}
	optional := f.Type.Kind() == reflect.Pointer
	p := &formPlan{enc: deepObject, param: name, t: t, optional: optional, byName: map[string]*formField{}}
	if err := r.formFields(p); err != nil {
		return paramPlan{}, err
	}
	return paramPlan{in: "query", name: name, index: index, optional: optional, desc: f.Tag.Get("doc"),
		use: &schema{Type: "object", Deprecated: s.Deprecated}, deep: p}, nil
}

// deepObject is the encoding of a deepObject query parameter's plan.
const deepObject = "deepObject"

// deepObjectType reports whether a query parameter of type t, or *t, is a
// deepObject: a struct with no unmarshaling method.
func deepObjectType(t reflect.Type) (reflect.Type, bool) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t, t.Kind() == reflect.Struct && !v2Reads(t) && t != fileType
}

// formFields plans the fields of p's struct tagged form.
func (r *registry) formFields(p *formPlan) error {
	multipart := p.enc == "multipart"
	seen := map[string]string{}
	var walk func(t reflect.Type, prefix []int) error
	walk = func(t reflect.Type, prefix []int) error {
		for i := range t.NumField() {
			sf := t.Field(i)
			index := append(append([]int(nil), prefix...), i)
			// Tags are checked as getavet checks them; the type is checked
			// below, once its kind is known.
			var embedded bool
			var err error
			if p.enc == deepObject {
				embedded, err = CheckDeepObjectField(vetField(sf), seen)
			} else {
				embedded, err = CheckFormField(vetField(sf), multipart, seen)
			}
			if err != nil {
				return fmt.Errorf("%s.%w", t, err)
			}
			if embedded {
				if err := walk(sf.Type, index); err != nil {
					return err
				}
				continue
			}
			name, ok := sf.Tag.Lookup("form")
			if !ok {
				continue
			}
			ff, err := r.formField(p, sf, name, index)
			if err != nil {
				return fmt.Errorf("%s.%s: %w", t, sf.Name, err)
			}
			p.fields = append(p.fields, ff)
		}
		return nil
	}
	if err := walk(p.t, nil); err != nil {
		return err
	}
	for i := range p.fields {
		p.byName[p.fields[i].name] = &p.fields[i]
	}
	return nil
}

// at returns a field's violation path: $.name in a form, param[name] in a
// deepObject.
func (p *formPlan) at(name string) string {
	if p.enc == deepObject {
		return p.param + "[" + name + "]"
	}
	return "$." + name
}

// in returns where the values are: body, or query for a deepObject.
func (p *formPlan) in() string {
	if p.enc == deepObject {
		return "query"
	}
	return "body"
}

func (r *registry) formField(p *formPlan, f reflect.StructField, name string, index []int) (formField, error) {
	multipart := p.enc == "multipart"
	t := f.Type
	optional := false
	if t.Kind() == reflect.Pointer {
		optional, t = true, t.Elem()
	}
	tag := f.Tag.Get("schema")
	pp := paramPlan{in: p.in(), name: p.at(name), index: index, optional: optional, desc: f.Tag.Get("doc")}
	slice := t.Kind() == reflect.Slice && t.Elem() == fileType
	if t == fileType || slice {
		kind := fileKind
		if slice {
			kind = "[]" + fileKind
		}
		if err := formType(name, multipart, t.String(), kind, tag); err != nil {
			return formField{}, err
		}
		base := fileSchema()
		if slice {
			base = &schema{Type: "array", Items: fileSchema()}
		}
		use, err := r.constrain(tag, base)
		if err != nil {
			return formField{}, err
		}
		pp.use = use
		return formField{name: name, pp: pp, file: true, slice: slice}, nil
	}
	c, err := r.codecFor(t)
	if err != nil {
		return formField{}, err
	}
	if err := formType(name, multipart, t.String(), vetKind(c), tag); err != nil {
		return formField{}, err
	}
	if p.enc == deepObject {
		if err := deepMember(name, t.String(), vetKind(c)); err != nil {
			return formField{}, err
		}
	}
	if pp.use, err = r.constrain(tag, c.use()); err != nil {
		return formField{}, err
	}
	if err := pp.use.checkValues(c); err != nil {
		return formField{}, err
	}
	pp.c = c
	return formField{name: name, pp: pp}, nil
}

// mediaType returns the form's media type.
func (p *formPlan) mediaType() string { return bodyMediaTypes[p.enc] }

// document returns the closed object schema of a form or deepObject. It
// states the backstops lim, including a []File's MaxItems (formField.most),
// and refers to split components by their request names (schema.render).
func (p *formPlan) document(lim *Limits, split map[string]bool) map[string]any {
	props := map[string]any{}
	var required []string
	for _, f := range p.fields {
		ps := f.pp.use.render(lim, split)
		if f.pp.desc != "" {
			ps["description"] = f.pp.desc
		}
		props[f.name] = ps
		// geta fills a missing field that has a default (paramPlan.bind).
		if !f.pp.optional && f.pp.use.Default == nil {
			required = append(required, f.name)
		}
	}
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// formValues is what a form body holds: each field's texts and, in
// multipart, each file field's files (up to what the field holds) and how
// many parts named it.
type formValues struct {
	values   url.Values
	files    map[string][]File
	parts    map[string]int
	unknown  map[string]bool
	nameless bool // a part named no field
}

// bind reads the body and fills dst. Content in a content coding or of
// another media type is a 415, judged as for a JSON body. tmp lists the
// temporary files for the caller to remove after the handler returns; on
// failure, bind has removed them.
func (p *formPlan) bind(w http.ResponseWriter, r *http.Request, dst reflect.Value, limits Limits) (be *bindError, tmp []string) {
	ct := r.Header.Get("Content-Type")
	mt, params, ctErr := mime.ParseMediaType(ct)
	judge := func() *bindError {
		if be := codingRefused(w, r); be != nil {
			return be
		}
		if ctErr != nil || mt != p.mediaType() {
			return unsupported(w, ct, p.mediaType(), p.method)
		}
		return nil
	}
	// Judge declared content before reading, so a client waiting for 100
	// Continue sends nothing.
	n := declaredLength(r)
	if n > 0 {
		if be := judge(); be != nil {
			return be, nil
		}
		if n > limits.MaxBodyBytes {
			return tooLong(limits.MaxBodyBytes), nil
		}
	}
	body := &errKeeper{r: http.MaxBytesReader(w, r.Body, limits.MaxBodyBytes)}
	// Otherwise peek one byte to tell an absent body from content, and judge
	// it before reading the rest.
	br := bufio.NewReader(body)
	if _, err := br.Peek(1); err != nil {
		if err == io.EOF {
			return p.absent(), nil
		}
		return readFailure(err, limits), nil
	}
	if n <= 0 {
		if be := judge(); be != nil {
			return be, nil
		}
	}
	if p.enc == "form" {
		data, err := io.ReadAll(br)
		if err != nil {
			return readFailure(err, limits), nil
		}
		// As a query string: '+' is a space and ';' is refused.
		values, err := url.ParseQuery(string(data))
		if err != nil {
			return bodyViolation("$", "the form body is malformed: "+err.Error()), nil
		}
		return p.fill(dst, &formValues{values: values}, limits), nil
	}
	boundary := params["boundary"]
	if boundary == "" {
		return bodyViolation("$", "the multipart/form-data Content-Type has no boundary parameter"), nil
	}
	cw := newCloseWatcher(br, boundary)
	fv, err := p.readParts(multipart.NewReader(cw, boundary), cw, limits, &tmp)
	// A body past MaxBodyBytes is a 413 wherever the excess lies: read on to learn it.
	if _, stored := errors.AsType[*storeError](err); !stored && !body.done {
		io.Copy(io.Discard, br)
	}
	if be := partFailure(err, body.err, limits); be != nil {
		removeFiles(tmp)
		return be, nil
	}
	if be := p.fill(dst, fv, limits); be != nil {
		removeFiles(tmp)
		return be, nil
	}
	return nil, tmp
}

// readParts reads a multipart body part by part. A field's part is read as
// text, a file field's as a File (readFile) up to as many as the field
// holds. Extra parts and parts naming no field are discarded. cw watches
// for the close delimiter.
func (p *formPlan) readParts(mr *multipart.Reader, cw *closeWatcher, limits Limits, tmp *[]string) (*formValues, error) {
	fv := &formValues{values: url.Values{}, files: map[string][]File{}, parts: map[string]int{}, unknown: map[string]bool{}}
	mem := limits.MaxMultipartMemory
	for {
		part, err := mr.NextPart()
		// NextPart returns io.EOF both at the close delimiter and for a body
		// cut short after a delimiter line or in part headers; cw tells them
		// apart.
		if err == io.EOF {
			if !cw.closed {
				return nil, errCutShort
			}
			return fv, nil
		}
		if err != nil {
			return nil, err
		}
		name := part.FormName()
		ff := p.byName[name]
		switch {
		case name == "":
			fv.nameless = true
			_, err = io.Copy(io.Discard, part)
		case ff == nil:
			fv.unknown[name] = true
			_, err = io.Copy(io.Discard, part)
		case ff.file:
			fv.parts[name]++
			if fv.parts[name] > ff.most(limits) {
				_, err = io.Copy(io.Discard, part)
				break
			}
			var f File
			if f, err = readFile(part, &mem, tmp); err == nil {
				fv.files[name] = append(fv.files[name], f)
			}
		default:
			var b []byte
			if b, err = io.ReadAll(part); err == nil {
				fv.values[name] = append(fv.values[name], string(b))
			}
		}
		if err != nil {
			return nil, err
		}
	}
}

// errKeeper keeps the first error reading the body other than io.EOF, and
// whether the body's reads ended. The body's own failure is taken from here,
// not from mime/multipart's error: bufio.Reader holds a read error back until
// its buffer is drained, and a mime/multipart error does not wrap it, so
// mime/multipart may fail on the bytes before a MaxBodyBytes cut, as a
// malformed header or part, with the MaxBytesError unseen.
type errKeeper struct {
	r    io.Reader
	err  error
	done bool // a read returned an error, io.EOF included
}

func (k *errKeeper) Read(p []byte) (int, error) {
	n, err := k.r.Read(p)
	if err != nil {
		k.done = true
		if err != io.EOF && k.err == nil {
			k.err = err
		}
	}
	return n, err
}

// errCutShort reports a multipart body that ends before its close
// delimiter where NextPart returns a bare io.EOF.
var errCutShort = errors.New("multipart: the body ends before its close delimiter")

// closeWatcher passes a multipart body through to mime/multipart and notes
// whether a close delimiter line (RFC 2046 §5.1.1) went by at a place where
// mime/multipart would recognize one. It mirrors mime/multipart's line
// rules, including a bare "\n" newline, and buffers only the first bytes of
// each line.
type closeWatcher struct {
	r        io.Reader
	boundary []byte // "--" and the boundary: a delimiter line's start
	nl       string // the newline delimiter lines end with
	parts    bool   // a delimiter line went by: the preamble is over
	header   bool   // the lines are a part's header
	line     []byte // the line's first bytes, up to len(boundary)+2
	size     int    // the line's length so far
	over     int    // past line: 0 nothing, 1 spaces and tabs, 2 those then '\r'
	dead     bool   // the line is no delimiter line, whatever follows
	cr       bool   // the line's last byte so far is '\r'
	eof      bool   // the body ended
	closed   bool   // a close delimiter line went by
}

func newCloseWatcher(r io.Reader, boundary string) *closeWatcher {
	b := []byte("--" + boundary)
	return &closeWatcher{r: r, boundary: b, nl: "\r\n", line: make([]byte, 0, len(b)+2)}
}

func (w *closeWatcher) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	w.scan(p[:n])
	if err == io.EOF && !w.eof {
		w.eof = true
		w.end(false)
	}
	return n, err
}

// scan scans the body's next bytes line by line.
func (w *closeWatcher) scan(b []byte) {
	for len(b) > 0 {
		if w.dead {
			i := bytes.IndexByte(b, '\n')
			if i < 0 {
				w.size += len(b)
				w.cr = b[len(b)-1] == '\r'
				return
			}
			if i > 0 {
				w.size += i
				w.cr = b[i-1] == '\r'
			}
			b = b[i+1:]
			w.end(true)
			continue
		}
		c := b[0]
		b = b[1:]
		if c == '\n' {
			w.end(true)
			continue
		}
		switch {
		case len(w.line) < cap(w.line):
			// The bytes before c matched, or the line would be dead: c alone
			// is compared.
			if i := len(w.line); i < len(w.boundary) && c != w.boundary[i] {
				w.dead = true
			}
			w.line = append(w.line, c)
		case w.over == 2 || c != ' ' && c != '\t' && c != '\r' || w.line[len(w.line)-1] == '\r':
			// After the boundary, only spaces, tabs, and a final '\r'.
			w.dead = true
		case c == '\r':
			w.over = 2
		default:
			w.over = 1
		}
		w.size++
		w.cr = c == '\r'
	}
}

// end ends the line at its '\n' (newline) or at the end of the body.
func (w *closeWatcher) end(newline bool) {
	if !w.dead {
		line := w.line
		if w.over == 2 {
			line = append(line, '\r')
		}
		if newline {
			line = append(line, '\n')
		}
		w.check(line)
	}
	blank := w.size == 0 || w.size == 1 && w.cr
	// The next line may be a delimiter if it follows the delimiters'
	// newline or begins a part's content.
	next := w.nl == "\n" || w.cr || w.header && blank
	if w.header && blank {
		w.header = false
	}
	w.line, w.size, w.over, w.cr = w.line[:0], 0, 0, false
	w.dead = w.parts && !next
}

// check classifies a whole line as mime/multipart does
// (Reader.isBoundaryDelimiterLine and Reader.isFinalBoundary).
func (w *closeWatcher) check(line []byte) {
	rest, ok := bytes.CutPrefix(line, w.boundary)
	if !ok {
		return
	}
	r := bytes.TrimLeft(rest, " \t")
	if !w.parts && string(r) == "\n" {
		w.nl = "\n"
	}
	if string(r) == w.nl {
		w.parts, w.header = true, true
		return
	}
	if r, ok := bytes.CutPrefix(rest, []byte("--")); ok {
		r = bytes.TrimLeft(r, " \t")
		w.closed = w.closed || len(r) == 0 || string(r) == w.nl
	}
}

// most returns how many files the field may hold: 1, its maxItems, or the
// MaxItems backstop.
func (ff *formField) most(limits Limits) int {
	switch {
	case !ff.slice:
		return 1
	case ff.pp.use.MaxItems != nil:
		return *ff.pp.use.MaxItems
	}
	return limits.MaxItems
}

// fill binds the form's values to dst, collecting every violation.
func (p *formPlan) fill(dst reflect.Value, fv *formValues, limits Limits) *bindError {
	target := dst
	if p.optional {
		ptr := reflect.New(p.t)
		dst.Set(ptr)
		target = ptr.Elem()
	}
	d := &decoder{limits: limits, in: p.in()}
	if fv.nameless {
		d.fail("$", "a part has no form-data name")
	}
	for i := range p.fields {
		ff := &p.fields[i]
		field := target.FieldByIndex(ff.pp.index)
		if ff.file {
			ff.bindFiles(d, fv.files[ff.name], fv.parts[ff.name], field)
			continue
		}
		raw := fv.values[ff.name]
		if len(raw) == 1 && ff.pp.bindFast(raw[0], field, limits) {
			continue
		}
		field.SetZero()
		ff.pp.bind(d, raw, field)
	}
	// The form is closed.
	unknown := fv.unknown
	if unknown == nil {
		for k := range fv.values {
			if p.byName[k] == nil {
				if unknown == nil {
					unknown = map[string]bool{}
				}
				unknown[k] = true
			}
		}
	}
	for _, k := range slices.Sorted(maps.Keys(unknown)) {
		// The problem is JSON, so invalid UTF-8 becomes U+FFFD.
		d.fail(p.at(strings.ToValidUTF8(k, string(utf8.RuneError))), "unknown field")
	}
	if len(d.errs) > 0 {
		dst.SetZero()
		return &bindError{status: http.StatusBadRequest, errs: d.errs, omitted: d.omitted}
	}
	return nil
}

// bindFiles binds a file field's files fs; n is how many parts named it.
func (ff *formField) bindFiles(d *decoder, fs []File, n int, dst reflect.Value) {
	pp := &ff.pp
	if n == 0 {
		if !pp.optional {
			d.fail(pp.name, "missing required field")
		}
		return
	}
	if !ff.slice && n > 1 {
		d.fail(pp.name, "given %d times, but takes one value", n)
		return
	}
	if ff.slice && !d.arrayLen(pp.use, n, pp.name) {
		return
	}
	target := dst
	if pp.optional {
		ptr := reflect.New(dst.Type().Elem())
		dst.Set(ptr)
		target = ptr.Elem()
	}
	if ff.slice {
		target.Set(reflect.ValueOf(fs).Convert(target.Type()))
		return
	}
	target.Set(reflect.ValueOf(fs[0]))
}

// absent handles an empty form body, which only an optional field accepts.
func (p *formPlan) absent() *bindError {
	if !p.optional {
		return bodyViolation("$", "missing required request body")
	}
	return nil
}

// bodyViolation returns a 400 naming one violation of the body.
func bodyViolation(path, msg string) *bindError {
	return &bindError{status: http.StatusBadRequest, errs: []Violation{{In: "body", Path: path, Message: msg}}}
}

// readFailure answers a body that could not be read: 408 past the read
// deadline, 413 past MaxBodyBytes, and 400 otherwise.
func readFailure(err error, limits Limits) *bindError {
	if be, ok := lateBody(err); ok {
		return be
	}
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return tooLong(limits.MaxBodyBytes)
	}
	return bodyViolation("$", "the request body could not be read")
}

// partFailure answers a multipart body from readParts' error err and the
// body's own read error read (errKeeper), or returns nil to take it: 500 for
// a file that could not be stored, then 413 past MaxBodyBytes and 408 past
// the read deadline whatever err is, then, if err is not nil, 400 for a body
// that could not be read or else a malformed body.
func partFailure(err, read error, limits Limits) *bindError {
	if se, ok := errors.AsType[*storeError](err); ok {
		return &bindError{status: http.StatusInternalServerError, err: se}
	}
	if _, ok := errors.AsType[*http.MaxBytesError](read); ok || errors.Is(read, errBodyLate) {
		return readFailure(read, limits)
	}
	switch {
	case err == nil:
		return nil
	case read != nil:
		return readFailure(read, limits)
	}
	return bodyViolation("$", "the multipart body is malformed: "+err.Error())
}
