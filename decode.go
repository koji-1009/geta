package geta

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/binary"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"hash/maphash"
	"maps"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Limits are the backstops applied where a schema declares no upper bound.
// A declared maxLength, maxItems, or maxProperties replaces the backstop for
// that value.
//
// Backstops apply only to what a request reads, and a request's schema in
// the document states them: maxLength on a string, maxItems on an array,
// maxProperties on a map or [WithSchema] object, and maxLength in
// propertyNames for its keys. A pattern is never run on more than 4096 code
// points, so where a pattern applies the stated maxLength is at most that,
// and [New] refuses a larger declared maxLength. A response's schema states
// no backstop, and [App.Conforms] applies none.
//
// For a value a request reads, [New] refuses a declared lower bound above
// the stated upper bound: minLength above MaxStringLength (or the pattern
// ceiling) with no maxLength, minItems above MaxItems with no maxItems, or
// minProperties above MaxItems with no maxProperties.
//
// [New] refuses MaxBodyBytes, MaxStringLength, MaxItems, or MaxDepth below 1.
// A zero field does not mean the default (zero has its own meaning for
// MaxMultipartMemory and MaxResponseBuffer), so start from [DefaultLimits]:
//
//	l := geta.DefaultLimits
//	l.MaxBodyBytes = 64 << 10
//	geta.WithLimits(l)
type Limits struct {
	// MaxBodyBytes caps a request body of any encoding; a larger body is a
	// 413. In a multipart body it caps each file and their sum.
	MaxBodyBytes int64
	// MaxMultipartMemory caps the multipart file bytes held in memory.
	// Beyond it, file content goes to a temporary file removed when the
	// handler returns. Field text stays in memory, bounded by MaxBodyBytes.
	// Zero or less writes every non-empty file to disk.
	MaxMultipartMemory int64
	// MaxStringLength caps, in code points, a string with no maxLength,
	// including the keys of a map or WithSchema object.
	MaxStringLength int
	// MaxItems caps an array with no maxItems, the member count of a map or
	// WithSchema object with no maxProperties, and the parts of a multipart
	// body, ignored ones included; more parts are a 400.
	MaxItems int
	// MaxDepth caps the nesting of a JSON body.
	MaxDepth int
	// MaxResponseBuffer caps the bytes of a JSON response body held in
	// memory. A body that fits is sent with a Content-Length, and an encoding
	// failure is a clean 500. A larger body commits the status and headers
	// (no Content-Length) once it passes the cap and is streamed; a later
	// encoding failure is logged and the connection aborted, so the client
	// never sees a complete response. A single encoder write larger than the
	// cap is passed through unsplit. Zero or less streams every body.
	// Middleware that rewrites a whole response, such as [Gzip] and [ETag],
	// still buffers all of it.
	MaxResponseBuffer int
}

// DefaultLimits are the backstops [New] applies unless [WithLimits] says
// otherwise.
var DefaultLimits = Limits{
	MaxBodyBytes: 1 << 20,
	// Equal to the default MaxBodyBytes: files go to disk only if
	// MaxBodyBytes is raised.
	MaxMultipartMemory: 1 << 20,
	MaxStringLength:    4096,
	MaxItems:           8192,
	MaxDepth:           512,
	// Equal to maxPooledBody, so buffered bodies never outgrow the pool.
	MaxResponseBuffer: 64 << 10,
}

// check refuses MaxBodyBytes, MaxStringLength, MaxItems, or MaxDepth below 1.
func (l Limits) check() error {
	var errs []error
	for _, f := range []struct {
		name string
		v    int64
	}{
		{"MaxBodyBytes", l.MaxBodyBytes},
		{"MaxStringLength", int64(l.MaxStringLength)},
		{"MaxItems", int64(l.MaxItems)},
		{"MaxDepth", int64(l.MaxDepth)},
	} {
		if f.v < 1 {
			errs = append(errs, fmt.Errorf("WithLimits: %s is %d, less than 1", f.name, f.v))
		}
	}
	return errors.Join(errs...)
}

// patternCeiling bounds, in code points, the input a pattern runs over in a
// request, whatever maxLength says.
const patternCeiling = 4096

// maxViolations bounds how many violations one response reports.
const maxViolations = 50

// Violation is one way a request failed its contract.
type Violation struct {
	In      string `json:"in"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

type decoder struct {
	limits Limits
	in     string
	errs   []Violation
	// omitted counts the violations found past maxViolations, not in errs.
	omitted int
	// written is set when checking a response (Conforms); the pattern
	// ceiling does not apply.
	written bool
	// hashes holds the hashes of the values uniqueItems compares
	// (duplicate), made at the first such array.
	hashes *hashMemo
}

// unbounded are the limits a response is checked under (Conforms).
var unbounded = Limits{MaxStringLength: math.MaxInt, MaxItems: math.MaxInt, MaxDepth: math.MaxInt}

// fail records a violation at path, up to maxViolations, and counts the
// rest. A request's path is cut by capPath; a response's is whole, so that
// Conforms names where a response departs from its schema.
func (d *decoder) fail(path, format string, args ...any) {
	if len(d.errs) < maxViolations {
		if !d.written {
			path = capPath(path)
		}
		d.errs = append(d.errs, Violation{In: d.in, Path: path, Message: fmt.Sprintf(format, args...)})
		return
	}
	d.omitted++
}

// failAt is fail at p, rendering p only for a violation that is kept.
func (d *decoder) failAt(p *vpath, format string, args ...any) {
	if len(d.errs) < maxViolations {
		limit := maxPathBytes
		if d.written {
			limit = math.MaxInt
		}
		d.errs = append(d.errs, Violation{In: d.in, Path: p.render(limit), Message: fmt.Sprintf(format, args...)})
		return
	}
	d.omitted++
}

// A vpath is the path of the value check is at, such as $.a[0]. It is
// rendered only for a violation that is kept: rendering it at every value
// would cost its length there, and a path grows with the nesting and with
// the member names a request chooses, so a body would cost depth × length.
type vpath struct {
	up    *vpath
	name  string // a member's name; the whole path when up is nil
	index int    // an element's index, or -1 for a member
}

// rootPath is the vpath of a value whose path is path.
func rootPath(path string) *vpath { return &vpath{name: path, index: -1} }

// A path names a member as RFC 9535 (JSONPath) does: by the shorthand .name
// where name is a member-name-shorthand (§2.5.1.1), and otherwise by a name
// selector in brackets, ['name'], escaped as a Normalized Path escapes it
// (§2.7). A name of digits, or one holding a dot, a bracket, or a quote, is
// then told from an element and from two members.

// isShorthand reports whether name is a member-name-shorthand (RFC 9535
// §2.5.1.1): a name-first (ALPHA, "_", or a character past U+007F) and then
// name-chars (those and DIGIT).
func isShorthand(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); {
		r, size := utf8.DecodeRuneInString(name[i:])
		switch {
		case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', r == '_':
		case '0' <= r && r <= '9':
			if i == 0 {
				return false
			}
		case r == utf8.RuneError && size == 1:
			return false // invalid UTF-8, which brackets write as U+FFFD
		case r < 0x80:
			return false
		}
		i += size
	}
	return true
}

// A pathWriter is what a member's selector is written to.
type pathWriter interface {
	WriteByte(byte) error
	WriteString(string) (int, error)
	WriteRune(rune) (int, error)
}

// writeMember writes the selector of member name to w.
func writeMember(w pathWriter, name string) {
	if isShorthand(name) {
		w.WriteByte('.')
		w.WriteString(name)
		return
	}
	w.WriteString("['")
	for _, r := range name {
		if s := escapeInName(r); s != "" {
			w.WriteString(s)
			continue
		}
		if r < 0x20 {
			w.WriteString(`\u00`)
			w.WriteByte(lowerHex[r>>4])
			w.WriteByte(lowerHex[r&0xf])
			continue
		}
		w.WriteRune(r) // an invalid byte as U+FFFD
	}
	w.WriteString("']")
}

const lowerHex = "0123456789abcdef"

// escapeInName returns the escape a Normalized Path writes for r in a name
// (RFC 9535 §2.7, normal-escapable), or "" for none or \u00XX.
func escapeInName(r rune) string {
	switch r {
	case '\b':
		return `\b`
	case '\f':
		return `\f`
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	case '\t':
		return `\t`
	case '\'':
		return `\'`
	case '\\':
		return `\\`
	}
	return ""
}

