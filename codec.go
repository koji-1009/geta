package geta

import (
	"encoding"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
	"uuid"
)

// A codec is one Go type's schema, derived by the rules encoding/json/v2 uses
// for the type. v2 reads and writes; the codec describes what v2 accepts and
// produces, plus what v2 does not check: required members, nulls, and schema
// keywords.
type codec struct {
	t      reflect.Type
	kind   ckind
	name   string // component name, for named structs
	fields []fieldCodec
	byName map[string]int // field index by JSON name, for structs with many fields
	elem   *codec         // slice element or map value
	schema *schema

	disc     string         // a sealed type's discriminator member
	variants []variantCodec // a sealed type's cases

	// keyMethods reports that v2 reads or writes a map's keys through a
	// method of the key type rather than as the raw member name.
	keyMethods bool
	// key is a map's key type's codec. Its format, if any, becomes the keys'
	// (schema.Keys, written as propertyNames).
	key *codec

	// unions reports that a value of the type can hold a sealed type. Set by
	// markUnions for nilUnion.
	unions bool

	// direct reports that the single pass sets a string, bool, integer, or
	// float value itself, as encoding/json/v2 would: neither the type nor its
	// pointer has a method v2 reads, and no unmarshaler in the body options
	// applies to it (registry.setsDirectly).
	direct bool
}

type ckind int

const (
	kString ckind = iota
	kBool
	kInt
	kUint
	kFloat
	kText
	kBytes
	kStruct
	kSlice
	kMap
	kJSON  // a type with its own JSON methods; its schema is declared
	kOneOf // a sealed interface type; its variants are declared
	kNull  // a Nullable: null, or a value of elem
)

type fieldCodec struct {
	json     string
	index    []int
	c        *codec
	optional bool    // the field is a pointer: absent on input, omitted when nil
	use      *schema // the schema at this use site, constraints included
	doc      string  // its description (doc tag)
}

// registry analyses types once per App and keeps component names unique.
type registry struct {
	codecs   map[reflect.Type]*codec
	names    map[string]reflect.Type
	declared map[reflect.Type]*schema // WithSchema declarations
	unions   map[reflect.Type]Union   // WithUnion declarations

	// encOpts and decOpts write and read bodies, including the sealed
	// types' marshalers and unmarshalers.
	encOpts, decOpts json.Options
	// unmarshaled are the types of the unmarshalers in decOpts, as each
	// unmarshaler names them (*time.Time, *T for a sealed type T).
	unmarshaled []reflect.Type

	// limits are the App's backstops that readable checks request schemas
	// against; nil when unknown.
	limits *Limits
	// read records the codecs checkRead has judged, keyed by the limits used,
	// since an operation's Doc.Limits may differ from the App's.
	read map[readLimits]bool
}

type readLimits struct {
	c   *codec
	lim Limits
}

// constrain applies the schema tag of a value requests read (a parameter, a
// form field, a body field) to base and checks the result with readable.
func (r *registry) constrain(tag string, base *schema) (*schema, error) {
	s, err := parseConstraints(tag, base)
	if err != nil {
		return nil, err
	}
	if err := s.readable(r.limits); err != nil {
		return nil, err
	}
	return s, nil
}

// checkRead applies readable to every member schema tag and WithSchema
// declaration reachable from c, a request body's codec. Types only written
// to responses are not checked. Each codec is judged once; every refusal is
// reported.
func (r *registry) checkRead(c *codec) error {
	var errs []error
	var walk func(c *codec)
	var lim Limits
	if r.limits != nil {
		lim = *r.limits
	}
	walk = func(c *codec) {
		if r.read[readLimits{c, lim}] {
			return
		}
		r.read[readLimits{c, lim}] = true
		switch c.kind {
		case kStruct:
			for _, f := range c.fields {
				// An untagged member uses its type's schema, judged below.
				if f.use != f.c.schema {
					if err := f.use.readable(r.limits); err != nil {
						errs = append(errs, fmt.Errorf("%s.%s: %w", c.t, c.t.FieldByIndex(f.index).Name, err))
					}
				}
				walk(f.c)
			}
		case kSlice, kMap, kNull:
			walk(c.elem)
		case kOneOf:
			for _, v := range c.variants {
				walk(v.c)
			}
		case kJSON:
			if s, ok := r.declared[c.t]; ok {
				if err := s.readable(r.limits); err != nil {
					errs = append(errs, fmt.Errorf("geta.WithSchema[%s]: %w", qualified(c.t), err))
				}
			}
		}
	}
	walk(c)
	return errors.Join(errs...)
}

