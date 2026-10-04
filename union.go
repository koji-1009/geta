package geta

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Variant is one case of a sealed type: a struct type and the discriminator
// value that selects it. Build it with [Case].
type Variant struct {
	tag string
	t   reflect.Type
}

// Case declares V as the variant a discriminator value of tag selects.
func Case[V any](tag string) Variant { return Variant{tag, reflect.TypeFor[V]()} }

// Union is a sealed type: an interface whose values are one of a closed set
// of struct variants, told apart by a discriminator member. It is documented
// as oneOf with a discriminator mapping. Build it with [Sealed] and pass it to
// [New] with [WithUnion].
type Union struct {
	t     reflect.Type
	disc  string
	cases []Variant
	enc   *json.Marshalers
	dec   *json.Unmarshalers
}

// Sealed declares the interface type T as a sealed type with the given
// discriminator member and variants:
//
//	var Events = geta.Sealed[Event]("kind", geta.Case[Created]("created"), geta.Case[Deleted]("deleted"))
//
// Each variant is a named struct that implements T and declares the
// discriminator as a required string member holding its tag. A request is
// checked against the variant its discriminator selects. A response value
// that is not a declared variant, or whose discriminator differs from its
// tag, is a defect.
func Sealed[T any](discriminator string, cases ...Variant) Union {
	u := Union{t: reflect.TypeFor[T](), disc: discriminator, cases: cases}
	tags := map[reflect.Type]string{}
	types := map[string]reflect.Type{}
	for _, c := range cases {
		tags[c.t], types[c.tag] = c.tag, c.t
	}
	// v2 calls this for every value implementing T. It checks the value is a
	// declared variant carrying its own tag, then lets v2 write it.
	u.enc = json.MarshalToFunc(func(_ *jsontext.Encoder, v T) error {
		// v is non-nil: v2 writes a nil pointer as null without calling a
		// marshaler, and nilUnion refuses one in a response first.
		rv := reflect.ValueOf(v)
		for rv.Kind() == reflect.Pointer {
			rv = rv.Elem()
		}
		tag, ok := tags[rv.Type()]
		if !ok {
			return fmt.Errorf("%s is not a declared variant of %s", rv.Type(), u.t)
		}
		if got := discriminatorOf(rv, discriminator); got != tag {
			return fmt.Errorf("%s has %s %q, but its variant's tag is %q", rv.Type(), discriminator, got, tag)
		}
		return errors.ErrUnsupported
	})
	u.dec = json.UnmarshalFromFunc(func(dec *jsontext.Decoder, v *T) error {
		raw, err := dec.ReadValue()
		if err != nil {
			return err
		}
		var members map[string]jsontext.Value
		if err := json.Unmarshal(raw, &members); err != nil {
			return err
		}
		var tag string
		if err := json.Unmarshal(members[discriminator], &tag); err != nil {
			return fmt.Errorf("%s: discriminator must be a string", discriminator)
		}
		vt, ok := types[tag]
		if !ok {
			return fmt.Errorf("%s: unknown %s %q", discriminator, u.t.Name(), tag)
		}
		nv := reflect.New(vt)
		if err := json.Unmarshal(raw, nv.Interface(), dec.Options()); err != nil {
			return err
		}
		*v = nv.Elem().Interface().(T)
		return nil
	})
	return u
}

// JSONOptions returns the encoding/json/v2 options that read and write the
// sealed type, for a client decoding this application's bodies.
func (u Union) JSONOptions() json.Options {
	return json.JoinOptions(json.WithMarshalers(u.enc), json.WithUnmarshalers(u.dec))
}

// WithUnion declares the sealed types the operations use. New fails on an
// interface type an input or output reaches without a declaration.
func WithUnion(us ...Union) Option {
	return func(c *config) { c.unions = append(c.unions, us...) }
}

// discriminatorOf reads the string member named disc from a variant value.
func discriminatorOf(v reflect.Value, disc string) string {
	for i := range v.NumField() {
		f := v.Type().Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" {
			name = f.Name
		}
		if f.Anonymous && f.Type.Kind() == reflect.Struct && f.Tag.Get("json") == "" {
			if s := discriminatorOf(v.Field(i), disc); s != "" {
				return s
			}
			continue
		}
		if name == disc && f.Type.Kind() == reflect.String {
			return v.Field(i).String()
		}
	}
	return ""
}

// variantCodec is one case of a sealed type, analysed.
type variantCodec struct {
	tag string
	c   *codec
}

