package geta

import (
	"encoding"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/koji-1009/geta/internal/vet"
)

// outPlan is how an operation's Out is written, derived once from its type.
type outPlan struct {
	kind    outKind
	body    *codec // the body's codec; the whole Out for outJSON
	bodyUse *schema
	bodyIdx []int   // envelope body field
	raw     *rawOut // or an envelope's raw body, the bytes of one media type
	headers []headerOut
	// location is the Location header field, or nil.
	location *headerOut
	cookies  []cookieOut
	// statuses are the success statuses an envelope's status field may
	// hold, and statusIdx the field; nil without one.
	statuses  []int
	statusIdx []int
	special   specialPlan // streams and upgrades

	opts   json.Options // how the body is written (registry.encOpts)
	unions bool         // the body can hold a sealed type: check none is nil
}

type outKind int

const (
	outNone     outKind = iota // OpNoBody
	outJSON                    // Out is the body
	outEnvelope                // Out carries headers, cookies, and a body
	outSpecial                 // Out is a *Stream or *Upgrade
)

type headerOut struct {
	name     string
	index    []int
	c        *codec
	optional bool
	use      *schema
	desc     string // its description (doc tag)
	field    string // the Go field's name
}

type cookieOut struct {
	name  string
	index []int
}

// specialPlan writes an Out that is not a JSON body.
type specialPlan interface {
	write(w http.ResponseWriter, r *http.Request, a *App, op *compiledOp, out any)
}

func (r *registry) outPlan(t reflect.Type) (*outPlan, error) {
	if t == nil {
		return &outPlan{kind: outNone}, nil
	}
	if sp, ok, err := specialFor(r, t); ok || err != nil {
		if err != nil {
			return nil, err
		}
		return &outPlan{kind: outSpecial, special: sp}, nil
	}
	if t.Kind() == reflect.Struct && isEnvelope(t) {
		p, err := r.envelope(t)
		if err != nil {
			return nil, err
		}
		p.opts = r.encOpts
		p.unions = p.body != nil && markUnions(p.body)
		return p, nil
	}
	c, err := r.codecFor(t)
	if err != nil {
		return nil, err
	}
	return &outPlan{kind: outJSON, body: c, bodyUse: c.use(), opts: r.encOpts, unions: markUnions(c)}, nil
}

// isEnvelope reports whether a field of t, or of a struct t embeds untagged,
// has a header, cookie, or body tag.
func isEnvelope(t reflect.Type) bool {
	for i := range t.NumField() {
		f := t.Field(i)
		for _, l := range envelopeTags {
			if _, ok := f.Tag.Lookup(l); ok {
				return true
			}
		}
		if f.Anonymous && f.Type.Kind() == reflect.Struct && isEnvelope(f.Type) {
			return true
		}
	}
	return false
}

func (r *registry) envelope(t reflect.Type) (*outPlan, error) {
	p := &outPlan{kind: outEnvelope}
	seen := map[string]bool{}
	var walk func(t reflect.Type, prefix []int) error
	walk = func(t reflect.Type, prefix []int) error {
		for i := range t.NumField() {
			f := t.Field(i)
			index := append(append([]int(nil), prefix...), i)
			// checkEnvelopeField is shared with getavet; a header's type is
			// checked below, once its codec is known.
			vf := vetField(f)
			if err := checkEnvelopeField(vf, seen); err != nil {
				return fmt.Errorf("%s.%w", t, err)
			}
			if envelopeEmbedded(vf) {
				if err := walk(f.Type, index); err != nil {
					return err
				}
				continue
			}
			if !f.IsExported() {
				continue
			}
			if st, isS := f.Tag.Lookup("status"); isS {
				// checkEnvelopeField has validated the tag.
				p.statuses, _ = successStatuses(st)
				p.statusIdx = index
				continue
			}
			if h, isH := f.Tag.Lookup("header"); isH {
				ft, opt := f.Type, false
				if ft.Kind() == reflect.Pointer {
					ft, opt = ft.Elem(), true
				}
				c, err := r.codecFor(ft)
				if err != nil {
					return fmt.Errorf("%s.%s: %w", t, f.Name, err)
				}
				if err := headerType(h, ft.String(), vetKind(c)); err != nil {
					return fmt.Errorf("%s.%s: %w", t, f.Name, err)
				}
				// The schema admits only what a header can carry as written;
				// setHeaders enforces it. carried cannot fail here: an output
				// header's schema has no enum.
				use, _ := carried("header", h, c.use())
				p.headers = append(p.headers, headerOut{name: h, index: index, c: c, optional: opt, use: use, desc: f.Tag.Get("doc"), field: f.Name})
			} else if ck, isC := f.Tag.Lookup("cookie"); isC {
				p.cookies = append(p.cookies, cookieOut{name: ck, index: index})
			} else if b := f.Tag.Get("body"); b != "json" {
				// Raw bytes of the media type the tag names.
				p.raw = &rawOut{index: index, mediaType: b, reader: f.Type == readerType}
			} else {
				c, err := r.codecFor(f.Type)
				if err != nil {
					return fmt.Errorf("%s.%s: %w", t, f.Name, err)
				}
				p.body, p.bodyUse, p.bodyIdx = c, c.use(), index
			}
		}
		return nil
	}
	if err := walk(t, nil); err != nil {
		return nil, err
	}
	for i := range p.headers {
		if http.CanonicalHeaderKey(p.headers[i].name) == "Location" {
			p.location = &p.headers[i]
		}
	}
	return p, nil
}

