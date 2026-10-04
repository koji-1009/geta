package geta

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"reflect"
	"strconv"
	"sync"
	"unicode/utf8"
)

// A fastDecoder validates and decodes a JSON body in one pass over its
// tokens, checking each value against the schema and handing leaves to
// encoding/json/v2.
//
// Invariant: it accepts exactly what the reference path (parseJSON, check,
// decodeJSON) accepts and produces the same value, except that it refuses a
// JSON-method type whose WithSchema declaration is an array or object. On
// the first thing it refuses it returns false, and the caller runs the
// reference path, which accepts the body or names every violation.
type fastDecoder struct {
	body    bytes.Buffer // the request body; the decoder reads it in place
	data    []byte       // the body as decode began reading it
	src     bytes.Reader // the body again, for the reference path
	dec     jsontext.Decoder
	limits  Limits
	opts    json.Options // the reference path's options (bodyPlan.opts)
	scratch []byte       // unquoted strings that contain escapes

	// base is the nesting depth outside this decoder's input: zero for a
	// request body, the enclosing depth for a sealed value read ahead.
	base int
}

var fastDecoders = sync.Pool{New: func() any { return new(fastDecoder) }}

// maxPooledBody bounds the buffer a pooled decoder or body writer keeps
// between requests. It equals DefaultLimits.MaxResponseBuffer.
const maxPooledBody = 64 << 10

// getFastDecoder returns a pooled decoder for limits. The caller sets opts.
func getFastDecoder(limits Limits) *fastDecoder {
	f := fastDecoders.Get().(*fastDecoder)
	f.limits = limits
	return f
}

// release returns f to the pool, keeping no reference to the request.
func (f *fastDecoder) release() {
	if f.body.Cap() > maxPooledBody || cap(f.scratch) > maxPooledBody {
		return
	}
	f.body.Reset()
	f.data = nil
	f.src.Reset(nil)
	f.dec.Reset(&f.body) // drops the decoder's view of the old body
	f.scratch = f.scratch[:0]
	f.base = 0
	f.opts = nil
	fastDecoders.Put(f)
}

// reread returns the decoder reset to read data from the start.
func (f *fastDecoder) reread(data []byte) *jsontext.Decoder {
	f.src.Reset(data)
	f.dec.Reset(&f.src)
	return &f.dec
}

// decode fills dst, a zero value, from the body. It reports false at the
// first thing the reference path would refuse; dst is then partly filled.
func (f *fastDecoder) decode(c *codec, s *schema, dst reflect.Value) bool {
	f.data = f.body.Bytes()
	f.dec.Reset(&f.body)
	if !f.value(c, s, dst) {
		return false
	}
	_, err := f.dec.ReadToken()
	return err == io.EOF
}

// value reads one value of c into dst. Nesting is bounded in open; every
// recursion reads a delimiter first, except kNull's (whose element is never
// a Nullable) and kOneOf's (which goes to object).
func (f *fastDecoder) value(c *codec, s *schema, dst reflect.Value) bool {
	if s.Ref != "" {
		s = c.schema
	}
	switch c.kind {
	case kStruct:
		return f.object(c, dst)
	case kMap:
		return f.mapValue(c, s, dst)
	case kSlice:
		return f.array(c, s, dst)
	case kOneOf:
		return f.oneOf(c, dst)
	case kNull:
		n := dst.Addr().Interface().(nullSlot)
		if f.dec.PeekKind() == 'n' {
			if _, err := f.dec.ReadToken(); err != nil {
				return false
			}
			n.setNull()
			return true
		}
		return f.value(c.elem, s, n.slot())
	}
	// A leaf: check its raw JSON against the schema, then let v2 decode it.
	if f.dec.PeekKind() == 'n' && !anyJSON(c, s) {
		return false
	}
	raw, err := f.dec.ReadValue()
	if err != nil {
		return false
	}
	switch c.kind {
	case kString, kText, kBytes:
		if raw.Kind() != '"' || !strOK(s, f.limits, f.unquote(raw)) {
			return false
		}
	case kBool:
		if k := raw.Kind(); k != 't' && k != 'f' {
			return false
		}
	case kInt, kUint:
		if raw.Kind() != '0' || !isIntegerLiteral(string(raw)) {
			return false
		}
		n, err := strconv.ParseFloat(string(raw), 64)
		if err != nil || !numOK(s, []byte(raw), n) {
			return false
		}
	case kFloat:
		if raw.Kind() != '0' {
			return false
		}
		_, n, err := parseFloat(string(raw), c.t.Bits())
		if err != nil || !numOK(s, []byte(raw), n) {
			return false
		}
	case kJSON:
		// Scalar declarations only; array and object go to the reference
		// path.
		switch s.Type {
		case "string":
			if raw.Kind() != '"' || !strOK(s, f.limits, f.unquote(raw)) {
				return false
			}
		case "integer", "number":
			if raw.Kind() != '0' || (s.Type == "integer" && !isIntegerLiteral(string(raw))) {
				return false
			}
			n, err := strconv.ParseFloat(string(raw), 64)
			if err != nil || !numOK(s, []byte(raw), n) {
				return false
			}
		case "boolean":
			if k := raw.Kind(); k != 't' && k != 'f' {
				return false
			}
		case "":
			// Any JSON value: the type's UnmarshalJSON decides.
		default:
			return false
		}
	default:
		// A leaf kind without a case here falls back to the reference
		// path rather than skip the schema check.
		return false
	}
	// The reference path's options reject unknown members and decode sealed
	// types inside JSON-method types.
	return json.Unmarshal(raw, dst.Addr().Interface(), f.opts) == nil
}

