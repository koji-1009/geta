# Constraints and limits

### Constraints (`schema` tag)

- The keywords are `schema:"minLength=1,maxLength=100,pattern=^[a-z]+$,format=date,enum=a|b,minimum=0,maximum=10,exclusiveMinimum=0,exclusiveMaximum=10,multipleOf=2,minItems=1,maxItems=5,uniqueItems=true"`.
- A map or a `WithSchema` object (not a struct) takes `minProperties=1,maxProperties=10`. The violation reads `object has 0 members, fewer than minProperties 1`. A declared `maxProperties` replaces the `MaxItems` backstop.
- An embedded struct takes no `schema` tag; constrain its fields instead.
- A `pattern` runs as Go's RE2 and is published for JSON Schema's dialect, ECMA-262 with the `u` flag. Stricter than JSON Schema: `geta.New` and getavet refuse what the two read otherwise, never rewriting it: `.`, `\s`, and `\S`, whose line terminators and white space differ (write the set: `[^\n]`, `[ \t]`), a group opened by `(?` other than `(?:` and `(?<name>` (flags such as `(?i)`, `(?P<name>`), a group name beginning with a digit (`(?<1a>`), `\A`, `\z`, `\Q`, `\E`, an octal escape such as `\012` (a back reference in ECMA-262), `\x{…}`, `\pL` without braces, `\p{Greek}` or any name but a general category (`L` or `Letter`) or `Any`, an escape ECMA-262 does not take (`\a`, `\@`, `\-` outside a class), a POSIX class `[[:alpha:]]`, a `]` first in a class, a class escape as a range's end (`[\w-a]`), a repeated assertion (`^*`, `$?`, `\b+`), and a `{`, `}`, or `]` standing for itself (escape it; RE2 reads a count with a leading zero, `{02}`, as no count). A `-` after a range stands for itself in both (`[a-z-\d]`). Write `^…$` for `\A…\z` and `[Aa]` for `(?i)a`.
- `geta.New` refuses a keyword on the wrong type, an unknown keyword, a bad pattern, impossible bounds, json tag options, types without a JSON form, and two bodies. It refuses numeric keywords that no value meets: an empty interval (exclusive bounds counted), an interval with no integer in it, one with no value in the Go integer type's range or float32's finite range, and one with no multiple of `multipleOf`. It also refuses a bound that a float64 cannot hold as written (`minimum=9007199254740993`), naming the number it would become.
- Numbers are checked exactly as written, as a JSON Schema validator checks them. This covers float32 fields, integers past 2^53, exact `multipleOf`, and `uniqueItems` by numeric value. Stricter than JSON Schema: a Go integer field takes only an integer literal, as encoding/json/v2 reads it, so `1.0` and `1e2` are 400 ("expected integer, got number") though JSON Schema counts them integers (and asks only that senders not write them so), and an unsigned field refuses `-0`. A `WithSchema` type declared `integer` reads the literal itself.
- Bounds looser than a fixed-size integer's or float32's range are documented as that range. `uint` and `uint64` are documented with `exclusiveMaximum` 18446744073709551616 (2^64, all its digits) unless a tighter bound is declared.
- `enum` takes strings, integers, and numbers. Number members are JSON numbers (`enum=1|2|3`, `enum=0.5|1.5`), documented as given. Request numbers match by exact value (`1.50` is `1.5`, and `9007199254740992` is not `9007199254740993`). `geta.New` fails a member that is not a JSON number of the type (`1.0`, `01`, or `+1` on an integer), is past the Go type's range, is held by a float as another number, is outside the field's bounds or `multipleOf`, or is a string that the field's `format` or the text type's `UnmarshalText` refuses (`enum=foo` on `geta.Date`).
- Violations come in one 400 that lists them, parameters first and then the body, 50 at most, within 16 KiB.
- A body's path is JSONPath (RFC 9535): `$`, an element `[0]`, and a member (or a form's field) `.name` where the name is a member-name-shorthand (a letter, `_`, or a non-ASCII character, then those or digits), else `['name']` with a Normalized Path's escapes (`\'`, `\\`, `\n`, `\u0001`, …): `$.labels.a`, `$.labels['0']`, `$.labels['a.b']`, `$.mail['a@b']`, `$['first-name']`. A 413 or 415 for the body is answered alone, without parameter violations.
- The list is bounded in bytes too, since a request chooses its paths and quoted values. A path longer than 256 bytes keeps its end, from a rune boundary, after `…` (`…kkk.v07`); a duplicate key's path is its violation's path (message `duplicate object key`). A message quotes at most 128 bytes of a request's value (a string, key, tag, or number), cut at a rune boundary and followed by `…`, and at most 512 bytes of an error's text that may hold the request's values (the type's own `UnmarshalJSON`, a malformed form or multipart body), so each violation is bounded. The listed violations' `errors` array stops before 16 KiB of JSON, though the first, so bounded, is always listed. The problem's `omitted` member counts the violations found but not listed, past 50 or past 16 KiB; it is absent when none were. A problem under both bounds, with no value past 128 bytes, is unchanged. `Conforms` cuts neither paths nor values of a response, and its error ends with `and N more not listed` past 50. A 415's detail quotes the request's `Content-Type` (`Content-Type "…" is not application/json`) or its codings (`Content-Encoding "…" is not supported`) the same way: at most 128 bytes, no rune split (a byte of invalid UTF-8 counts as one), then `…`. No other detail geta writes quotes a request's header, query, or path.