func newRegistry() *registry {
	return &registry{codecs: map[reflect.Type]*codec{}, names: map[string]reflect.Type{}, read: map[readLimits]bool{},
		declared: map[reflect.Type]*schema{}, unions: map[reflect.Type]Union{},
		encOpts: marshalOptions, decOpts: json.JoinOptions(json.RejectUnknownMembers(true), json.WithUnmarshalers(timeUnmarshaler)),
		unmarshaled: []reflect.Type{reflect.PointerTo(timeType)}}
}

// setsDirectly reports whether the single pass may set a value of t, of a
// string, bool, integer, or float kind, itself rather than through
// encoding/json/v2: v2 reads t by its kind alone when neither t nor *t has a
// method v2 reads and no unmarshaler in decOpts applies to t, as v2 matches
// them (castableTo in encoding/json/v2).
func (r *registry) setsDirectly(t reflect.Type) bool {
	if v2Reads(t) {
		return false
	}
	for _, u := range r.unmarshaled {
		switch {
		case u.Kind() == reflect.Interface && reflect.PointerTo(t).Implements(u),
			u.Kind() == reflect.Pointer && reflect.PointerTo(t) == u,
			u == t:
			return false
		}
	}
	return true
}

// timeUnmarshaler reads a JSON string into a time.Time with readTime, as geta
// reads times elsewhere, instead of time.Time.UnmarshalText. Other JSON
// values are left to v2.
var timeUnmarshaler = json.UnmarshalFromFunc(func(dec *jsontext.Decoder, t *time.Time) error {
	if dec.PeekKind() != '"' {
		return errors.ErrUnsupported
	}
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	v, err := readTime(tok.String())
	if err != nil {
		return err
	}
	*t = v
	return nil
})

// sealDeclarations fixes the JSON options once every declaration is in.
func (r *registry) sealDeclarations() {
	enc, dec := r.unionOptions()
	r.encOpts = json.JoinOptions(marshalOptions, enc)
	r.decOpts = json.JoinOptions(json.RejectUnknownMembers(true), dec)
	r.unmarshaled = []reflect.Type{reflect.PointerTo(timeType)}
	for t := range r.unions {
		r.unmarshaled = append(r.unmarshaled, reflect.PointerTo(t))
	}
	// A codec analysed before now is judged again with these options.
	for _, c := range r.codecs {
		if c.direct {
			c.direct = r.setsDirectly(c.t)
		}
	}
}

var (
	textMarshaler   = reflect.TypeFor[encoding.TextMarshaler]()
	textAppender    = reflect.TypeFor[encoding.TextAppender]()
	textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
	jsonMethods     = []reflect.Type{
		reflect.TypeFor[json.Marshaler](), reflect.TypeFor[json.MarshalerTo](),
		reflect.TypeFor[json.Unmarshaler](), reflect.TypeFor[json.UnmarshalerFrom](),
	}
	timeType = reflect.TypeFor[time.Time]()
	uuidType = reflect.TypeFor[uuid.UUID]()
)

// writesText reports whether v2 writes t as a string through AppendText or
// MarshalText on t or *t. JSON methods take precedence (ownJSON).
func writesText(t reflect.Type) bool {
	for _, m := range []reflect.Type{textAppender, textMarshaler} {
		if t.Implements(m) || reflect.PointerTo(t).Implements(m) {
			return true
		}
	}
	return false
}

// ownJSON reports whether t (other than time.Time) has its own JSON methods,
// which v2 prefers to everything else.
func ownJSON(t reflect.Type) bool {
	if t == timeType {
		return false
	}
	for _, m := range jsonMethods {
		if t.Implements(m) || reflect.PointerTo(t).Implements(m) {
			return true
		}
	}
	return false
}

var declarableTypes = []string{"string", "integer", "number", "boolean", "array", "object"}

// declare records a WithSchema declaration.
func (r *registry) declare(d declaredSchema) error {
	where := "geta.WithSchema[" + qualified(d.t) + "]"
	if !ownJSON(d.t) {
		return fmt.Errorf("%s: %s does not write its own JSON", where, d.t)
	}
	if !slices.Contains(declarableTypes, d.jsonType) {
		return fmt.Errorf("%s: JSON type %q is not one of %s", where, d.jsonType, strings.Join(declarableTypes, ", "))
	}
	if _, dup := r.declared[d.t]; dup {
		return fmt.Errorf("%s: declared twice", where)
	}
	// checkRead judges readability once the type's use is known.
	s, err := parseConstraints(d.constraints, &schema{Type: d.jsonType})
	if err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	// The type reads itself, so geta cannot apply a default or check an
	// example: it takes neither.
	if err := s.checkValues(&codec{t: d.t, kind: kJSON}); err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	r.declared[d.t] = s
	return nil
}