// memberLen returns the bytes writeMember writes for name.
func memberLen(name string) int {
	if isShorthand(name) {
		return 1 + len(name)
	}
	n := len("['']")
	for _, r := range name {
		switch {
		case escapeInName(r) != "":
			n += 2
		case r < 0x20:
			n += len(`\u0000`)
		default:
			n += utf8.RuneLen(r) // U+FFFD for an invalid byte
		}
	}
	return n
}

// memberPath returns path followed by the selector of member name.
func memberPath(path, name string) string {
	var b strings.Builder
	b.Grow(len(path) + memberLen(name))
	b.WriteString(path)
	writeMember(&b, name)
	return b.String()
}

// member is the path of p's member name: path + its selector (writeMember).
func (p *vpath) member(name string) *vpath { return &vpath{up: p, name: name, index: -1} }

// item is the path of p's element i: path + "[i]".
func (p *vpath) item(i int) *vpath { return &vpath{up: p, index: i} }

// String renders the path as capPath cuts it.
func (p *vpath) String() string { return p.render(maxPathBytes) }

// render renders the path whole if it fits limit, maxPathBytes or
// math.MaxInt, and otherwise as capPath cuts it, walking up only as far as
// the kept end needs.
func (p *vpath) render(limit int) string {
	n := 0
	top := p
	for ; top.up != nil && n <= limit; top = top.up {
		if top.index < 0 {
			n += memberLen(top.name)
		} else {
			n += 2 + len(strconv.Itoa(top.index))
		}
	}
	var b strings.Builder
	p.writeTo(&b, top)
	s := b.String()
	if len(s) <= limit {
		return s
	}
	return capPath(s)
}

// maxPathBytes bounds the bytes of a violation's path. A path is as long as
// the nesting and the member names a request chooses, so without a bound 50
// violations would answer a small body with a response many times its size.
const maxPathBytes = 256

// pathMark begins a path capPath cut.
const pathMark = "…"

// capPath returns path if it fits maxPathBytes, and otherwise pathMark and
// the end of path, from a rune boundary, in maxPathBytes in all. The end is
// kept because it names the value that failed; In names where it is.
func capPath(path string) string {
	if len(path) <= maxPathBytes {
		return path
	}
	i := len(path) - (maxPathBytes - len(pathMark))
	for i < len(path) && !utf8.RuneStart(path[i]) {
		i++
	}
	return pathMark + path[i:]
}

// writeTo writes p's steps below top to b, top's first, and top's name
// before them when top is the root. It keeps no pointer to p, so that the
// vpaths check makes stay on its stack.
func (p *vpath) writeTo(b *strings.Builder, top *vpath) {
	if p == top {
		if p.up == nil {
			b.WriteString(p.name) // the root: the path is whole
		}
		return
	}
	p.up.writeTo(b, top)
	if p.index < 0 {
		writeMember(b, p.name)
		return
	}
	b.WriteByte('[')
	b.WriteString(strconv.Itoa(p.index))
	b.WriteByte(']')
}

// maxValueBytes bounds the bytes of a request's value that a violation's
// message quotes. A value is as long as the request makes it, within
// MaxStringLength or the schema's maxLength, or, for a number, the body, and
// quoting and JSON escaping make it longer still.
const maxValueBytes = 128

// maxErrorBytes bounds the bytes of an error's text that a violation's
// message carries, when the text may hold the request's values (a body's
// own UnmarshalJSON, a malformed form).
const maxErrorBytes = 512

// clip returns s if it fits n bytes, and otherwise its first n bytes, less
// the start of a rune the cut would split, and pathMark. A byte of invalid
// UTF-8, which a header value may hold, is no rune and is kept whole.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := n
	for j := n - 1; j >= 0 && j > n-utf8.UTFMax; j-- {
		if utf8.RuneStart(s[j]) {
			if r, size := utf8.DecodeRuneInString(s[j:]); (r != utf8.RuneError || size > 1) && j+size > n {
				i = j
			}
			break
		}
	}
	return s[:i] + pathMark
}

// value returns v, a value of the request, as a message quotes it: cut by
// clip to maxValueBytes. A response's value is whole (Conforms), as its path
// is.
func (d *decoder) value(v string) string {
	if d.written {
		return v
	}
	return clip(v, maxValueBytes)
}

// check validates the parsed value v against s and c: type, required
// members, nulls, unknown members, and every schema keyword, including what
// v2 would silently accept (a missing member, an unwanted null). s is the
// schema at this use site; a reference resolves to c's own. The recursion
// depth is bounded by v's nesting, which its parser bounded.
func (d *decoder) check(c *codec, s *schema, v any, path string) {
	d.checkAt(c, s, v, vpath{name: path, index: -1})
}