// anyJSON reports whether s, at a use of c, takes any JSON value including
// null: a JSON-method type with no WithSchema declaration (schema {}).
func anyJSON(c *codec, s *schema) bool {
	if s.Ref != "" {
		s = c.schema
	}
	return c.kind == kJSON && s.Type == ""
}

// unquote returns the content of raw, a string the decoder has already
// validated, so AppendUnquote cannot fail. The result is valid until the
// next read or unquote.
func (f *fastDecoder) unquote(raw jsontext.Value) []byte {
	if bytes.IndexByte(raw, '\\') < 0 {
		return raw[1 : len(raw)-1]
	}
	f.scratch, _ = jsontext.AppendUnquote(f.scratch[:0], raw)
	return f.scratch
}

// open reads the delimiter that begins an object or array and enforces
// MaxDepth as parseJSON does.
func (f *fastDecoder) open(kind jsontext.Kind) bool {
	if f.dec.PeekKind() != kind {
		return false
	}
	if _, err := f.dec.ReadToken(); err != nil {
		return false
	}
	return f.base+f.dec.StackDepth() <= f.limits.MaxDepth
}

// close reads the '}' or ']' that PeekKind just reported. The read cannot
// fail, since every value inside was read whole.
func (f *fastDecoder) close() {
	f.dec.ReadToken()
}

func (f *fastDecoder) object(c *codec, dst reflect.Value) bool {
	if !f.open('{') {
		return false
	}
	var seen uint64
	var seenMany []bool
	if len(c.fields) > 64 {
		seenMany = make([]bool, len(c.fields))
	}
	for f.dec.PeekKind() != '}' {
		raw, err := f.dec.ReadValue()
		if err != nil {
			return false
		}
		i := c.fieldIndex(f.unquote(raw)) // a name is a string
		if i < 0 {
			return false // an unknown member
		}
		if seenMany != nil {
			seenMany[i] = true
		} else {
			seen |= 1 << i
		}
		fc := &c.fields[i]
		fv := dst.FieldByIndex(fc.index)
		// Optional members are pointers, except sealed values (interfaces)
		// and Nullables.
		if fc.optional && fv.Kind() == reflect.Pointer {
			// null for an anyJSON type is valid and leaves the pointer nil,
			// as v2 does.
			if f.dec.PeekKind() == 'n' && anyJSON(fc.c, fc.use) {
				if _, err := f.dec.ReadToken(); err != nil {
					return false
				}
				continue
			}
			p := reflect.New(fc.c.t)
			fv.Set(p)
			fv = p.Elem()
		}
		if !f.value(fc.c, fc.use, fv) {
			return false
		}
	}
	f.close()
	for i := range c.fields {
		fc := &c.fields[i]
		if fc.optional || seenMany != nil && seenMany[i] || seenMany == nil && seen&(1<<i) != 0 {
			continue
		}
		if fc.use.Default == nil {
			return false // a missing required member
		}
		// An omitted member takes its default, as in applyDefaults.
		setScalar(fc.c, *fc.use.Default, dst.FieldByIndex(fc.index))
	}
	return true
}

