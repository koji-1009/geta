package geta

import (
	"cmp"
	"encoding/json/v2"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// schema is the JSON Schema subset geta derives from Go types and enforces.
// Invariant: everything it can write into the document, geta enforces.
type schema struct {
	Ref    string // component name; when set, nothing else is
	Type   string // string, integer, number, boolean, array, object
	Format string
	// check validates Format where geta checks it (formatChecks); nil for a
	// FormatType, whose UnmarshalText does the checking.
	check func(string) bool

	Properties []property // object, in declaration order
	Required   []string
	Additional *schema // object with arbitrary keys (map)
	Closed     bool    // additionalProperties: false
	// Keys is the schema of member names when anyMembers: from a map key
	// type with a format, or from propertyNames. keywords. nil means a plain
	// string (see keys).
	Keys *schema
	// MinProperties and MaxProperties bound the member count of a map or
	// WithSchema object.
	MinProperties, MaxProperties *int

	Items *schema
	// unjudged marks an array or map whose element schema is unknown where
	// getavet judges the tag; items. and additionalProperties. keywords are
	// left to geta.New.
	unjudged bool

	// Enum lists the members as written. For numbers, numEnum holds them
	// exactly, and membership is by value (1.50 equals 1.5).
	Enum    []string
	numEnum []decimal

	MinLength, MaxLength *int
	Pattern              string
	re                   *regexp.Regexp
	// implied are patterns geta adds itself, from the type (time.Time: no
	// leap second) or a parameter's location. Clones share the slice, so
	// replace it; never append in place.
	implied []impliedPattern

	Minimum, Maximum                   *float64
	ExclusiveMinimum, ExclusiveMaximum *float64
	MultipleOf                         *float64
	goInt                              reflect.Type // the Go integer type, which bounds the range

	MinItems, MaxItems *int
	UniqueItems        bool

	ContentEncoding string // base64 for []byte
	// ContentMediaType is set for a file (fileSchema). binary marks a file,
	// which takes no keyword.
	ContentMediaType string
	binary           bool

	// Null admits null (a Nullable).
	Null bool

	// Default is the text geta binds when a request omits a non-pointer
	// value. Examples are admitted values. Deprecated marks the value as one
	// to stop using.
	Default    *string
	Examples   []string
	Deprecated bool

	// A sealed type.
	OneOf         []*schema
	Discriminator string
	Mapping       map[string]string // tag → component reference
}

type property struct {
	Name   string
	Schema *schema
	Doc    string // the member's description (its doc tag)
}

// An impliedPattern is a pattern geta adds to a schema itself. holds accepts
// exactly the strings pattern matches; why describes a refused string.
type impliedPattern struct {
	pattern string
	holds   func(string) bool
	why     string
}

func (s *schema) clone() *schema {
	c := *s
	return &c
}

// constraintTypes maps each schema tag keyword to the types it applies to.
var constraintTypes = map[string][]string{
	"minLength":        {"string"},
	"maxLength":        {"string"},
	"pattern":          {"string"},
	"format":           {"string"},
	"enum":             {"string", "integer", "number"},
	"minimum":          {"integer", "number"},
	"maximum":          {"integer", "number"},
	"exclusiveMinimum": {"integer", "number"},
	"exclusiveMaximum": {"integer", "number"},
	"multipleOf":       {"integer", "number"},
	"minItems":         {"array"},
	"maxItems":         {"array"},
	"uniqueItems":      {"array"},
	// Maps only; parseEntries refuses them on a struct.
	"minProperties": {"object"},
	"maxProperties": {"object"},
	// Annotations. Any value, a component reference included, may be
	// deprecated.
	"default":    {"string", "integer", "number", "boolean"},
	"examples":   {"string", "integer", "number", "boolean"},
	"deprecated": nil,
}

// keyPrefix matches the start of a tag entry: a keyword, optionally behind
// any sequence of items., additionalProperties., and propertyNames.
var keyPrefix = regexp.MustCompile(`^(?:(?:items|additionalProperties|propertyNames)\.)*[A-Za-z]+=`)

// itemsPrefix applies a keyword to an array's elements: items.enum=a|b.
const itemsPrefix = "items."

// valuesPrefix applies a keyword to a map's values:
// additionalProperties.maxLength=3.
const valuesPrefix = "additionalProperties."

// keysPrefix applies a keyword to a map's keys: propertyNames.maxLength=63.
// Keys are strings on the wire, so only string keywords apply.
const keysPrefix = "propertyNames."

// plainKey is the schema of a key no propertyNames. keyword constrains.
var plainKey = &schema{Type: "string"}

// keys returns the schema of member names when anyMembers.
func (s *schema) keys() *schema {
	if s.Keys != nil {
		return s.Keys
	}
	return plainKey
}

// annotations are the keywords that describe a value rather than constrain
// it. Elements, map values, and keys take none.
var annotations = []string{"default", "examples", "deprecated"}

// tagEntries splits a schema tag into its key=value entries. A comma not
// followed by a keyword belongs to the previous value, so a pattern may
// contain commas.
func tagEntries(tag string) []string {
	var parts []string
	for p := range strings.SplitSeq(tag, ",") {
		if len(parts) > 0 && !keyPrefix.MatchString(p) {
			parts[len(parts)-1] += "," + p
			continue
		}
		parts = append(parts, p)
	}
	return parts
}

// declaresDefault reports whether a schema tag declares a default.
func declaresDefault(tag string) bool {
	for _, p := range tagEntries(tag) {
		if strings.HasPrefix(p, "default=") {
			return true
		}
	}
	return false
}

// parseConstraints applies a schema tag such as "minLength=1,maximum=100" to
// a copy of base. Prefixed keywords apply to array elements
// ("items.enum=a|b"), map values ("additionalProperties.maxLength=3"), or
// keys of a map or WithSchema object ("propertyNames.maxLength=63").
func parseConstraints(tag string, base *schema) (*schema, error) {
	if tag == "" {
		return base, nil
	}
	return parseEntries(tag, tagEntries(tag), base)
}

// parseEntries is parseConstraints, on the entries of tag.
func parseEntries(tag string, entries []string, base *schema) (*schema, error) {
	var parts, items, values, keys []string
	for _, p := range entries {
		if rest, ok := strings.CutPrefix(p, itemsPrefix); ok {
			items = append(items, rest)
			continue
		}
		if rest, ok := strings.CutPrefix(p, valuesPrefix); ok {
			values = append(values, rest)
			continue
		}
		if rest, ok := strings.CutPrefix(p, keysPrefix); ok {
			keys = append(keys, rest)
			continue
		}
		parts = append(parts, p)
	}
	for _, nested := range []struct {
		prefix  string
		entries []string
	}{{itemsPrefix, items}, {valuesPrefix, values}, {keysPrefix, keys}} {
		if len(nested.entries) == 0 {
			continue
		}
		s, err := parseNested(nested.prefix, nested.entries, base)
		if err != nil {
			return nil, err
		}
		base = s
	}
	if len(parts) == 0 && (len(items) > 0 || len(values) > 0 || len(keys) > 0) {
		return base, nil
	}
	if base.Ref != "" && slices.ContainsFunc(parts, func(p string) bool { return !strings.HasPrefix(p, "deprecated=") }) {
		return nil, fmt.Errorf("schema tag %q on a struct type", tag)
	}
	if base.binary {
		return nil, fmt.Errorf("schema tag %q on a geta.File", tag)
	}
	s := base.clone()
	seen := map[string]bool{}
	for _, p := range parts {
		key, val, ok := strings.Cut(p, "=")
		if !ok {
			return nil, fmt.Errorf("schema tag entry %q is not key=value", p)
		}
		types, known := constraintTypes[key]
		if !known {
			return nil, fmt.Errorf("schema tag has unknown keyword %q", key)
		}
		if seen[key] {
			return nil, fmt.Errorf("schema tag repeats keyword %q", key)
		}
		seen[key] = true
		if types != nil && s.Type == "" {
			// A JSON-method type: schema {}, which no keyword narrows.
			return nil, fmt.Errorf("schema keyword %s applies to %s, not a type with its own JSON methods; use geta.WithSchema", key, strings.Join(types, " or "))
		}
		if types != nil && !slices.Contains(types, s.Type) {
			return nil, fmt.Errorf("schema keyword %s applies to %s, not %s", key, strings.Join(types, " or "), s.Type)
		}
		// An unnamed struct's member count is fixed by its fields.
		if types != nil && s.Type == "object" && s.Closed {
			return nil, fmt.Errorf("schema keyword %s applies to a map, not a struct", key)
		}
		if err := s.set(key, val); err != nil {
			return nil, fmt.Errorf("schema keyword %s: %w", key, err)
		}
	}
	s.keepTypeBounds(base)
	if err := s.consistent(); err != nil {
		return nil, err
	}
	return s, nil
}

// parseNested applies entries, given after prefix, to base.Items,
// base.Additional, or base.Keys. Annotations are refused there: they belong
// on the whole array or map. Elements whose schema getavet cannot see
// (unjudged) are left for geta.New.
func parseNested(prefix string, entries []string, base *schema) (*schema, error) {
	key, _, _ := strings.Cut(entries[0], "=")
	what := base.Type
	switch {
	case base.Ref != "":
		what = "a struct type"
	case what == "":
		what = "a type with JSON methods of its own"
	case what == "object" && base.Closed:
		what = "a struct"
	}
	var elems *schema
	var whole, elem string
	switch prefix {
	case itemsPrefix:
		elems, whole, elem = base.Items, "array", "element"
		if base.Type != "array" {
			return nil, fmt.Errorf("schema keyword %s%s applies to the elements of an array, not %s", prefix, key, what)
		}
	case valuesPrefix:
		elems, whole, elem = base.Additional, "map", "value"
		if base.Type != "object" || base.Closed || base.Ref != "" {
			return nil, fmt.Errorf("schema keyword %s%s applies to the values of a map, not %s", prefix, key, what)
		}
	default:
		// A key is always a JSON string, so its schema is always known.
		if !base.anyMembers() {
			return nil, fmt.Errorf("schema keyword %s%s applies to the keys of a map, not %s", prefix, key, what)
		}
		elems, whole, elem = base.keys(), "map", "key"
		if base.Additional == nil {
			whole = "object"
		}
	}
	if elems == nil && base.unjudged {
		return base, nil // geta.New judges them
	}
	if elems == nil {
		// A WithSchema array or object: the type reads its own elements.
		return nil, fmt.Errorf("schema keyword %s%s: the geta.WithSchema %s declares no %s schema", prefix, key, base.Type, elem)
	}
	for _, p := range entries {
		if key, _, _ := strings.Cut(p, "="); slices.Contains(annotations, key) {
			return nil, fmt.Errorf("schema keyword %s%s: %s %s takes no %s; put it on the %s",
				prefix, key, article(elem), elem, key, whole)
		}
	}
	tag := strings.Join(entries, ",")
	parsed, err := parseEntries(tag, entries, elems)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", strings.TrimSuffix(prefix, "."), err)
	}
	s := base.clone()
	switch prefix {
	case itemsPrefix:
		s.Items = parsed
	case valuesPrefix:
		s.Additional = parsed
	default:
		s.Keys = parsed
	}
	return s, nil
}