// checkAt is check at the path at. It takes the path by value, so that
// escape analysis, which cannot tell one level of the recursion from the
// next, keeps each level's path on the stack.
func (d *decoder) checkAt(c *codec, s *schema, v any, at vpath) {
	p := &at
	if s.Ref != "" {
		s = c.schema
	}
	if c.kind == kOneOf {
		d.checkOneOf(c, v, at)
		return
	}
	if c.kind == kNull {
		if v != nil {
			d.checkAt(c.elem, s, v, at)
		}
		return
	}
	if c.kind == kJSON && s.Type == "" {
		return // any JSON value: the type's own UnmarshalJSON decides (decodeJSON)
	}
	want := s.Type
	got := jsonType(v)
	if got != want && !(want == "number" && got == "integer") {
		d.failAt(p, "expected %s, got %s", want, got)
		return
	}
	switch c.kind {
	case kString:
		d.text(s, v.(string), p, "string", "")
	case kText:
		str := v.(string)
		if !d.text(s, str, p, "string", "") {
			return
		}
		if err := unmarshalText(reflect.New(c.t).Interface(), []byte(str)); err != nil {
			what := s.Format
			if what == "" {
				what = c.t.Name()
			}
			d.failAt(p, "%q is not a valid %s", d.value(str), what)
		}
	case kBytes:
		str := v.(string)
		if !d.text(s, str, p, "string", "") {
			return
		}
		if _, err := base64.StdEncoding.DecodeString(str); err != nil {
			d.failAt(p, "%q is not valid base64", d.value(str))
		}
	case kInt:
		lit := string(v.(number))
		n, err := strconv.ParseInt(lit, 10, c.t.Bits())
		if err != nil {
			d.failAt(p, "%s is out of range for %s", d.value(lit), c.t.Kind())
			return
		}
		d.num(s, float64(n), lit, p)
	case kUint:
		lit := string(v.(number))
		n, err := strconv.ParseUint(lit, 10, c.t.Bits())
		if err != nil {
			d.failAt(p, "%s is out of range for %s", d.value(lit), c.t.Kind())
			return
		}
		d.num(s, float64(n), lit, p)
	case kFloat:
		lit := string(v.(number))
		_, f, err := parseFloat(lit, c.t.Bits())
		if err != nil {
			d.failAt(p, "%s is out of range for %s", d.value(lit), c.t.Kind())
			return
		}
		d.num(s, f, lit, p)
	case kJSON:
		// The declared keywords; UnmarshalJSON judges the rest (decodeJSON).
		switch s.Type {
		case "string":
			d.text(s, v.(string), p, "string", "")
		case "integer", "number":
			// The type reads the number, so a float64 need not hold it: past
			// float64's range, f is an infinity, which the keywords compare
			// as they would the number itself.
			lit := string(v.(number))
			f, _ := strconv.ParseFloat(lit, 64)
			d.num(s, f, lit, p)
		case "array":
			d.array(s, v.([]any), p)
		case "object":
			// Member count and names are checked as a map's are.
			m := v.(map[string]any)
			if d.objectLen(s, len(m), p) {
				for _, k := range slices.Sorted(maps.Keys(m)) {
					d.key(s, k, p.member(k))
				}
			}
		}
	case kSlice:
		a := v.([]any)
		if !d.array(s, a, p) {
			return
		}
		for i, el := range a {
			d.checkAt(c.elem, s.Items, el, *p.item(i))
		}
	case kMap:
		m := v.(map[string]any)
		if !d.objectLen(s, len(m), p) {
			return
		}
		// A value is checked only if its key passes.
		for _, k := range slices.Sorted(maps.Keys(m)) {
			kp := p.member(k)
			if d.key(s, k, kp) && d.keyText(c.key, k, kp) {
				d.checkAt(c.elem, s.Additional, m[k], *kp)
			}
		}
	case kStruct:
		m := v.(map[string]any)
		known := make(map[string]bool, len(c.fields))
		for _, f := range c.fields {
			known[f.json] = true
			fv, ok := m[f.json]
			fp := p.member(f.json)
			if !ok {
				// A request may omit a member with a default (applyDefaults);
				// a response may not.
				if !f.optional && (f.use.Default == nil || d.written) {
					d.failAt(fp, "missing required member")
				}
				continue
			}
			d.checkAt(f.c, f.use, fv, *fp)
		}
		var unknown []string
		for k := range m {
			if !known[k] {
				unknown = append(unknown, k)
			}
		}
		slices.Sort(unknown)
		for _, k := range unknown {
			d.failAt(p.member(k), "unknown member")
		}
	}
}

// decodeJSON decodes data, which passed check, into dst. A failure is
// reported as a violation at its JSON Pointer.
func (d *decoder) decodeJSON(data []byte, dst reflect.Value, opts json.Options) {
	if err := json.Unmarshal(data, dst.Addr().Interface(), opts); err != nil {
		d.failJSON(err, data)
	}
}

// failJSON reports err, from encoding/json/v2 reading data, as a violation
// at its JSON Pointer, the body itself when it has none.
func (d *decoder) failJSON(err error, data []byte) {
	path := "$"
	if p, ok := errorPointer(err); ok {
		path = pointerPath(data, p)
	}
	d.fail(path, "%s", errorText(err))
}

// errorPointer returns the JSON Pointer err, from encoding/json/v2, is at,
// if it has one.
func errorPointer(err error) (jsontext.Pointer, bool) {
	if se, ok := errors.AsType[*json.SemanticError](err); ok {
		return se.JSONPointer, true
	}
	if se, ok := errors.AsType[*jsontext.SyntacticError](err); ok {
		return se.JSONPointer, true
	}
	return "", false
}

// errorText returns err's text as a violation's message carries it. The
// text is the type's own, and may quote the value.
func errorText(err error) string { return clip(err.Error(), maxErrorBytes) }

// A methodWalk runs the JSON methods of the types under a body that read
// themselves (kJSON values, and map keys of such types), each on its own
// value, so that every refusal is a violation listed with the rest of the
// body's, as check lists the schema's. encoding/json/v2 stops at the first
// refusal, and decodeJSON runs only on a body check passed.
//
// It reads the body (dec) in step with its parsed tree, and runs a method
// only where check reaches the value and the value's schema passes, so that
// it reports nothing check reports or does not reach: a member check found
// unknown, an array or object past its bound, a key the keys' schema
// refuses, a variant no discriminator selects, a declared type's keyword.
type methodWalk struct {
	d   *decoder
	dec *jsontext.Decoder
	// opts are the body's options (bodyPlan.opts), with which v2 runs the
	// methods.
	opts json.Options
	// under are the codecs at or under which a method runs (methodsUnder).
	under map[*codec]bool
	// scratch is a decoder whose violations are discarded: it asks check's
	// own questions of one value.
	scratch decoder
	// refused counts the refusals, listed or not.
	refused int
	// ptr is the JSON Pointer of the value being read, kept as the walk
	// enters and leaves values, so that a refusal's costs one copy of it
	// (jsontext.Decoder.StackPointer escapes every name again).
	ptr []byte
	// path is the value's path as check names it (vpath), kept as ptr is.
	path bytes.Buffer
	// root is the path the scratch decoder's violations are at.
	root vpath
}

