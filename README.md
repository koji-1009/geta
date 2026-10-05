# geta

geta is a Go web framework. The shape of the server is fixed before the program runs: routes are directories, operations are typed values, and the input and output types are the contract. The OpenAPI document is derived from those types.

What can be decided before the program runs is a type or a value that `geta.New` checks once at assembly. That covers which URLs exist, the order middleware runs in, what each operation takes and returns, and what it requires. Only what changes per request, such as a token's validity or a row's existence, is checked per request.

- Routes are directories. Each exports `func Route(env *app.Env) geta.Route`.
- Methods are struct fields. An operation is `geta.Op(status, handler, doc)`, and the compiler checks the handler's shape on that line.
- The input and output types are the contract. Binding, validation, encoding, and the OpenAPI document all come from them.
- Errors are ordinary values. A failure table in each operation's `Doc` maps them to statuses, so the documented failures and the failures on the wire are the same set.
- `geta.New` is the one assembly step. Production and `getatest` both run it.

geta needs Go 1.27 or later. The core and every package under it use only the standard library. geta ships no database layer, driver, or JWT verifier. An application uses `database/sql` with its own driver and writes a small adapter that classifies the driver's errors as its store's own errors, which its failure tables map; [`examples/register-sql`](examples/register-sql) has adapters for SQLite and PostgreSQL and a conformance suite that checks them. [`examples/booking`](examples/booking) verifies bearer JWTs with its own package on `golang-jwt/jwt/v5`.

```go
// routes/users/id_/route.go — /users/{id}
func Route(env *app.Env) geta.Route {
	h := Handler{Users: env.Users}
	notFound := geta.On(store.ErrNotFound, http.StatusNotFound, "user not found")
	return geta.Route{
		Get:    geta.Op(http.StatusOK, h.Get, geta.Doc{Summary: "Fetch a user", Failures: []geta.Failure{notFound}}),
		Delete: geta.OpNoBody(http.StatusNoContent, h.Delete, geta.Doc{Summary: "Delete a user", Failures: []geta.Failure{notFound}}),
	}
}
```

## Documentation

