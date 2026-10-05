package geta

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"

	"encoding/json/jsontext"
)

// number is a JSON number as it was written.
type number string

// parseJSON reads exactly one JSON value into a tree of map[string]any, []any,
// string, number, bool, and nil, so the schema can be checked against what
// was sent. It refuses duplicate names, invalid UTF-8, trailing data, and
// nesting past maxDepth.
func parseJSON(data []byte, maxDepth int) (any, error) {
	return parseJSONFrom(jsontext.NewDecoder(bytes.NewReader(data)), maxDepth, nil)
}

// parseJSONFrom is parseJSON reading from dec, a decoder with default options
// at the start of the data. A non-nil sp records the member names written
// with an escape.
func parseJSONFrom(dec *jsontext.Decoder, maxDepth int, sp *spellings) (any, error) {
	v, err := parseValue(dec, maxDepth, sp)
	if err != nil {
		return nil, err
	}
	if _, err := dec.ReadToken(); err != io.EOF {
		return nil, errors.New("trailing data after the JSON value")
	}
	return v, nil
}

// spellings holds, per object, the member names written with an escape, as
// written with their quotes. The tree holds names unescaped, but
// encoding/json/v2 hands a map key type's UnmarshalJSON the name as written,
// so applyDefaults reads such a key from its spelling.
type spellings struct {
	data  []byte
	names map[uintptr]map[string][]byte // by the object's map (reflect's Pointer)
}

// add records that m's member name was written raw.
func (sp *spellings) add(m map[string]any, name string, raw []byte) {
	if sp.names == nil {
		sp.names = map[uintptr]map[string][]byte{}
	}
	id := reflect.ValueOf(m).Pointer()
	if sp.names[id] == nil {
		sp.names[id] = map[string][]byte{}
	}
	sp.names[id][name] = raw
}

// spelling returns m's member name as written: the recorded spelling, or
// quote(name) when it had no escape.
func (sp *spellings) spelling(m map[string]any, name string) []byte {
	if raw, ok := sp.names[reflect.ValueOf(m).Pointer()][name]; ok {
		return raw
	}
	return quote(name)
}

// parseValue reads one value. Reading the '}' or ']' that PeekKind just
// reported cannot fail, so its error is ignored.
func parseValue(dec *jsontext.Decoder, maxDepth int, sp *spellings) (any, error) {
	tok, err := dec.ReadToken()
	if err != nil {
		return nil, syntaxError(err)
	}
	switch tok.Kind() {
	case jsontext.KindNull:
		return nil, nil
	case jsontext.KindTrue, jsontext.KindFalse:
		return tok.Bool(), nil
	case jsontext.KindString:
		return tok.String(), nil
	case jsontext.KindNumber:
		return number(tok.String()), nil
	case jsontext.KindBeginObject:
		if dec.StackDepth() > maxDepth {
			return nil, fmt.Errorf("JSON nesting exceeds the ceiling of %d", maxDepth)
		}
		m := map[string]any{}
		for dec.PeekKind() != jsontext.KindEndObject {
			before := dec.InputOffset()
			name, err := dec.ReadToken()
			if err != nil {
				return nil, syntaxError(err)
			}
			k := name.String()
			if sp != nil {
				// The raw name follows the previous token, whitespace, and a
				// comma.
				if raw := bytes.TrimLeft(sp.data[before:dec.InputOffset()], " \t\r\n,"); bytes.IndexByte(raw, '\\') >= 0 {
					sp.add(m, k, raw)
				}
			}
			v, err := parseValue(dec, maxDepth, sp)
			if err != nil {
				return nil, err
			}
			m[k] = v
		}
		dec.ReadToken() // '}'
		return m, nil
	case jsontext.KindBeginArray:
		if dec.StackDepth() > maxDepth {
			return nil, fmt.Errorf("JSON nesting exceeds the ceiling of %d", maxDepth)
		}
		a := []any{}
		for dec.PeekKind() != jsontext.KindEndArray {
			v, err := parseValue(dec, maxDepth, sp)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		dec.ReadToken() // ']'
		return a, nil
	}
	// Unreachable: ReadToken returns '}' or ']' only where a container ends.
	return nil, errors.New("malformed JSON")
}

// syntaxError describes what jsontext refused, with its offset. errors.Is
// catches a truncated value whether or not it is wrapped in a
// *jsontext.SyntacticError.
func syntaxError(err error) error {
	if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
		return errors.New("unexpected end of JSON input")
	}
	if se, ok := errors.AsType[*jsontext.SyntacticError](err); ok {
		if errors.Is(se.Err, jsontext.ErrDuplicateName) {
			return fmt.Errorf("duplicate object key at %s", pointerPath(se.JSONPointer))
		}
		return fmt.Errorf("malformed JSON at byte %d: %v", se.ByteOffset, se.Err)
	}
	// Unreachable: the underlying reader is a bytes.Reader, which fails only
	// with io.EOF.
	return errors.New("malformed JSON")
}

// pointerPath renders a JSON Pointer the way violations name paths: $.a[0].
// It is built in one buffer, since a pointer can have as many tokens as the
// body has nesting and be as long as the body.
func pointerPath(p jsontext.Pointer) string {
	var b strings.Builder
	b.WriteByte('$')
	for tok := range p.Tokens() {
		if _, err := strconv.Atoi(tok); err == nil && tok != "" {
			b.WriteByte('[')
			b.WriteString(tok)
			b.WriteByte(']')
			continue
		}
		b.WriteByte('.')
		b.WriteString(tok)
	}
	return b.String()
}

func jsonType(v any) string {
	switch v := v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case number:
		if isIntegerLiteral(string(v)) {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	// Unreachable: the cases cover every type parseJSON and paramValue make.
	return fmt.Sprintf("%T", v)
}

// isIntegerLiteral reports whether a JSON number is written without a
// fraction or exponent; 1.0 is not an integer.
func isIntegerLiteral(s string) bool {
	for i, c := range s {
		if c == '-' && i == 0 {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != "" && s != "-"
}

// isJSONInteger reports whether s is a JSON integer: an optional minus and
// digits with no leading zero.
func isJSONInteger(s string) bool {
	d := s
	if d != "" && d[0] == '-' {
		d = d[1:]
	}
	return isIntegerLiteral(s) && (len(d) == 1 || d[0] != '0')
}

// appendFloat formats like encoding/json: shortest representation, with an
// exponent only for very large or very small magnitudes.
func appendFloat(b []byte, f float64) []byte {
	abs := math.Abs(f)
	format := byte('f')
	if abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	b = strconv.AppendFloat(b, f, format, -1, 64)
	if format == 'e' {
		n := len(b)
		if n >= 4 && b[n-4] == 'e' && b[n-3] == '-' && b[n-2] == '0' {
			b[n-2] = b[n-1]
			b = b[:n-1]
		}
	}
	return b
}