// A mark is the lengths of ptr and path before a step, which leave restores.
type mark struct{ ptr, path int }

// enter appends a member name to ptr and path and returns their lengths
// before it.
func (w *methodWalk) enter(name string) mark {
	n := mark{len(w.ptr), w.path.Len()}
	writeMember(&w.path, name)
	w.ptr = append(w.ptr, '/')
	for i := 0; i < len(name); i++ {
		// RFC 6901 §3, as jsontext writes a pointer.
		switch c := name[i]; c {
		case '~':
			w.ptr = append(w.ptr, "~0"...)
		case '/':
			w.ptr = append(w.ptr, "~1"...)
		default:
			w.ptr = append(w.ptr, c)
		}
	}
	return n
}

// enterIndex appends an array index to ptr and path, as enter does a name.
func (w *methodWalk) enterIndex(i int) mark {
	n := mark{len(w.ptr), w.path.Len()}
	w.path.WriteByte('[')
	w.path.WriteString(strconv.Itoa(i))
	w.path.WriteByte(']')
	w.ptr = strconv.AppendInt(append(w.ptr, '/'), int64(i), 10)
	return n
}

// leave cuts ptr and path back to the mark enter returned.
func (w *methodWalk) leave(n mark) { w.ptr = w.ptr[:n.ptr]; w.path.Truncate(n.path) }

// methods runs the JSON methods under c, as methodWalk describes, on data,
// whose parsed tree is v, and reports each refusal at its path. It returns
// how many methods refused.
func (d *decoder) methods(c *codec, s *schema, v any, data []byte, opts json.Options, under map[*codec]bool) int {
	if !under[c] {
		return 0
	}
	w := &methodWalk{d: d, dec: jsontext.NewDecoder(bytes.NewReader(data)), opts: opts, under: under,
		scratch: decoder{limits: d.limits, in: d.in, written: d.written}, root: vpath{index: -1}}
	w.path.WriteByte('$')
	w.value(c, s, v)
	return w.refused
}

// passes reports whether check, at the value only, finds no violation.
func (w *methodWalk) passes(check func(d *decoder, p *vpath) bool) bool {
	w.scratch.errs, w.scratch.omitted = w.scratch.errs[:0], 0
	return check(&w.scratch, &w.root) && len(w.scratch.errs) == 0
}

// skip reads the next value, which the walk does not enter. The body was
// parsed already, so the read cannot fail.
func (w *methodWalk) skip() { w.dec.SkipValue() }

// value reads one value of c, at the use s, whose parsed tree is v. It
// enters the value where checkAt does.
func (w *methodWalk) value(c *codec, s *schema, v any) {
	if !w.under[c] {
		w.skip()
		return
	}
	if s.Ref != "" {
		s = c.schema
	}
	switch c.kind {
	case kOneOf:
		// The variant checkOneOf checks the object against, if any.
		m, _ := v.(map[string]any)
		tag, _ := m[c.disc].(string)
		if vc := c.variant([]byte(tag)); vc != nil && m != nil {
			w.value(vc, vc.use(), v)
			return
		}
	case kNull:
		if v != nil {
			w.value(c.elem, s, v)
			return
		}
	case kJSON:
		if w.passes(func(d *decoder, p *vpath) bool { d.checkAt(c, s, v, *p); return true }) {
			w.run(c.t)
			return
		}
	case kStruct:
		if m, ok := v.(map[string]any); ok {
			w.object(c, m)
			return
		}
	case kSlice:
		a, ok := v.([]any)
		if ok && w.passes(func(d *decoder, p *vpath) bool { return d.arrayLenAt(s, len(a), p) }) {
			w.dec.ReadToken() // '['
			for i, el := range a {
				n := w.enterIndex(i)
				w.value(c.elem, s.Items, el)
				w.leave(n)
			}
			w.dec.ReadToken() // ']'
			return
		}
	case kMap:
		m, ok := v.(map[string]any)
		if ok && w.passes(func(d *decoder, p *vpath) bool { return d.objectLen(s, len(m), p) }) {
			w.mapValue(c, s, m)
			return
		}
	}
	w.skip()
}

// object reads the members of a struct whose parsed tree is m.
func (w *methodWalk) object(c *codec, m map[string]any) {
	w.dec.ReadToken() // '{'
	for w.dec.PeekKind() != '}' {
		name, _ := w.dec.ReadToken()
		k := name.String()
		i := c.fieldIndex([]byte(k))
		if i < 0 {
			w.skip() // an unknown member
			continue
		}
		fc := &c.fields[i]
		// A pointer member sent null stays nil: v2 runs no method.
		if fc.optional && m[k] == nil && c.t.FieldByIndex(fc.index).Type.Kind() == reflect.Pointer {
			w.skip()
			continue
		}
		n := w.enter(k)
		w.value(fc.c, fc.use, m[k])
		w.leave(n)
	}
	w.dec.ReadToken() // '}'
}

// mapValue reads the members of a map whose parsed tree is m. A value is
// entered only if its key passes, as in checkAt; a key of a type that reads
// itself is then read by its method, and the value only if the method takes
// the key.
func (w *methodWalk) mapValue(c *codec, s *schema, m map[string]any) {
	w.dec.ReadToken() // '{'
	for w.dec.PeekKind() != '}' {
		raw, _ := w.dec.ReadValue() // the name as written, which v2 hands the key's method
		k := memberName(raw)
		if !w.passes(func(d *decoder, p *vpath) bool { return d.key(s, k, p) && d.keyText(c.key, k, p) }) {
			w.skip()
			continue
		}
		n := w.enter(k)
		if c.keyMethods && c.key.kind == kJSON && !w.runOn(c.key.t, raw) {
			w.skip()
		} else {
			w.value(c.elem, s.Additional, m[k])
		}
		w.leave(n)
	}
	w.dec.ReadToken() // '}'
}