// locationField describes the Location header field for checkSuccess, or
// returns nil if there is none.
func (p *outPlan) locationField() *vet.Field {
	if p.location == nil {
		return nil
	}
	typ := p.location.c.t.String()
	if p.location.optional {
		typ = "*" + typ
	}
	return &vet.Field{Name: p.location.field, Type: typ, Kind: vetKind(p.location.c)}
}

// checkLocation returns an error if status is a redirect and the Location
// header field is nil or empty.
func (p *outPlan) checkLocation(status int, v reflect.Value) error {
	if !redirect(status) || p.location == nil {
		return nil
	}
	fv := v.FieldByIndex(p.location.index)
	if p.location.optional {
		if fv.IsNil() {
			return fmt.Errorf("status %d: Location field %s is nil", status, p.location.field)
		}
		fv = fv.Elem()
	}
	// A value that cannot be rendered is reported by setHeaders.
	if s, err := scalarText(p.location.c, fv); err == nil && s == "" {
		return fmt.Errorf("status %d: Location field %s is empty", status, p.location.field)
	}
	return nil
}

// hasBody reports whether the plan can write a body.
func (p *outPlan) hasBody() bool {
	return p.kind == outJSON || (p.kind == outEnvelope && (p.body != nil || p.raw != nil)) || p.kind == outSpecial
}

// write sends a success response. A body of up to limit bytes is encoded
// before the status is written, so an encoding failure is still a clean 500.
// A larger body commits the response and streams; a failure after that is a
// *committedError.
func (p *outPlan) write(w http.ResponseWriter, status int, out any, limit int) error {
	switch p.kind {
	case outNone:
		w.WriteHeader(status)
		return nil
	case outJSON:
		if p.unions {
			if err := nilUnion(p.body, reflect.ValueOf(out).Elem(), "$"); err != nil {
				return err
			}
		}
		return p.send(w, status, out, reflect.Value{}, limit)
	case outEnvelope:
		v := reflect.ValueOf(out).Elem()
		if p.raw != nil && p.raw.reader {
			// The reader is closed even if the response fails before it.
			if c, ok := v.FieldByIndex(p.raw.index).Interface().(io.Closer); ok {
				defer c.Close()
			}
		}
		if p.statusIdx != nil {
			// Zero means the operation's own status.
			if s := int(v.FieldByIndex(p.statusIdx).Int()); s != 0 {
				if !slices.Contains(p.statuses, s) {
					return fmt.Errorf("status field holds undeclared status %d (want %s)", s, statusList(p.statuses))
				}
				status = s
			}
		}
		if err := p.checkLocation(status, v); err != nil {
			return err
		}
		if p.raw != nil {
			return p.writeRaw(w, status, v, limit)
		}
		if p.body != nil {
			bv := v.FieldByIndex(p.bodyIdx)
			if p.unions {
				if err := nilUnion(p.body, bv, "$"); err != nil {
					return err
				}
			}
			return p.send(w, status, bv.Addr().Interface(), v, limit)
		}
		if err := p.setHeaders(w.Header(), v); err != nil {
			return err
		}
		w.WriteHeader(status)
		return nil
	}
	// Unreachable: outSpecial is written by its specialPlan.
	return fmt.Errorf("geta: unexpected output kind %d", p.kind)
}

// statusList writes statuses as a status tag does: 200|201.
func statusList(statuses []int) string {
	s := make([]string, len(statuses))
	for i, n := range statuses {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, "|")
}