### Element, value, and key keywords

- `items.` applies a keyword to each slice element: ``Feature *[]string `query:"feature" schema:"maxItems=3,items.enum=dark|wide"` ``, `items.maxLength=3`, `items.minimum=1`, `items.items.maximum=9` on `[][]int`, and `items.maxProperties=2` on `[]map[string]int`. It works on query, header, and form slices and on JSON arrays. A violation names the element (`feature[1]`). The keyword is documented in `items`.
- `additionalProperties.` applies a keyword to each map value: ``Labels map[string]string `json:"labels" schema:"maxProperties=10,additionalProperties.maxLength=64,additionalProperties.pattern=^[a-z]+$"` ``, and `additionalProperties.minimum=0` on `map[string]int`. The prefixes nest either way (`additionalProperties.items.maximum=9`, `items.additionalProperties.maxLength=3`). A violation names the member (`$.labels.a`), whatever found the violation.
- `propertyNames.` applies a keyword to each map key: ``schema:"propertyNames.maxLength=63,propertyNames.pattern=^[a-z][a-z0-9-]*$"``, `propertyNames.minLength=1`, `propertyNames.enum=en|ja`, and `propertyNames.format=uuid`. It nests as `items.propertyNames.` and `additionalProperties.propertyNames.`. A violation says that it is the key's (`key "AB" does not match pattern ^[a-z]+$`). `WithSchema` objects take it too: `geta.WithSchema[T]("object", "propertyNames.maxLength=64")`.
- Request map keys are held to the `MaxStringLength` backstop in code points, as sent, before the key type's methods read them. A key past it gets `$.labels.<key>: key length 5000 exceeds the ceiling of 4096 code points`, and its value is not checked. A declared `propertyNames.maxLength` replaces the backstop.
- A key type that carries a format (`geta.Password`, or your own string-kind `geta.FormatType`, as in `map[Email]int`) gives its format to the keys. The format is stated in `propertyNames` and enforced on each key (`key "x" is not a valid email`), in `App.Conforms` too.
- `geta.New` and getavet refuse a prefix where there is no array or map (`items.` and `additionalProperties.` also on a `WithSchema` object or array), a keyword the element, value, or key does not take (`items.minLength` on `[]int`, or any keyword on `[]Struct`), and annotations (`default`, `examples`, `deprecated`). They also refuse what goes past the pattern ceiling or a request backstop, an enum member the type refuses, a header list enum member with a comma or whitespace at either end, keys that no value meets, `propertyNames.format=` on a key type with its own format, and a FormatType key that is not a text type or has JSON methods. Only `geta.New` checks `propertyNames.enum` members against the key type's methods.
- For required fixed names, use struct fields, not a key enum.

### Backstops and limits

| Where a schema declares no bound | Backstop |
| --- | --- |
| string length, map keys included | 4096 code points |
| array items; members per map or `WithSchema` object | 8192 |
| depth | 512 |
| body (`MaxBodyBytes`) | 1 MiB (413 at the exact boundary) |