// memberName returns the unquoted member name raw, which the parse
// validated.
func memberName(raw jsontext.Value) string {
	b, _ := jsontext.AppendUnquote(nil, raw)
	return string(b)
}

// run reads the next value and runs t's method on it.
func (w *methodWalk) run(t reflect.Type) {
	raw, _ := w.dec.ReadValue()
	w.runOn(t, raw)
}

// runOn runs t's method on raw, the value or member name just read, and
// reports whether it took it. A refusal is reported where v2 reading the
// whole body would report it, in the same words: the error's position is
// moved from raw's start to the body's.
func (w *methodWalk) runOn(t reflect.Type, raw jsontext.Value) bool {
	err := json.Unmarshal(raw, reflect.New(t).Interface(), w.opts)
	if err == nil {
		return true
	}
	w.refused++
	if len(w.d.errs) >= maxViolations {
		// Counted, not listed: its pointer, as long as the nesting, is not
		// copied.
		w.d.omitted++
		return false
	}
	// The path: the value's, then where in raw the error is.
	path := w.path.String()
	if rel, ok := errorPointer(err); ok && rel != "" {
		path += pointerPath(raw, rel)[len("$"):]
	}
	at, off := jsontext.Pointer(w.ptr), w.dec.InputOffset()-int64(len(raw))
	if se, ok := errors.AsType[*json.SemanticError](err); ok {
		se.JSONPointer, se.ByteOffset = at+se.JSONPointer, off+se.ByteOffset
	} else if se, ok := errors.AsType[*jsontext.SyntacticError](err); ok {
		se.JSONPointer, se.ByteOffset = at+se.JSONPointer, off+se.ByteOffset
	}
	w.d.fail(path, "%s", errorText(err))
	return false
}

// methodsUnder returns the codecs reachable from c at or under which a type
// reads itself with its JSON methods (methodWalk), or nil if there are none.
func methodsUnder(c *codec) map[*codec]bool {
	return codecsUnder(c, func(c *codec) bool {
		return c.kind == kJSON || c.kind == kMap && c.keyMethods && c.key.kind == kJSON
	})
}

// applyDefaults sets, at any depth of dst, every member missing from v that
// declares a default, as fastDecoder.object does. v is the parsed tree dst
// was decoded from; under comes from defaultsUnder. opts and sp re-read map
// keys that have their own methods, as decodeJSON read them.
func applyDefaults(c *codec, v any, dst reflect.Value, under map[*codec]bool, opts json.Options, sp *spellings) {
	if !under[c] {
		return
	}
	switch c.kind {
	case kStruct:
		m, _ := v.(map[string]any)
		for _, f := range c.fields {
			fv := dst.FieldByIndex(f.index)
			x, ok := m[f.json]
			if !ok {
				if f.use.Default != nil {
					setScalar(f.c, *f.use.Default, fv)
				}
				continue
			}
			if fv.Kind() == reflect.Pointer {
				// A nil pointer here was sent null (allowed by anyJSON).
				if fv.IsNil() {
					continue
				}
				fv = fv.Elem()
			}
			applyDefaults(f.c, x, fv, under, opts, sp)
		}
	case kSlice:
		a, _ := v.([]any)
		for i := range a {
			applyDefaults(c.elem, a[i], dst.Index(i), under, opts, sp)
		}
	case kMap:
		// Map values are not addressable: fill a copy.
		m := v.(map[string]any)
		for name, x := range m {
			k := reflect.ValueOf(name)
			if c.keyMethods {
				// Re-read the key as decodeJSON did. If the method now
				// answers differently, no entry matches and the value's
				// defaults are left unfilled.
				p := reflect.New(c.t.Key())
				if json.Unmarshal(sp.spelling(m, name), p.Interface(), opts) != nil {
					continue
				}
				k = p.Elem()
			} else if k.Type() != c.t.Key() {
				k = k.Convert(c.t.Key())
			}
			cur := dst.MapIndex(k)
			if !cur.IsValid() {
				continue
			}
			nv := reflect.New(cur.Type()).Elem()
			nv.Set(cur)
			applyDefaults(c.elem, x, nv, under, opts, sp)
			dst.SetMapIndex(k, nv)
		}
	case kNull:
		if v != nil {
			applyDefaults(c.elem, v, dst.Addr().Interface().(nullSlot).slot(), under, opts, sp)
		}
	case kOneOf:
		// The interface holds the variant by value: fill a copy.
		cur := dst.Elem()
		for _, vc := range c.variants {
			if vc.c.t == cur.Type() {
				nv := reflect.New(cur.Type()).Elem()
				nv.Set(cur)
				applyDefaults(vc.c, v, nv, under, opts, sp)
				dst.Set(nv)
			}
		}
	}
}

// defaultsUnder returns the codecs reachable from c under which some member
// declares a default, or nil if there are none.
func defaultsUnder(c *codec) map[*codec]bool {
	return codecsUnder(c, func(c *codec) bool {
		return c.kind == kStruct && slices.ContainsFunc(c.fields, func(f fieldCodec) bool { return f.use.Default != nil })
	})
}

// codecsUnder returns the codecs reachable from c that are, or reach, one
// that self holds of, or nil if there are none.
func codecsUnder(c *codec, self func(c *codec) bool) map[*codec]bool {
	var all []*codec
	seen := map[*codec]bool{}
	var walk func(c *codec)
	walk = func(c *codec) {
		if seen[c] {
			return
		}
		seen[c] = true
		all = append(all, c)
		switch c.kind {
		case kStruct:
			for _, f := range c.fields {
				walk(f.c)
			}
		case kSlice, kMap, kNull:
			walk(c.elem)
		case kOneOf:
			for _, v := range c.variants {
				walk(v.c)
			}
		}
	}
	walk(c)
	var under map[*codec]bool
	for changed := true; changed; {
		changed = false
		for _, c := range all {
			if under[c] {
				continue
			}
			has := self(c)
			switch c.kind {
			case kStruct:
				has = has || slices.ContainsFunc(c.fields, func(f fieldCodec) bool { return under[f.c] })
			case kSlice, kMap, kNull:
				has = has || under[c.elem]
			case kOneOf:
				has = has || slices.ContainsFunc(c.variants, func(v variantCodec) bool { return under[v.c] })
			}
			if has {
				if under == nil {
					under = map[*codec]bool{}
				}
				under[c], changed = true, true
			}
		}
	}
	return under
}

// str checks the string keywords. It reports whether the value passed.
func (d *decoder) str(s *schema, v, path string) bool {
	return d.text(s, v, rootPath(path), "string", "")
}