// declareUnion records a WithUnion declaration; its variants are analysed
// when a type first reaches the interface.
func (r *registry) declareUnion(u Union) error {
	where := "geta.Sealed[" + qualified(u.t) + "]"
	switch {
	case u.t.Kind() != reflect.Interface:
		return fmt.Errorf("%s: %s is not an interface type", where, u.t)
	case u.disc == "":
		return fmt.Errorf("%s: the discriminator member must be named", where)
	case len(u.cases) == 0:
		return fmt.Errorf("%s: a sealed type needs at least one variant", where)
	}
	if _, dup := r.unions[u.t]; dup {
		return fmt.Errorf("%s: declared twice", where)
	}
	tags, types := map[string]bool{}, map[reflect.Type]bool{}
	for _, c := range u.cases {
		switch {
		case c.tag == "":
			return fmt.Errorf("%s: variant %s has an empty tag", where, c.t)
		case tags[c.tag]:
			return fmt.Errorf("%s: tag %q names two variants", where, c.tag)
		case types[c.t]:
			return fmt.Errorf("%s: %s is a variant twice", where, c.t)
		case c.t.Kind() != reflect.Struct || c.t.Name() == "":
			return fmt.Errorf("%s: variant %s is not a named struct type", where, c.t)
		case !c.t.Implements(u.t):
			return fmt.Errorf("%s: variant %s does not implement %s", where, c.t, u.t)
		}
		tags[c.tag], types[c.t] = true, true
	}
	r.unions[u.t] = u
	return nil
}

// unionCodec analyses a declared sealed type. Each variant must declare the
// discriminator as a required string member.
func (r *registry) unionCodec(t reflect.Type) (*codec, error) {
	u, ok := r.unions[t]
	if !ok {
		return nil, fmt.Errorf("interface type %s has no JSON form; declare it with geta.WithUnion", t)
	}
	if err := CheckSchemaName(t.String()); err != nil {
		return nil, err
	}
	c := &codec{t: t, kind: kOneOf, name: componentName(t), disc: u.disc}
	if other, ok := r.names[c.name]; ok && other != t {
		return nil, fmt.Errorf("schema name %q is taken by both %s and %s", c.name, qualified(other), qualified(t))
	}
	r.names[c.name] = t
	r.codecs[t] = c
	s := &schema{Discriminator: u.disc, Mapping: map[string]string{}}
	for _, k := range u.cases {
		vc, err := r.codecFor(k.t)
		if err != nil {
			delete(r.codecs, t)
			return nil, fmt.Errorf("%s variant %s: %w", t, k.t, err)
		}
		i := vc.fieldIndex([]byte(u.disc))
		switch {
		case i < 0:
			return nil, fmt.Errorf("%s variant %s does not declare the discriminator member %q", t, k.t, u.disc)
		case vc.fields[i].optional:
			return nil, fmt.Errorf("%s variant %s: the discriminator member %q is optional", t, k.t, u.disc)
		case vc.fields[i].c.kind != kString:
			return nil, fmt.Errorf("%s variant %s: the discriminator member %q must be a string", t, k.t, u.disc)
		case vc.fields[i].use.Default != nil:
			// A default would imply the discriminator may be omitted.
			return nil, fmt.Errorf("%s variant %s: the discriminator member %q takes no default", t, k.t, u.disc)
		}
		// The discriminator's schema must admit the variant's tag, or no
		// request could select the variant.
		if d := (&decoder{limits: unbounded, written: true}); !d.str(vc.fields[i].use, k.tag, "") {
			return nil, fmt.Errorf("%s variant %s: tag %q does not fit discriminator member %q: %s",
				t, k.t, k.tag, u.disc, d.errs[0].Message)
		}
		c.variants = append(c.variants, variantCodec{k.tag, vc})
		s.OneOf = append(s.OneOf, vc.use())
		s.Mapping[k.tag] = componentRef + vc.name
	}
	c.schema = s
	return c, nil
}

// unionOptions returns the marshalers and unmarshalers of every declared
// sealed type, plus timeUnmarshaler.
func (r *registry) unionOptions() (enc, dec json.Options) {
	var ms []*json.Marshalers
	us := []*json.Unmarshalers{timeUnmarshaler}
	for _, u := range r.unions {
		ms, us = append(ms, u.enc), append(us, u.dec)
	}
	return json.WithMarshalers(json.JoinMarshalers(ms...)), json.WithUnmarshalers(json.JoinUnmarshalers(us...))
}