// article is the indefinite article before word.
func article(word string) string {
	if strings.ContainsRune("aeiou", rune(word[0])) {
		return "an"
	}
	return "a"
}

func (s *schema) set(key, val string) error {
	nonNeg := func() (*int, error) {
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("%q is not a non-negative integer", val)
		}
		return &n, nil
	}
	num := func() (*float64, error) {
		f, err := strconv.ParseFloat(val, 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, fmt.Errorf("%q is not a number", val)
		}
		// The bound is published and enforced as f's shortest decimal, so
		// it must equal val.
		if !heldExactly(val, f) {
			return nil, fmt.Errorf("%q is not exact as a float64; use %s", val, fmtNum(f))
		}
		return &f, nil
	}
	var err error
	switch key {
	case "minLength":
		s.MinLength, err = nonNeg()
	case "maxLength":
		s.MaxLength, err = nonNeg()
	case "minItems":
		s.MinItems, err = nonNeg()
	case "maxItems":
		s.MaxItems, err = nonNeg()
	case "minProperties":
		s.MinProperties, err = nonNeg()
	case "maxProperties":
		s.MaxProperties, err = nonNeg()
	case "pattern":
		if val == "" {
			return fmt.Errorf("empty pattern")
		}
		s.re, err = regexp.Compile(val)
		if err != nil {
			return fmt.Errorf("%q does not compile: %v", val, err)
		}
		s.Pattern = val
	case "format":
		// The FormatType message names no format: getavet, which shares it,
		// cannot see what the method returns.
		switch {
		case s.Format != "" && s.check == nil:
			return fmt.Errorf("the type has its own format")
		case s.Format != "":
			return fmt.Errorf("the type already has format %q", s.Format)
		}
		if val == "" {
			return fmt.Errorf("empty format")
		}
		// Only formats geta enforces may be documented.
		check, ok := formatChecks[val]
		if !ok {
			return fmt.Errorf("unknown format %q; geta checks %s", val, checkedFormats())
		}
		s.Format, s.check = val, check
	case "enum":
		vals := strings.Split(val, "|")
		for _, v := range vals {
			if v == "" {
				return fmt.Errorf("%q has an empty member", val)
			}
		}
		s.Enum, s.numEnum = vals, nil
		if s.Type == "integer" || s.Type == "number" {
			// The document writes members verbatim, so each must be a JSON
			// number, and an integer's must have no fraction or exponent.
			nums := make([]decimal, len(vals))
			for i, v := range vals {
				switch {
				case !jsonNumberSyntax.MatchString(v):
					return fmt.Errorf("member %q is not a JSON number", v)
				case s.Type == "integer" && !isIntegerLiteral(v):
					return fmt.Errorf("member %q is not an integer", v)
				}
				nums[i] = parseDecimal(v)
			}
			s.numEnum = nums
		}
	case "minimum":
		s.Minimum, err = num()
	case "maximum":
		s.Maximum, err = num()
	case "exclusiveMinimum":
		s.ExclusiveMinimum, err = num()
	case "exclusiveMaximum":
		s.ExclusiveMaximum, err = num()
	case "multipleOf":
		s.MultipleOf, err = num()
		if err == nil && *s.MultipleOf <= 0 {
			return fmt.Errorf("%q is not greater than zero", val)
		}
	case "uniqueItems":
		switch val {
		case "true":
			s.UniqueItems = true
		case "false":
		default:
			return fmt.Errorf("%q is not true or false", val)
		}
	case "deprecated":
		switch val {
		case "true":
			s.Deprecated = true
		case "false":
		default:
			return fmt.Errorf("%q is not true or false", val)
		}
	case "default":
		// A default would hide a Nullable's absent state.
		if s.Null {
			return fmt.Errorf("a geta.Nullable takes no default")
		}
		s.Default = &val
	case "examples":
		vals := strings.Split(val, "|")
		for _, v := range vals {
			if v == "" && s.Type != "string" {
				return fmt.Errorf("%q has an empty member", val)
			}
		}
		s.Examples = vals
	}
	return err
}

