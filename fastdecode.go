package geta

import (
	"bytes"
	"encoding/binary"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"hash/maphash"
	"io"
	"math"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"unicode/utf8"
)

// A fastDecoder validates and decodes a JSON body in one pass over its
// tokens, checking each value against the schema. It sets a string, bool,
// integer, or float leaf itself where v2 would read it by its kind alone
// (codec.direct), and hands every other leaf to encoding/json/v2.
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
	// interned are the strings this request has set (intern), and touched
	// the slots they fill, which release clears.
	interned [256][2]string
	touched  []uint8
	ahead    lookahead // finds sealed values' discriminators in this body

	// hashing counts the uniqueItems arrays whose elements are being read.
	// While it is positive, each value read leaves its valueHash in last,
	// made from the hashes of the values inside it, so that unique compares
	// elements without reading any of them again.
	hashing int
	last    uint64
}

var fastDecoders = sync.Pool{New: func() any { return new(fastDecoder) }}

// maxPooledBody bounds the buffer a pooled decoder or body writer keeps
// between requests. It equals DefaultLimits.MaxResponseBuffer.
const maxPooledBody = 64 << 10

// maxPooledSpans bounds the spans a pooled decoder keeps between requests.
const maxPooledSpans = 4 << 10

// getFastDecoder returns a pooled decoder for limits. The caller sets opts.
func getFastDecoder(limits Limits) *fastDecoder {
	f := fastDecoders.Get().(*fastDecoder)
	f.limits = limits
	return f
}

// release returns f to the pool, keeping no reference to the request: the
// strings it interned are cleared too.
func (f *fastDecoder) release() {
	if f.body.Cap() > maxPooledBody || cap(f.scratch) > maxPooledBody || cap(f.ahead.spans) > maxPooledSpans {
		return
	}
	f.body.Reset()
	f.data = nil
	f.src.Reset(nil)
	f.dec.Reset(&f.body) // drops the decoder's view of the old body
	f.scratch = f.scratch[:0]
	f.hashing, f.last = 0, 0
	f.opts = nil
	for _, i := range f.touched {
		f.interned[i] = [2]string{}
	}
	f.touched = f.touched[:0]
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
	f.ahead.reset()
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
			if f.hashing > 0 {
				f.last = hashBytes(tagNull, nil)
			}
			return true
		}
		return f.value(c.elem, s, n.slot())
	}
	// A leaf: check its raw JSON against the schema, then set it (codec.direct)
	// or let v2 decode it.
	if f.dec.PeekKind() == 'n' && !anyJSON(c, s) {
		return false
	}
	raw, err := f.dec.ReadValue()
	if err != nil {
		return false
	}
	switch c.kind {
	case kString, kText, kBytes:
		if raw.Kind() != '"' {
			return false
		}
		// The decoder has already refused invalid UTF-8 and escapes, as v2's
		// would.
		b := f.unquote(raw)
		if !strOK(s, f.limits, b) {
			return false
		}
		if c.direct {
			if f.hashing > 0 {
				f.last = hashBytes(tagString, b)
			}
			dst.SetString(f.intern(b))
			return true
		}
	case kBool:
		k := raw.Kind()
		if k != 't' && k != 'f' {
			return false
		}
		if c.direct {
			if f.hashing > 0 {
				f.last = f.leafHash(raw)
			}
			dst.SetBool(k == 't')
			return true
		}
	case kInt, kUint:
		if raw.Kind() != '0' || !isIntegerLiteral(string(raw)) {
			return false
		}
		n, err := strconv.ParseFloat(string(raw), 64)
		if err != nil || !numOK(s, []byte(raw), n) {
			return false
		}
		if c.direct {
			if f.hashing > 0 {
				f.last = hashNumber(string(raw))
			}
			return setInteger(c, raw, dst)
		}
	case kFloat:
		if raw.Kind() != '0' {
			return false
		}
		// v2 refuses a literal out of the type's range (1e400, or 1e39 for a
		// float32), as parseFloat does, and otherwise sets v.
		v, n, err := parseFloat(string(raw), c.t.Bits())
		if err != nil || !numOK(s, []byte(raw), n) {
			return false
		}
		if c.direct {
			if f.hashing > 0 {
				f.last = hashNumber(string(raw))
			}
			dst.SetFloat(v)
			return true
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
			// Any JSON value: the type's UnmarshalJSON decides, within
			// MaxDepth as parseJSON holds it.
			if k := raw.Kind(); (k == '[' || k == '{') && f.dec.StackDepth()+nesting(raw) > f.limits.MaxDepth {
				return false
			}
		default:
			return false
		}
	default:
		// A leaf kind without a case here falls back to the reference
		// path rather than skip the schema check.
		return false
	}
	if f.hashing > 0 {
		f.last = f.leafHash(raw)
	}
	// The reference path's options reject unknown members and decode sealed
	// types inside JSON-method types.
	return json.Unmarshal(raw, dst.Addr().Interface(), f.opts) == nil
}

