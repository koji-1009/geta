# The geta command and getavet

## The geta command

- `geta sync [-check] [dir]` writes `zz_routes.go`, `zz_routes_test.go`, purely from the tree, each whole or not at all (a temporary file renamed over it; written 0644). `-check` fails on a forgotten sync, and reports a file it cannot read as that error, not as out of date.
- `geta check [dir]` names drift: forgotten directory, stale entries, edits, a scope guarding nothing, added/removed `options.go`. It reads the table's paths as sync writes them, so a directory name with `"` or `\` agrees.
- Both refuse bad parameter names, alias collisions (scope packages included), `route.go`/`scope.go` in a go-ignored directory, and a symbolic link to a directory holding either (neither the walk nor `./...` follows a link). A link to a directory holding neither is left alone; a root that is a link holding either is refused too.
- Exit codes: 0 with nothing to report; 1 after reporting or on an unreadable tree or file; 2 on a usage error (any subcommand, nothing read).
- Usage errors: missing or unknown subcommand, undefined flag, too many arguments (at most one directory; `geta check a b`, `geta check -check`), an empty directory argument (`geta sync ""`, most likely an unset variable). Flags first: `geta sync -check routes`, not `geta sync routes -check`.

## getavet

- getavet is a module of its own: `go get -tool github.com/koji-1009/geta/getavet/cmd/getavet` once, then `go tool getavet ./...`, or `go vet -vettool=<getavet binary> ./...`. A plain `go run github.com/koji-1009/geta/getavet/cmd/getavet` fails in a module that does not require it.
- Reports before anything runs, in `geta.New`'s text:
  - path parameters the URL lacks or does not bind (URL from a directory holding `route.go` under a `zz_routes.go`; a helper package in the tree has none, and a cgo route is read by its source's directory);
  - schema-tag mistakes: malformed, wrong type, unchecked format, format on a type carrying its own;
  - every input-field, envelope-field, JSON-member, and type refusal of `geta.New` decidable from source, incl. a stream's event type, a `geta.OnAsProblem` description (type arguments inferred, given, or the first given), and a `geta.Deferred` anywhere but a `body:"json"` field (whose value type is judged as the body's);
  - a body on a `Get`/`Delete` operation built in place by a `geta.Route` literal;
  - a `Put`/`Patch`/`Delete` operation built in place by a `geta.Route` literal whose input embeds neither `geta.Conditional` nor `geta.RequireConditional`, beside a `Get` built in place whose input embeds one or whose output has an `ETag` or `Last-Modified` header field ([Conditional requests](conditional.md));
  - a negative constant `Doc.Timeout`; a constant status outside a status field's list, a 204 or 205 for an output with a body, or other than a stream's 200 or an upgrade's 101.
- Path parameters are not judged in a `_test.go` file. Where a declaration has several mistakes, getavet may name one `geta.New` names only once the first is fixed. An alias is what it aliases (`type Cookie = http.Cookie`, `type Feed = geta.Stream[Event]`). Of geta's own types, only `*geta.Stream` and `*geta.Upgrade` outputs are special; `*geta.Problem` is judged as any other output.
- Shared rules: the rules `geta.New` shares with getavet (input, form, deepObject, envelope, member, type, self-holding type, component name, schema-tag, problem, method-body, conditional-write, success-status, bodyless-status, special-status, and `Doc.Timeout` checks) live in geta's internal package `internal/vet`, which is not part of the API. Silent where `geta.New` is (unexported members).
- Left to `geta.New` (needs options or the whole chain): a `geta.Op` whose input or output is or holds a type parameter (`Page[T]`), in a generic function, and such a row (judged nowhere by getavet, neither there nor where it is instantiated); positive `Doc.Timeout` without `geta.Timeout`; `WithUnion` sealed types; `WithSchema` declarations; keywords on types with own JSON methods; whether a `Query` operation has the 3.2 document; schema-name collisions; `SchemaFormat` returning ""; a `propertyNames.enum` member the key type's methods refuse; an input embedding `geta.Conditional` under an unexported alias; a write beside a GET that `geta.ETag` tags (the chain decides it: root, directory, or `Doc.Scope`), or beside a GET or a write placed in the `geta.Route` by name; bounds only your `Limits` make unreachable (getavet reads no `WithLimits`, so reports no lower bound or enum member past a backstop, even `DefaultLimits`).
- It does report `maxLength`, `minLength`, or an enum member past the 4096 pattern ceiling beside a pattern on an input-read value (the request-schema-tag rule in `internal/vet`).