// checkValues checks that the default and examples are scalar values s
// admits, read as c reads a request's value, with numbers in JSON syntax.
// It also checks enum members against a text type's UnmarshalText or a key
// type's methods. c is nil when getavet knows the kind but not the type.
func (s *schema) checkValues(c *codec) error {
	if c != nil && c.kind == kNull {
		c = c.elem
	}
	if c != nil && c.kind == kSlice {
		if err := s.itemsHold(func(items *schema) error { return items.checkValues(c.elem) }); err != nil {
			return err
		}
	}
	if c != nil && c.kind == kMap {
		if err := s.valuesHold(func(values *schema) error { return values.checkValues(c.elem) }); err != nil {
			return err
		}
		// A key enum member the key type's methods refuse is unreachable.
		if c.keyMethods {
			if err := s.keysHold(func(keys *schema) error {
				for _, e := range keys.Enum {
					if json.Unmarshal(quote(e), reflect.New(c.t.Key()).Interface()) != nil {
						return fmt.Errorf("enum member %q is not a valid %s", e, c.t.Key().Name())
					}
				}
				return nil
			}); err != nil {
				return err
			}
		}
	}
	// Likewise an enum member UnmarshalText refuses.
	if c != nil && c.kind == kText {
		for _, e := range s.Enum {
			if unmarshalText(reflect.New(c.t).Interface(), []byte(e)) != nil {
				what := s.Format
				if what == "" {
					what = c.t.Name()
				}
				return fmt.Errorf("enum member %q is not a valid %s", e, what)
			}
		}
	}
	if s.Default == nil && len(s.Examples) == 0 {
		return nil
	}
	if c != nil {
		switch c.kind {
		case kString, kBool, kInt, kUint, kFloat, kText:
		default:
			return fmt.Errorf("default and examples do not apply to %s", c.t)
		}
	}
	check := func(what, text string) error {
		if (s.Type == "integer" || s.Type == "number") && !jsonNumberSyntax.MatchString(text) {
			return fmt.Errorf("%s %q is not a JSON number", what, text)
		}
		d := &decoder{limits: unbounded, written: true}
		if c != nil {
			d.check(c, s, paramValue(c, text), "")
		} else {
			d.str(s, text, "")
		}
		if len(d.errs) > 0 {
			return fmt.Errorf("%s %q: %s", what, text, d.errs[0].Message)
		}
		return nil
	}
	if s.Default != nil {
		if err := check("default", *s.Default); err != nil {
			return err
		}
	}
	for _, e := range s.Examples {
		if err := check("example", e); err != nil {
			return err
		}
	}
	return nil
}