// committedError is a failure after the response was committed, which can
// no longer become a 500.
type committedError struct {
	err   error
	write bool // the writer failed, not the encoding
}

func (e *committedError) Error() string { return e.err.Error() }

// send encodes body through a bodyWriter. env is the envelope whose headers
// and cookies go out with the status, or the zero Value.
func (p *outPlan) send(w http.ResponseWriter, status int, body any, env reflect.Value, limit int) error {
	bw := bodyWriters.Get().(*bodyWriter)
	defer bw.release()
	bw.w, bw.p, bw.env, bw.status, bw.limit = w, p, env, status, limit
	err := json.MarshalWrite(bw, body, p.opts)
	switch {
	case bw.headerErr != nil:
		return bw.headerErr // found at the commit, before any byte was written
	case bw.writeErr != nil:
		return &committedError{err: bw.writeErr, write: true}
	case err != nil && bw.committed:
		return &committedError{err: err}
	case err != nil:
		return err
	case bw.committed:
		if len(bw.buf) > 0 {
			if _, err := w.Write(bw.buf); err != nil {
				return &committedError{err: err, write: true}
			}
		}
		return nil
	}
	if env.IsValid() {
		if err := p.setHeaders(w.Header(), env); err != nil {
			return err
		}
	}
	if err := writeJSON(w, status, bw.buf); err != nil {
		return &committedError{err: err, write: true}
	}
	return nil
}

// bodyWriter holds an encoded body up to limit bytes. Past limit it commits
// the response without a Content-Length and passes the body on to w,
// gathering small writes in buf; a write larger than limit goes to w as is.
// buf never grows past limit.
type bodyWriter struct {
	buf       []byte
	limit     int
	w         http.ResponseWriter
	p         *outPlan
	env       reflect.Value
	status    int
	committed bool
	headerErr error
	writeErr  error
}

var bodyWriters = sync.Pool{New: func() any { return new(bodyWriter) }}

func (b *bodyWriter) release() {
	buf := b.buf[:0]
	if cap(buf) > maxPooledBody {
		buf = nil
	}
	*b = bodyWriter{buf: buf}
	bodyWriters.Put(b)
}

func (b *bodyWriter) Write(p []byte) (int, error) {
	if b.headerErr != nil || b.writeErr != nil {
		return 0, errors.Join(b.headerErr, b.writeErr)
	}
	if len(b.buf)+len(p) <= b.limit {
		b.hold(p)
		return len(p), nil
	}
	if !b.committed {
		if err := b.commit(); err != nil {
			b.headerErr = err
			return 0, err
		}
	}
	if len(b.buf) > 0 {
		if _, err := b.w.Write(b.buf); err != nil {
			b.writeErr = err
			return 0, err
		}
		b.buf = b.buf[:0]
	}
	if len(p) > b.limit {
		if _, err := b.w.Write(p); err != nil {
			b.writeErr = err
			return 0, err
		}
		return len(p), nil
	}
	b.hold(p)
	return len(p), nil
}

// hold appends p, which fits within limit, growing buf to no more than limit.
func (b *bodyWriter) hold(p []byte) {
	if n := len(b.buf) + len(p); n > cap(b.buf) {
		grown := make([]byte, len(b.buf), min(max(2*cap(b.buf), n, 512), b.limit))
		copy(grown, b.buf)
		b.buf = grown
	}
	b.buf = append(b.buf, p...)
}

// commit writes the status and headers of a body too large to hold.
func (b *bodyWriter) commit() error {
	h := b.w.Header()
	if b.env.IsValid() {
		if err := b.p.setHeaders(h, b.env); err != nil {
			return err
		}
	}
	delete(h, "Content-Length")
	// As Header.Set would, with the two values in one allocation.
	v := make([]string, 2)
	v[0], v[1] = "application/json", "nosniff"
	h["Content-Type"], h["X-Content-Type-Options"] = v[0:1:1], v[1:2:2]
	b.committed = true
	b.w.WriteHeader(b.status)
	return nil
}

