package geta

import (
	"bytes"
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
	"sync"
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
	// marshal builds the marshaler, refusing a value of a type that is no
	// variant only where foreign reports it to be one the App has no codec
	// for; nil foreign refuses none (enc, for a client).
	marshal func(foreign func(reflect.Type) bool) *json.Marshalers
}

// Sealed declares the interface type T as a sealed type with the given
// discriminator member and variants:
//
//	var Events = geta.Sealed[Event]("kind", geta.Case[Created]("created"), geta.Case[Deleted]("deleted"))
//
// Each variant is a named struct that implements T and declares the
// discriminator as a required string member holding its tag. A request is
// checked against the variant its discriminator selects. A response value
// whose discriminator differs from its variant's tag is a defect, and so is
// one of a type that is no variant and that no input or output of the App
// names. A type the App names in its own right, such as a struct field's
// type, is written as itself wherever it implements T. Sealed copies cases.
func Sealed[T any](discriminator string, cases ...Variant) Union {
	cases = slices.Clone(cases)
	u := Union{t: reflect.TypeFor[T](), disc: discriminator, cases: cases}
	tags := map[reflect.Type]string{}
	types := map[string]reflect.Type{}
	for _, c := range cases {
		tags[c.t], types[c.tag] = c.tag, c.t
	}
	// v2 calls this for every value implementing T, whatever the static type
	// it is written as. It checks a variant carries its own tag, then lets
	// v2 write it.
	u.marshal = func(foreign func(reflect.Type) bool) *json.Marshalers {
		return json.MarshalToFunc(func(_ *jsontext.Encoder, v T) error {
			// v is non-nil: v2 writes a nil pointer as null without calling
			// a marshaler, and nilUnion refuses one in a response first.
			rv := reflect.ValueOf(v)
			for rv.Kind() == reflect.Pointer {
				rv = rv.Elem()
			}
			tag, ok := tags[rv.Type()]
			if !ok {
				if foreign != nil && foreign(rv.Type()) {
					return fmt.Errorf("%s is not a declared variant of %s", rv.Type(), u.t)
				}
				return errors.ErrUnsupported
			}
			if got := discriminatorOf(rv, discriminator); got != tag {
				return fmt.Errorf("%s has %s %q, but its variant's tag is %q", rv.Type(), discriminator, got, tag)
			}
			return errors.ErrUnsupported
		})
	}
	u.enc = u.marshal(nil)
	s := &sealedReader{t: u.t, disc: discriminator, types: types}
	u.dec = json.UnmarshalFromFunc(func(dec *jsontext.Decoder, v *T) error {
		x, err := s.read(dec)
		if err != nil {
			return err
		}
		*v = x.(T)
		return nil
	})
	return u
}

// A sealedReader reads a sealed type's values (Sealed's unmarshaler).
type sealedReader struct {
	t     reflect.Type // the interface
	disc  string
	types map[string]reflect.Type
}

// read reads the next value of dec as the variant its discriminator selects.
//
// Reading the object whole, finding the discriminator, and reading the
// object again (whole) costs, for sealed values nested in one another, time
// proportional to the depth times the input. So where it can, read checks
// the outermost sealed object once, finds each discriminator with a
// lookahead, and reads each variant from dec itself. An error is reported
// where encoding/json/v2 reports one outside a sealed value, at its offset
// and pointer in dec's input, whichever way the value was read (whole moves
// its errors there).
func (s *sealedReader) read(dec *jsontext.Decoder) (any, error) {
	st, nested := sealedStates.Load(dec)
	if !nested {
		st = s.begin(dec)
		if st == nil {
			return s.whole(dec)
		}
		defer sealedStates.Delete(dec)
	}
	return s.stream(dec, st.(*sealedState))
}

// sealedStates holds, for a decoder inside an outermost sealed object read
// by sealedReader.stream, what the reads of the sealed values in it share.
var sealedStates sync.Map // *jsontext.Decoder → *sealedState

type sealedState struct {
	ahead lookahead
}

// begin checks that the object dec is about to read is valid JSON as dec
// would read it, all in dec's buffer, and returns the state of the reads in
// it; nil when it cannot tell, and whole reads the value instead.
func (s *sealedReader) begin(dec *jsontext.Decoder) any {
	if dec.PeekKind() != '{' {
		return nil
	}
	// The look-ahead refuses what these allow; whole reads the value under
	// them.
	opts := dec.Options()
	if v, _ := json.GetOption(opts, jsontext.AllowDuplicateNames); v {
		return nil
	}
	if v, _ := json.GetOption(opts, jsontext.AllowInvalidUTF8); v {
		return nil
	}
	buf := dec.UnreadBuffer()
	i := bytes.IndexByte(buf, '{')
	if i < 0 {
		return nil
	}
	// A decoder reads a bytes.Buffer in place; a bytes.Reader it would copy.
	raw, err := jsontext.NewDecoder(bytes.NewBuffer(buf[i:])).ReadValue()
	// dec refuses nesting past jsontext's ceiling of 10000, counted from its
	// own root.
	if err != nil || dec.StackDepth()+nesting(raw) >= 9990 {
		return nil
	}
	st := new(sealedState)
	sealedStates.Store(dec, st)
	return st
}