- Patterns never run over more than 4096 code points, whatever the Limits.
- `geta.WithLimits(l)` replaces `DefaultLimits` whole, so start from `l := geta.DefaultLimits` and change fields. `geta.New` refuses `MaxBodyBytes`, `MaxStringLength`, `MaxItems`, and `MaxDepth` below 1. For `MaxMultipartMemory` and `MaxResponseBuffer`, zero has its own meaning.
- To allow more, declare `maxLength`, `maxItems`, `maxProperties`, or `propertyNames.maxLength`.
- Limits can be set per operation with `Doc{Limits: func(l geta.Limits) geta.Limits { l.MaxBodyBytes = 50 << 20; return l }}`, which is called once with the App's limits. They govern the operation's body, parameters, members, and response buffering, and its document states them (the 413 names the body limit). `geta.New` refuses them as it does for `WithLimits`. For slow uploads, also set `Doc.Timeout`.
- When two operations read a component under limits that state it differently (another `MaxStringLength` or `MaxItems` where it declares no bound), `geta.New` fails. Declare bounds on the fields, or give the operations the same limits. A larger `MaxBodyBytes` alone never conflicts.
- Request schemas state each backstop as a bound (`maxLength`, `maxItems`, `maxProperties`, `propertyNames.maxLength`). Response schemas state none; they state only declarations and facts of the type, and `App.Conforms` holds responses to those alone.
- A format with a longest string (`date` 10, `ipv4` 15, `ipv6` 45, `uuid` 36 code points) is bounded by it, as an enum is, and no `maxLength` backstop is stated.
- On values a request reads (a parameter, a form field, a body member, a `WithSchema` declaration it reaches), `geta.New` refuses bounds that no value meets under the operation's limits:
  - `minLength`, `minItems`, or `minProperties` past the backstop with no declared maximum (also after `items.`, `additionalProperties.`, and `propertyNames.`);
  - `minLength` or `maxLength` past 4096 beside a `pattern`;
  - an enum member longer than `MaxStringLength` (with no `maxLength`), or longer than 4096 beside a pattern;
  - without a `maxLength`, a `MaxStringLength` below a format's shortest string (`uuid` 36, `date-time` 20, `date` 10, `time` 9, `ipv4` 7, `duration` 3, `ipv6` 2) or below the longest string of `date`, `ipv4`, `ipv6`, or `uuid`.
- None of these apply to types only a response uses.
- `geta.New` and getavet refuse a `maxLength` below a format's shortest string (`format=uuid,maxLength=30`) and a `minLength` past its longest (`format=date,minLength=11`), on values and keys.

### Descriptions and annotations

- `doc:"free text"` describes a parameter, a JSON member (also beside a `$ref`), a form field, an input body (`requestBody.description`), or an output header. It fails on an embedded struct, a cookie, and an output body.
- Schema-tag annotations are written as a query parameter carries them: `deprecated=true` (on any value, struct-typed members included; it marks parameters deprecated), `examples=a|b`, and `default=v` (on a string, number, boolean, or text type).
- The schema must admit examples and the default: the keywords, the format, `UnmarshalText`, what a header or cookie carries, and the backstops. Numbers are written as JSON numbers. Otherwise `geta.New` fails, and getavet reports the same.
- A default goes on a non-pointer (``Limit int `query:"limit" schema:"default=20,maximum=100"` ``). It is documented as not required and bound when the request omits the value. This works for a query parameter, a header, a cookie, a form field, and a JSON member at any depth (slice elements, map values, Nullable, a sealed variant). A pointer, a path parameter, a body, a `geta.Nullable`, and a sealed type's discriminator take no default.
- A member with a default is optional in the request schema and required in the response schema (the component splits, as `Name-Input`).
- geta writes no `title`.

### Response checks

- Response bodies are checked against the schema in tests (getatest, `App.Conforms`), never at runtime.
- At runtime, a value that cannot be written as documented gets 500. That is a sealed value whose discriminator is not its variant's tag, one of a type that is no variant and that no input or output names, a required one left nil, and output of a type with its own JSON methods that is not JSON. A type the App names in its own right (a struct field's type, another sealed type's variant) is written as itself wherever it implements a sealed type. `geta.Sealed` copies its variants.
- Output headers are checked at runtime. A value no header carries as written (net/http would send its CR and LF as spaces), or a float header holding NaN or an infinity, gets 500.

### Response buffering

- A JSON body up to `Limits.MaxResponseBuffer` (64 KiB) is encoded before the status is sent and gets Content-Length, so an encoding failure is a clean 500. A larger body streams without Content-Length, with its headers and cookies intact. A later failure is logged and the connection is aborted, so the client never sees a complete 200. A buffer of 0 or less streams every body. At most 64 KiB is held.
- `Compress`, `Gzip`, and `ETag` hold the whole response, so streamed bodies still get a validator, Content-Length, a coding, and 304.
- When the connection refuses a write (the client is gone), geta logs it at Info ("geta: the response could not be written") and aborts the connection (`panic(http.ErrAbortHandler)`; `AccessLog` marks it `aborted`). `geta.WriteProblem` without a request aborts without the log line.

