package geta

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"reflect"
	"strings"
)

// Nullable is a JSON member, element, or map value that may be null. It has
// three states: absent (the zero value), null, and a value. Its schema is
// T's with null admitted: type [T, "null"], or anyOf T's component and null.
//
// A Nullable[T] member is required unless tagged omitzero. An optional one
// tells absent, null, and a value apart, as JSON Merge Patch (RFC 7396)
// tells keep, clear, and set:
//
//	type UserPatch struct {
//		Nickname geta.Nullable[string] `json:"nickname,omitzero" schema:"maxLength=32"`
//	}
//
//	if v, ok := in.Body.Nickname.Get(); ok { /* set v */ } else if in.Body.Nickname.IsNull() { /* clear */ }
//
// It is written as its value, or as null; an optional member that is absent
// is omitted, and a required one is written as null. Its schema tag
// constrains the value and takes no default.
//
// [New] fails on a pointer to a Nullable (null would read as a nil pointer,
// the same as absent) and on a Nullable parameter, form field, or header.
type Nullable[T any] struct {
	v     T
	state nullState
}

type nullState uint8

const (
	nullAbsent nullState = iota
	nullNull
	nullValue
)

// Null is a Nullable that is null.
func Null[T any]() Nullable[T] { return Nullable[T]{state: nullNull} }

// NotNull is a Nullable that holds v.
func NotNull[T any](v T) Nullable[T] { return Nullable[T]{v: v, state: nullValue} }

// Get returns the value and true when n holds one, and the zero value and
// false when it is null or absent.
func (n Nullable[T]) Get() (T, bool) { return n.v, n.state == nullValue }

// IsNull reports whether n is null: a request sent null.
func (n Nullable[T]) IsNull() bool { return n.state == nullNull }

// IsZero reports whether n is absent: an optional member a request did not
// send, or one the handler left unset, which omitzero omits.
func (n Nullable[T]) IsZero() bool { return n.state == nullAbsent }

// MarshalJSONTo writes the value, or null when n holds none.
func (n Nullable[T]) MarshalJSONTo(enc *jsontext.Encoder) error {
	if n.state != nullValue {
		return enc.WriteToken(jsontext.Null)
	}
	return json.MarshalEncode(enc, &n.v)
}

// UnmarshalJSONFrom reads null or a value, with the decoder's options.
func (n *Nullable[T]) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	if dec.PeekKind() == 'n' {
		if _, err := dec.ReadToken(); err != nil {
			return err
		}
		*n = Nullable[T]{state: nullNull}
		return nil
	}
	var v T
	if err := json.UnmarshalDecode(dec, &v); err != nil {
		return err
	}
	*n = Nullable[T]{v: v, state: nullValue}
	return nil
}

// nullable identifies a Nullable by reflection and reports its value type.
type nullable interface{ nullableOf() reflect.Type }

func (Nullable[T]) nullableOf() reflect.Type { return reflect.TypeFor[T]() }

// nullHeld returns the value the Nullable v holds, or false when it is null
// or absent. It relies on the field order: v first, state second.
func nullHeld(v reflect.Value) (reflect.Value, bool) {
	if nullState(v.Field(1).Uint()) != nullValue {
		return reflect.Value{}, false
	}
	return v.Field(0), true
}

// nullSlot lets the single-pass decoder fill a Nullable in place.
type nullSlot interface {
	setNull()
	slot() reflect.Value
}

func (n *Nullable[T]) setNull() { *n = Nullable[T]{state: nullNull} }

// slot marks n as holding a value and returns where the value goes.
func (n *Nullable[T]) slot() reflect.Value {
	n.state = nullValue
	return reflect.ValueOf(&n.v).Elem()
}

var (
	nullableType = reflect.TypeFor[nullable]()
	nullablePath = reflect.TypeFor[Nullable[int]]().PkgPath()
)

// nullableOf returns the value type of t when t is a geta.Nullable.
func nullableOf(t reflect.Type) (reflect.Type, bool) {
	if t.PkgPath() != nullablePath || !strings.HasPrefix(t.Name(), "Nullable[") || !t.Implements(nullableType) {
		return nil, false
	}
	return reflect.Zero(t).Interface().(nullable).nullableOf(), true
}