// values returns the examples and the default as text.
func (s *schema) values() []string {
	vs := slices.Clone(s.Examples)
	if s.Default != nil {
		vs = append(vs, *s.Default)
	}
	return vs
}

// jsonValue converts a default's or example's text to the JSON value the
// document writes.
func (s *schema) jsonValue(text string) any {
	switch s.Type {
	case "integer", "number":
		return rawNumber(text)
	case "boolean":
		return text == "true"
	}
	return text
}

// heldExactly reports whether val equals the shortest decimal of f, its
// ParseFloat result: 0.1 does, 9007199254740993 does not. It handles the
// underscores and hex mantissas ParseFloat accepts.
func heldExactly(val string, f float64) bool {
	val = strings.ReplaceAll(val, "_", "")
	near := strconv.FormatFloat(f, 'e', -1, 64)
	if m := strings.TrimLeft(val, "+-"); len(m) > 1 && m[0] == '0' && (m[1] == 'x' || m[1] == 'X') {
		a, ok := new(big.Rat).SetString(val)
		b, _ := new(big.Rat).SetString(near)
		return ok && a.Cmp(b) == 0
	}
	return parseDecimal(val).cmp(parseDecimal(near)) == 0
}

// keepTypeBounds replaces a declared bound looser than the type's own range
// with the type's (inclusive) bound. For int32 and float32 the range is
// implied by the format and is written only in place of a looser bound.
func (s *schema) keepTypeBounds(base *schema) {
	lo, hi := base.Minimum, base.Maximum
	if l, h, ok := formatRange(base); ok {
		lo, hi = &l, &h
	}
	if lo != nil {
		if s.ExclusiveMinimum != nil && *s.ExclusiveMinimum < *lo {
			s.ExclusiveMinimum, s.Minimum = nil, cmp.Or(s.Minimum, lo)
		}
		if s.Minimum != nil && *s.Minimum < *lo {
			s.Minimum = lo
		}
	}
	if hi != nil {
		if s.ExclusiveMaximum != nil && *s.ExclusiveMaximum > *hi {
			s.ExclusiveMaximum, s.Maximum = nil, cmp.Or(s.Maximum, hi)
		}
		if s.Maximum != nil && *s.Maximum > *hi {
			s.Maximum = hi
		}
	}
}

// formatRange returns the range implied by format int32 or float (finite
// float32).
func formatRange(s *schema) (lo, hi float64, ok bool) {
	switch {
	case s.Type == "integer" && s.Format == "int32":
		return math.MinInt32, math.MaxInt32, true
	case s.Type == "number" && s.Format == "float":
		return -math.MaxFloat32, math.MaxFloat32, true
	}
	return 0, 0, false
}

