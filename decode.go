package geta

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
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
	// MaxItems caps an array with no maxItems, and the member count of a map
	// or WithSchema object with no maxProperties.
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
	// written is set when checking a response (Conforms); the pattern
	// ceiling does not apply.
	written bool
}

// unbounded are the limits a response is checked under (Conforms).
var unbounded = Limits{MaxStringLength: math.MaxInt, MaxItems: math.MaxInt, MaxDepth: math.MaxInt}

func (d *decoder) fail(path, format string, args ...any) {
	if len(d.errs) < maxViolations {
		d.errs = append(d.errs, Violation{In: d.in, Path: path, Message: fmt.Sprintf(format, args...)})
	}
}

// check validates the parsed value v against s and c: type, required
// members, nulls, unknown members, and every schema keyword, including what
// v2 would silently accept (a missing member, an unwanted null). s is the
// schema at this use site; a reference resolves to c's own. The recursion
// depth is bounded by v's nesting, which its parser bounded.
func (d *decoder) check(c *codec, s *schema, v any, path string) {
	if s.Ref != "" {
		s = c.schema
	}
	if c.kind == kOneOf {
		d.checkOneOf(c, v, path)
		return
	}
	if c.kind == kNull {
		if v != nil {
			d.check(c.elem, s, v, path)
		}
		return
	}
	if c.kind == kJSON && s.Type == "" {
		return // any JSON value: the type's own UnmarshalJSON decides (decodeJSON)
	}
	want := s.Type
	got := jsonType(v)
	if got != want && !(want == "number" && got == "integer") {
		d.fail(path, "expected %s, got %s", want, got)
		return
	}
	switch c.kind {
	case kString:
		d.str(s, v.(string), path)
	case kText:
		str := v.(string)
		if !d.str(s, str, path) {
			return
		}
		if err := unmarshalText(reflect.New(c.t).Interface(), []byte(str)); err != nil {
			what := s.Format
			if what == "" {
				what = c.t.Name()
			}
			d.fail(path, "%q is not a valid %s", str, what)
		}
	case kBytes:
		str := v.(string)
		if !d.str(s, str, path) {
			return
		}
		if _, err := base64.StdEncoding.DecodeString(str); err != nil {
			d.fail(path, "%q is not valid base64", str)
		}
	case kInt:
		lit := string(v.(number))
		n, err := strconv.ParseInt(lit, 10, c.t.Bits())
		if err != nil {
			d.fail(path, "%s is out of range for %s", lit, c.t.Kind())
			return
		}
		d.num(s, float64(n), lit, path)
	case kUint:
		lit := string(v.(number))
		n, err := strconv.ParseUint(lit, 10, c.t.Bits())
		if err != nil {
			d.fail(path, "%s is out of range for %s", lit, c.t.Kind())
			return
		}
		d.num(s, float64(n), lit, path)
	case kFloat:
		lit := string(v.(number))
		_, f, err := parseFloat(lit, c.t.Bits())
		if err != nil {
			d.fail(path, "%s is out of range for %s", lit, c.t.Kind())
			return
		}
		d.num(s, f, lit, path)
	case kJSON:
		// The declared keywords; UnmarshalJSON judges the rest (decodeJSON).
		switch s.Type {
		case "string":
			d.str(s, v.(string), path)
		case "integer", "number":
			lit := string(v.(number))
			f, err := strconv.ParseFloat(lit, 64)
			if err != nil {
				d.fail(path, "%s is out of range", lit)
				return
			}
			d.num(s, f, lit, path)
		case "array":
			d.array(s, v.([]any), path)
		case "object":
			// Member count and names are checked as a map's are.
			m := v.(map[string]any)
			if d.objectLen(s, len(m), path) {
				for _, k := range slices.Sorted(maps.Keys(m)) {
					d.key(s, k, path+"."+k)
				}
			}
		}
	case kSlice:
		a := v.([]any)
		if !d.array(s, a, path) {
			return
		}
		for i, el := range a {
			d.check(c.elem, s.Items, el, fmt.Sprintf("%s[%d]", path, i))
		}
	case kMap:
		m := v.(map[string]any)
		if !d.objectLen(s, len(m), path) {
			return
		}
		// A value is checked only if its key passes.
		for _, k := range slices.Sorted(maps.Keys(m)) {
			if d.key(s, k, path+"."+k) && d.keyText(c.key, k, path+"."+k) {
				d.check(c.elem, s.Additional, m[k], path+"."+k)
			}
		}
	case kStruct:
		m := v.(map[string]any)
		known := make(map[string]bool, len(c.fields))
		for _, f := range c.fields {
			known[f.json] = true
			fv, ok := m[f.json]
			fpath := path + "." + f.json
			if !ok {
				// A request may omit a member with a default (applyDefaults);
				// a response may not.
				if !f.optional && (f.use.Default == nil || d.written) {
					d.fail(fpath, "missing required member")
				}
				continue
			}
			d.check(f.c, f.use, fv, fpath)
		}
		var unknown []string
		for k := range m {
			if !known[k] {
				unknown = append(unknown, k)
			}
		}
		slices.Sort(unknown)
		for _, k := range unknown {
			d.fail(path+"."+k, "unknown member")
		}
	}
}

