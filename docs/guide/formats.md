# Format types

## Format types

Each string format geta checks has a Go type, documented with the format; request values are held to it in any location (path, query, header, cookie, member, element, map value, map key). Every geta type but `Password` is a text type: `geta.ParseX(s)` builds, `String()` reads, usable as output header or member. Headers and bodies write it as it writes itself (`AppendText` first).

```
date-time              time.Time                  RFC 3339 date-time, no leap second
date-time              geta.DateTime              RFC 3339 date-time        (NewDateTime, DateTimeOf, ParseDateTime, Time, LeapSecond)
date                   geta.Date                  RFC 3339 full-date        (NewDate, DateOf, ParseDate)
time                   geta.TimeOfDay             RFC 3339 full-time        (NewTimeOfDay, TimeOfDayOf, ParseTimeOfDay)
duration               geta.Duration              RFC 3339 Appendix A       (DurationOf, ParseDuration, Std)
ipv4                   geta.IPv4                  RFC 2673 dotted-quad      (ParseIPv4, IPv4Of, Addr)
ipv6                   geta.IPv6                  RFC 4291 text form        (ParseIPv6, IPv6Of, Addr)
uuid                   uuid.UUID                  RFC 4122, 36-character form
json-pointer           geta.JSONPointer           RFC 6901                  (NewJSONPointer, ParseJSONPointer, Tokens)
relative-json-pointer  geta.RelativeJSONPointer   draft-handrews-relative-json-pointer-01  (ParseRelativeJSONPointer)
password               geta.Password              any string; String(), fmt, and log/slog give [redacted]
int32, int64           int32, int64 (and int)     the integer's range
float, double          float32, float64           the float's range
```

- Each type accepts exactly what the JSON Schema Test Suite's format tests accept.
- `geta.Password`: string type; `geta.Password(s)` builds, `string(p)` reads, no `ParsePassword`. JSON writes it in full; `String()`, `fmt`, `log/slog` print `[redacted]`.
- `time.Time`: RFC 3339 grammar, `t`/`z` in either case; offset past 23:59 or comma before the fraction is a 400. No leap second: its schema adds a pattern refusing one (documented, enforced); a leap second is a 400 ("is a leap second, which a time.Time does not hold"); a leap-second enum member fails `geta.New`.
- `geta.DateTime` takes every date-time. `dt.Time()` reads second 60 as second 0 of the next minute; `dt.LeapSecond()` tells them apart.
- `time.Duration` fails `geta.New` wherever read or written; use `geta.Duration`.
- A `string` tagged `format=` (any format above) is held to it as the type is; `format=date-time` also takes a leap second. Stricter than JSON Schema, whose 2020-12 dialect takes `format` as an annotation unless a validator is told to assert it.
- `format=` names only these. On a type carrying its own format (format type or `geta.FormatType`) it fails: "the type has its own format".

### Formats your application decides: `geta.FormatType`

geta has no type for `email`, `hostname`, `uri`, `uri-reference`, `iri`, `iri-reference`, `uri-template`, `idn-email`, `idn-hostname`, `regex`; `format=` naming one (or any unchecked format) fails `geta.New`. Acceptable values differ per application (strict RFC 5321 refuses `a..b@docomo.ne.jp`, which mail carries). Declare a text type with `SchemaFormat() string`: the document names that format, and only your `UnmarshalText` holds requests to it (parameter, member, element; map key for a string-kind type). You own its grammar and tests.

```go
// Email is a mail address as this application takes one.
type Email struct{ local, domain string }

func (Email) SchemaFormat() string { return "email" }

func (e Email) MarshalText() ([]byte, error) { return []byte(e.local + "@" + e.domain), nil }

func (e *Email) UnmarshalText(b []byte) error {
	local, domain, ok := strings.Cut(string(b), "@")
	if !ok || local == "" || !strings.Contains(domain, ".") || strings.ContainsAny(string(b), " \t\r\n") {
		return errors.New("not a mail address this application takes")
	}
	*e = Email{local, domain}
	return nil
}
```

- `geta.New` refuses a `SchemaFormat` type lacking text methods (`MarshalText`/`AppendText` and `UnmarshalText`), having own JSON methods, or returning "".
- It may name a format geta's types carry (`date`); your type then holds requests, not geta's check.
- As header/cookie parameter or output header, its schema states what that place carries.