// checkOneOf validates v against the variant its discriminator selects.
func (d *decoder) checkOneOf(c *codec, v any, path string) {
	m, ok := v.(map[string]any)
	if !ok {
		d.fail(path, "expected object, got %s", jsonType(v))
		return
	}
	raw, ok := m[c.disc]
	if !ok {
		d.fail(path+"."+c.disc, "missing required member")
		return
	}
	tag, ok := raw.(string)
	if !ok {
		d.fail(path+"."+c.disc, "discriminator must be a string")
		return
	}
	for _, vc := range c.variants {
		if vc.tag == tag {
			d.check(vc.c, vc.c.use(), v, path)
			return
		}
	}
	tags := make([]string, len(c.variants))
	for i, vc := range c.variants {
		tags[i] = vc.tag
	}
	d.fail(path+"."+c.disc, "unknown %s %q; one of %s", c.t.Name(), tag, strings.Join(tags, ", "))
}

// hasUnion reports whether a value of c can hold a sealed type.
func hasUnion(c *codec, seen map[*codec]bool) bool {
	if seen[c] {
		return false
	}
	seen[c] = true
	switch c.kind {
	case kOneOf:
		return true
	case kStruct:
		for _, f := range c.fields {
			if hasUnion(f.c, seen) {
				return true
			}
		}
	case kSlice, kMap, kNull:
		return hasUnion(c.elem, seen)
	}
	return false
}

// markUnions sets codec.unions on c and every codec it reaches, and returns
// c's value.
func markUnions(c *codec) bool {
	all := map[*codec]bool{}
	var reach func(c *codec)
	reach = func(c *codec) {
		if all[c] {
			return
		}
		all[c] = true
		for _, f := range c.fields {
			reach(f.c)
		}
		for _, vc := range c.variants {
			reach(vc.c)
		}
		if c.elem != nil {
			reach(c.elem)
		}
	}
	reach(c)
	for x := range all {
		x.unions = hasUnion(x, map[*codec]bool{})
	}
	return c.unions
}

// nilUnion reports a required sealed-type value left nil, which v2 would
// write as null where the schema allows only a variant. path is v's.
func nilUnion(c *codec, v reflect.Value, path string) error {
	n := findNilUnion(c, v)
	if n == nil {
		return nil
	}
	var b strings.Builder
	b.WriteString(path)
	for i := len(n.path) - 1; i >= 0; i-- {
		b.WriteString(n.path[i])
	}
	if n.ptr != nil {
		return fmt.Errorf("%s: a required %s holds a nil %s", b.String(), n.c.t, n.ptr)
	}
	return fmt.Errorf("%s: a required %s is nil", b.String(), n.c.t)
}

// nilFound is a sealed value of c that is nil or holds a nil pointer of type
// ptr. path lists its steps, innermost first.
type nilFound struct {
	c    *codec
	ptr  reflect.Type
	path []string
}

// findNilUnion walks v for nilUnion, skipping codecs without codec.unions.
func findNilUnion(c *codec, v reflect.Value) *nilFound {
	if !c.unions {
		return nil
	}
	switch c.kind {
	case kOneOf:
		if v.IsNil() {
			return &nilFound{c: c}
		}
		// A variant held by a nil pointer would also be written as null.
		e := v.Elem()
		for e.Kind() == reflect.Pointer {
			if e.IsNil() {
				return &nilFound{c: c, ptr: e.Type()}
			}
			e = e.Elem()
		}
		for _, vc := range c.variants {
			if vc.c.t == e.Type() {
				return findNilUnion(vc.c, e)
			}
		}
	case kNull:
		// Read the fields rather than call methods: a Nullable reached
		// through an unexported embedded struct cannot be Interface()d.
		if v, ok := nullHeld(v); ok {
			return findNilUnion(c.elem, v)
		}
	case kStruct:
		for _, f := range c.fields {
			if !f.c.unions {
				continue
			}
			fv := v.FieldByIndex(f.index)
			// An optional Nullable is a struct, not a pointer.
			if f.optional && fv.Kind() != reflect.Struct {
				if fv.IsNil() {
					continue
				}
				if fv.Kind() == reflect.Pointer {
					fv = fv.Elem()
				}
			}
			if n := findNilUnion(f.c, fv); n != nil {
				n.path = append(n.path, "."+f.json)
				return n
			}
		}
	case kSlice:
		for i := range v.Len() {
			if n := findNilUnion(c.elem, v.Index(i)); n != nil {
				n.path = append(n.path, "["+strconv.Itoa(i)+"]")
				return n
			}
		}
	case kMap:
		for it := v.MapRange(); it.Next(); {
			if n := findNilUnion(c.elem, it.Value()); n != nil {
				n.path = append(n.path, "."+it.Key().String())
				return n
			}
		}
	}
	return nil
}
