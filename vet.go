package geta

import (
	"cmp"
	"fmt"
	"maps"
	"mime"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/koji-1009/geta/internal/vet"
)

// The rules getavet and getatest read through internal/vet.
func init() {
	vet.CheckSchemaTag = checkTag
	vet.CheckRequestSchemaTag = checkRequestTag
	vet.FieldOf = vetField
	vet.CheckInputType = checkInputType
	vet.CheckInputField = checkInputField
	vet.CheckDeepObjectField = checkDeepObjectField
	vet.CheckFormField = checkFormField
	vet.CheckEnvelopeField = checkEnvelopeField
	vet.EnvelopeEmbedded = envelopeEmbedded
	vet.CheckProblemType = checkProblemType
	vet.CheckProblemField = checkProblemField
	vet.CheckProblemMember = checkProblemMember
	vet.CheckEnvelopeStatus = checkEnvelopeStatus
	vet.CheckDocTimeout = checkDocTimeout
	vet.CheckMethodBody = checkMethodBody
	vet.CheckConditionalWrite = checkConditionalWrite
	vet.CheckSuccessStatus = checkSuccessStatus
	vet.CheckBodylessStatus = checkBodylessStatus
	vet.CheckSpecialStatus = checkSpecialStatus
	vet.CheckMemberField = checkMemberField
	vet.CheckStructMembers = checkStructMembers
	vet.DeclaresDefault = declaresDefault
	vet.CheckSchemaName = checkSchemaName
	vet.CheckSelfHolding = checkSelfHolding
	vet.CheckJSONType = checkJSONType
}

// vetKinds maps the kind names getavet passes for Go basic kinds, []byte,
// and format types ("time.Time", "geta.Date") to their types.
var vetKinds = func() map[string]reflect.Type {
	m := map[string]reflect.Type{
		"string": reflect.TypeFor[string](), "bool": reflect.TypeFor[bool](),
		"int": reflect.TypeFor[int](), "int8": reflect.TypeFor[int8](), "int16": reflect.TypeFor[int16](),
		"int32": reflect.TypeFor[int32](), "int64": reflect.TypeFor[int64](),
		"uint": reflect.TypeFor[uint](), "uint8": reflect.TypeFor[uint8](), "uint16": reflect.TypeFor[uint16](),
		"uint32": reflect.TypeFor[uint32](), "uint64": reflect.TypeFor[uint64](),
		"float32": reflect.TypeFor[float32](), "float64": reflect.TypeFor[float64](),
		"[]byte": reflect.TypeFor[[]byte](),
	}
	for t := range formatTypes {
		m[t.String()] = t
	}
	return m
}()

// checkTag reports whether a schema tag is valid on a value of kind.
// It applies geta.New's rule. kind is one of:
//
//   - a Go basic kind: "string", "int8", "float64", ...
//   - "[]byte"
//   - a format type by qualified name: "time.Time", "uuid.UUID",
//     "geta.Date", "geta.Password", ...
//   - "text": another encoding.TextMarshaler
//   - "format": a TextMarshaler that is a [FormatType]
//   - "geta.File": a multipart file, which takes no keyword
//   - "struct": a named struct; "object": an unnamed struct
//   - "slice" or "map": a slice or map whose element kind is none of these;
//     items. and additionalProperties. keywords are left to geta.New
//   - "?K" for a geta.Nullable of K ("?int8")
//   - "[]K" for a slice of K ("[]string", "[][]int8"); items. keywords are
//     judged as K's
//   - "map[string]K" for a map of K; additionalProperties. keywords are
//     judged as K's. A key type with a format is named in place of
//     "string": "map[format]int", "map[geta.Password]int", or
//     "map[format]" when the value kind is unknown. propertyNames. keywords
//     are judged as the key's.
//
// The rule covers every use, responses included; values a request reads
// are also subject to checkRequestTag. checkTag does not see
// the program's [Limits], and it does not check a key enum against the key
// type's methods; geta.New does both.
func checkTag(tag, kind string) error {
	_, err := checkSchemaTag(tag, kind)
	return err
}

// checkRequestTag is checkTag for a value a request reads: a
// parameter, a form field, or a member of a request body type. Alongside a
// pattern, it also refuses a maxLength, minLength, enum member, default, or
// example beyond the fixed pattern-validation ceiling of 4096 code points.
// It applies geta.New's rule.
func checkRequestTag(tag, kind string) error {
	s, err := checkSchemaTag(tag, kind)
	if err != nil {
		return err
	}
	return s.withinCeiling()
}

func checkSchemaTag(tag, kind string) (*schema, error) {
	base, c, err := kindSchema(kind)
	if err != nil {
		return nil, err
	}
	s, err := parseConstraints(tag, base)
	if err != nil {
		return nil, err
	}
	if err := s.checkValues(c); err != nil {
		return nil, err
	}
	return s, nil
}

// kindSchema returns the schema of a value of kind, and its codec when kind
// names one type (nil for "text", "format", and the like).
func kindSchema(kind string) (*schema, *codec, error) {
	if inner, ok := strings.CutPrefix(kind, "?"); ok {
		s, c, err := kindSchema(inner)
		if err != nil {
			return nil, nil, err
		}
		s = s.clone()
		s.Null = true
		return s, c, nil
	}
	if elem, ok := strings.CutPrefix(kind, "[]"); ok && kind != "[]byte" {
		s, c, err := kindSchema(elem)
		if err != nil {
			return nil, nil, err
		}
		var sc *codec
		if c != nil {
			sc = &codec{kind: kSlice, elem: c}
		}
		return &schema{Type: "array", Items: s}, sc, nil
	}
	if rest, ok := strings.CutPrefix(kind, "map["); ok {
		// map[key]value; an empty value leaves additionalProperties. to
		// geta.New.
		key, value, ok := strings.Cut(rest, "]")
		if !ok {
			return nil, nil, fmt.Errorf("geta: unknown kind %q", kind)
		}
		ks, _, err := kindSchema(key)
		if err != nil {
			return nil, nil, err
		}
		if ks.Type != "string" || ks.binary || ks.ContentEncoding != "" || ks.Null {
			return nil, nil, fmt.Errorf("geta: kind %q has a non-string key", kind)
		}
		var keys *schema
		if ks.Format != "" {
			keys = ks
		}
		if value == "" {
			return &schema{Type: "object", unjudged: true, Keys: keys}, nil, nil
		}
		s, c, err := kindSchema(value)
		if err != nil {
			return nil, nil, err
		}
		var mc *codec
		if c != nil {
			mc = &codec{kind: kMap, elem: c}
		}
		return &schema{Type: "object", Additional: s, Keys: keys}, mc, nil
	}
	switch kind {
	case "text":
		return &schema{Type: "string"}, nil, nil
	case "format":
		// SchemaFormat's result is known only at run time.
		return &schema{Type: "string", Format: "(SchemaFormat)"}, nil, nil
	case "slice":
		return &schema{Type: "array", unjudged: true}, nil, nil
	case "map":
		return &schema{Type: "object", unjudged: true}, nil, nil
	case "object":
		return &schema{Type: "object", Closed: true}, nil, nil
	case "struct":
		// A named struct is a component reference.
		return &schema{Ref: kind}, nil, nil
	case fileKind:
		return fileSchema(), nil, nil
	}
	t, ok := vetKinds[kind]
	if !ok {
		return nil, nil, fmt.Errorf("geta: unknown kind %q", kind)
	}
	// Every kind vetKinds holds has a codec (TestEveryVetKindHasACodec).
	c, _ := newRegistry().codecFor(t)
	return c.schema, c, nil
}