// stream reads the sealed object dec is about to read, inside the object
// begin checked, as the variant its discriminator selects, from dec itself.
func (s *sealedReader) stream(dec *jsontext.Decoder, st *sealedState) (any, error) {
	if dec.PeekKind() != '{' {
		return s.whole(dec) // whole reports why
	}
	buf, base := dec.UnreadBuffer(), int(dec.InputOffset())
	// begin held the nesting of the whole object to jsontext's ceiling.
	tag, ok := st.ahead.tag(buf, base, 0, s.disc, math.MaxInt)
	vt := s.types[string(tag)]
	if !ok || vt == nil {
		return s.whole(dec) // whole reports why
	}
	// Read from dec itself, an error is at its place in dec's input.
	nv := reflect.New(vt)
	if err := json.UnmarshalDecode(dec, nv.Interface()); err != nil {
		return nil, err
	}
	return nv.Elem().Interface(), nil
}

// inInput moves err, from reading on its own a value dec has just read
// whole, from the value's start to its place in dec's input: its pointer
// follows the value's, and its offset the value's.
func inInput(err error, dec *jsontext.Decoder, raw jsontext.Value) error {
	at, off := dec.StackPointer(), dec.InputOffset()-int64(len(raw))
	if se, ok := errors.AsType[*json.SemanticError](err); ok {
		se.JSONPointer, se.ByteOffset = at+se.JSONPointer, off+se.ByteOffset
	} else if se, ok := errors.AsType[*jsontext.SyntacticError](err); ok {
		se.JSONPointer, se.ByteOffset = at+se.JSONPointer, off+se.ByteOffset
	}
	return err
}

// whole reads the next value of dec whole, finds its discriminator, and
// reads the value again as the variant it selects. An error from reading it
// again is moved to its place in dec's input (inInput).
func (s *sealedReader) whole(dec *jsontext.Decoder) (any, error) {
	raw, err := dec.ReadValue()
	if err != nil {
		return nil, err
	}
	// Under dec's options, so what they allow (duplicate names, invalid
	// UTF-8) is read as the variant would read it.
	opts := dec.Options()
	var members map[string]jsontext.Value
	if err := json.Unmarshal(raw, &members, opts); err != nil {
		return nil, inInput(err, dec, raw)
	}
	var tag string
	if err := json.Unmarshal(members[s.disc], &tag, opts); err != nil {
		return nil, fmt.Errorf("%s: discriminator must be a string", s.disc)
	}
	vt, ok := s.types[tag]
	if !ok {
		return nil, fmt.Errorf("%s: unknown %s %q", s.disc, s.t.Name(), tag)
	}
	nv := reflect.New(vt)
	if err := json.Unmarshal(raw, nv.Interface(), opts); err != nil {
		return nil, inInput(err, dec, raw)
	}
	return nv.Elem().Interface(), nil
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
	if u.t == nil {
		return errors.New("WithUnion: a zero geta.Union; build one with geta.Sealed")
	}
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
	if err := checkSchemaName(t.String()); err != nil {
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
//
// A marshaler refuses a value of a type that is no variant of its sealed
// type only if the App has no codec for the type: one it has is written
// where the App names it, and v2 calls every marshaler whose interface the
// value implements. Once New returns, the codecs are read only. The order
// is fixed, so every assembly writes alike.
func (r *registry) unionOptions() (enc, dec json.Options) {
	var ms []*json.Marshalers
	us := []*json.Unmarshalers{timeUnmarshaler}
	foreign := func(t reflect.Type) bool {
		_, named := r.codecs[t]
		return !named
	}
	for _, t := range slices.SortedFunc(maps.Keys(r.unions), func(a, b reflect.Type) int { return strings.Compare(qualified(a), qualified(b)) }) {
		u := r.unions[t]
		ms, us = append(ms, u.marshal(foreign)), append(us, u.dec)
	}
	return json.WithMarshalers(json.JoinMarshalers(ms...)), json.WithUnmarshalers(json.JoinUnmarshalers(us...))
}

// checkOneOf validates v against the variant its discriminator selects.
func (d *decoder) checkOneOf(c *codec, v any, at vpath) {
	p := &at
	m, ok := v.(map[string]any)
	if !ok {
		d.failAt(p, "expected object, got %s", jsonType(v))
		return
	}
	raw, ok := m[c.disc]
	if !ok {
		d.failAt(p.member(c.disc), "missing required member")
		return
	}
	tag, ok := raw.(string)
	if !ok {
		d.failAt(p.member(c.disc), "discriminator must be a string")
		return
	}
	for _, vc := range c.variants {
		if vc.tag == tag {
			d.checkAt(vc.c, vc.c.use(), v, at)
			return
		}
	}
	tags := make([]string, len(c.variants))
	for i, vc := range c.variants {
		tags[i] = vc.tag
	}
	d.failAt(p.member(c.disc), "unknown %s %q; one of %s", c.t.Name(), d.value(tag), strings.Join(tags, ", "))
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
