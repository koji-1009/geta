package geta

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// JSONPointer is an RFC 6901 JSON Pointer (format json-pointer), such as
// /users/0/name. The zero value is "", which identifies the whole document.
type JSONPointer struct {
	text string
}

// NewJSONPointer returns the pointer made of tokens, escaping ~ as ~0 and /
// as ~1.
func NewJSONPointer(tokens ...string) JSONPointer {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(t))
	}
	return JSONPointer{text: b.String()}
}

// ParseJSONPointer reads an RFC 6901 JSON Pointer.
func ParseJSONPointer(s string) (JSONPointer, error) {
	if !validJSONPointer(s) {
		return JSONPointer{}, formatError("json-pointer", []byte(s))
	}
	return JSONPointer{text: s}, nil
}

// Tokens returns the pointer's reference tokens, unescaped.
func (p JSONPointer) Tokens() []string {
	if p.text == "" {
		return nil
	}
	tokens := strings.Split(p.text[1:], "/")
	for i, t := range tokens {
		tokens[i] = strings.ReplaceAll(strings.ReplaceAll(t, "~1", "/"), "~0", "~")
	}
	return tokens
}

// String returns the pointer.
func (p JSONPointer) String() string { return p.text }

// AppendText appends the pointer.
func (p JSONPointer) AppendText(b []byte) ([]byte, error) { return append(b, p.text...), nil }

// MarshalText writes the pointer.
func (p JSONPointer) MarshalText() ([]byte, error) { return p.AppendText(nil) }

// UnmarshalText reads an RFC 6901 JSON Pointer.
func (p *JSONPointer) UnmarshalText(b []byte) error {
	v, err := ParseJSONPointer(string(b))
	if err != nil {
		return err
	}
	*p = v
	return nil
}

// validJSONPointer reads
//
//	json-pointer    = *( "/" reference-token )
//	reference-token = *( unescaped / escaped )
//	unescaped       = %x00-2E / %x30-7D / %x7F-10FFFF
//	escaped         = "~" ( "0" / "1" )
//
// over Unicode characters, so s must be UTF-8.
func validJSONPointer(s string) bool {
	if s != "" && s[0] != '/' || !utf8.ValidString(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '~' {
			if i+1 == len(s) || s[i+1] != '0' && s[i+1] != '1' {
				return false
			}
			i++
		}
	}
	return true
}

// RelativeJSONPointer is a Relative JSON Pointer
// (draft-handrews-relative-json-pointer-01, format relative-json-pointer),
// such as 0/name, 2/items/0, or 1#. The zero value is "", which is not a
// valid pointer.
type RelativeJSONPointer struct {
	text string
	n    int // the length of the non-negative-integer prefix
}

// ParseRelativeJSONPointer reads a Relative JSON Pointer.
func ParseRelativeJSONPointer(s string) (RelativeJSONPointer, error) {
	n, ok := parseRelativeJSONPointer(s)
	if !ok {
		return RelativeJSONPointer{}, formatError("relative-json-pointer", []byte(s))
	}
	return RelativeJSONPointer{text: s, n: n}, nil
}

// Up returns how many levels the pointer goes up. It returns false if the
// number exceeds a uint64.
func (p RelativeJSONPointer) Up() (uint64, bool) {
	n, err := strconv.ParseUint(p.text[:p.n], 10, 64)
	return n, err == nil
}

// Hash reports whether the pointer ends in #, identifying the name or index
// of the value it reaches rather than the value.
func (p RelativeJSONPointer) Hash() bool { return p.text != "" && p.text[len(p.text)-1] == '#' }

// Pointer returns the JSON Pointer after the number, or the empty pointer if
// p ends in #.
func (p RelativeJSONPointer) Pointer() JSONPointer {
	if p.Hash() {
		return JSONPointer{}
	}
	return JSONPointer{text: p.text[p.n:]}
}

// String returns the pointer.
func (p RelativeJSONPointer) String() string { return p.text }

// AppendText appends the pointer.
func (p RelativeJSONPointer) AppendText(b []byte) ([]byte, error) { return append(b, p.text...), nil }

// MarshalText writes the pointer.
func (p RelativeJSONPointer) MarshalText() ([]byte, error) { return p.AppendText(nil) }

// UnmarshalText reads a Relative JSON Pointer.
func (p *RelativeJSONPointer) UnmarshalText(b []byte) error {
	v, err := ParseRelativeJSONPointer(string(b))
	if err != nil {
		return err
	}
	*p = v
	return nil
}

// parseRelativeJSONPointer reads
//
//	relative-json-pointer =  non-negative-integer <json-pointer>
//	relative-json-pointer =/ non-negative-integer "#"
//	non-negative-integer  =  %x30 / %x31-39 *( %x30-39 )
//
// and returns the length of the integer.
func parseRelativeJSONPointer(s string) (int, bool) {
	n := 0
	for n < len(s) && isDigit(s[n]) {
		n++
	}
	if n == 0 || n > 1 && s[0] == '0' {
		return 0, false
	}
	return n, s[n:] == "#" || validJSONPointer(s[n:])
}
