package getaclient

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/koji-1009/geta/internal/vet"
)

// absence is one field named by Absent. The type tells the field apart from
// a struct that begins at the same address.
type absence struct {
	ptr  uintptr
	typ  reflect.Type
	used bool
}

// absences are the fields a call leaves out.
type absences []*absence

func newAbsences(fields []any) (absences, error) {
	var out absences
	for _, f := range fields {
		v := reflect.ValueOf(f)
		if v.Kind() != reflect.Pointer || v.IsNil() {
			return nil, fmt.Errorf("Absent: %T is not a pointer to a field of the input", f)
		}
		out = append(out, &absence{ptr: v.Pointer(), typ: v.Type().Elem()})
	}
	return out, nil
}

// of reports whether v is a field the call leaves out.
func (as absences) of(v reflect.Value) bool {
	if len(as) == 0 || !v.CanAddr() {
		return false
	}
	p := v.Addr().Pointer()
	for _, a := range as {
		if a.ptr == p && a.typ == v.Type() {
			a.used = true
			return true
		}
	}
	return false
}

// leave reports whether field f, holding v, is left out. Leaving out a field
// with no default is an error; what names the field in it.
func (as absences) leave(f reflect.StructField, v reflect.Value, what string) (bool, error) {
	if !as.of(v) {
		return false, nil
	}
	if !vet.DeclaresDefault(f.Tag.Get("schema")) {
		return false, fmt.Errorf("Absent: field %s (%s) declares no default", f.Name, what)
	}
	return true, nil
}

// unused returns an error for a field named by Absent that the call did not
// reach.
func (as absences) unused() error {
	for _, a := range as {
		if !a.used {
			return fmt.Errorf("Absent: %s is not a field of the input", a.typ)
		}
	}
	return nil
}

// jsonBody encodes v as JSON with opts, omitting the members in abs.
func jsonBody(v reflect.Value, opts json.Options, abs absences) ([]byte, error) {
	b, err := json.Marshal(v.Interface(), opts)
	if err != nil || len(abs) == 0 {
		return b, err
	}
	var drop []string
	if err := memberPaths(v, nil, abs, &drop); err != nil {
		return nil, err
	}
	if len(drop) == 0 {
		return b, nil
	}
	return omitMembers(b, drop, opts)
}

// pathKey is a member's path, its names and indices joined.
func pathKey(path []string) string { return strings.Join(path, "\x00") }

// memberPaths appends to drop the paths of the members in abs, walking v's
// members (vet.CheckMemberField) through pointers, interfaces, and slice
// elements. A type with its own JSON or text methods is not walked.
func memberPaths(v reflect.Value, path []string, abs absences, drop *[]string) error {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	t := v.Type()
	if isText(t) || ownJSON(t) {
		return nil
	}
	switch v.Kind() {
	case reflect.Struct:
		seen := map[string]bool{}
		var walk func(v reflect.Value) error
		walk = func(v reflect.Value) error {
			t := v.Type()
			for i := range t.NumField() {
				f, fv := t.Field(i), v.Field(i)
				member, embedded, err := vet.CheckMemberField(vet.FieldOf(f), seen)
				if err != nil {
					continue // geta.New refuses the type
				}
				if embedded {
					if err := walk(fv); err != nil {
						return err
					}
					continue
				}
				if member == "" {
					continue
				}
				p := append(slices.Clone(path), member)
				if left, err := abs.leave(f, fv, fmt.Sprintf("member %q", member)); err != nil {
					return err
				} else if left {
					*drop = append(*drop, pathKey(p))
					continue
				}
				if err := memberPaths(fv, p, abs, drop); err != nil {
					return err
				}
			}
			return nil
		}
		return walk(v)
	case reflect.Slice:
		for i := range v.Len() {
			if err := memberPaths(v.Index(i), append(slices.Clone(path), strconv.Itoa(i)), abs, drop); err != nil {
				return err
			}
		}
	}
	return nil
}

// ownJSON reports whether t or *t has an encoding/json/v2 marshal method.
func ownJSON(t reflect.Type) bool {
	p := reflect.PointerTo(t)
	for _, name := range []string{"MarshalJSON", "MarshalJSONTo"} {
		if _, ok := p.MethodByName(name); ok {
			return true
		}
	}
	return false
}

// omitMembers copies the JSON value b without the members at the paths in
// drop (see pathKey). opts are the options b was marshaled with, so its
// formatting is kept.
func omitMembers(b []byte, drop []string, opts json.Options) ([]byte, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(b), opts)
	var out bytes.Buffer
	enc := jsontext.NewEncoder(&out, opts)
	// The encoder sees only tokens the decoder accepted, so it should not
	// fail; its first error is returned regardless.
	var werr error
	write := func(tok jsontext.Token) {
		if werr == nil {
			werr = enc.WriteToken(tok)
		}
	}
	// A decoder error must end the copy: PeekKind then never returns the
	// closing delimiter the loops wait for.
	var copyValue func(path []string) error
	copyValue = func(path []string) error {
		tok, err := dec.ReadToken()
		if err != nil {
			return err
		}
		write(tok)
		switch tok.Kind() {
		case '{':
			for dec.PeekKind() != '}' {
				name, err := dec.ReadToken()
				if err != nil {
					return err
				}
				p := append(slices.Clone(path), name.String())
				if slices.Contains(drop, pathKey(p)) {
					if err := dec.SkipValue(); err != nil {
						return err
					}
					continue
				}
				write(name)
				if err := copyValue(p); err != nil {
					return err
				}
			}
		case '[':
			for i := 0; dec.PeekKind() != ']'; i++ {
				if err := copyValue(append(slices.Clone(path), strconv.Itoa(i))); err != nil {
					return err
				}
			}
		default:
			return nil
		}
		// PeekKind just returned the closing delimiter, so this cannot fail.
		end, _ := dec.ReadToken()
		write(end)
		return nil
	}
	if err := copyValue(nil); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), werr
}