// use returns the schema a reference to c should carry.
func (c *codec) use() *schema {
	if c.name != "" {
		return &schema{Ref: c.name}
	}
	return c.schema
}

// codecFor analyses t. Errors name the type and the field path.
func (r *registry) codecFor(t reflect.Type) (*codec, error) {
	if c, ok := r.codecs[t]; ok {
		return c, nil
	}
	// A Nullable has JSON methods, but its schema is its value's plus null.
	if et, ok := nullableOf(t); ok {
		_, nested := nullableOf(et)
		if err := CheckJSONType(VetType{Type: t.String(), Nullable: true, Elem: et.String(), ElemNullable: nested}); err != nil {
			return nil, err
		}
		ec, err := r.codecFor(et)
		if err != nil {
			return nil, err
		}
		s := ec.use().clone()
		s.Null = true
		c := &codec{t: t, kind: kNull, elem: ec, schema: s}
		r.codecs[t] = c
		return c, nil
	}
	// A type with its own JSON methods (not time.Time) has schema {}: a
	// request value is valid when its UnmarshalJSON takes it. WithSchema may
	// narrow the schema.
	if ownJSON(t) {
		if err := CheckJSONType(VetType{Type: t.String(), JSON: true, Format: namesFormat(t)}); err != nil {
			return nil, err
		}
		s, ok := r.declared[t]
		if !ok {
			s = &schema{}
		}
		c := &codec{t: t, kind: kJSON, schema: s}
		r.codecs[t] = c
		return c, nil
	}
	if t.Kind() == reflect.Interface {
		return r.unionCodec(t)
	}
	// CheckJSONType is shared with getavet.
	vt := vetType(t)
	if err := CheckJSONType(vt); err != nil {
		return nil, err
	}
	if vt.Marshaler {
		f, err := textFormat(t)
		if err != nil {
			return nil, err
		}
		// geta's own format types also get the format's check, which is
		// stricter than e.g. uuid.UUID's parser. A FormatType is checked by
		// its UnmarshalText alone.
		c := &codec{t: t, kind: kText, schema: &schema{Type: "string", Format: f, check: formatChecks[formatTypes[t]]}}
		if t == timeType {
			c.schema.implied = []impliedPattern{noLeapSecond}
		}
		r.codecs[t] = c
		return c, nil
	}
	c := &codec{t: t}
	switch t.Kind() {
	case reflect.String:
		c.kind, c.schema = kString, &schema{Type: "string", Format: formatTypes[t], check: formatChecks[formatTypes[t]]} // password, for Password
	case reflect.Bool:
		c.kind, c.schema = kBool, &schema{Type: "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		c.kind, c.schema = kInt, intSchema(t)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		c.kind, c.schema = kUint, intSchema(t)
	case reflect.Float32:
		c.kind, c.schema = kFloat, &schema{Type: "number", Format: "float"}
	case reflect.Float64:
		c.kind, c.schema = kFloat, &schema{Type: "number", Format: "double"}
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			c.kind, c.schema = kBytes, &schema{Type: "string", ContentEncoding: "base64"}
			break
		}
		c.kind = kSlice
		r.codecs[t] = c
		el, err := r.codecFor(t.Elem())
		if err != nil {
			delete(r.codecs, t)
			return nil, fmt.Errorf("%s: %w", t, err)
		}
		c.elem = el
		c.schema = &schema{Type: "array", Items: el.use()}
	case reflect.Map:
		c.kind = kMap
		c.keyMethods = v2Reads(t.Key())
		r.codecs[t] = c
		// A key type is judged as a value of that type is.
		kc, err := r.codecFor(t.Key())
		if err != nil {
			// Not wrapped: getavet names the key type alone.
			delete(r.codecs, t)
			return nil, err
		}
		c.key = kc
		el, err := r.codecFor(t.Elem())
		if err != nil {
			delete(r.codecs, t)
			return nil, fmt.Errorf("%s: %w", t, err)
		}
		c.elem = el
		c.schema = &schema{Type: "object", Additional: el.use()}
		// A key type with a format (Password, a FormatType) gives the keys
		// its schema, checked as a value of that type is.
		if (kc.kind == kString || kc.kind == kText) && kc.schema.Format != "" {
			c.schema.Keys = kc.schema
		}
	case reflect.Struct:
		c.kind = kStruct
		if t.Name() != "" {
			// CheckSchemaName is shared with getavet.
			if err := CheckSchemaName(t.String()); err != nil {
				return nil, err
			}
			c.name = componentName(t)
			if other, ok := r.names[c.name]; ok && other != t {
				return nil, fmt.Errorf("schema name %q is taken by both %s and %s",
					c.name, qualified(other), qualified(t))
			}
			r.names[c.name] = t
		}
		r.codecs[t] = c // before the fields, so a recursive type resolves
		if err := r.structFields(c); err != nil {
			delete(r.codecs, t)
			if c.name != "" {
				delete(r.names, c.name)
			}
			return nil, err
		}
	}
	// No default: CheckJSONType passes only the kinds above.
	switch c.kind {
	case kString, kBool, kInt, kUint, kFloat:
		c.direct = r.setsDirectly(t)
	}
	r.codecs[t] = c
	return c, nil
}

