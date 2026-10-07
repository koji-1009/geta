# The route tree

## The tree

```
routes/
  scope.go              package routes: type Env = *app.Env; func Scope(env Env) geta.Scope
  options.go            optional: func Options(env Env) []geta.Option, the options every assembly passes geta.New
  health/route.go       /health          func Route(env *app.Env) geta.Route
  users/route.go        /users
  users/scope.go        middleware for /users and below: func Scope(env *app.Env) geta.Scope
  users/id_/route.go    /users/{id}      (a directory ending in _ is a parameter)
  zz_routes.go          written by `geta sync`; never edit
  zz_routes_test.go     written by `geta sync`: runs geta.New on the table under go test
  env_test.go           yours: func testEnv(testing.TB) Env, the Env that test assembles with
```

- One directory is one URL. `route.go` exports `func Route(env *app.Env) geta.Route`, and `scope.go` exports `func Scope(env *app.Env) geta.Scope`. Package names and other files are up to you.
- After you add, move, or remove a `route.go` or `scope.go`, run `go run github.com/koji-1009/geta/cmd/geta sync routes` from the directory that holds `routes/` (or pass its path). `geta check routes` names what is out of sync.
- `go test ./...` runs every `geta.New` check (path tags, schema tags, failure rows, order, security) through `zz_routes_test.go`. It needs only `testEnv` in a `_test.go` file of the root package. Without it, the build fails with `undefined: testEnv`.
- Assembly options (`geta.WithUnion`, `WithSchema`, `WithLimits`, `WithInfo`, `WithOpenAPI`) go in `func Options(env Env) []geta.Option` in the root's `options.go`, which `geta sync` finds by name. The generated test then calls `getatest.New(t, Table(env), Options(env)...)`. main and your tests call `geta.New(routes.Table(env), routes.Options(env)...)`, and append an option such as `geta.WithOpenAPI(geta.OpenAPI32)` for a variant. `geta check` and `geta sync -check` name an `options.go` that was added or removed since the last sync. Without `options.go`, the test assembles the table alone.
- Never name a directory `_x`, `.x`, `testdata`, or `vendor`. `./...` skips such a directory, so `go vet` and `go test` never reach it. `geta sync` and `geta check` refuse a `route.go` or `scope.go` in or below one, naming the file. Such a directory that holds neither file is skipped. The rule applies from the routes root up to the module root (a module root with such a name is still matched by `./...` run inside it). A subdirectory with its own `go.mod` is another module, outside the tree.

### Matching

- Matching goes segment by segment. A literal is tried before a parameter, and matching backs out of a literal that leads to a dead end. Any tree serves, and the order of the table does not matter. With `/users/by-role/{role}` and `/users/{id}/name`, `/users/by-role/name` matches the first, `/users/7/name` matches the second, and `/users/by-role` matches `/users/{id}`.
- geta refuses templates that differ only in parameter names (`/users/{id}`, `/users/{uid}`). It also refuses paths no client can send as written: a `.` or `..` segment, or a literal holding `?`, `#`, a space, or a control character. Literals are written unescaped and match however the client escapes them.
- Parameter names follow ServeMux's rule and may be in any script.
- Methods match by name, case-sensitively. A method a served path does not serve gets 405 with `Allow`, the union over the matching templates. GET answers HEAD (`Matched` reports GET, and `Observe` reports HEAD). A stream sends only its headers to HEAD. When a path is served only by QUERY or POST, HEAD gets 405, and `Allow` lists no HEAD.
- `%2E` and `%2E%2E` are ordinary segments. `%2F` stays in its segment, also beside bytes that net/http takes raw (`"`, `{`, `|`, non-ASCII).
- An unclean path (`//`, `.`, `..`) gets a 307 to the clean form of the client's own URL (`RequestURI`, or the URL for a request built in process). The redirect keeps the mount prefix, the query, and a trailing slash (`/api//users` redirects to `/api/users`).
  - `Location` percent-encodes a backslash, a tab, a line break, a space, control and non-ASCII bytes, and a stray `%`. It keeps the client's escapes and never names another host (`//\evil.example/x` becomes `/%5Cevil.example/x`).
  - The 307 has `Content-Length: 0` and no body or `Content-Type`. It passes through the root scope with the clean path's match, so a root gate asks for that operation's credentials.
  - A path that is unclean only after a root rewrite or a handler in front is served, not redirected. CONNECT is never redirected, and its authority form matches nothing.
- Middleware sees `r.Pattern` and `r.PathValue`, also under a ServeMux. After a root rewrite, the scopes and the handler read only the served operation's values (a name only the first-matched template had reads as ""). Under a ServeMux pattern that names the same parameter, geta binds its own value, and the outer request keeps its own.
- A request in asterisk form with any method but OPTIONS (`GET *`) gets a 400 problem inside the root scope, as a 404 does. It matches nothing (not `/{name}`), is never redirected, and is in no operation's document.

### OPTIONS

- OPTIONS is geta's, never a route's. On a served path it answers 204 with `Allow` listing every method of every matching template, HEAD beside GET, and OPTIONS. The `Allow` of a 405 includes OPTIONS. On an unserved path it answers 404.
- OPTIONS runs only the root scope. Root `Secure` gates check their defaults, as for an unmatched URL, so a public GET does not make OPTIONS public. `geta.CORS` answers preflights first. No `Doc.BeforeGate` runs.
- `OPTIONS *` answers 204 with every method any path serves, plus OPTIONS (with no routes, `Allow: OPTIONS`). Root `Secure` gates check their defaults for `OPTIONS *` and `GET *` (401 without credentials). `geta.Run` and `geta.Serve` set `http.Server.DisableGeneralOptionsHandler` so that the request reaches the App. On your own server, set it yourself, or net/http answers 200 without `Allow`.
- The document has an `options` operation for each path, with a derived operationId (`optionsUsersById`, as [The document](serving.md#the-document) derives one); paths that would share one (`/a/b` and `/aB`) fail `geta.New`. A `Doc.OperationID` that takes that name fails `geta.New`. Its 204 states the `Allow` values (`const`, or `enum` where templates overlap), the path parameters, and the root scope's security and statuses, with a 504 only where a root middleware answers one. `App.Documented` and `App.Types` know it.

## Without the routes tree

`geta.New` takes a plain `geta.Table{Root Scope; Routes []Entry}`, where `Entry` is `{Path string; Route Route; Scopes []Scope}`. The tree is one way to build it, and writing it by hand works too.

```go
table := geta.Table{
	Root: geta.Scope{observe},                  // wraps every request, 404 and 405 included; the only scope OPTIONS runs
	Routes: []geta.Entry{
		{Path: "/health", Route: health.Route(env)},
		{
			Path:   "/users/{id}",              // a parameter is a whole segment: {name}
			Route:  user.Route(env),
			Scopes: []geta.Scope{{authenticate}}, // what the directories above would hold, outermost first
		},
	},
}
app, err := geta.New(table)
```

- `Path` starts with `/` and has no empty segment. Each segment is a literal or a whole `{name}`, where the name is a Go identifier used once. `geta.New` checks it as it checks a generated table.
- `Scopes` holds the directories' scopes, outermost first. The root scope goes only in `Root`.
- Types, the document, validation, the path-parameter checks, and `getatest.New(t, table)` (your test replaces `zz_routes_test.go`) work the same.
- You lose `geta sync` and `geta check`, so a route missing from `Routes` goes unnoticed. You also lose getavet's path-parameter report, which reads URLs from directories; `geta.New` still refuses a mismatch in a test. The other getavet checks do not depend on the tree.