// consistent rejects constraint combinations no value can satisfy.
func (s *schema) consistent() error {
	if s.MinLength != nil && s.MaxLength != nil && *s.MinLength > *s.MaxLength {
		return fmt.Errorf("minLength %d exceeds maxLength %d", *s.MinLength, *s.MaxLength)
	}
	if l, ok := s.formatLength(); ok {
		if s.MaxLength != nil && *s.MaxLength < l.shortest {
			return fmt.Errorf("maxLength %d is below format %s's minimum length %d", *s.MaxLength, s.Format, l.shortest)
		}
		if s.MinLength != nil && l.longest > 0 && *s.MinLength > l.longest {
			return fmt.Errorf("minLength %d exceeds format %s's maximum length %d", *s.MinLength, s.Format, l.longest)
		}
	}
	if s.MinItems != nil && s.MaxItems != nil && *s.MinItems > *s.MaxItems {
		return fmt.Errorf("minItems %d exceeds maxItems %d", *s.MinItems, *s.MaxItems)
	}
	if s.MinProperties != nil && s.MaxProperties != nil && *s.MinProperties > *s.MaxProperties {
		return fmt.Errorf("minProperties %d exceeds maxProperties %d", *s.MinProperties, *s.MaxProperties)
	}
	if s.Minimum != nil && s.Maximum != nil && *s.Minimum > *s.Maximum {
		return fmt.Errorf("minimum %s exceeds maximum %s", fmtNum(*s.Minimum), fmtNum(*s.Maximum))
	}
	if s.Type == "integer" && s.MultipleOf != nil && *s.MultipleOf != math.Trunc(*s.MultipleOf) {
		return fmt.Errorf("multipleOf %v on an integer is not an integer", *s.MultipleOf)
	}
	if err := s.reachable(); err != nil {
		return err
	}
	for _, e := range s.Enum {
		if s.MaxLength != nil && utf8.RuneCountInString(e) > *s.MaxLength {
			return fmt.Errorf("enum member %q exceeds maxLength %d", e, *s.MaxLength)
		}
		if s.MinLength != nil && utf8.RuneCountInString(e) < *s.MinLength {
			return fmt.Errorf("enum member %q is shorter than minLength %d", e, *s.MinLength)
		}
		if s.re != nil && !s.re.MatchString(e) {
			return fmt.Errorf("enum member %q does not match pattern %s", e, s.Pattern)
		}
		if s.numEnum == nil && !s.validFormat(e) {
			return fmt.Errorf("enum member %q is not a valid %s", e, s.Format)
		}
		for _, p := range s.implied {
			if !p.holds(e) {
				return fmt.Errorf("enum member %q %s", e, p.why)
			}
		}
	}
	for i := range s.numEnum {
		if err := s.numberMember(s.Enum[i], i); err != nil {
			return err
		}
	}
	return nil
}

// numberMember rejects the i-th numeric enum member lit if it is outside the
// Go type's range, would round to a different number in the type, or fails
// the schema's bounds or multipleOf.
func (s *schema) numberMember(lit string, i int) error {
	var err error
	switch {
	case s.goInt != nil && s.goInt.Kind() >= reflect.Uint && s.goInt.Kind() <= reflect.Uintptr:
		_, err = strconv.ParseUint(lit, 10, s.goInt.Bits())
	case s.goInt != nil:
		_, err = strconv.ParseInt(lit, 10, s.goInt.Bits())
	case s.Type == "number" && (s.Format == "float" || s.Format == "double"):
		bits := 64
		if s.Format == "float" {
			bits = 32
		}
		var v float64
		if v, err = strconv.ParseFloat(lit, bits); err == nil {
			if near := strconv.FormatFloat(v, 'e', -1, bits); parseDecimal(near).cmp(s.numEnum[i]) != 0 {
				return fmt.Errorf("enum member %s is not exact as a float%d; use %s",
					lit, bits, strconv.FormatFloat(v, 'g', -1, bits))
			}
		}
	}
	if err != nil {
		what := "float64"
		switch {
		case s.goInt != nil:
			what = s.goInt.String()
		case s.Format == "float":
			what = "float32"
		}
		return fmt.Errorf("enum member %s is outside the range of %s", lit, what)
	}
	f, _ := strconv.ParseFloat(lit, 64)
	if !numOK(s, lit, f) {
		return fmt.Errorf("enum member %s does not meet the schema's bounds or multipleOf", lit)
	}
	return nil
}

// readable checks a schema that requests read against the pattern ceiling
// and lim's backstops. Response-only schemas are not checked.
func (s *schema) readable(lim *Limits) error {
	if err := s.withinCeiling(); err != nil {
		return err
	}
	return s.withinLimits(lim)
}

// itemsHold applies check to an array's element schema.
func (s *schema) itemsHold(check func(*schema) error) error {
	if s.Type != "array" || s.Items == nil || s.Items.Ref != "" {
		return nil
	}
	if err := check(s.Items); err != nil {
		return fmt.Errorf("items: %w", err)
	}
	return nil
}

// valuesHold applies check to a map's value schema.
func (s *schema) valuesHold(check func(*schema) error) error {
	if s.Additional == nil || s.Additional.Ref != "" {
		return nil
	}
	if err := check(s.Additional); err != nil {
		return fmt.Errorf("additionalProperties: %w", err)
	}
	return nil
}

// keysHold applies check to declared key schemas of a map or WithSchema
// object.
func (s *schema) keysHold(check func(*schema) error) error {
	if s.Keys == nil || !s.anyMembers() {
		return nil
	}
	if err := check(s.Keys); err != nil {
		return fmt.Errorf("propertyNames: %w", err)
	}
	return nil
}

// nestedHold applies itemsHold, valuesHold, and keysHold.
func (s *schema) nestedHold(check func(*schema) error) error {
	if err := s.itemsHold(check); err != nil {
		return err
	}
	if err := s.valuesHold(check); err != nil {
		return err
	}
	return s.keysHold(check)
}