// leafHash returns the valueHash of raw, a leaf's JSON.
func (f *fastDecoder) leafHash(raw jsontext.Value) uint64 {
	switch raw.Kind() {
	case 'n':
		return hashBytes(tagNull, nil)
	case 't':
		return hashBytes(tagTrue, nil)
	case 'f':
		return hashBytes(tagFalse, nil)
	case '"':
		return hashBytes(tagString, f.unquote(raw))
	case '0':
		return hashNumber(string(raw))
	}
	// Any JSON value, which a JSON-method type reads. Each leaf is hashed
	// once, whatever number of uniqueItems arrays it is nested in.
	tree, _ := parseJSON(raw, math.MaxInt)
	return valueHash(tree, nil)
}

// nesting returns the deepest nesting of arrays and objects in raw, a valid
// JSON value.
func nesting(raw []byte) int {
	depth, deepest := 0, 0
	inString := false
	for i := 0; i < len(raw); i++ {
		switch c := raw[i]; {
		case inString:
			if c == '\\' {
				i++ // the escaped byte
			} else if c == '"' {
				inString = false
			}
		case c == '"':
			inString = true
		case c == '[' || c == '{':
			depth++
			deepest = max(deepest, depth)
		case c == ']' || c == '}':
			depth--
		}
	}
	return deepest
}

// setInteger sets dst, of c's integer kind, to raw, a JSON integer, and
// reports false where v2 refuses raw: out of the type's range, or negative
// (-0 too) for an unsigned type.
func setInteger(c *codec, raw jsontext.Value, dst reflect.Value) bool {
	if c.kind == kInt {
		n, err := strconv.ParseInt(string(raw), 10, c.t.Bits())
		if err != nil {
			return false
		}
		dst.SetInt(n)
		return true
	}
	n, err := strconv.ParseUint(string(raw), 10, c.t.Bits())
	if err != nil {
		return false
	}
	dst.SetUint(n)
	return true
}

// intern returns b as a string. Like v2 (makeString in encoding/json/v2), it
// keeps the strings it made, up to 256 bytes each, in a small table, so that
// a string that recurs in the body costs one allocation. Each slot holds the
// two strings last used there, so two strings that share a slot do not evict
// each other in turn. release clears the table, so that a pooled decoder
// keeps no request's strings; a string every request sends is then made
// once per request, not once per decoder, an allocation accepted for that.
func (f *fastDecoder) intern(b []byte) string {
	if len(b) < 2 || len(b) > 256 {
		return string(b) // the runtime interns single bytes
	}
	i := uint8(internHash(b))
	slot := &f.interned[i]
	if string(b) == slot[0] {
		return slot[0]
	}
	if string(b) == slot[1] {
		slot[0], slot[1] = slot[1], slot[0]
		return slot[0]
	}
	if slot[0] == "" {
		f.touched = append(f.touched, i)
	}
	s := string(b)
	slot[0], slot[1] = s, slot[0]
	return s
}

// internHash hashes the length of b and up to 8 bytes at each end, so that
// it takes constant time.
func internHash(b []byte) uint64 {
	var lo, hi uint64
	if len(b) >= 8 {
		lo, hi = binary.LittleEndian.Uint64(b), binary.LittleEndian.Uint64(b[len(b)-8:])
	} else {
		for i, c := range b {
			lo |= uint64(c) << (8 * i)
		}
	}
	h := lo*0x9e3779b97f4a7c15 ^ hi*0xc2b2ae3d27d4eb4f ^ uint64(len(b))
	h ^= h >> 31
	h *= 0xbf58476d1ce4e5b9
	return h ^ h>>29
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
	return f.dec.StackDepth() <= f.limits.MaxDepth
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
	var sum uint64 // of memberHash, while hashing
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
				if f.hashing > 0 {
					sum += memberHash(hashString(tagString, fc.json), hashBytes(tagNull, nil))
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
		if f.hashing > 0 {
			sum += memberHash(hashString(tagString, fc.json), f.last)
		}
	}
	f.close()
	if f.hashing > 0 {
		f.last = objectHash(sum) // the members sent; defaults are not
	}
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
	var sum uint64 // of memberHash, while hashing
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
		var nameHash uint64
		if f.hashing > 0 {
			nameHash = hashBytes(tagString, name)
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
		if f.hashing > 0 {
			sum += memberHash(nameHash, f.last)
		}
		dst.SetMapIndex(k, v)
	}
	f.close()
	if f.hashing > 0 {
		f.last = objectHash(sum)
	}
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
	var spans []int64   // with uniqueItems: where each element starts and ends
	var hashes []uint64 // with uniqueItems: each element's valueHash
	hashing := f.hashing > 0 || s.UniqueItems
	var h maphash.Hash // the array's valueHash, while hashing
	if hashing {
		startArray(&h)
	}
	if s.UniqueItems {
		f.hashing++
		defer func() { f.hashing-- }()
	}
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
		if hashing {
			writeWord(&h, f.last)
		}
		if s.UniqueItems {
			spans = append(spans, f.dec.InputOffset())
			hashes = append(hashes, f.last)
		}
		n++
	}
	f.close()
	if s.MinItems != nil && n < *s.MinItems {
		return false
	}
	if s.UniqueItems && !f.unique(spans, hashes) {
		return false
	}
	if hashing {
		f.last = h.Sum64()
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
	off := int(f.dec.InputOffset())
	// The members' values may nest as deep as MaxDepth allows inside the
	// object; past that, the look-ahead stops and the reference path names
	// the nesting.
	depth := f.limits.MaxDepth - f.dec.StackDepth() - 1
	tag, ok := f.ahead.tag(f.data, 0, off, c.disc, depth)
	if !ok {
		return false
	}
	vc := c.variant(tag)
	if vc == nil {
		return false
	}
	v := reflect.New(vc.t).Elem()
	if !f.object(vc, v) { // leaves the variant's valueHash in last, while hashing
		return false
	}
	dst.Set(v)
	return true
}