func (f *fastDecoder) mapValue(c *codec, s *schema, dst reflect.Value) bool {
	if !f.open('{') {
		return false
	}
	if dst.IsNil() {
		dst.Set(reflect.MakeMap(c.t))
	}
	kt, et := c.t.Key(), c.t.Elem()
	// A declared maxProperties replaces the MaxItems backstop.
	limit := f.limits.MaxItems
	if s.MaxProperties != nil {
		limit = *s.MaxProperties
	}
	n := 0
	for f.dec.PeekKind() != '}' {
		raw, err := f.dec.ReadValue()
		if err != nil {
			return false
		}
		name := f.unquote(raw) // a name is a string
		if n++; n > limit {
			return false
		}
		if !strOK(s.keys(), f.limits, name) {
			return false
		}
		var k reflect.Value
		if c.keyMethods {
			// v2 reads the key by its methods; two names that read as one
			// key are refused.
			p := reflect.New(kt)
			if json.Unmarshal(raw, p.Interface(), f.opts) != nil {
				return false
			}
			if k = p.Elem(); dst.MapIndex(k).IsValid() {
				return false
			}
		} else if k = reflect.ValueOf(string(name)); k.Type() != kt {
			k = k.Convert(kt)
		}
		v := reflect.New(et).Elem()
		if !f.value(c.elem, s.Additional, v) {
			return false
		}
		dst.SetMapIndex(k, v)
	}
	f.close()
	return s.MinProperties == nil || n >= *s.MinProperties
}

func (f *fastDecoder) array(c *codec, s *schema, dst reflect.Value) bool {
	if !f.open('[') {
		return false
	}
	limit := f.limits.MaxItems
	if s.MaxItems != nil {
		limit = *s.MaxItems
	}
	n := 0
	var spans []int64 // with uniqueItems: where each element starts and ends
	for f.dec.PeekKind() != ']' {
		if n == limit {
			return false
		}
		if s.UniqueItems {
			spans = append(spans, f.dec.InputOffset())
		}
		if n == dst.Cap() {
			if n == 0 {
				dst.Grow(min(4, limit)) // most arrays are short: one allocation
			} else {
				dst.Grow(1)
			}
		}
		dst.SetLen(n + 1)
		if !f.value(c.elem, s.Items, dst.Index(n)) {
			return false
		}
		if s.UniqueItems {
			spans = append(spans, f.dec.InputOffset())
		}
		n++
	}
	f.close()
	if s.MinItems != nil && n < *s.MinItems {
		return false
	}
	if s.UniqueItems && !f.unique(spans) {
		return false
	}
	if n == 0 {
		dst.Set(reflect.MakeSlice(c.t, 0, 0)) // [] is empty, not nil, as v2 makes it
	}
	return true
}

// oneOf reads a sealed value as the variant its discriminator selects and
// stores it in dst. It refuses what checkOneOf refuses.
func (f *fastDecoder) oneOf(c *codec, dst reflect.Value) bool {
	if f.dec.PeekKind() != '{' {
		return false
	}
	if tag, ok := f.leadingTag(c.disc); ok {
		vc := c.variant(tag)
		if vc == nil {
			return false
		}
		v := reflect.New(vc.t).Elem()
		if !f.object(vc, v) {
			return false
		}
		dst.Set(v)
		return true
	}
	// The discriminator is not first or is escaped: read the object whole,
	// find the discriminator, and decode it again with a sub-decoder that
	// carries the outer depth.
	base := f.base + f.dec.StackDepth()
	raw, err := f.dec.ReadValue()
	if err != nil {
		return false
	}
	sub := getFastDecoder(f.limits)
	defer sub.release()
	sub.opts = f.opts
	sub.base = base
	vc := sub.variantIn(c, raw)
	if vc == nil {
		return false
	}
	v := reflect.New(vc.t).Elem()
	sub.body.Reset()
	sub.body.Write(raw)
	sub.data = sub.body.Bytes()
	sub.dec.Reset(&sub.body)
	if !sub.object(vc, v) {
		return false
	}
	dst.Set(v)
	return true
}

// leadingTag peeks, without reading, at the discriminator of the next object
// when it is the first member and has no escapes (the order geta writes).
// It is only a hint: the object is then read and validated in full. The
// caller must have seen PeekKind report '{'.
func (f *fastDecoder) leadingTag(disc string) ([]byte, bool) {
	const ws = " \t\r\n"
	b := bytes.TrimLeft(f.data[f.dec.InputOffset():], ws+",:")
	b = bytes.TrimLeft(b[1:], ws)
	n := len(disc)
	if len(b) < n+2 || b[0] != '"' || string(b[1:1+n]) != disc || b[1+n] != '"' {
		return nil, false
	}
	b = bytes.TrimLeft(b[n+2:], ws)
	if len(b) == 0 || b[0] != ':' {
		return nil, false
	}
	b = bytes.TrimLeft(b[1:], ws)
	if len(b) == 0 || b[0] != '"' {
		return nil, false
	}
	end := bytes.IndexByte(b[1:], '"')
	if end < 0 {
		return nil, false
	}
	tag := b[1 : 1+end]
	if bytes.IndexByte(tag, '\\') >= 0 {
		return nil, false
	}
	return tag, true
}