// withinCeiling rejects, alongside a pattern, a maxLength, minLength, enum
// member, default, or example longer than patternCeiling, since geta refuses
// such strings. getavet applies it too (checkRequestTag).
func (s *schema) withinCeiling() error {
	if s.re == nil {
		return s.nestedHold((*schema).withinCeiling)
	}
	if s.MaxLength != nil && *s.MaxLength > patternCeiling {
		return fmt.Errorf("maxLength %d with a pattern exceeds the pattern ceiling of %d code points", *s.MaxLength, patternCeiling)
	}
	if s.MinLength != nil && *s.MinLength > patternCeiling {
		return fmt.Errorf("minLength %d with a pattern exceeds the pattern ceiling of %d code points", *s.MinLength, patternCeiling)
	}
	for _, e := range s.Enum {
		if utf8.RuneCountInString(e) > patternCeiling {
			return fmt.Errorf("enum member %q with a pattern exceeds the pattern ceiling of %d code points", e, patternCeiling)
		}
	}
	for _, v := range s.values() {
		if utf8.RuneCountInString(v) > patternCeiling {
			return fmt.Errorf("default or example %q with a pattern exceeds the pattern ceiling of %d code points", v, patternCeiling)
		}
	}
	return nil
}

// withinLimits rejects what lim's backstops leave unmeetable when no upper
// bound is declared: minLength, an enum member, a default, an example, or a
// format's strings longer than MaxStringLength; minItems or minProperties
// above MaxItems. It recurses into elements, values, and keys. Only geta.New
// runs it; getavet does not know the Limits.
func (s *schema) withinLimits(lim *Limits) error {
	if lim == nil {
		return nil
	}
	if n, ok := s.backstopLength(lim); ok {
		if l, ok := s.formatLength(); ok {
			switch {
			case l.shortest > n:
				return fmt.Errorf("format %s strings exceed Limits.MaxStringLength %d; declare a maxLength", s.Format, n)
			case l.longest > n:
				return fmt.Errorf("format %s takes up to %d code points, past Limits.MaxStringLength %d; declare a maxLength", s.Format, l.longest, n)
			}
		}
		if s.MinLength != nil && *s.MinLength > n {
			return fmt.Errorf("minLength %d exceeds Limits.MaxStringLength %d; declare a maxLength", *s.MinLength, n)
		}
		for _, e := range s.Enum {
			if utf8.RuneCountInString(e) > n {
				return fmt.Errorf("enum member %q exceeds Limits.MaxStringLength %d; declare a maxLength", e, n)
			}
		}
		for _, v := range s.values() {
			if utf8.RuneCountInString(v) > n {
				return fmt.Errorf("default or example %q exceeds Limits.MaxStringLength %d; declare a maxLength", v, n)
			}
		}
	}
	if s.Type == "array" && s.MaxItems == nil && s.MinItems != nil && *s.MinItems > lim.MaxItems {
		return fmt.Errorf("minItems %d exceeds Limits.MaxItems %d; declare a maxItems", *s.MinItems, lim.MaxItems)
	}
	if s.anyMembers() && s.MaxProperties == nil && s.MinProperties != nil && *s.MinProperties > lim.MaxItems {
		return fmt.Errorf("minProperties %d exceeds Limits.MaxItems %d; declare a maxProperties", *s.MinProperties, lim.MaxItems)
	}
	return s.nestedHold(func(items *schema) error { return items.withinLimits(lim) })
}

// anyMembers reports whether s is an object with arbitrary member names: a
// map or a WithSchema object, not a struct.
func (s *schema) anyMembers() bool {
	return s.Type == "object" && s.Ref == "" && !s.Closed && len(s.Properties) == 0 && len(s.OneOf) == 0
}

// reachable rejects numeric keywords no value meets: an empty interval, or
// one with no integer (within the Go type's range), no float32, or no
// multiple of multipleOf. The arithmetic is exact on the bounds' shortest
// decimals, as cmpNum compares them.
func (s *schema) reachable() error {
	if s.Type != "integer" && s.Type != "number" {
		return nil
	}
	exact := func(f float64) *big.Rat {
		r, _ := new(big.Rat).SetString(strconv.FormatFloat(f, 'e', -1, 64))
		return r
	}
	var named []string
	var lo, hi *big.Rat
	var loExcl, hiExcl bool
	bound := func(key string, f *float64, excl, upper bool) {
		if f == nil {
			return
		}
		named = append(named, key+" "+fmtNum(*f))
		// The tighter bound holds; of two equal ones, the exclusive.
		r := exact(*f)
		if upper {
			c := 1 // how far r is inside hi
			if hi != nil {
				c = hi.Cmp(r)
			}
			if c > 0 || c == 0 && excl {
				hi, hiExcl = r, excl
			}
			return
		}
		c := 1
		if lo != nil {
			c = r.Cmp(lo)
		}
		if c > 0 || c == 0 && excl {
			lo, loExcl = r, excl
		}
	}
	bound("minimum", s.Minimum, false, false)
	bound("exclusiveMinimum", s.ExclusiveMinimum, true, false)
	bound("maximum", s.Maximum, false, true)
	bound("exclusiveMaximum", s.ExclusiveMaximum, true, true)

	what := s.Type
	if _, h, ok := formatRange(s); ok && s.Type == "number" {
		// A float32 past its finite range does not decode.
		what = fmt.Sprintf("float32 value (%s to %s)", fmtNum(-h), fmtNum(h))
		if r := exact(h); hi == nil || hi.Cmp(r) > 0 {
			hi, hiExcl = r, false
		}
		if r := exact(-h); lo == nil || lo.Cmp(r) < 0 {
			lo, loExcl = r, false
		}
	}
	// Admitted values are whole multiples of step (1 for an integer).
	var step *big.Rat
	if s.MultipleOf != nil {
		named = append(named, "multipleOf "+fmtNum(*s.MultipleOf))
		step = exact(*s.MultipleOf)
	} else if s.Type == "integer" {
		step = big.NewRat(1, 1)
	}
	if step == nil {
		if lo == nil || hi == nil {
			return nil
		}
		if c := lo.Cmp(hi); c > 0 || c == 0 && (loExcl || hiExcl) {
			return fmt.Errorf("no %s meets %s", what, strings.Join(named, ", "))
		}
		return nil
	}
	// The whole k with lo ≤ k×step ≤ hi (< for an exclusive bound).
	var kLo, kHi *big.Int
	if lo != nil {
		q := new(big.Rat).Quo(lo, step)
		if kLo = floor(q); loExcl || !q.IsInt() {
			kLo.Add(kLo, big.NewInt(1))
		}
	}
	if hi != nil {
		q := new(big.Rat).Quo(hi, step)
		if kHi = floor(q); hiExcl && q.IsInt() {
			kHi.Sub(kHi, big.NewInt(1))
		}
	}
	if s.Type == "integer" && s.goInt != nil {
		tlo, thi := intRange(s.goInt)
		what = fmt.Sprintf("%s value (%s to %s)", s.goInt, tlo, thi)
		// Whole multiples of an integer step within [tlo, thi].
		qlo := new(big.Rat).Quo(new(big.Rat).SetInt(tlo), step)
		k := floor(qlo)
		if !qlo.IsInt() {
			k.Add(k, big.NewInt(1))
		}
		if kLo == nil || k.Cmp(kLo) > 0 {
			kLo = k
		}
		if k := floor(new(big.Rat).Quo(new(big.Rat).SetInt(thi), step)); kHi == nil || k.Cmp(kHi) < 0 {
			kHi = k
		}
	}
	if kLo != nil && kHi != nil && kLo.Cmp(kHi) > 0 {
		return fmt.Errorf("no %s meets %s", what, strings.Join(named, ", "))
	}
	return nil
}

