package geta

import (
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"
	"uuid"
)

// formatTypes maps the Go type of each string format geta checks to the
// format's name. The document gives the type's schema that format, and a
// request's value is checked against the format before the handler runs.
// Each type reads and writes itself as text, so it can be a parameter, an
// output header, or a JSON member.
//
//	date-time              DateTime, time.Time      RFC 3339 date-time
//	date                   Date                     RFC 3339 full-date
//	time                   TimeOfDay                RFC 3339 full-time
//	duration               Duration                 RFC 3339 Appendix A duration
//	ipv4                   IPv4                     RFC 2673 dotted-quad
//	ipv6                   IPv6                     RFC 4291 text form
//	uuid                   uuid.UUID                RFC 4122 UUID
//	json-pointer           JSONPointer              RFC 6901 json-pointer
//	relative-json-pointer  RelativeJSONPointer      draft-handrews-relative-json-pointer-01
//	password               Password                 any string (OpenAPI 3.1)
//	int32, int64           int32, int64 (and int)   the integer's range
//	float, double          float32, float64         the float's range
//
// A time.Time holds no leap second, so its schema adds a pattern that
// refuses one (noLeapSecond); a DateTime accepts one. A []byte is a string
// with contentEncoding base64 (RFC 4648). time.Duration is refused; use
// Duration.
//
// A schema tag on a string may name any of these formats (formatChecks).
//
// email, hostname, uri, uri-reference, iri, iri-reference, uri-template,
// idn-email, idn-hostname, and regex have no type here; an application
// carries them with its own type ([FormatType]). A schema tag cannot name a
// format geta does not check.
var formatTypes = map[reflect.Type]string{
	timeType:                               "date-time",
	uuidType:                               "uuid",
	reflect.TypeFor[DateTime]():            "date-time",
	reflect.TypeFor[Date]():                "date",
	reflect.TypeFor[TimeOfDay]():           "time",
	reflect.TypeFor[Duration]():            "duration",
	reflect.TypeFor[IPv4]():                "ipv4",
	reflect.TypeFor[IPv6]():                "ipv6",
	reflect.TypeFor[JSONPointer]():         "json-pointer",
	reflect.TypeFor[RelativeJSONPointer](): "relative-json-pointer",
	passwordType:                           "password",
}

var (
	passwordType = reflect.TypeFor[Password]()
	durationType = reflect.TypeFor[time.Duration]()
)

// formatChecks checks a string against each format of formatTypes exactly
// as the format's type reads its text. A date-time string may hold a leap
// second.
var formatChecks = map[string]func(string) bool{
	"date-time":             validDateTime,
	"date":                  func(s string) bool { _, ok := parseDate(s); return ok },
	"time":                  func(s string) bool { _, ok := parseTimeOfDay(s); return ok },
	"duration":              validDuration,
	"ipv4":                  parses(ParseIPv4),
	"ipv6":                  parses(ParseIPv6),
	"json-pointer":          parses(ParseJSONPointer),
	"relative-json-pointer": parses(ParseRelativeJSONPointer),
	"uuid":                  validUUID,
	"password":              func(string) bool { return true },
}

// formatLengths are the lengths in code points of the shortest and longest
// string each format accepts; longest is 0 when unbounded. A bounded format
// needs no MaxStringLength backstop, and New refuses a backstop shorter than
// a format's bound (schema.withinLimits).
var formatLengths = map[string]struct{ shortest, longest int }{
	"date-time":             {20, 0}, // 0000-01-01T00:00:00Z
	"date":                  {10, 10},
	"time":                  {9, 0}, // 00:00:00Z
	"duration":              {3, 0}, // P1D
	"ipv4":                  {7, 15},
	"ipv6":                  {2, 45}, // ::, and six full groups before a dotted quad
	"json-pointer":          {0, 0},
	"relative-json-pointer": {1, 0},
	"uuid":                  {36, 36},
	"password":              {0, 0},
}

// parses turns a Parse function into a check.
func parses[T any](parse func(string) (T, error)) func(string) bool {
	return func(s string) bool { _, err := parse(s); return err == nil }
}

// validUUID reports whether s is an RFC 4122 UUID in 36-character form.
func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	_, err := uuid.Parse(s)
	return err == nil
}

// checkedFormats names the formats of formatChecks.
func checkedFormats() string {
	return strings.Join(slices.Sorted(maps.Keys(formatChecks)), ", ")
}

// A FormatType is an application's text type that carries a string format.
// The document gives its schema the format SchemaFormat names, and the
// type's UnmarshalText checks every value a request carries. geta calls
// SchemaFormat on a zero value; it must return the same non-empty name for
// every value.
//
// The name may be one of geta's own formats, such as date; the
// application's UnmarshalText then replaces geta's check.
//
// New refuses a type with a SchemaFormat method that is not a text type
// (encoding.TextMarshaler or encoding.TextAppender, and
// encoding.TextUnmarshaler) or that has its own JSON methods. As a header or
// cookie parameter, its schema is that of any other text type.
type FormatType interface {
	SchemaFormat() string
}

var formatTypeType = reflect.TypeFor[FormatType]()

// namesFormat reports whether t or *t has a SchemaFormat method.
func namesFormat(t reflect.Type) bool {
	return t.Implements(formatTypeType) || reflect.PointerTo(t).Implements(formatTypeType)
}

// textFormat returns the format of a text type t, or "" if it has none.
func textFormat(t reflect.Type) (string, error) {
	if f, ok := formatTypes[t]; ok || !namesFormat(t) {
		return f, nil
	}
	f := reflect.New(t).Interface().(FormatType).SchemaFormat()
	if f == "" {
		return "", fmt.Errorf("type %s: SchemaFormat names no format", t)
	}
	return f, nil
}

// Password is a string the document marks with format password, which tells
// a client to obscure it; any string is valid. fmt and log/slog print it as
// [redacted]. string(p) and encoding/json give the full value.
type Password string

const redacted = "[redacted]"

// String returns [redacted].
func (Password) String() string { return redacted }

// GoString returns the Go syntax of a Password holding [redacted].
func (Password) GoString() string { return `geta.Password("` + redacted + `")` }

// Format writes [redacted] for every verb.
func (Password) Format(f fmt.State, verb rune) { f.Write([]byte(redacted)) }

// LogValue returns [redacted].
func (Password) LogValue() slog.Value { return slog.StringValue(redacted) }

// formatError reports text that a format type refuses.
func formatError(format string, text []byte) error {
	return fmt.Errorf("%q is not a valid %s", text, format)
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

func isHex(c byte) bool { return isDigit(c) || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F' }

// upper upper-cases an ASCII letter. ABNF strings are case-insensitive
// (RFC 5234 §2.3).
func upper(c byte) byte {
	if 'a' <= c && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}
