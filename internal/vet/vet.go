// Package vet holds the rules of geta.New that hold for what source shows,
// for getavet, which reads source, and getatest. Package geta sets every
// rule when it is initialized, so a caller imports geta, if only for that.
package vet

import (
	"reflect"
	"time"
)

// Field describes a struct field for the rules that take one. geta.New
// builds it by reflection, getavet from source.
type Field struct {
	Name     string            // the field's name
	Exported bool              // the field is exported
	Embedded bool              // the field is embedded
	Tag      reflect.StructTag // the field's tag
	Type     string            // the field's type, as reflect writes it ("*main.Body")
	Pointer  bool              // the type is a pointer
	Struct   bool              // the type is a struct
	// Object reports that the type, after one pointer, is a struct (in a
	// query, a deepObject unless it has methods or is a geta.File).
	Object    bool
	Interface bool // the type is an interface
	Cookie    bool // the type is *http.Cookie
	// Text reports that the type (after one pointer) or a pointer to it
	// implements encoding.TextMarshaler or encoding.TextUnmarshaler.
	Text bool
	// Methods reports that the type (after one pointer) or a pointer to it
	// has a JSON or text method that encoding/json/v2 uses instead of its
	// fields.
	Methods bool
	// Kind is the kind of the type after one pointer: a CheckSchemaTag kind
	// ("[]string" for a slice), "json" for a type with its own JSON
	// methods, "nullable", or "union" for an interface. "" leaves the type
	// unjudged.
	Kind string
	// Nullable reports that the type after one pointer is a geta.Nullable.
	Nullable bool
}

// Type describes a type for CheckJSONType. geta.New builds it by
// reflection, getavet from source.
type Type struct {
	Type string // the type, as reflect writes it ("map[string]*main.Item")
	Kind string // its kind, as reflect.Kind writes it ("map", "ptr", "chan")
	// Marshaler reports that T or *T implements encoding.TextMarshaler or
	// encoding.TextAppender; Unmarshaler, that *T implements
	// encoding.TextUnmarshaler.
	Marshaler, Unmarshaler bool
	// StringKey reports that a map's key is of kind string.
	StringKey bool
	// Key is a map's key type. KeyMarshaler and KeyUnmarshaler are its
	// Marshaler and Unmarshaler, both false when it has its own JSON
	// methods.
	Key                          string
	KeyMarshaler, KeyUnmarshaler bool
	// Elem is a slice's or map's element type; ElemPointer reports that it
	// is a pointer.
	Elem        string
	ElemPointer bool
	// Duration reports that the type is time.Duration.
	Duration bool
	// Format reports that T or *T has SchemaFormat() string (a FormatType).
	Format bool
	// JSON reports that T or *T has its own JSON methods.
	JSON bool
	// File reports that the type is geta.File.
	File bool
	// Nullable reports that the type is a geta.Nullable of Elem;
	// ElemNullable, that Elem is a geta.Nullable too.
	Nullable, ElemNullable bool
}

// The rules. Each is documented where package geta defines it.
var (
	CheckSchemaTag        func(tag, kind string) error
	CheckRequestSchemaTag func(tag, kind string) error
	FieldOf               func(f reflect.StructField) Field
	CheckInputType        func(typ string, isStruct bool) error
	CheckInputField       func(f Field, seen map[string]string) (walk bool, err error)
	CheckDeepObjectField  func(f Field, seen map[string]string) (walk bool, err error)
	CheckFormField        func(f Field, multipart bool, seen map[string]string) (walk bool, err error)
	CheckEnvelopeField    func(f Field, seen map[string]bool) error
	EnvelopeEmbedded      func(f Field) bool
	CheckProblemType      func(typ string, isStruct bool) error
	CheckProblemField     func(f Field) error
	CheckProblemMember    func(member, kind string) error
	CheckEnvelopeStatus   func(status int, tag string) error
	CheckDocTimeout       func(t time.Duration) error
	CheckMethodBody       func(method string) error
	CheckSuccessStatus    func(status int, output, tag string, location *Field) error
	CheckBodylessStatus   func(status int, tag string, hasBody bool) error
	CheckSpecialStatus    func(status, answers int) error
	CheckMemberField      func(f Field, seen map[string]bool) (member string, walk bool, err error)
	CheckStructMembers    func(typ string, fields, members int) error
	DeclaresDefault       func(tag string) bool
	CheckSchemaName       func(typ string) error
	CheckSelfHolding      func(typ string, throughNamedStruct bool) error
	CheckJSONType         func(t Type) error
)