// floor is the greatest integer not above r.
func floor(r *big.Rat) *big.Int {
	// Rat keeps its denominator positive, and Div rounds toward -∞ then.
	return new(big.Int).Div(r.Num(), r.Denom())
}

// intRange is the least and greatest value of the integer type t.
func intRange(t reflect.Type) (lo, hi *big.Int) {
	one := big.NewInt(1)
	switch t.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		hi = new(big.Int).Lsh(one, uint(t.Bits()))
		return new(big.Int), hi.Sub(hi, one)
	}
	hi = new(big.Int).Lsh(one, uint(t.Bits()-1))
	lo = new(big.Int).Neg(hi)
	return lo, hi.Sub(hi, one)
}

// componentRef is how the document refers to a component.
const componentRef = "#/components/schemas/"

// inputSuffix names a type's request schema component when it differs from
// the response one: Booking and Booking-Input. Component names never contain
// a hyphen, so the two cannot collide.
const inputSuffix = "-Input"

// document renders the schema as a JSON-ready value for OpenAPI 3.1. For a
// request schema, lim is non-nil and its backstops are stated where no bound
// is declared, so the document admits nothing geta refuses. A response
// schema is rendered with lim nil and states no backstop.
func (s *schema) document(lim *Limits) map[string]any { return s.render(lim, nil) }