// decodeJSON decodes data, which passed check, into dst. A failure is
// reported as a violation at its JSON Pointer.
func (d *decoder) decodeJSON(data []byte, dst reflect.Value, opts json.Options) {
	err := json.Unmarshal(data, dst.Addr().Interface(), opts)
	if err == nil {
		return
	}
	path := "$"
	if se, ok := errors.AsType[*json.SemanticError](err); ok {
		path = pointerPath(se.JSONPointer)
	} else if se, ok := errors.AsType[*jsontext.SyntacticError](err); ok {
		path = pointerPath(se.JSONPointer)
	}
	d.fail(path, "%v", err)
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
			has := false
			switch c.kind {
			case kStruct:
				has = slices.ContainsFunc(c.fields, func(f fieldCodec) bool { return f.use.Default != nil || under[f.c] })
			case kSlice, kMap, kNull:
				has = under[c.elem]
			case kOneOf:
				has = slices.ContainsFunc(c.variants, func(v variantCodec) bool { return under[v.c] })
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
func (d *decoder) str(s *schema, v, path string) bool { return d.text(s, v, path, "string", "") }

// key checks a map's or WithSchema object's member name k against the keys'
// schema (schema.keys). It reports whether the key passed.
func (d *decoder) key(s *schema, k, path string) bool {
	return d.text(s.keys(), k, path, "key", "key ")
}

// keyText checks a map key k of a text type (codec kc) with the type's
// UnmarshalText, as check does for a value. It reports whether the key
// passed.
func (d *decoder) keyText(kc *codec, k, path string) bool {
	if kc == nil || kc.kind != kText {
		return true
	}
	if err := unmarshalText(reflect.New(kc.t).Interface(), []byte(k)); err != nil {
		what := kc.schema.Format
		if what == "" {
			what = kc.t.Name()
		}
		d.fail(path, "key %q is not a valid %s", k, what)
		return false
	}
	return true
}

// text implements str and key. what names the thing in length messages, and
// lead prefixes the quoted value.
func (d *decoder) text(s *schema, v, path, what, lead string) bool {
	// A body's strings are valid UTF-8 already; parameter text may not be.
	if !utf8.ValidString(v) {
		d.fail(path, "the text is not valid UTF-8")
		return false
	}
	n := utf8.RuneCountInString(v)
	switch {
	case s.MaxLength != nil && n > *s.MaxLength:
		d.fail(path, "%s length %d exceeds maxLength %d", what, n, *s.MaxLength)
		return false
	case s.MaxLength == nil && n > d.limits.MaxStringLength:
		d.fail(path, "%s length %d exceeds the ceiling of %d code points", what, n, d.limits.MaxStringLength)
		return false
	case s.MinLength != nil && n < *s.MinLength:
		d.fail(path, "%s length %d is shorter than minLength %d", what, n, *s.MinLength)
		return false
	}
	if len(s.Enum) > 0 && !slices.Contains(s.Enum, v) {
		d.fail(path, "%s%q is not one of %s", lead, v, strings.Join(s.Enum, ", "))
		return false
	}
	if !s.validFormat(v) {
		d.fail(path, "%s%q is not a valid %s", lead, v, s.Format)
		return false
	}
	if s.re != nil {
		if n > patternCeiling && !d.written {
			d.fail(path, "%s length %d exceeds the pattern-validation ceiling of %d code points", what, n, patternCeiling)
			return false
		}
		if !s.re.MatchString(v) {
			d.fail(path, "%s%q does not match pattern %s", lead, v, s.Pattern)
			return false
		}
	}
	for _, p := range s.implied {
		if !p.holds(v) {
			d.fail(path, "%s%q %s", lead, v, p.why)
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
func (d *decoder) num(s *schema, f float64, lit, path string) bool {
	ok := true
	if s.Minimum != nil && cmpNum(lit, f, *s.Minimum) < 0 {
		d.fail(path, "%s is less than minimum %s", lit, fmtNum(*s.Minimum))
		ok = false
	}
	if s.Maximum != nil && cmpNum(lit, f, *s.Maximum) > 0 {
		d.fail(path, "%s is greater than maximum %s", lit, fmtNum(*s.Maximum))
		ok = false
	}
	if s.ExclusiveMinimum != nil && cmpNum(lit, f, *s.ExclusiveMinimum) <= 0 {
		d.fail(path, "%s is not greater than exclusiveMinimum %s", lit, fmtNum(*s.ExclusiveMinimum))
		ok = false
	}
	if s.ExclusiveMaximum != nil && cmpNum(lit, f, *s.ExclusiveMaximum) >= 0 {
		d.fail(path, "%s is not less than exclusiveMaximum %s", lit, fmtNum(*s.ExclusiveMaximum))
		ok = false
	}
	if s.MultipleOf != nil && !isMultiple(lit, *s.MultipleOf) {
		d.fail(path, "%s is not a multiple of %s", lit, fmtNum(*s.MultipleOf))
		ok = false
	}
	if len(s.numEnum) > 0 && !s.inNumEnum(lit) {
		d.fail(path, "%s is not one of %s", lit, strings.Join(s.Enum, ", "))
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
func (d *decoder) array(s *schema, a []any, path string) bool {
	if !d.arrayLen(s, len(a), path) {
		return false
	}
	if s.UniqueItems {
		seen := make(map[string]int, len(a))
		for i, el := range a {
			k := canonical(el)
			if j, dup := seen[k]; dup {
				d.fail(path, "array items at [%d] and [%d] are equal (uniqueItems)", j, i)
				break
			}
			seen[k] = i
		}
	}
	return true
}

// arrayLen checks the length keywords of an array of n items.
func (d *decoder) arrayLen(s *schema, n int, path string) bool {
	switch {
	case s.MaxItems != nil && n > *s.MaxItems:
		d.fail(path, "array length %d exceeds maxItems %d", n, *s.MaxItems)
		return false
	case s.MaxItems == nil && n > d.limits.MaxItems:
		d.fail(path, "array length %d exceeds the ceiling of %d items", n, d.limits.MaxItems)
		return false
	}
	if s.MinItems != nil && n < *s.MinItems {
		d.fail(path, "array length %d is shorter than minItems %d", n, *s.MinItems)
	}
	return true
}

// objectLen checks the member-count keywords of a map or WithSchema object
// of n members, with MaxItems as the backstop. It reports false when the
// object is past its upper bound.
func (d *decoder) objectLen(s *schema, n int, path string) bool {
	switch {
	case s.MaxProperties != nil && n > *s.MaxProperties:
		d.fail(path, "object has %d members, more than maxProperties %d", n, *s.MaxProperties)
		return false
	case s.MaxProperties == nil && n > d.limits.MaxItems:
		d.fail(path, "object has %d members, over the ceiling of %d", n, d.limits.MaxItems)
		return false
	}
	if s.MinProperties != nil && n < *s.MinProperties {
		d.fail(path, "object has %d members, fewer than minProperties %d", n, *s.MinProperties)
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