// paramCarried reports whether a parameter's schema tag admits only values
// its location can carry (carried). A tag checkTag refuses is left to
// it. kind is one paramType accepted.
func paramCarried(loc, name, tag, kind string) error {
	base, c, _ := kindSchema(kind)
	use, err := parseConstraints(tag, base)
	if err != nil {
		return nil
	}
	held, err := carried(loc, name, use)
	if err != nil {
		return err
	}
	// Values the schema refuses anyway are checkTag's to report.
	if use.checkValues(c) == nil {
		return held.checkValues(c)
	}
	return nil
}

// v2Methods are the interfaces encoding/json/v2 uses instead of a type's
// fields.
var v2Methods = append(append([]reflect.Type(nil), jsonMethods...), textMarshaler, textAppender, textUnmarshaler)

// vetField describes f, leaving Kind for the caller to set once the codec
// is known.
func vetField(f reflect.StructField) vet.Field {
	t := f.Type
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	methods := v2Reads(t)
	_, null := nullableOf(t)
	_, deferred := deferredOf(t)
	return vet.Field{Name: f.Name, Exported: f.IsExported(), Embedded: f.Anonymous, Tag: f.Tag,
		Type: f.Type.String(), Pointer: f.Type.Kind() == reflect.Pointer, Struct: f.Type.Kind() == reflect.Struct,
		Object:    t.Kind() == reflect.Struct,
		Interface: f.Type.Kind() == reflect.Interface, Cookie: f.Type == cookieType,
		Text: t.Implements(textMarshaler) || reflect.PointerTo(t).Implements(textMarshaler) ||
			reflect.PointerTo(t).Implements(textUnmarshaler),
		Methods: methods, Nullable: null, Deferred: deferred}
}

// v2Reads reports whether t or *t has a method encoding/json/v2 uses
// instead of t's fields or kind.
func v2Reads(t reflect.Type) bool {
	for _, m := range v2Methods {
		if t.Implements(m) || reflect.PointerTo(t).Implements(m) {
			return true
		}
	}
	return false
}

// vetKind returns c's kind as vet.Field.Kind names it.
func vetKind(c *codec) string {
	switch c.kind {
	case kString:
		return "string"
	case kBool:
		return "bool"
	case kInt, kUint, kFloat:
		return c.t.Kind().String()
	case kText:
		return "text"
	case kBytes:
		return "[]byte"
	case kSlice:
		return "[]" + vetKind(c.elem)
	case kMap:
		return "map"
	case kStruct:
		return "struct"
	case kJSON:
		return "json"
	case kNull:
		return "nullable"
	}
	return "union"
}

// scalarKind reports whether a value of kind is a single text, as a
// parameter or header is.
func scalarKind(kind string) bool {
	switch kind {
	case "string", "bool", "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "float32", "float64", "text", "format":
		return true
	}
	t, ok := vetKinds[kind]
	return ok && formatTypes[t] != ""
}

// inputLocation returns the location and name an input field's tag binds;
// loc is "" for an untagged field.
func inputLocation(f vet.Field) (loc, name string, err error) {
	for _, l := range paramLocations {
		if v, ok := f.Tag.Lookup(l); ok {
			if loc != "" {
				return "", "", fmt.Errorf("%s has both %s and %s tags", f.Name, loc, l)
			}
			loc, name = l, v
		}
	}
	return loc, name, nil
}

// checkInputType reports whether geta.New accepts typ as an operation's
// input type. It applies geta.New's rule.
func checkInputType(typ string, isStruct bool) error {
	if !isStruct {
		return fmt.Errorf("input type %s is not a struct", typ)
	}
	return nil
}