// key checks a map's or WithSchema object's member name k against the keys'
// schema (schema.keys). It reports whether the key passed.
func (d *decoder) key(s *schema, k string, p *vpath) bool {
	return d.text(s.keys(), k, p, "key", "key ")
}

// keyText checks a map key k of a text type (codec kc) with the type's
// UnmarshalText, as check does for a value. It reports whether the key
// passed.
func (d *decoder) keyText(kc *codec, k string, p *vpath) bool {
	if kc == nil || kc.kind != kText {
		return true
	}
	if err := unmarshalText(reflect.New(kc.t).Interface(), []byte(k)); err != nil {
		what := kc.schema.Format
		if what == "" {
			what = kc.t.Name()
		}
		d.failAt(p, "key %q is not a valid %s", d.value(k), what)
		return false
	}
	return true
}

// text implements str and key. what names the thing in length messages, and
// lead prefixes the quoted value.
func (d *decoder) text(s *schema, v string, p *vpath, what, lead string) bool {
	// A body's strings are valid UTF-8 already; parameter text may not be.
	if !utf8.ValidString(v) {
		d.failAt(p, "the text is not valid UTF-8")
		return false
	}
	n := utf8.RuneCountInString(v)
	switch {
	case s.MaxLength != nil && n > *s.MaxLength:
		d.failAt(p, "%s length %d exceeds maxLength %d", what, n, *s.MaxLength)
		return false
	case s.MaxLength == nil && n > d.limits.MaxStringLength:
		d.failAt(p, "%s length %d exceeds the ceiling of %d code points", what, n, d.limits.MaxStringLength)
		return false
	case s.MinLength != nil && n < *s.MinLength:
		d.failAt(p, "%s length %d is shorter than minLength %d", what, n, *s.MinLength)
		return false
	}
	if len(s.Enum) > 0 && !slices.Contains(s.Enum, v) {
		d.failAt(p, "%s%q is not one of %s", lead, d.value(v), strings.Join(s.Enum, ", "))
		return false
	}
	if !s.validFormat(v) {
		d.failAt(p, "%s%q is not a valid %s", lead, d.value(v), s.Format)
		return false
	}
	if s.re != nil {
		if n > patternCeiling && !d.written {
			d.failAt(p, "%s length %d exceeds the pattern-validation ceiling of %d code points", what, n, patternCeiling)
			return false
		}
		if !s.re.MatchString(v) {
			d.failAt(p, "%s%q does not match pattern %s", lead, d.value(v), s.Pattern)
			return false
		}
	}
	for _, ip := range s.implied {
		if !ip.holds(v) {
			d.failAt(p, "%s%q %s", lead, d.value(v), ip.why)
			return false
		}
	}
	return true
}

// validFormat reports whether v meets the format geta checks (schema.check).
// A FormatType's format is checked by its UnmarshalText in the caller.
func (s *schema) validFormat(v string) bool {
	return s.check == nil || s.check(v)
}

// num checks the numeric keywords. lit is the value as written, and f the
// float64 nearest it, whatever the target Go type.
func (d *decoder) num(s *schema, f float64, lit string, p *vpath) bool {
	ok := true
	if s.Minimum != nil && cmpNum(lit, f, *s.Minimum) < 0 {
		d.failAt(p, "%s is less than minimum %s", d.value(lit), fmtNum(*s.Minimum))
		ok = false
	}
	if s.Maximum != nil && cmpNum(lit, f, *s.Maximum) > 0 {
		d.failAt(p, "%s is greater than maximum %s", d.value(lit), fmtNum(*s.Maximum))
		ok = false
	}
	if s.ExclusiveMinimum != nil && cmpNum(lit, f, *s.ExclusiveMinimum) <= 0 {
		d.failAt(p, "%s is not greater than exclusiveMinimum %s", d.value(lit), fmtNum(*s.ExclusiveMinimum))
		ok = false
	}
	if s.ExclusiveMaximum != nil && cmpNum(lit, f, *s.ExclusiveMaximum) >= 0 {
		d.failAt(p, "%s is not less than exclusiveMaximum %s", d.value(lit), fmtNum(*s.ExclusiveMaximum))
		ok = false
	}
	if s.MultipleOf != nil && !isMultiple(lit, *s.MultipleOf) {
		d.failAt(p, "%s is not a multiple of %s", d.value(lit), fmtNum(*s.MultipleOf))
		ok = false
	}
	if len(s.numEnum) > 0 && !s.inNumEnum(lit) {
		d.failAt(p, "%s is not one of %s", d.value(lit), strings.Join(s.Enum, ", "))
		ok = false
	}
	return ok
}

func fmtNum(f float64) string { return string(appendFloat(nil, f)) }

// parseFloat parses lit as a float of the given size (v) and as a float64
// (near). Keywords compare near, so a float32 0.1 still meets maximum=0.1.
func parseFloat(lit string, bits int) (v, near float64, err error) {
	v, err = strconv.ParseFloat(lit, bits)
	if err != nil || bits == 64 {
		return v, v, err
	}
	near, err = strconv.ParseFloat(lit, 64)
	return v, near, err
}

// cmpNum compares the literal lit with the bound b exactly, taking b as the
// shortest decimal that reads as b (as the document writes it). f is the
// float64 nearest lit. Rounding preserves order, so only when f == b are
// the decimals compared (e.g. 9007199254740993 vs 9007199254740992).
func cmpNum[T string | []byte](lit T, f, b float64) int {
	switch {
	case f < b:
		return -1
	case f > b:
		return 1
	}
	var buf [32]byte
	return parseDecimal(string(lit)).cmp(parseDecimal(string(strconv.AppendFloat(buf[:0], b, 'e', -1, 64))))
}

// isMultiple reports whether lit is exactly an integer multiple of the
// shortest decimal that reads as m: 0.3 is a multiple of 0.1.
func isMultiple[T string | []byte](lit T, m float64) bool {
	v := parseDecimal(string(lit))
	if v.digits == "" {
		return true // zero is a multiple of everything
	}
	var buf [32]byte
	q := parseDecimal(string(strconv.AppendFloat(buf[:0], m, 'e', -1, 64)))
	// With v = V×10^i and q = Q×10^j (V, Q not divisible by 10), v/q is an
	// integer only if i >= j and Q divides V×10^(i-j).
	t := (v.exp - int64(v.n)) - (q.exp - int64(q.n))
	if t < 0 {
		return false
	}
	var mod uint64 // Q, at most 17 digits (the shortest form of a float64)
	for i := 0; i < len(q.digits); i++ {
		if c := q.digits[i]; c != '.' {
			mod = mod*10 + uint64(c-'0')
		}
	}
	var r uint64 // V mod Q
	for i := 0; i < len(v.digits); i++ {
		if c := v.digits[i]; c != '.' {
			r = (r*10 + uint64(c-'0')) % mod
		}
	}
	// Q < 2^57, so 64 factors of 10 cover every power of 2 and 5 in Q, and
	// a saturated exponent gives the exact answer.
	for range min(t, 64) {
		if r == 0 {
			break
		}
		r = r * 10 % mod
	}
	return r == 0
}