// variantIn returns the variant obj's discriminator selects, or nil when it
// is missing, not a string, or an unknown tag. obj was already validated by
// ReadValue, so the reads here cannot fail.
func (f *fastDecoder) variantIn(c *codec, obj jsontext.Value) *codec {
	f.body.Reset()
	f.body.Write(obj)
	f.data = f.body.Bytes()
	f.dec.Reset(&f.body)
	f.dec.ReadToken() // '{'
	for f.dec.PeekKind() == '"' {
		raw, _ := f.dec.ReadValue()
		if string(f.unquote(raw)) != c.disc {
			f.dec.SkipValue()
			continue
		}
		if raw, _ = f.dec.ReadValue(); raw.Kind() != '"' {
			return nil
		}
		return c.variant(f.unquote(raw))
	}
	return nil
}

// variant returns the codec of the variant tag selects, or nil.
func (c *codec) variant(tag []byte) *codec {
	for i := range c.variants {
		if c.variants[i].tag == string(tag) {
			return c.variants[i].c
		}
	}
	return nil
}

// unique reports whether the array elements at spans are pairwise distinct
// in canonical form, as d.array compares them. Each span was already
// accepted, so parseJSON cannot fail on it.
func (f *fastDecoder) unique(spans []int64) bool {
	seen := make(map[string]bool, len(spans)/2)
	for i := 0; i < len(spans); i += 2 {
		// A span starts after the previous token: past whitespace and a comma.
		raw := bytes.TrimLeft(f.data[spans[i]:spans[i+1]], " \t\r\n,")
		tree, _ := parseJSON(raw, f.limits.MaxDepth)
		k := canonical(tree)
		if seen[k] {
			return false
		}
		seen[k] = true
	}
	return true
}

// fieldIndex finds the field a member name denotes, or -1.
func (c *codec) fieldIndex(name []byte) int {
	if c.byName != nil {
		if i, ok := c.byName[string(name)]; ok {
			return i
		}
		return -1
	}
	for i := range c.fields {
		if c.fields[i].json == string(name) {
			return i
		}
	}
	return -1
}

// strOK reports whether d.str would pass b.
func strOK[T string | []byte](s *schema, limits Limits, b T) bool {
	var n int
	switch v := any(b).(type) {
	case string:
		n = utf8.RuneCountInString(v)
	case []byte:
		n = utf8.RuneCount(v)
	}
	switch {
	case s.MaxLength != nil && n > *s.MaxLength:
		return false
	case s.MaxLength == nil && n > limits.MaxStringLength:
		return false
	case s.MinLength != nil && n < *s.MinLength:
		return false
	}
	if len(s.Enum) > 0 {
		found := false
		for _, e := range s.Enum {
			if e == string(b) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if s.check != nil && !s.check(string(b)) {
		return false
	}
	if s.re != nil {
		if n > patternCeiling {
			return false
		}
		switch v := any(b).(type) {
		case string:
			if !s.re.MatchString(v) {
				return false
			}
		case []byte:
			if !s.re.Match(v) {
				return false
			}
		}
	}
	for _, p := range s.implied {
		if !p.holds(string(b)) {
			return false
		}
	}
	return true
}

// numOK reports whether d.num would pass the literal lit, whose value is f.
func numOK[T string | []byte](s *schema, lit T, f float64) bool {
	return (s.Minimum == nil || cmpNum(lit, f, *s.Minimum) >= 0) &&
		(s.Maximum == nil || cmpNum(lit, f, *s.Maximum) <= 0) &&
		(s.ExclusiveMinimum == nil || cmpNum(lit, f, *s.ExclusiveMinimum) > 0) &&
		(s.ExclusiveMaximum == nil || cmpNum(lit, f, *s.ExclusiveMaximum) < 0) &&
		(s.MultipleOf == nil || isMultiple(lit, *s.MultipleOf)) &&
		(len(s.numEnum) == 0 || s.inNumEnum(string(lit)))
}

// fastEligible reports whether every field under c is settable by
// reflection. An unexported embedded struct with a json name is a member v2
// can set but reflection cannot; the single pass would panic on it.
func fastEligible(c *codec, seen map[*codec]bool) bool {
	if seen[c] {
		return true
	}
	seen[c] = true
	switch c.kind {
	case kOneOf:
		for _, vc := range c.variants {
			if !fastEligible(vc.c, seen) {
				return false
			}
		}
	case kStruct:
		zero := reflect.New(c.t).Elem()
		for _, fc := range c.fields {
			if !zero.FieldByIndex(fc.index).CanSet() || !fastEligible(fc.c, seen) {
				return false
			}
		}
	case kSlice, kMap, kNull:
		return fastEligible(c.elem, seen)
	}
	return true
}