// setHeaders sets the envelope's headers and cookies on h. Every value is
// rendered before any is set, so on error h is left unchanged.
func (p *outPlan) setHeaders(h http.Header, v reflect.Value) error {
	type header struct{ name, value string }
	out := make([]header, 0, len(p.headers)+len(p.cookies))
	for _, ho := range p.headers {
		fv := v.FieldByIndex(ho.index)
		if ho.optional {
			if fv.IsNil() {
				continue
			}
			fv = fv.Elem()
		}
		s, err := scalarText(ho.c, fv)
		if err != nil {
			return fmt.Errorf("header %s: %w", ho.name, err)
		}
		// A value the schema does not admit (one net/http or the client
		// would alter, such as a control character) is a defect.
		for _, ip := range ho.use.implied {
			if !ip.holds(s) {
				return fmt.Errorf("header %s: %q %s", ho.name, s, ip.why)
			}
		}
		out = append(out, header{ho.name, s})
	}
	cookies := len(out)
	for _, co := range p.cookies {
		c := v.FieldByIndex(co.index).Interface().(*http.Cookie)
		if c == nil {
			continue
		}
		line, err := setCookie(co.name, c)
		if err != nil {
			return fmt.Errorf("cookie %s: %w", co.name, err)
		}
		out = append(out, header{"Set-Cookie", line})
	}
	for i, hv := range out {
		if i < cookies {
			h.Set(hv.name, hv.value)
		} else {
			h.Add(hv.name, hv.value)
		}
	}
	return nil
}

// reservedHeaders maps the header names an envelope's header field cannot
// take to the reason.
var reservedHeaders = map[string]string{
	"Content-Type":           bodyHeader,
	"Content-Length":         bodyHeader,
	"X-Content-Type-Options": bodyHeader,
	"Set-Cookie":             "is reserved; use a *http.Cookie field tagged `cookie:\"name\"`",
}

const bodyHeader = "is reserved for the body geta writes"

// validHeaderName reports whether name is an RFC 9110 field name (a token).
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := range len(name) {
		c := name[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// writeJSON writes a held JSON body with its headers.
func writeJSON(w http.ResponseWriter, status int, body []byte) error {
	// As Header.Set would, with the values in one allocation.
	h := w.Header()
	v := make([]string, 3)
	v[0], v[1], v[2] = "application/json", strconv.Itoa(len(body)), "nosniff"
	h["Content-Type"], h["Content-Length"], h["X-Content-Type-Options"] = v[0:1:1], v[1:2:2], v[2:3:3]
	w.WriteHeader(status)
	_, err := w.Write(body)
	return err
}

// marshalText renders a text type's value as encoding/json/v2 does:
// AppendText if the type has it, else MarshalText.
func marshalText(v reflect.Value) ([]byte, error) {
	appends := reflect.PointerTo(v.Type()).Implements(textAppender)
	switch m := v.Interface().(type) {
	case encoding.TextAppender:
		return m.AppendText(nil)
	case encoding.TextMarshaler:
		if !appends {
			return m.MarshalText()
		}
	}
	p := reflect.New(v.Type())
	p.Elem().Set(v)
	if appends {
		return p.Interface().(encoding.TextAppender).AppendText(nil)
	}
	return p.Interface().(encoding.TextMarshaler).MarshalText()
}

// scalarText renders a scalar as header text.
func scalarText(c *codec, v reflect.Value) (string, error) {
	switch c.kind {
	case kString:
		return v.String(), nil
	case kBool:
		return strconv.FormatBool(v.Bool()), nil
	case kInt:
		return strconv.FormatInt(v.Int(), 10), nil
	case kUint:
		return strconv.FormatUint(v.Uint(), 10), nil
	case kFloat:
		// The schema is a JSON number, which has no NaN or infinity.
		f := v.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return "", fmt.Errorf("%v is not a number its schema admits", f)
		}
		return floatText(f, c.t.Bits()), nil
	case kText:
		b, err := marshalText(v)
		return string(b), err
	}
	// Unreachable: headerType admits only the kinds above.
	return "", fmt.Errorf("%s is not a scalar", c.t)
}

// floatText formats f, a float of bits bits, as encoding/json/v2 does: the
// shortest form that reads back as that float (1.1 for a float32 1.1), with
// an exponent only for very large or very small magnitudes.
func floatText(f float64, bits int) string {
	if bits == 64 {
		return string(appendFloat(nil, f))
	}
	abs := float32(math.Abs(f))
	format := byte('f')
	if abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	b := strconv.AppendFloat(nil, f, format, -1, 32)
	if n := len(b); format == 'e' && n >= 4 && b[n-4] == 'e' && b[n-3] == '-' && b[n-2] == '0' {
		b[n-2] = b[n-1]
		b = b[:n-1]
	}
	return string(b)
}