// A lookahead scans JSON ahead of a decoder, without reading it, to find a
// sealed value's discriminator wherever it is among the members. It records
// the objects and arrays it steps over by their offsets in the input, and
// jumps over one it has recorded, so that the look-ahead of sealed values
// nested in one another, each with its discriminator last, steps over the
// input a bounded number of times rather than once per level.
//
// It records an object or array only where at least minSpan of its bytes lie
// outside the ones it records inside it, so that spans holds at most one
// entry per minSpan bytes of the input. One it does not record is stepped
// over again by each look-ahead that reaches it, at a cost below minSpan, and
// a look-ahead reaches it only from a sealed object it does not record
// either: one whose bytes are fewer than minSpan.
//
// A look-ahead records anew only past the input every look-ahead before it
// stepped over: before that point it jumps over what was recorded and
// leaves out again what was left out, as its bytes are the same. So spans
// stays in order of offset.
type lookahead struct {
	spans   []span
	opened  []opening // the spans still open in skipValue
	scratch []byte    // names and tags that contain escapes
	// scanned counts the bytes stepped over, for tests.
	scanned int
}

// minSpan is the fewest bytes, outside the spans recorded in it, of an
// object or array a lookahead records.
const minSpan = 32

// A span is an object or array of the input: the offsets of its opening and
// closing brackets, end < 0 while it is open.
type span struct{ start, end int }

// An opening is a span open in skipValue: its index in spans, and the bytes
// in it that the spans recorded inside it cover.
type opening struct{ at, covered int }

func (l *lookahead) reset() { l.spans, l.scanned = l.spans[:0], 0 }

// tag returns, unquoted, the string value of member disc of the object that
// begins in b at or after index i (past a separator and whitespace), or
// false when it is missing or not a string, or when a member's value nests
// more than depth arrays and objects deep. b[0] is at offset base of the
// input. The bytes it scans need not be validated: on valid JSON the tag is
// the one a read of the object finds, and the caller reads the object in
// full. The result is valid until the next call.
func (l *lookahead) tag(b []byte, base, i int, disc string, depth int) ([]byte, bool) {
	i += bytes.IndexByte(b[i:], '{') + 1
	for {
		from := i
		i = skipSpace(b, i)
		if i == len(b) || b[i] != '"' {
			return nil, false // the end of the object, or not JSON
		}
		end := stringEnd(b, i)
		if end < 0 {
			return nil, false
		}
		name := b[i:end]
		i = skipSpace(b, end)
		if i == len(b) || b[i] != ':' {
			return nil, false
		}
		i = skipSpace(b, i+1)
		l.scanned += i - from
		if q, ok := l.unquote(name); ok && string(q) == disc {
			if i == len(b) || b[i] != '"' {
				return nil, false
			}
			end := stringEnd(b, i)
			if end < 0 {
				return nil, false
			}
			return l.unquote(b[i:end])
		}
		if i = l.skipValue(b, base, i, depth); i == len(b) || b[i] != ',' {
			return nil, false
		}
		i++
	}
}

// unquote is fastDecoder.unquote for a string not yet validated.
func (l *lookahead) unquote(raw []byte) ([]byte, bool) {
	if bytes.IndexByte(raw, '\\') < 0 {
		return raw[1 : len(raw)-1], true
	}
	var err error
	l.scratch, err = jsontext.AppendUnquote(l.scratch[:0], raw)
	return l.scratch, err == nil
}