// A decimal is a JSON number read exactly: ±0.digits × 10^exp. digits has no
// leading or trailing zero, may contain the written '.', which is skipped,
// and is empty for zero. n counts the digits without the point.
type decimal struct {
	neg    bool
	digits string
	n      int
	exp    int64

	// An exponent of more than 16 digits saturates exp at hugeExp, which
	// still orders against any bound. etext (the written exponent) plus adj
	// gives the exact value (exactExp).
	huge  bool
	etext string
	adj   int64
}

// hugeExp is the magnitude past which a written exponent saturates.
const hugeExp = 1e17

// parseDecimal reads a number in JSON syntax, also accepting a leading plus
// and leading zeros as parameter text may have.
func parseDecimal(s string) decimal {
	var d decimal
	if s != "" && (s[0] == '-' || s[0] == '+') {
		d.neg = s[0] == '-'
		s = s[1:]
	}
	m, e := s, ""
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		m, e = s[:i], s[i+1:]
	}
	point := strings.IndexByte(m, '.')
	if point < 0 {
		point = len(m)
	}
	first, last := -1, -1
	for i := 0; i < len(m); i++ {
		if c := m[i]; '1' <= c && c <= '9' {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return decimal{} // zero, however written: 0, -0, 0.000e5
	}
	d.digits = m[first : last+1]
	d.n = len(d.digits)
	if first < point && point < last {
		d.n--
	}
	if first < point {
		d.adj = int64(point - first)
	} else {
		d.adj = -int64(first - point - 1)
	}
	var x int64
	neg, digits := false, e
	if digits != "" && (digits[0] == '-' || digits[0] == '+') {
		neg = digits[0] == '-'
		digits = digits[1:]
	}
	digits = strings.TrimLeft(digits, "0")
	if len(digits) > 16 {
		d.huge, d.etext = true, e
		x = hugeExp
	} else {
		for i := 0; i < len(digits); i++ {
			x = x*10 + int64(digits[i]-'0')
		}
	}
	if neg {
		x = -x
	}
	d.exp = x + d.adj
	return d
}

func (d decimal) sign() int {
	switch {
	case d.digits == "":
		return 0
	case d.neg:
		return -1
	}
	return 1
}

// cmp compares d and e by value. An exponent saturated in both compares
// only the digits; a bound never has one.
func (d decimal) cmp(e decimal) int {
	sd, se := d.sign(), e.sign()
	if sd != se || sd == 0 {
		return cmp.Compare(sd, se)
	}
	c := cmp.Compare(d.exp, e.exp)
	if c == 0 {
		i, j := 0, 0
		for c == 0 {
			if i < len(d.digits) && d.digits[i] == '.' {
				i++
			}
			if j < len(e.digits) && e.digits[j] == '.' {
				j++
			}
			if i == len(d.digits) || j == len(e.digits) {
				// The digits end in a significant digit, so what remains is more.
				c = cmp.Compare(len(d.digits)-i, len(e.digits)-j)
				break
			}
			c = cmp.Compare(d.digits[i], e.digits[j])
			i++
			j++
		}
	}
	return sd * c
}

// appendCanonical writes d so that numbers are written alike exactly when
// equal: 1, 1.0, 10e-1, and 0.1e1 all as 1e1.
func (d decimal) appendCanonical(b []byte) []byte {
	if d.digits == "" {
		return append(b, '0')
	}
	if d.neg {
		b = append(b, '-')
	}
	for i := 0; i < len(d.digits); i++ {
		if c := d.digits[i]; c != '.' {
			b = append(b, c)
		}
	}
	b = append(b, 'e')
	if !d.huge {
		return strconv.AppendInt(b, d.exp, 10)
	}
	return d.exactExp(b)
}

// exactExp appends etext + adj for an exponent too long for an int64. adj
// is far smaller, so the sign is unchanged. The sum is done digit by digit
// because big.Int parses long decimals in quadratic time.
func (d decimal) exactExp(b []byte) []byte {
	e, adj := d.etext, d.adj
	if e[0] == '-' || e[0] == '+' {
		if e[0] == '-' {
			b = append(b, '-')
			adj = -adj
		}
		e = e[1:]
	}
	digits := []byte(strings.TrimLeft(e, "0"))
	carry := adj
	for i := len(digits) - 1; i >= 0 && carry != 0; i-- {
		v := int64(digits[i]-'0') + carry
		q, r := v/10, v%10
		if r < 0 {
			q, r = q-1, r+10
		}
		digits[i], carry = byte('0'+r), q
	}
	if carry > 0 {
		return append(strconv.AppendInt(b, carry, 10), digits...)
	}
	return append(b, bytes.TrimLeft(digits, "0")...)
}

// array checks the array keywords and reports whether to check the
// elements: false only for an array past its upper bound.
func (d *decoder) array(s *schema, a []any, p *vpath) bool {
	if !d.arrayLenAt(s, len(a), p) {
		return false
	}
	if s.UniqueItems {
		if d.hashes == nil {
			d.hashes = &hashMemo{arrays: map[*any]uint64{}, objects: map[uintptr]uint64{}}
		}
		if j, i, dup := duplicate(a, d.hashes); dup {
			d.failAt(p, "array items at [%d] and [%d] are equal (uniqueItems)", j, i)
		}
	}
	return true
}

// duplicate finds the first element of a equal to an earlier one, as
// canonical compares them, and the first such earlier one. Elements are told
// apart by valueHash, and canonical decides a match, so an array nested in
// a's elements is hashed once (memo) rather than rendered again for each
// enclosing uniqueItems array.
func duplicate(a []any, memo *hashMemo) (j, i int, dup bool) {
	seen := make(map[uint64]int, len(a))
	for i, el := range a {
		h := valueHash(el, memo)
		j, ok := seen[h]
		if !ok {
			seen[h] = i
			continue
		}
		if canonical(a[j]) == canonical(el) {
			return j, i, true
		}
		return duplicateExact(a) // two values share a hash
	}
	return 0, 0, false
}

