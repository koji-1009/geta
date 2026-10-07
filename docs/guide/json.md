# JSON bodies, null, and components

### JSON bodies and types

- geta reads a request body once. It walks the `encoding/json/jsontext` tokens, checks the schema on the way, and hands the values to `encoding/json/v2`, which also writes responses.
- Unknown members, duplicate keys, names in the wrong case, and trailing data get 400.
- Types are structs with `json:"name"` tags, slices, `map[string]T`, scalars, and text types. `any`, channels, and `time.Duration` are not allowed (use `geta.Duration`). A type may hold itself only through a named struct, which its component refers to: `type Node struct { Kids map[string]Node }` is taken, `type Tree map[string]Tree` fails `geta.New`, and getavet reports it.
- An optional field is a pointer tagged `json:"name,omitzero"`. A pointer without `omitzero`, or `omitzero` on a non-pointer, fails. No other tag options are allowed. Slice and map elements are never pointers (`[]T`, not `[]*T`).
- Map keys are of string kind. A key type with text methods is read and written by them (as v2 does, on any receiver), so it needs both.
- A json tag on an unexported field fails (use `json:"-"`, or export the field). The members of an untagged embedded struct are promoted. An untagged embedded pointer, non-struct, or type with JSON or text methods fails; a json name makes it one member. A named embedded pointer is optional and needs `omitzero`.
- The kind follows v2's method precedence: `MarshalJSONTo`, then `MarshalJSON`, then `AppendText`, then `MarshalText`.
- A type with its own JSON methods (`decimal.Decimal`, `*big.Int`) needs no declaration, and its schema is `{}`. A request value is valid exactly when its `UnmarshalJSON` accepts it; otherwise it gets 400 at that path. Null is read as v2 reads it. An optional (pointer) member stays nil, and no method runs. A value's `UnmarshalJSON` gets the null (types v2 reads as structs stay zero). Output that is not JSON gets 500.
- A narrower schema, documented and checked, is declared in `geta.New` with `geta.WithSchema[decimal.Decimal]("string", "pattern=^-?[0-9]+(\\.[0-9]+)?$")`. Declared JSON types refuse null, even behind a pointer. A type declared `integer` or `number` reads the number itself, so it need not fit a float64 (a 400-digit integer, or `1e400` for a `number`, is taken unless a keyword refuses it, compared exactly; an `integer` takes only an integer literal, so not `1e400`). A wrong declaration fails `geta.New`. Schema keywords on a type with its own JSON methods need a `WithSchema` declaration.
- On output, a nil slice is written as `[]`, a nil map as `{}`, and a nil pointer field is omitted. Map keys are sorted. Strings are HTML-safe: `<`, `>`, and `&` are escaped, in output of types with their own JSON methods and in every problem body too.
- JSON success bodies carry `X-Content-Type-Options: nosniff`, as problems do.
- A `[]byte` member is a string with `contentEncoding: base64`. A slice of a named byte type (`type B byte`; `[]B`) is an array of integers 0 to 255, as encoding/json/v2 writes it.
- `WithSchema` refuses a pointer type (declare its element) and a `geta.Nullable` (declare its value). `geta.New` refuses a declared type used as a map key, which is read as a member name, never through the declaration: constrain keys with `propertyNames.`.

### Null: `geta.Nullable[T]`

- Null is written and accepted only in `geta.Nullable[T]` members.
- The schema is T's plus null: `type: [T, "null"]` (an enum lists null), or `anyOf` of the component and null. A schema tag constrains the value.
- Untagged, the member is required and is null or a T. Tagged `json:"name,omitzero"`, it is optional and has three states: absent (the zero value), null, and a value. Read it with `if v, ok := n.Get(); ok { set } else if n.IsNull() { clear }`, and keep the old value when it is absent.
- Build one with `geta.Null[T]()` or `geta.NotNull(v)`. An absent optional member is omitted, and an absent required member is written as null.
- geta refuses a pointer to a Nullable, a Nullable of a Nullable, a Nullable parameter, form field, or header, and a default on a Nullable.
- A Nullable may hold a sealed type (null or a variant; no variant on output gets 500). It may be a slice element, a map value, or a component.

### Components

- Named struct types become components named after the type, without the package path (`Page[lib.User]` becomes `Page_User`). The same name twice anywhere in the app fails `geta.New`, so give route-local types names that are unique in the document.
- A name outside `^[a-zA-Z0-9._-]+$` (a non-ASCII letter, or a generic argument that is not a named type, such as `Page[struct{...}]`) fails `geta.New` and getavet, and so does `GetaProblem`, the name of geta's problem schema.
- Only referenced components are listed.
- A named type that a request reads and a response writes with different schemas becomes two components: `User` for the response, and `User-Input` for the request, which refers to the inner types' request schemas, sealed mappings included. When the schemas are equal, there is one component.