// skipSpace returns the index of the first byte of b at or after i that is
// not JSON whitespace.
func skipSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\r' || b[i] == '\n') {
		i++
	}
	return i
}

// stringEnd returns the index just past the string that begins at b[i], a
// '"', or -1 when it does not end.
func stringEnd(b []byte, i int) int {
	j := i + 1
	for {
		k := bytes.IndexByte(b[j:], '"')
		if k < 0 {
			return -1
		}
		j += k
		// The quote ends the string unless an odd number of backslashes
		// precede it; b[i] stops the count.
		n := 0
		for b[j-1-n] == '\\' {
			n++
		}
		j++
		if n%2 == 0 {
			return j
		}
	}
}

// skipValue returns the index of the ',', '}', or ']' that ends the value
// beginning in b at or after index i, or len(b) when the value nests more
// than depth arrays and objects deep or does not end; b[0] is at offset base
// of the input. It records the objects and arrays it passes (lookahead) and
// jumps over one already recorded. It is exact on valid JSON and bounded on
// anything else.
func (l *lookahead) skipValue(b []byte, base, i, depth int) int {
	open := l.opened[:0]
	from := i
	recorded := len(l.spans) // the spans this call may jump over
	defer func() {
		if len(open) > 0 {
			l.spans = l.spans[:open[0].at] // none is left open
		}
		l.opened = open[:0]
	}()
	for i < len(b) {
		switch b[i] {
		case '"':
			e := stringEnd(b, i)
			if e < 0 {
				l.scanned += len(b) - from
				return len(b)
			}
			i = e
			continue
		case '{', '[':
			if end, ok := l.spanAt(base+i, recorded); ok {
				if n := len(open); n > 0 {
					open[n-1].covered += end - (base + i) + 1
				}
				l.scanned += i - from
				i = end - base + 1
				from = i
				continue
			}
			if len(open) >= depth {
				l.scanned += i - from
				return len(b)
			}
			l.spans = append(l.spans, span{base + i, -1})
			open = append(open, opening{at: len(l.spans) - 1})
		case '}', ']':
			if len(open) == 0 {
				l.scanned += i - from
				return i
			}
			o := open[len(open)-1]
			open = open[:len(open)-1]
			l.spans[o.at].end = base + i
			size := base + i - l.spans[o.at].start + 1
			covered := size
			if size-o.covered < minSpan {
				// Left out: the spans recorded inside it now count toward
				// the span around it.
				l.spans = slices.Delete(l.spans, o.at, o.at+1)
				covered = o.covered
			}
			if n := len(open); n > 0 {
				open[n-1].covered += covered
			}
		case ',':
			if len(open) == 0 {
				l.scanned += i - from
				return i
			}
		}
		i++
	}
	l.scanned += i - from
	return i
}

// spanAt returns the offset of the bracket that closes the object or array
// recorded, among the first n spans, as opening at offset off.
func (l *lookahead) spanAt(off, n int) (int, bool) {
	lo, hi := 0, n
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		if l.spans[m].start < off {
			lo = m + 1
		} else {
			hi = m
		}
	}
	if lo < n && l.spans[lo].start == off && l.spans[lo].end >= 0 {
		return l.spans[lo].end, true
	}
	return 0, false
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

// unique reports whether the array elements at spans, whose valueHash values
// are hashes, are pairwise distinct in canonical form, as d.array compares
// them. Only elements whose hashes match are read again, so an element is
// not read once for each uniqueItems array it is nested in.
func (f *fastDecoder) unique(spans []int64, hashes []uint64) bool {
	seen := make(map[uint64]int, len(hashes))
	for i, h := range hashes {
		j, ok := seen[h]
		if !ok {
			seen[h] = i
			continue
		}
		if f.canonicalAt(spans, j) == f.canonicalAt(spans, i) {
			return false
		}
		return f.uniqueExact(spans) // two values share a hash
	}
	return true
}

// uniqueExact is unique by canonical form alone.
func (f *fastDecoder) uniqueExact(spans []int64) bool {
	seen := make(map[string]bool, len(spans)/2)
	for i := range len(spans) / 2 {
		k := f.canonicalAt(spans, i)
		if seen[k] {
			return false
		}
		seen[k] = true
	}
	return true
}

// canonicalAt renders element i of spans in canonical form. Each span was
// already accepted, so parseJSON cannot fail on it.
func (f *fastDecoder) canonicalAt(spans []int64, i int) string {
	// A span starts after the previous token: past whitespace and a comma.
	raw := bytes.TrimLeft(f.data[spans[2*i]:spans[2*i+1]], " \t\r\n,")
	tree, _ := parseJSON(raw, f.limits.MaxDepth)
	return canonical(tree)
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