// render is document; a request schema refers to components in split by
// their -Input name.
func (s *schema) render(lim *Limits, split map[string]bool) map[string]any {
	if s.Ref != "" {
		m := map[string]any{"$ref": componentRef + refName(s.Ref, lim, split)}
		if s.Null {
			// A $ref cannot admit null, so wrap it in anyOf.
			m = map[string]any{"anyOf": []any{m, map[string]any{"type": "null"}}}
		}
		if s.Deprecated {
			m["deprecated"] = true
		}
		return m
	}
	m := map[string]any{}
	if len(s.OneOf) > 0 {
		refs := make([]any, len(s.OneOf))
		for i, v := range s.OneOf {
			refs[i] = v.render(lim, split)
		}
		m["oneOf"] = refs
		mapping := s.Mapping
		if lim != nil && len(split) > 0 {
			mapping = make(map[string]string, len(s.Mapping))
			for tag, ref := range s.Mapping {
				mapping[tag] = componentRef + refName(strings.TrimPrefix(ref, componentRef), lim, split)
			}
		}
		m["discriminator"] = map[string]any{"propertyName": s.Discriminator, "mapping": mapping}
		return m
	}
	switch {
	case s.Type != "" && s.Null:
		// Other keywords ignore null; enumValues adds null to an enum.
		m["type"] = []string{s.Type, "null"}
	case s.Type != "":
		m["type"] = s.Type
	}
	if s.Format != "" {
		m["format"] = s.Format
	}
	if s.ContentEncoding != "" {
		m["contentEncoding"] = s.ContentEncoding
	}
	if s.ContentMediaType != "" {
		m["contentMediaType"] = s.ContentMediaType
	}
	if s.Type == "object" {
		props := map[string]any{}
		var defaulted []string
		for _, p := range s.Properties {
			ps := p.Schema.render(lim, split)
			if p.Doc != "" {
				ps["description"] = p.Doc
			}
			props[p.Name] = ps
			if p.Schema.Default != nil {
				defaulted = append(defaulted, p.Name)
			}
		}
		if len(s.Properties) > 0 || s.Closed {
			m["properties"] = props
		}
		// A request may omit a member with a default; a response may not.
		required := s.Required
		if lim != nil && len(defaulted) > 0 {
			required = slices.DeleteFunc(slices.Clone(required), func(n string) bool { return slices.Contains(defaulted, n) })
		}
		if len(required) > 0 {
			m["required"] = required
		}
		if s.Additional != nil {
			m["additionalProperties"] = s.Additional.render(lim, split)
		} else if s.Closed {
			m["additionalProperties"] = false
		}
		if s.anyMembers() && lim != nil && s.MaxProperties == nil {
			m["maxProperties"] = lim.MaxItems
		}
		// Keys are always strings, so propertyNames omits the type.
		if s.anyMembers() {
			if k := s.keys().render(lim, split); len(k) > 1 {
				delete(k, "type")
				m["propertyNames"] = k
			}
		}
		if s.MinProperties != nil {
			m["minProperties"] = *s.MinProperties
		}
		if s.MaxProperties != nil {
			m["maxProperties"] = *s.MaxProperties
		}
	}
	if s.Items != nil {
		m["items"] = s.Items.render(lim, split)
	}
	if len(s.Enum) > 0 {
		m["enum"] = s.enumValues()
	}
	putInt := func(k string, v *int) {
		if v != nil {
			m[k] = *v
		}
	}
	putNum := func(k string, v *float64) {
		if v != nil {
			m[k] = jsonNumber(*v)
		}
	}
	putInt("minLength", s.MinLength)
	putInt("maxLength", s.MaxLength)
	if n, ok := s.backstopLength(lim); ok {
		m["maxLength"] = n
	}
	// A schema holds one pattern; the rest go in allOf.
	var patterns []string
	if s.Pattern != "" {
		patterns = append(patterns, s.Pattern)
	}
	for _, p := range s.implied {
		patterns = append(patterns, p.pattern)
	}
	for i, p := range patterns {
		if i == 0 {
			m["pattern"] = p
			continue
		}
		all, _ := m["allOf"].([]any)
		m["allOf"] = append(all, map[string]any{"pattern": p})
	}
	putNum("minimum", s.Minimum)
	putNum("maximum", s.Maximum)
	putNum("exclusiveMinimum", s.ExclusiveMinimum)
	putNum("exclusiveMaximum", s.ExclusiveMaximum)
	putNum("multipleOf", s.MultipleOf)
	putInt("minItems", s.MinItems)
	putInt("maxItems", s.MaxItems)
	if s.Type == "array" && s.MaxItems == nil && lim != nil {
		m["maxItems"] = lim.MaxItems
	}
	if s.UniqueItems {
		m["uniqueItems"] = true
	}
	if s.Default != nil {
		m["default"] = s.jsonValue(*s.Default)
	}
	if len(s.Examples) > 0 {
		ex := make([]any, len(s.Examples))
		for i, e := range s.Examples {
			ex[i] = s.jsonValue(e)
		}
		m["examples"] = ex
	}
	if s.Deprecated {
		m["deprecated"] = true
	}
	return m
}

// refName returns name, with inputSuffix when a request schema (lim non-nil)
// refers to a split component.
func refName(name string, lim *Limits, split map[string]bool) string {
	if lim != nil && split[name] {
		return name + inputSuffix
	}
	return name
}

// backstopLength returns the maxLength a request schema states for a string
// with none declared: MaxStringLength, or patternCeiling if smaller and a
// pattern applies. ok is false when nothing need be stated, as for an enum
// or a format whose strings all fit.
func (s *schema) backstopLength(lim *Limits) (int, bool) {
	if s.Type != "string" || s.MaxLength != nil || lim == nil || s.binary {
		return 0, false
	}
	n := lim.MaxStringLength
	if s.re != nil {
		n = min(n, patternCeiling)
	}
	if len(s.Enum) > 0 && !slices.ContainsFunc(s.Enum, func(e string) bool { return utf8.RuneCountInString(e) > n }) {
		return 0, false
	}
	if l, ok := s.formatLength(); ok && l.longest > 0 && l.longest <= n {
		return 0, false
	}
	return n, true
}

// formatLength returns the shortest and longest string lengths of the format
// geta checks on s. ok is false when geta checks none (as for a FormatType).
func (s *schema) formatLength() (struct{ shortest, longest int }, bool) {
	if s.check == nil {
		return struct{ shortest, longest int }{}, false
	}
	l, ok := formatLengths[s.Format]
	return l, ok
}

// enumValues returns the enum members as the document writes them: strings
// or verbatim JSON numbers, plus null when admitted.
func (s *schema) enumValues() any {
	if s.numEnum == nil && !s.Null {
		return s.Enum
	}
	out := make([]any, len(s.Enum), len(s.Enum)+1)
	for i, e := range s.Enum {
		out[i] = e
		if s.numEnum != nil {
			out[i] = rawNumber(e)
		}
	}
	if s.Null {
		out = append(out, nil)
	}
	return out
}

// rawNumber is a JSON number written as it was given (jsonNumberSyntax).
type rawNumber string

func (n rawNumber) MarshalJSON() ([]byte, error) { return []byte(n), nil }

// inNumEnum reports whether lit equals a numeric enum member in value.
func (s *schema) inNumEnum(lit string) bool {
	d := parseDecimal(lit)
	for _, e := range s.numEnum {
		if d.cmp(e) == 0 {
			return true
		}
	}
	return false
}

// jsonNumber marshals a float as 100, not 1e+02.
type jsonNumber float64

func (n jsonNumber) MarshalJSON() ([]byte, error) {
	return appendFloat(nil, float64(n)), nil
}