- [`llms.txt`](llms.txt) is the complete guide. It covers getting started, the route tree, routes, bodies, formats, errors, scopes and middleware, security, streams and upgrades, serving, the OpenAPI document, testing, and the database. It is meant to be read whole, by a person or a coding agent.
- [`docs/claims.md`](docs/claims.md) lists each behaviour geta states and the test that holds it.
- [`examples/`](examples) holds running applications (see [Examples](#examples)).

## Install

```
go get github.com/koji-1009/geta
```

## Quick start

A route tree lives in one directory, `routes/`. Its root package declares `type Env`, the type every `Route` receives. `geta sync` writes the table that passes it.

```go
// app/env.go — what every route receives
package app

type Env struct{ Greeting string }

func Open() *Env { return &Env{Greeting: "hello"} }
```

```go
// routes/env.go — required: the generated table refers to Env
package routes

import "example.com/hello/app"

type Env = *app.Env
```

```go
// routes/greeting/route.go — /greeting
func Route(env *app.Env) geta.Route {
	h := handler{word: env.Greeting}
	return geta.Route{Get: geta.Op(http.StatusOK, h.get, geta.Doc{Summary: "Greet"})}
}

type handler struct{ word string }

type GreetIn struct {
	Name string `query:"name" schema:"minLength=1,maxLength=64"`
}

type Greeting struct {
	Text string `json:"text"`
}

func (h handler) get(ctx context.Context, in *GreetIn) (*Greeting, error) {
	return &Greeting{Text: h.word + ", " + in.Name}, nil
}
```

```go
// routes/env_test.go — the Env the generated assembly test uses
func testEnv(testing.TB) Env { return app.Open() }
```

```go
// main.go
a, err := geta.New(routes.Table(app.Open()))
if err != nil {
	log.Fatal(err) // every assembly mistake, all at once
}
if err := geta.Run(context.Background(), &http.Server{Addr: ":8080", Handler: a}); err != nil {
	log.Fatal(err)
}
```

```go
// main_test.go
func TestGreeting(t *testing.T) {
	c := getatest.New(t, routes.Table(app.Open()))
	if res := c.Get("/greeting?name=Ada"); res.JSON[greeting.Greeting]().Text != "hello, Ada" {
		t.Fatal(res.Status, res.Text())
	}
}
```

```
go run github.com/koji-1009/geta/cmd/geta sync routes   # writes routes/zz_routes.go and its assembly test
go test ./...
go run .
```

`llms.txt` has the whole application, file by file, under "Getting started".

The tree is optional. `geta.New` also takes a table written by hand:

```go
geta.Table{Root: scope, Routes: []geta.Entry{{Path: "/users/{id}", Route: r, Scopes: []geta.Scope{s}}}}
```

`Scopes` lists the directory scopes, outermost first; the root scope goes in `Root`. Types, the document, validation, the path-parameter checks in `geta.New`, `getatest.New(t, table)`, and `getaclient` all work the same. You lose `geta sync`, `geta check`, and getavet's path-parameter report, which reads an operation's URL from its directory. Your own test that calls `getatest.New` replaces the generated `zz_routes_test.go`. See "Without the routes tree" in `llms.txt`.

## Layout

| Path | What |
| --- | --- |
| `.` (`geta`) | Routes, operations, JSON, form, and multipart bodies, failure tables, scopes and ordering, built-in middleware (including pluggable content codings), automatic `OPTIONS`, the security gate, streams, upgrades, `New`, OpenAPI 3.1 or 3.2, `Run` |
| `getatest` | In-memory test client, event-stream and upgrade helpers, a dialer for third-party clients, golden documents |
| `getaclient` | A Go client that calls an app with its handlers' own input and output types; no generated code |
| `getaotel` | OpenTelemetry spans and HTTP server metrics named by route template, on otelhttp (separate module) |
| `getavet` | An analyzer that reports path-binding and schema-tag mistakes, and the field and type refusals of `geta.New` that source alone decides, before the program runs (separate module) |
| `cmd/geta` | `geta sync [-check] [dir]`, `geta check [dir]` |
| `examples/*` | Running applications; see [Examples](#examples) |
| `internal/tree` | What `geta sync` and `geta check` read and write; `internal/fixture` holds packages the tests load |
| `testdata` | Compile-failure packages, golden documents, the JSON Schema Test Suite's format tests, a fuzz corpus |
| `docs` | [`claims.md`](docs/claims.md) |

## Examples

| Example | What it shows |
| --- | --- |
| [`examples/register`](examples/register) | Users and teams: CRUD behind a bearer gate, shared failure rows, admin-only writes through `Doc.Scope`, conditional writes with `ETag`, a multipart upload, an event feed |
| [`examples/auth`](examples/auth) | Bearer tokens and cookie sessions behind one gate, 401 versus 403, a rate-limited login form, revocation that closes a live feed, OpenAPI 3.2 beside 3.1 |
| [`examples/booking`](examples/booking) | OpenID ES256 tokens verified by its own `jwtauth` package on golang-jwt, sealed types, format types, zstd beside gzip, getaotel, a WebSocket over `geta.Upgrade`, HTTP/3 (separate module) |
| [`examples/bookmarks`](examples/bookmarks) | A cross-origin single-page app with a session cookie behind a reverse proxy: CORS with credentials, a rate limit keyed through trusted proxies, an `Idempotency-Key` header, per-template counts with `geta.Observe`, a QUERY operation |
| [`examples/register-sql`](examples/register-sql) | How to write a database adapter: SQLite and PostgreSQL adapters checked by its conformance suite, and the register example served over SQLite (separate module) |

## Running the checks

```
go test ./...                                 # the core, the examples, the tools
go test -C getavet ./...                      # the analyzer
go test -C getaotel ./...                     # spans and metrics on otelhttp, streams and upgrades through it
go build -C getavet -o "$TMPDIR/getavet" ./cmd/getavet
"$TMPDIR/getavet" -test=false ./...            # getavet over this module, less the tests' deliberately bad fixtures
go test -C examples/register-sql ./...        # the example adapters' conformance and the register flow on SQLite and PostgreSQL (embedded, or GETA_POSTGRES_URL)
go test -C examples/booking ./...             # the booking example: JWT, sealed types, zstd, OpenTelemetry, WebSocket, HTTP/3
go vet -C examples/booking -vettool="$TMPDIR/getavet" ./...   # getavet over the booking module
go test -run XXX -fuzz FuzzBinding -fuzzminimizetime 10s .   # fuzz the binder
uvx openapi-spec-validator examples/register/openapi.json examples/auth/openapi.json examples/auth/openapi.3.2.json examples/booking/openapi.json examples/booking/openapi.3.2.json testdata/formats.openapi.json testdata/forms.openapi.json testdata/query.openapi.3.2.json testdata/schema.openapi.json testdata/responses.openapi.json testdata/security.openapi.json testdata/security.openapi.3.2.json examples/bookmarks/openapi.json
```

Go's fuzzer minimizes each input that finds new coverage, running the target on the order of n² times for an input of n bytes, for up to `-fuzzminimizetime` (a minute by default) and with no progress shown meanwhile. Most fuzz targets cut their inputs to the size in which every limit they hold is reached, which keeps that to seconds; `-fuzzminimizetime 10s` bounds the rest: FuzzRouter, whose every input builds an App, and FuzzContentHeaders and FuzzConditional, which hold inputs past a kilobyte.

Each example's `clientcheck/roundtrip.py` drives the running server through a client that `openapi-python-client` generates from the committed `openapi.json`. The script's docstring has the commands.

The committed `go.work` makes getaotel, getavet, and the separate examples use the root module in this checkout. Releasing is in [`RELEASING.md`](RELEASING.md).

Every behaviour geta states has a test or a command that checks it: see [`docs/claims.md`](docs/claims.md).

## License

MIT. See [LICENSE](LICENSE).