func intSchema(t reflect.Type) *schema {
	s := &schema{Type: "integer", goInt: t}
	switch t.Kind() {
	case reflect.Int32:
		s.Format = "int32"
	case reflect.Int64, reflect.Int:
		s.Format = "int64"
	case reflect.Int8, reflect.Int16:
		lo, hi := float64(int64(-1)<<(t.Bits()-1)), float64(int64(1)<<(t.Bits()-1)-1)
		s.Minimum, s.Maximum = &lo, &hi
	case reflect.Uint8, reflect.Uint16, reflect.Uint32:
		lo, hi := 0.0, float64(uint64(1)<<t.Bits()-1)
		s.Minimum, s.Maximum = &lo, &hi
	default: // uint, uint64
		lo := 0.0
		s.Minimum = &lo
	}
	return s
}

func (r *registry) structFields(c *codec) error {
	obj := &schema{Type: "object", Closed: true}
	seen := map[string]bool{}
	var walk func(t reflect.Type, prefix []int) error
	walk = func(t reflect.Type, prefix []int) error {
		for i := range t.NumField() {
			f := t.Field(i)
			index := append(append([]int(nil), prefix...), i)
			// CheckMemberField is shared with getavet.
			name, embedded, err := CheckMemberField(vetField(f), seen)
			if err != nil {
				return fmt.Errorf("%s.%w", c.t, err)
			}
			if embedded {
				if err := walk(f.Type, index); err != nil {
					return err
				}
				continue
			}
			if name == "" {
				continue
			}
			fc, err := r.field(f, name, index)
			if err != nil {
				return fmt.Errorf("%s.%s: %w", c.t, f.Name, err)
			}
			// A sealed or Nullable field tagged omitzero is optional.
			if _, opts, _ := strings.Cut(f.Tag.Get("json"), ","); opts == "omitzero" && (f.Type.Kind() == reflect.Interface || fc.c.kind == kNull) {
				fc.optional = true
			}
			c.fields = append(c.fields, fc)
			obj.Properties = append(obj.Properties, property{name, fc.use, fc.doc})
			if !fc.optional {
				obj.Required = append(obj.Required, name)
			}
		}
		return nil
	}
	if err := walk(c.t, nil); err != nil {
		return err
	}
	if err := CheckStructMembers(c.t.String(), c.t.NumField(), len(c.fields)); err != nil {
		return err
	}
	if len(c.fields) > 8 {
		c.byName = make(map[string]int, len(c.fields))
		for i, f := range c.fields {
			c.byName[f.json] = i
		}
	}
	c.schema = obj
	return nil
}

func (r *registry) field(f reflect.StructField, name string, index []int) (fieldCodec, error) {
	t := f.Type
	optional := false
	if t.Kind() == reflect.Pointer {
		optional, t = true, t.Elem()
	}
	fc, err := r.codecFor(t)
	if err != nil {
		return fieldCodec{}, err
	}
	// checkRead judges readability once the member's use is known.
	use, err := parseConstraints(f.Tag.Get("schema"), fc.use())
	if err != nil {
		return fieldCodec{}, err
	}
	if err := use.checkValues(fc); err != nil {
		return fieldCodec{}, err
	}
	return fieldCodec{json: name, index: index, c: fc, optional: optional, use: use, doc: f.Tag.Get("doc")}, nil
}

var pkgQualifier = regexp.MustCompile(`[\w./-]*\.`)

// componentName is a type's name with package paths removed: Page[pkg.User]
// becomes Page_User.
func componentName(t reflect.Type) string { return componentNameOf(t.Name()) }

// componentNameOf is componentName for a type name as reflect writes it
// ("lib.Page[example.com/app/lib.User]").
func componentNameOf(typ string) string {
	n := pkgQualifier.ReplaceAllString(typ, "")
	return strings.NewReplacer("[", "_", "]", "", ",", "_", " ", "", "*", "").Replace(n)
}

func qualified(t reflect.Type) string {
	if t.PkgPath() == "" {
		return t.String()
	}
	return t.PkgPath() + "." + t.Name()
}