// checkInputField reports whether geta.New accepts f as an input field. walk
// reports that f is an embedded struct whose fields the caller must check
// in turn. seen records what earlier fields bind and is updated with f. The
// error begins with the field's name. It applies geta.New's rule.
func checkInputField(f vet.Field, seen map[string]string) (walk bool, err error) {
	loc, name, err := inputLocation(f)
	if err != nil {
		return false, err
	}
	if f.Deferred && (loc != "" || f.Embedded) {
		switch {
		case loc != "body" || name != "json":
			return false, fmt.Errorf("%s: a geta.Deferred is a body:\"json\" field", f.Name)
		case f.Pointer:
			return false, fmt.Errorf("%s: the body is a pointer to a geta.Deferred; for an optional body, the Deferred holds a pointer", f.Name)
		}
	}
	if loc == "" {
		switch {
		case f.Embedded && f.Struct:
			// A text type's fields would bind nothing.
			if f.Text {
				return false, embeddedText(f, "tag it path, query, header, or cookie")
			}
			if err := embeddedSchema(f); err != nil {
				return false, err
			}
			return true, nil
		case f.Embedded && f.Pointer:
			return false, fmt.Errorf("%s: embedded pointer types are not supported in an input; embed %s",
				f.Name, strings.TrimPrefix(f.Type, "*"))
		case !f.Exported:
			return false, nil
		}
		return false, fmt.Errorf("%s has no path, query, header, cookie, or body tag", f.Name)
	}
	if !f.Exported {
		return false, fmt.Errorf("%s is tagged %s but unexported", f.Name, loc)
	}
	if loc == "body" {
		raw := strings.Contains(name, "/")
		if _, ok := bodyMediaTypes[name]; !ok && !raw {
			return false, fmt.Errorf("%s: unknown body tag %q", f.Name, name)
		}
		if raw {
			if err := rawMediaType(f.Name, name, false); err != nil {
				return false, err
			}
			if err := rawInput(f.Name, name, f.Type); err != nil {
				return false, err
			}
		}
		if _, dup := seen["body"]; dup {
			return false, fmt.Errorf("%s: a second body field", f.Name)
		}
		seen["body"] = f.Name
		if declaresDefault(f.Tag.Get("schema")) {
			return false, fmt.Errorf("%s: a body takes no default", f.Name)
		}
		if raw {
			if _, ok := f.Tag.Lookup("schema"); ok {
				return false, fmt.Errorf("%s: a body:%q field takes no schema tag", f.Name, name)
			}
			return false, nil
		}
		if mt, ok := f.Tag.Lookup("mediatype"); ok {
			if name != "json" {
				return false, fmt.Errorf("%s: a mediatype tag goes only with body:\"json\"", f.Name)
			}
			if err := jsonMediaType(f.Name, mt); err != nil {
				return false, err
			}
		}
		if name != "json" {
			if _, ok := f.Tag.Lookup("schema"); ok {
				return false, fmt.Errorf("%s: a body:%q field takes no schema tag", f.Name, name)
			}
			if f.Kind != "" {
				if err := formBody(f.Name, name, strings.TrimPrefix(f.Type, "*"), f.Kind); err != nil {
					return false, err
				}
			}
		}
		return false, nil
	}
	if name == "" {
		return false, fmt.Errorf("%s: empty %s name", f.Name, loc)
	}
	if _, ok := f.Tag.Lookup("mediatype"); ok {
		return false, fmt.Errorf("%s: a mediatype tag goes only with body:\"json\"", f.Name)
	}
	// A cookie or header name that is not a token (RFC 9110 §5.1) never
	// reaches the handler, so such a parameter could never arrive.
	if loc == "cookie" && !validCookieName(name) {
		return false, fmt.Errorf("%s: %q is not a valid cookie name", f.Name, name)
	}
	if loc == "header" && !validHeaderName(name) {
		return false, fmt.Errorf("%s: %q is not a valid header name", f.Name, name)
	}
	key := loc + " " + name
	if loc == "header" {
		key = loc + " " + http.CanonicalHeaderKey(name)
	}
	if prev, dup := seen[key]; dup {
		return false, fmt.Errorf("%s binds %s %q, already bound by %s", f.Name, loc, name, prev)
	}
	if loc == "query" {
		deep := f.Object && !f.Methods && strings.TrimPrefix(f.Type, "*") != fileKind
		if err := deepKeys(f.Name, name, deep, seen); err != nil {
			return false, err
		}
		if deep {
			seen["deep "+name] = f.Name
		}
	}
	seen[key] = f.Name
	if loc == "path" && f.Pointer {
		return false, fmt.Errorf("%s: path parameter %q is a pointer", f.Name, name)
	}
	if loc == "path" && declaresDefault(f.Tag.Get("schema")) {
		return false, fmt.Errorf("%s: path parameter %q takes no default", f.Name, name)
	}
	if err := pointerDefault(f); err != nil {
		return false, err
	}
	if loc == "path" && !validWildcard(name) {
		return false, fmt.Errorf("%s: path parameter name %q is not a Go identifier", f.Name, name)
	}
	if f.Kind != "" {
		if err := paramType(loc, name, strings.TrimPrefix(f.Type, "*"), f.Kind); err != nil {
			return false, fmt.Errorf("%s: %w", f.Name, err)
		}
		if err := paramCarried(loc, name, f.Tag.Get("schema"), f.Kind); err != nil {
			return false, fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	return false, nil
}

// deepKeys refuses a query parameter that overlaps a deepObject's keys: a
// deepObject d binds every d[...], so a parameter named d[min] would be
// bound twice. seen is checkInputField's, holding "deep d" for each
// deepObject and "query q" for each query parameter.
func deepKeys(field, name string, deep bool, seen map[string]string) error {
	for _, k := range slices.Sorted(maps.Keys(seen)) {
		switch {
		case strings.HasPrefix(k, "deep "):
			d := k[len("deep "):]
			if strings.HasPrefix(name, d+"[") {
				return fmt.Errorf("%s binds query parameter %q, which deepObject %q (%s) also binds", field, name, d, seen[k])
			}
		case deep && strings.HasPrefix(k, "query "):
			q := k[len("query "):]
			if strings.HasPrefix(q, name+"[") {
				return fmt.Errorf("%s: deepObject %q also binds query parameter %q (%s)", field, name, q, seen[k])
			}
		}
	}
	return nil
}

// paramType reports whether a parameter at loc may have type typ of kind.
func paramType(loc, name, typ, kind string) error {
	switch {
	case scalarKind(kind):
	case strings.HasPrefix(kind, "[]") && scalarKind(kind[2:]) && (loc == "query" || loc == "header"):
	case kind == "struct" && loc == "query":
		// A deepObject (checkDeepObjectField).
	case loc == "query":
		return fmt.Errorf("%s parameter %q has unsupported type %s", loc, name, typ)
	default:
		return fmt.Errorf("%s parameter %q has unsupported type %s", loc, name, typ)
	}
	return nil
}

// checkDeepObjectField reports whether geta.New accepts f as a field of a
// deepObject query parameter's struct. A field tagged form is one member,
// sent as name[member]=value; it follows checkFormField's rules but must
// be a scalar, since OpenAPI defines no array member of a deepObject. walk
// reports an untagged embedded struct for the caller to check in turn. It
// applies geta.New's rule.
func checkDeepObjectField(f vet.Field, seen map[string]string) (walk bool, err error) {
	walk, err = checkFormField(f, false, seen)
	if err != nil || walk {
		return walk, err
	}
	if name, ok := f.Tag.Lookup("form"); ok && f.Kind != "" {
		if err := deepMember(name, strings.TrimPrefix(f.Type, "*"), f.Kind); err != nil {
			return false, fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	return false, nil
}

// deepMember reports whether a deepObject member may have type typ of kind,
// which formType has accepted.
func deepMember(name, typ, kind string) error {
	if !scalarKind(kind) {
		return fmt.Errorf("deepObject member %q has non-scalar type %s", name, typ)
	}
	return nil
}

// bodyMediaTypes maps an input's body tag encodings to their media types.
var bodyMediaTypes = map[string]string{
	"json":      "application/json",
	"form":      "application/x-www-form-urlencoded",
	"multipart": "multipart/form-data",
}

// jsonMediaType reports whether mt, a body:"json" field's mediatype tag, is a
// JSON media type the body may be sent as instead of its default
// (application/json; application/merge-patch+json on PATCH): application/json
// or application/*+json (RFC 6839), canonical, without parameters. The error
// begins with field.
func jsonMediaType(field, mt string) error {
	essence, params, err := mime.ParseMediaType(mt)
	switch {
	case err != nil:
		return fmt.Errorf("%s: mediatype %q is not a media type: %v", field, mt, err)
	case len(params) > 0:
		return fmt.Errorf("%s: mediatype %q has parameters; name the media type alone", field, mt)
	case essence != mt:
		return fmt.Errorf("%s: mediatype %q is not canonical; write %q", field, mt, essence)
	case essence != "application/json" && !(strings.HasPrefix(essence, "application/") && strings.HasSuffix(essence, "+json")):
		return fmt.Errorf("%s: mediatype %q is not JSON (application/json or application/*+json)", field, mt)
	}
	return nil
}

// rawMediaType reports whether body tag mt is valid for a raw body of an
// input or (output) an envelope: a single media type, written in
// mime.FormatMediaType form, that no geta encoding handles. The error begins
// with field.
func rawMediaType(field, mt string, output bool) error {
	essence, params, err := mime.ParseMediaType(mt)
	switch {
	case err != nil:
		return fmt.Errorf("%s: body tag %q is not a media type: %v", field, mt, err)
	case strings.Contains(essence, "*"):
		return fmt.Errorf("%s: body tag %q is a media range", field, mt)
	case mime.FormatMediaType(essence, params) != mt:
		return fmt.Errorf("%s: body tag %q is not canonical; write %q", field, mt,
			mime.FormatMediaType(essence, params))
	case essence == "application/json" || strings.HasPrefix(essence, "application/") && strings.HasSuffix(essence, "+json"):
		return fmt.Errorf("%s: body tag %q is JSON; use body:\"json\"", field, mt)
	case !output && essence == bodyMediaTypes["form"]:
		return fmt.Errorf("%s: body tag %q is a form; use body:\"form\"", field, mt)
	case !output && essence == bodyMediaTypes["multipart"]:
		return fmt.Errorf("%s: body tag %q is a multipart form; use body:\"multipart\"", field, mt)
	case output && essence == "text/event-stream":
		return fmt.Errorf("%s: body tag %q is an event stream; return a *geta.Stream", field, mt)
	}
	return nil
}

// rawInput reports whether typ may hold an input's raw body: []byte,
// *[]byte (optional), or io.Reader.
func rawInput(field, mt, typ string) error {
	switch typ {
	case "[]uint8", "*[]uint8", "io.Reader":
		return nil
	}
	return fmt.Errorf("%s: a body:%q field has type %s, not []byte, *[]byte, or io.Reader", field, mt, typ)
}

// rawOutput reports whether typ may hold an envelope's raw body: []byte, or
// an io.Reader geta copies (and closes, if an io.Closer).
func rawOutput(field, mt, typ string) error {
	switch typ {
	case "[]uint8", "io.Reader":
		return nil
	}
	return fmt.Errorf("%s: a body:%q field has type %s, not []byte or io.Reader", field, mt, typ)
}

// formBody reports whether a body:"form" or body:"multipart" field may have
// type typ of kind; it must be a struct.
func formBody(field, enc, typ, kind string) error {
	if kind == "struct" {
		return nil
	}
	return fmt.Errorf("%s: a body:%q field has type %s, not a struct", field, enc, typ)
}

// checkFormField reports whether geta.New accepts f as a field of a form
// body's struct: body:"form", or body:"multipart" when multipart is set. A
// field is tagged form with the field name it binds, and is a scalar or a
// slice of scalars; in a multipart body, also a geta.File or []geta.File
// (getavet passes those as Kind). walk reports an untagged embedded struct
// for the caller to check in turn. seen records the names bound so far and
// is updated with f's. The error begins with the field's name. It applies
// geta.New's rule.
func checkFormField(f vet.Field, multipart bool, seen map[string]string) (walk bool, err error) {
	name, tagged := f.Tag.Lookup("form")
	if !tagged {
		switch {
		case f.Embedded && f.Struct:
			if f.Text {
				return false, embeddedText(f, "tag it form")
			}
			if err := embeddedSchema(f); err != nil {
				return false, err
			}
			return true, nil
		case f.Embedded && f.Pointer:
			return false, fmt.Errorf("%s: embedded pointer types are not supported in a form; embed %s",
				f.Name, strings.TrimPrefix(f.Type, "*"))
		case !f.Exported:
			return false, nil
		}
		return false, fmt.Errorf("%s has no form tag", f.Name)
	}
	if !f.Exported {
		return false, fmt.Errorf("%s is tagged form but unexported", f.Name)
	}
	if name == "" {
		return false, fmt.Errorf("%s: empty form field name", f.Name)
	}
	if prev, dup := seen[name]; dup {
		return false, fmt.Errorf("%s binds form field %q, already bound by %s", f.Name, name, prev)
	}
	seen[name] = f.Name
	if err := pointerDefault(f); err != nil {
		return false, err
	}
	if f.Kind != "" {
		if err := formType(name, multipart, strings.TrimPrefix(f.Type, "*"), f.Kind, f.Tag.Get("schema")); err != nil {
			return false, fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	return false, nil
}

// formType reports whether form field name may have type typ of kind with
// schema tag tag.
func formType(name string, multipart bool, typ, kind, tag string) error {
	switch {
	case kind == fileKind || kind == "[]"+fileKind:
		if !multipart {
			return fmt.Errorf("form field %q has file type %s outside a multipart body", name, typ)
		}
		// geta does not compare file contents.
		if kind != fileKind {
			if s, err := parseConstraints(tag, &schema{Type: "array"}); err == nil && s.UniqueItems {
				return fmt.Errorf("form field %q: uniqueItems on files", name)
			}
		}
	case scalarKind(kind), strings.HasPrefix(kind, "[]") && scalarKind(kind[2:]):
	default:
		return fmt.Errorf("form field %q has unsupported type %s", name, typ)
	}
	return nil
}

// checkEnvelopeField reports whether geta.New accepts f as a field of an
// output envelope. The fields of an accepted untagged embedded struct
// (envelopeEmbedded) are written by their own tags; the caller checks them
// in turn. seen records the status, body, header, and cookie names taken so
// far and is updated with f's. The error begins with the field's name. It
// applies geta.New's rule.
func checkEnvelopeField(f vet.Field, seen map[string]bool) error {
	h, isH := f.Tag.Lookup("header")
	ck, isC := f.Tag.Lookup("cookie")
	b, isB := f.Tag.Lookup("body")
	st, isS := f.Tag.Lookup("status")
	_, schema := f.Tag.Lookup("schema")
	untagged := !isH && !isC && !isB && !isS
	switch {
	case isS && f.Exported:
		if isH || isC || isB {
			return fmt.Errorf("%s: a status field takes no header, cookie, or body tag", f.Name)
		}
		if seen["status"] {
			return fmt.Errorf("%s: a second status field", f.Name)
		}
		if f.Type != "int" {
			return fmt.Errorf("%s: a status field is an int, not %s", f.Name, f.Type)
		}
		if _, err := successStatuses(st); err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
		if _, doc := f.Tag.Lookup("doc"); doc || schema {
			return fmt.Errorf("%s: a status field takes no schema or doc tag", f.Name)
		}
		seen["status"] = true
	case untagged && f.Embedded && f.Pointer:
		return fmt.Errorf("%s: embedded pointer types are not supported in an envelope; embed %s",
			f.Name, strings.TrimPrefix(f.Type, "*"))
	case untagged && f.Embedded && f.Struct:
		// A text type's fields would write nothing.
		if f.Text {
			return embeddedText(f, "tag it header")
		}
		if err := embeddedSchema(f); err != nil {
			return err
		}
	case !f.Exported:
		if isH || isC || isB || isS {
			return fmt.Errorf("%s is tagged but unexported", f.Name)
		}
	case isH:
		if h == "" || seen["h "+http.CanonicalHeaderKey(h)] {
			return fmt.Errorf("%s: empty or repeated header name %q", f.Name, h)
		}
		if !validHeaderName(h) {
			return fmt.Errorf("%s: %q is not a valid header name", f.Name, h)
		}
		if why, ok := reservedHeaders[http.CanonicalHeaderKey(h)]; ok {
			return fmt.Errorf("%s: header %q %s", f.Name, h, why)
		}
		if schema {
			return fmt.Errorf("%s: an output header takes no schema tag", f.Name)
		}
		seen["h "+http.CanonicalHeaderKey(h)] = true
		if f.Kind != "" {
			if err := headerType(h, strings.TrimPrefix(f.Type, "*"), f.Kind); err != nil {
				return fmt.Errorf("%s: %w", f.Name, err)
			}
		}
	case isC:
		if !f.Cookie {
			return fmt.Errorf("%s: a cookie field has type *http.Cookie, not %s", f.Name, f.Type)
		}
		if ck == "" || seen["c "+ck] {
			return fmt.Errorf("%s: empty or repeated cookie name %q", f.Name, ck)
		}
		if !validCookieName(ck) {
			return fmt.Errorf("%s: %q is not a valid cookie name", f.Name, ck)
		}
		if schema {
			return fmt.Errorf("%s: a cookie field takes no schema tag", f.Name)
		}
		if _, doc := f.Tag.Lookup("doc"); doc {
			return fmt.Errorf("%s: a cookie field takes no doc tag", f.Name)
		}
		seen["c "+ck] = true
	case isB:
		raw := b != "json"
		if raw && !strings.Contains(b, "/") {
			return fmt.Errorf("%s: unknown body tag %q", f.Name, b)
		}
		if raw {
			if err := rawMediaType(f.Name, b, true); err != nil {
				return err
			}
		}
		if seen["body"] {
			return fmt.Errorf("%s: a second body field", f.Name)
		}
		if schema {
			return fmt.Errorf("%s: an output body takes no schema tag", f.Name)
		}
		if _, doc := f.Tag.Lookup("doc"); doc {
			return fmt.Errorf("%s: an output body takes no doc tag", f.Name)
		}
		if f.Pointer {
			return fmt.Errorf("%s: the body field is a pointer; use %s", f.Name, strings.TrimPrefix(f.Type, "*"))
		}
		if raw {
			if err := rawOutput(f.Name, b, f.Type); err != nil {
				return err
			}
		}
		seen["body"] = true
	default:
		return fmt.Errorf("%s: an envelope field needs a header, cookie, or body tag", f.Name)
	}
	return nil
}

// envelopeEmbedded reports whether f, an envelope field checkEnvelopeField
// accepts, is an embedded struct whose fields are written by their own tags.
func envelopeEmbedded(f vet.Field) bool {
	for _, l := range envelopeTags {
		if _, ok := f.Tag.Lookup(l); ok {
			return false
		}
	}
	return f.Embedded && f.Struct && !f.Text
}

// checkProblemType reports whether geta.New accepts typ as the return type
// of a geta.OnAsProblem row's function, or as that envelope's body: it must
// be a struct. It applies geta.New's rule.
func checkProblemType(typ string, isStruct bool) error {
	if !isStruct {
		return fmt.Errorf("geta.OnAsProblem: %s is not a struct", typ)
	}
	return nil
}

// checkProblemField reports whether geta.New accepts f, already accepted by
// checkEnvelopeField, in an envelope a geta.OnAsProblem row's function
// returns: only header fields and a body:"json" are allowed, not a cookie or
// status field. It applies geta.New's rule.
func checkProblemField(f vet.Field) error {
	switch {
	case !f.Exported:
		return nil
	case hasTag(f, "cookie"):
		return fmt.Errorf("%s: a problem's envelope takes no cookie field", f.Name)
	case hasTag(f, "status"):
		return fmt.Errorf("%s: a problem's envelope takes no status field", f.Name)
	case hasTag(f, "body") && f.Tag.Get("body") != "json":
		return fmt.Errorf("%s: a problem's body is not body:\"json\"", f.Name)
	}
	return nil
}

func hasTag(f vet.Field, name string) bool {
	_, ok := f.Tag.Lookup(name)
	return ok
}

// problemMembers are the members of [Problem] geta writes itself.
var problemMembers = []string{"type", "title", "status", "instance", "errors", "omitted"}

// checkProblemMember reports whether geta.New accepts member, of kind, as an
// extension member of a geta.OnAsProblem row's problem. Members geta writes
// itself are refused, and "detail" must be a string. It applies geta.New's
// rule.
func checkProblemMember(member, kind string) error {
	switch {
	case slices.Contains(problemMembers, member):
		return fmt.Errorf("geta.OnAsProblem: member %q is reserved", member)
	case member == "detail" && kind != "string":
		return fmt.Errorf("geta.OnAsProblem: member \"detail\" has kind %s, not string", kind)
	}
	return nil
}

// envelopeTags are the tags that make an output struct an envelope.
var envelopeTags = []string{"header", "cookie", "body", "status"}

// successStatuses parses a status field's tag: |-separated distinct 2xx or
// 3xx statuses other than 304, 305, and 306.
func successStatuses(tag string) ([]int, error) {
	var out []int
	for s := range strings.SplitSeq(tag, "|") {
		n, err := strconv.Atoi(s)
		switch {
		case err != nil || strconv.Itoa(n) != s:
			return nil, fmt.Errorf("status tag %q: %q is not a status code", tag, s)
		case n < 200 || n > 399 || n == http.StatusNotModified:
			return nil, fmt.Errorf("status tag %q: %d is not a 2xx or 3xx status other than 304", tag, n)
		case retiredStatus(n) != "":
			return nil, fmt.Errorf("status tag %q: %d %s", tag, n, retiredStatus(n))
		case slices.Contains(out, n):
			return nil, fmt.Errorf("status tag %q names %d twice", tag, n)
		}
		out = append(out, n)
	}
	return out, nil
}

// checkEnvelopeStatus reports whether a status field's tag lists the
// operation's success status, which the field's zero value answers. A tag
// checkEnvelopeField refuses is left to it. It applies geta.New's rule.
func checkEnvelopeStatus(status int, tag string) error {
	statuses, err := successStatuses(tag)
	if err != nil {
		return nil
	}
	return statusDeclared(status, statuses)
}

// checkDocTimeout reports whether geta.New accepts t as a Doc.Timeout. Zero
// keeps the chain's geta.Timeout, a positive value replaces its length, and
// a negative value is refused. Only geta.New checks that a positive value
// has a geta.Timeout in the chain.
func checkDocTimeout(t time.Duration) error {
	if t < 0 {
		return fmt.Errorf("Doc.Timeout %v is negative", t)
	}
	return nil
}

// checkMethodBody reports whether geta.New accepts an input body for method.
// GET, HEAD, and DELETE are refused: their content has no defined semantics
// (RFC 9110 §9.3.1, §9.3.2, §9.3.5) and OpenAPI says to avoid a requestBody
// there. It applies geta.New's rule.
func checkMethodBody(method string) error {
	var instead string
	switch method {
	case http.MethodGet, http.MethodHead:
		instead = "Query or Post"
	case http.MethodDelete:
		instead = "Post"
	default:
		return nil
	}
	return fmt.Errorf("the input has a body, which a %s request does not take; use %s", method, instead)
}

// checkConditionalWrite reports whether geta.New accepts an operation of
// method on a route whose GET declares get, when its input does or does not
// (conditional) embed geta.Conditional or geta.RequireConditional.
//
// PUT, PATCH, and DELETE are defined on the state of the target resource,
// which the route's GET selects a representation of: PUT "requests that the
// state of the target resource be created or replaced" (RFC 9110 §9.3.4),
// PATCH is "defined for partial updates" (§14.5), and DELETE removes "the
// association between the target resource and its current functionality"
// (§9.3.5). Where the GET declares a validator, such an operation must
// evaluate the preconditions against it: without Conditional, geta evaluates
// them against none, so If-Match with the tag the GET sent fails (412,
// §13.1.1) and If-Unmodified-Since is ignored (§13.1.4). POST processes its
// content "according to the resource's own specific semantics" (§9.3.3), so
// its method does not say it writes what the GET selects; QUERY is safe like
// GET (§9.2.1). Neither is judged. It applies geta.New's rule.
func checkConditionalWrite(method string, get vet.Validator, conditional bool) error {
	switch method {
	case http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return nil
	}
	var declares string
	switch {
	case get.Input:
		declares = "its input embeds geta.Conditional"
	case get.Header != "":
		declares = fmt.Sprintf("its output's %s header field", get.Header)
	case get.Chain:
		declares = "geta.ETag tags it"
	default:
		return nil
	}
	if conditional {
		return nil
	}
	return fmt.Errorf("the route's GET declares a validator (%s), but the input embeds neither geta.Conditional nor geta.RequireConditional, "+
		"so geta would evaluate its preconditions against none; embed one and call Check with the current validators", declares)
}

// retiredStatus returns why 305 or 306 cannot be a success status, or "".
func retiredStatus(n int) string {
	switch n {
	case http.StatusUseProxy:
		return "is deprecated"
	case 306:
		return "is unused"
	}
	return ""
}

// redirect reports whether n is 301, 302, 303, 307, or 308, whose Location
// RFC 9110 §15.4 says the server SHOULD send.
func redirect(n int) bool {
	switch n {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// checkSuccessStatus reports whether geta.New accepts status as the success
// status of an operation whose output is neither a stream nor an upgrade. It
// must be a 2xx or 3xx other than 304, 305, and 306. If it, or a status the
// status field declares, is a redirect (301, 302, 303, 307, 308), the output
// must be an envelope with a Location header field of a string or text type
// (RFC 9110 §15.4).
//
// output is the output type ("" for geta.OpNoBody), tag its status field's
// tag ("" if none), and location its Location header field (nil if none). A
// tag checkEnvelopeField refuses is left to it. It applies geta.New's rule.
func checkSuccessStatus(status int, output, tag string, location *vet.Field) error {
	var statuses []int
	if tag != "" {
		statuses, _ = successStatuses(tag)
	}
	return checkSuccess(status, output, statuses, location)
}

// checkSuccess is checkSuccessStatus with the status field's statuses parsed
// (nil if none or invalid).
func checkSuccess(status int, output string, statuses []int, location *vet.Field) error {
	if status < 200 || status > 399 || status == http.StatusNotModified {
		return fmt.Errorf("success status %d is not a 2xx or 3xx status", status)
	}
	if why := retiredStatus(status); why != "" {
		return fmt.Errorf("success status %d %s", status, why)
	}
	for _, s := range append([]int{status}, statuses...) {
		if !redirect(s) {
			continue
		}
		what := fmt.Sprintf("success status %d is a redirect", s)
		if s != status {
			what = fmt.Sprintf("the output's status field declares %d, a redirect", s)
		}
		switch {
		case output == "":
			return fmt.Errorf("%s, but geta.OpNoBody sets no Location; use geta.Op", what)
		case location == nil:
			return fmt.Errorf("%s, but the output %s has no Location header field", what, output)
		case !locationKind(location.Kind):
			return fmt.Errorf("%s, but the output %s's Location header field %s has type %s, not a string or text type",
				what, output, location.Name, location.Type)
		}
		return nil // every redirect needs the same Location
	}
	return nil
}

// locationKind reports whether a header field of kind can hold a URI
// reference, i.e. is not a number or bool.
func locationKind(kind string) bool {
	switch kind {
	case "bool", "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "float32", "float64":
		return false
	}
	return true
}

// checkBodylessStatus refuses a success status that takes no body (204,
// 205) for an output with one. status is the operation's, replaced by the
// statuses of tag, the output's status field's tag ("" if none). It applies
// geta.New's rule.
func checkBodylessStatus(status int, tag string, hasBody bool) error {
	statuses := []int{status}
	if tag != "" {
		if s, err := successStatuses(tag); err == nil {
			statuses = s
		}
	}
	return bodylessStatuses(statuses, hasBody)
}

// checkSpecialStatus refuses a success status other than the one a stream
// (200) or an upgrade (101) answers. It applies geta.New's rule.
func checkSpecialStatus(status, answers int) error {
	if status != answers {
		return fmt.Errorf("success status %d, but this output answers %d", status, answers)
	}
	return nil
}

// bodylessStatuses is checkBodylessStatus over the success statuses.
func bodylessStatuses(statuses []int, hasBody bool) error {
	for _, s := range statuses {
		if hasBody && (s == http.StatusNoContent || s == http.StatusResetContent) {
			return fmt.Errorf("success status %d takes no body, but the output has one", s)
		}
	}
	return nil
}

func statusDeclared(status int, statuses []int) error {
	if !slices.Contains(statuses, status) {
		return fmt.Errorf("success status %d is not in status tag %q", status, statusList(statuses))
	}
	return nil
}

// headerType reports whether an output header may have type typ of kind.
func headerType(name, typ, kind string) error {
	if !scalarKind(kind) {
		return fmt.Errorf("header %q has unsupported type %s", name, typ)
	}
	return nil
}

// embeddedSchema refuses a schema or doc tag on an embedded struct, whose
// fields carry their own tags.
func embeddedSchema(f vet.Field) error {
	if _, ok := f.Tag.Lookup("schema"); ok {
		return fmt.Errorf("%s: an embedded struct takes no schema tag", f.Name)
	}
	if _, ok := f.Tag.Lookup("doc"); ok {
		return fmt.Errorf("%s: an embedded struct takes no doc tag", f.Name)
	}
	return nil
}

// pointerDefault refuses a default on a pointer field, which is nil when
// omitted.
func pointerDefault(f vet.Field) error {
	if f.Pointer && declaresDefault(f.Tag.Get("schema")) {
		return fmt.Errorf("%s: a pointer field takes no default; use %s", f.Name, strings.TrimPrefix(f.Type, "*"))
	}
	return nil
}

// embeddedText refuses an untagged embedded text type, which has no fields
// to bind or write; what completes the message.
func embeddedText(f vet.Field, what string) error {
	return fmt.Errorf("%s: embedded %s is a text type with no fields; %s", f.Name, f.Type, what)
}

// checkMemberField reports whether geta.New accepts f as a field of a JSON
// body struct. It returns f's JSON member name ("" if f is not a member)
// and walk, reporting an embedded struct whose promoted members the caller
// checks in turn. seen records member names taken so far and is updated
// with f's. The error begins with the field's name. It applies geta.New's
// rule.
func checkMemberField(f vet.Field, seen map[string]bool) (member string, walk bool, err error) {
	tag, hasTag := f.Tag.Lookup("json")
	if tag == "-" {
		return "", false, nil
	}
	name, opts, _ := strings.Cut(tag, ",")
	// v2 refuses to write a struct that tags an unexported field.
	if !f.Exported && !f.Embedded {
		if hasTag {
			return "", false, fmt.Errorf("%s is unexported but has a json tag", f.Name)
		}
		return "", false, nil
	}
	promoted := f.Embedded && name == ""
	// An optional member must be absent, not null, so pointers need
	// omitzero; besides pointers, only interfaces and Nullables take it.
	switch {
	case opts != "" && opts != "omitzero":
		return "", false, fmt.Errorf("%s: json tag option %q is not supported", f.Name, opts)
	case opts == "omitzero" && !f.Pointer && !f.Interface && !f.Nullable:
		return "", false, fmt.Errorf("%s: omitzero on a non-pointer field", f.Name)
	case f.Pointer && f.Nullable && !promoted:
		return "", false, fmt.Errorf("%s is a pointer to geta.Nullable; use %s tagged `json:\"%s,omitzero\"`",
			f.Name, strings.TrimPrefix(f.Type, "*"), cmp.Or(name, f.Name))
	case f.Embedded && !f.Exported && !promoted:
		// v2 cannot allocate or call methods on an unexported embedded
		// member.
		switch {
		case f.Pointer:
			return "", false, fmt.Errorf("%s: embedded %s is unexported; export the type or embed %s",
				f.Name, f.Type, strings.TrimPrefix(f.Type, "*"))
		case !f.Struct:
			return "", false, fmt.Errorf("%s: embedded %s is unexported and not a struct", f.Name, f.Type)
		case f.Methods:
			return "", false, fmt.Errorf("%s: embedded %s is unexported and has JSON or text methods", f.Name, f.Type)
		}
	case f.Pointer && opts != "omitzero" && !promoted:
		return "", false, fmt.Errorf("%s is a pointer without omitzero; tag it `json:\"%s,omitzero\"`", f.Name, cmp.Or(name, f.Name))
	}
	if promoted {
		switch {
		case f.Pointer:
			return "", false, fmt.Errorf("%s: embedded pointer types are not supported", f.Name)
		case !f.Struct:
			return "", false, fmt.Errorf("%s: embedded %s is not a struct; give it a json name", f.Name, f.Type)
		case f.Methods:
			return "", false, fmt.Errorf("%s: embedded %s has its own JSON or text methods; give it a json name", f.Name, f.Type)
		}
		if err := embeddedSchema(f); err != nil {
			return "", false, err
		}
		return "", true, nil
	}
	if err := pointerDefault(f); err != nil {
		return "", false, err
	}
	if !hasTag || name == "" {
		name = f.Name
	}
	if seen[name] {
		return "", false, fmt.Errorf("%s: two fields have the JSON name %q", f.Name, name)
	}
	seen[name] = true
	return name, false, nil
}

// checkStructMembers reports whether geta.New accepts struct type typ with
// the given number of fields and of JSON members (promoted ones included):
// a struct with fields but no members is refused. It applies geta.New's
// rule.
func checkStructMembers(typ string, fields, members int) error {
	if members == 0 && fields > 0 {
		return fmt.Errorf("%s has fields but no JSON members", typ)
	}
	return nil
}

// componentKey is OpenAPI's pattern for Components Object keys.
var componentKey = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// checkSchemaName reports whether geta.New accepts typ
// ("lib.Page[example.com/app/lib.User]") as a component. Its component
// name, without package paths ("Page_User"), may contain only ASCII
// letters, digits, '.', '_', and '-', as OpenAPI requires, and is not
// GetaProblem, geta's own. Only geta.New refuses two types with the same
// component name.
func checkSchemaName(typ string) error {
	name := componentNameOf(typ)
	if !componentKey.MatchString(name) {
		return fmt.Errorf("type %s: component name %q does not match %s", typ, name, componentKey)
	}
	if name == problemComponent {
		return fmt.Errorf("type %s: component name %q is reserved for geta's problem schema; rename the type", typ, name)
	}
	return nil
}

// checkSelfHolding reports whether geta.New accepts typ, met again inside
// its own elements or members: a type may hold itself only through a named
// struct type, which its component refers to. throughNamedStruct reports
// that a named struct type (typ itself included) lies on the cycle from typ
// back to typ. It applies geta.New's rule.
func checkSelfHolding(typ string, throughNamedStruct bool) error {
	if throughNamedStruct {
		return nil
	}
	return fmt.Errorf("%s holds itself other than through a named struct type", typ)
}

// vetType describes t for checkJSONType.
func vetType(t reflect.Type) vet.Type {
	vt := vet.Type{Type: t.String(), Kind: t.Kind().String(),
		Marshaler:   writesText(t),
		Unmarshaler: reflect.PointerTo(t).Implements(textUnmarshaler),
		Duration:    t == durationType,
		Format:      namesFormat(t),
		File:        t == fileType}
	switch t.Kind() {
	case reflect.Map:
		// v2 prefers a key's JSON methods to its text methods.
		k := t.Key()
		vt.StringKey, vt.Key = k.Kind() == reflect.String, k.String()
		if !ownJSON(k) {
			vt.KeyMarshaler, vt.KeyUnmarshaler = writesText(k), reflect.PointerTo(k).Implements(textUnmarshaler)
		}
		fallthrough
	case reflect.Slice:
		vt.Elem, vt.ElemPointer = t.Elem().String(), t.Elem().Kind() == reflect.Pointer
	}
	return vt
}

// checkJSONType reports whether geta.New derives a JSON form for t, which is
// not an interface (interfaces are sealed types declared by
// geta.WithUnion). It refuses geta.File, time.Duration, a Nullable of a
// Nullable, a FormatType that is not a text type, a type with only one of
// the text methods, a map without string keys, and pointer elements. A type
// with its own JSON methods is refused only if it also has a format.
// Elements and fields are the caller's to check in turn. It applies
// geta.New's rule.
func checkJSONType(t vet.Type) error {
	switch {
	case t.Nullable && t.ElemNullable:
		return fmt.Errorf("type %s is a geta.Nullable of a geta.Nullable; use %s", t.Type, t.Elem)
	case t.Nullable:
		return nil
	case t.File:
		return fmt.Errorf("type geta.File outside a body:\"multipart\" form field")
	case t.Format && t.JSON:
		return fmt.Errorf("type %s has both a SchemaFormat method and its own JSON methods", t.Type)
	case t.JSON:
		return nil
	case t.Format && !t.Marshaler && !t.Unmarshaler:
		return fmt.Errorf("type %s has a SchemaFormat method but is not a text type", t.Type)
	}
	// v2 has no default representation for time.Duration.
	if t.Duration {
		return fmt.Errorf("type time.Duration has no JSON form; use geta.Duration")
	}
	if t.Marshaler || t.Unmarshaler {
		return oneSided(t.Type, t.Marshaler, t.Unmarshaler)
	}
	switch t.Kind {
	case "string", "bool", "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "float32", "float64", "struct":
		return nil
	case "map":
		if err := oneSided(t.Key, t.KeyMarshaler, t.KeyUnmarshaler); err != nil {
			return err
		}
		if !t.StringKey {
			return fmt.Errorf("map type %s: only string keys have a JSON form", t.Type)
		}
		fallthrough
	case "slice":
		// A nil element would be written as null, and cannot be omitted.
		if t.ElemPointer {
			return fmt.Errorf("%s: element type %s is a pointer; use %s",
				t.Type, t.Elem, strings.TrimPrefix(t.Elem, "*"))
		}
		return nil
	}
	return noJSONForm(t.Type, t.Kind)
}

// oneSided refuses a type that implements only one of the text marshaling
// and unmarshaling interfaces.
func oneSided(typ string, marshaler, unmarshaler bool) error {
	switch {
	case marshaler && !unmarshaler:
		return fmt.Errorf("type %s implements encoding.TextMarshaler but not encoding.TextUnmarshaler", typ)
	case unmarshaler && !marshaler:
		return fmt.Errorf("type %s implements encoding.TextUnmarshaler but not encoding.TextMarshaler", typ)
	}
	return nil
}

// noJSONForm refuses a type of a kind geta derives no JSON form for.
func noJSONForm(typ, kind string) error {
	return fmt.Errorf("type %s has no JSON form geta can derive (%s)", typ, kind)
}