// duplicateExact is duplicate by canonical alone.
func duplicateExact(a []any) (j, i int, dup bool) {
	seen := make(map[string]int, len(a))
	for i, el := range a {
		k := canonical(el)
		if j, ok := seen[k]; ok {
			return j, i, true
		}
		seen[k] = i
	}
	return 0, 0, false
}

// uniqueSeed keys the hashes uniqueItems compares, so that a request cannot
// choose values whose hashes collide.
var uniqueSeed = maphash.MakeSeed()

// Tags that keep the hashes of values of different kinds apart.
const (
	tagNull byte = iota + 1
	tagFalse
	tagTrue
	tagString
	tagNumber
	tagArray
	tagObject
	tagMember
)

func hashBytes(tag byte, b []byte) uint64 {
	var h maphash.Hash
	h.SetSeed(uniqueSeed)
	h.WriteByte(tag)
	h.Write(b)
	return h.Sum64()
}

func hashString(tag byte, s string) uint64 {
	var h maphash.Hash
	h.SetSeed(uniqueSeed)
	h.WriteByte(tag)
	h.WriteString(s)
	return h.Sum64()
}

// hashNumber hashes the number written lit by its exact value.
func hashNumber(lit string) uint64 {
	var buf [32]byte
	return hashBytes(tagNumber, parseDecimal(lit).appendCanonical(buf[:0]))
}

// writeWord adds x to h.
func writeWord(h *maphash.Hash, x uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], x)
	h.Write(b[:])
}

// startArray begins the hash of an array; each element's hash follows
// (writeWord).
func startArray(h *maphash.Hash) {
	h.SetSeed(uniqueSeed)
	h.WriteByte(tagArray)
}

// memberHash hashes a member from its name's hash and its value's. An
// object's members are summed, so that their order does not count.
func memberHash(name, value uint64) uint64 {
	var h maphash.Hash
	h.SetSeed(uniqueSeed)
	h.WriteByte(tagMember)
	writeWord(&h, name)
	writeWord(&h, value)
	return h.Sum64()
}

// objectHash hashes an object from the sum of its memberHash values.
func objectHash(sum uint64) uint64 {
	var h maphash.Hash
	h.SetSeed(uniqueSeed)
	h.WriteByte(tagObject)
	writeWord(&h, sum)
	return h.Sum64()
}

// A hashMemo holds the valueHash of each array and object hashed, by
// identity.
type hashMemo struct {
	arrays  map[*any]uint64    // by the first element
	objects map[uintptr]uint64 // by the map (reflect's Pointer)
}

// valueHash hashes a parsed value so that values canonical renders alike
// hash alike. A non-nil memo keeps the hash of each array and object, so
// that one nested in another is hashed once.
func valueHash(v any, memo *hashMemo) uint64 {
	switch v := v.(type) {
	case nil:
		return hashBytes(tagNull, nil)
	case bool:
		if v {
			return hashBytes(tagTrue, nil)
		}
		return hashBytes(tagFalse, nil)
	case string:
		return hashString(tagString, v)
	case number:
		return hashNumber(string(v))
	case []any:
		if len(v) > 0 && memo != nil {
			if x, ok := memo.arrays[&v[0]]; ok {
				return x
			}
		}
		var h maphash.Hash
		startArray(&h)
		for _, el := range v {
			writeWord(&h, valueHash(el, memo))
		}
		x := h.Sum64()
		if len(v) > 0 && memo != nil {
			memo.arrays[&v[0]] = x
		}
		return x
	case map[string]any:
		var id uintptr
		if memo != nil {
			id = reflect.ValueOf(v).Pointer()
			if x, ok := memo.objects[id]; ok {
				return x
			}
		}
		var sum uint64
		for k, el := range v {
			sum += memberHash(hashString(tagString, k), valueHash(el, memo))
		}
		x := objectHash(sum)
		if memo != nil {
			memo.objects[id] = x
		}
		return x
	}
	// Unreachable: the cases cover every type parseJSON makes.
	return 0
}

// arrayLen checks the length keywords of an array of n items.
func (d *decoder) arrayLen(s *schema, n int, path string) bool {
	return d.arrayLenAt(s, n, rootPath(path))
}

// arrayLenAt is arrayLen at the path p.
func (d *decoder) arrayLenAt(s *schema, n int, p *vpath) bool {
	switch {
	case s.MaxItems != nil && n > *s.MaxItems:
		d.failAt(p, "array length %d exceeds maxItems %d", n, *s.MaxItems)
		return false
	case s.MaxItems == nil && n > d.limits.MaxItems:
		d.failAt(p, "array length %d exceeds the ceiling of %d items", n, d.limits.MaxItems)
		return false
	}
	if s.MinItems != nil && n < *s.MinItems {
		d.failAt(p, "array length %d is shorter than minItems %d", n, *s.MinItems)
	}
	return true
}

// objectLen checks the member-count keywords of a map or WithSchema object
// of n members, with MaxItems as the backstop. It reports false when the
// object is past its upper bound.
func (d *decoder) objectLen(s *schema, n int, p *vpath) bool {
	switch {
	case s.MaxProperties != nil && n > *s.MaxProperties:
		d.failAt(p, "object has %d members, more than maxProperties %d", n, *s.MaxProperties)
		return false
	case s.MaxProperties == nil && n > d.limits.MaxItems:
		d.failAt(p, "object has %d members, over the ceiling of %d", n, d.limits.MaxItems)
		return false
	}
	if s.MinProperties != nil && n < *s.MinProperties {
		d.failAt(p, "object has %d members, fewer than minProperties %d", n, *s.MinProperties)
	}
	return true
}

// canonical renders a parsed JSON value so that equal values render equally:
// keys sorted, numbers by their exact value (appendCanonical).
func canonical(v any) string {
	var b strings.Builder
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case nil:
			b.WriteString("null")
		case bool:
			b.WriteString(strconv.FormatBool(v))
		case string:
			b.Write(quote(v))
		case number:
			var buf [32]byte
			b.Write(parseDecimal(string(v)).appendCanonical(buf[:0]))
		case []any:
			b.WriteByte('[')
			for i, el := range v {
				if i > 0 {
					b.WriteByte(',')
				}
				walk(el)
			}
			b.WriteByte(']')
		case map[string]any:
			b.WriteByte('{')
			for i, k := range slices.Sorted(maps.Keys(v)) {
				if i > 0 {
					b.WriteByte(',')
				}
				b.Write(quote(k))
				b.WriteByte(':')
				walk(v[k])
			}
			b.WriteByte('}')
		}
	}
	walk(v)
	return b.String()
}

func quote(s string) []byte {
	b, _ := jsontext.AppendQuote(nil, s)
	return b
}
