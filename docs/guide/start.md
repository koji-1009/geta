# Starting a geta application

## Getting started

```
go get github.com/koji-1009/geta     # Go 1.27 or later
```

A minimal application:

```
go.mod                     module example.com/hello
app/env.go                 package app: type Env, what every route receives
routes/env.go              package routes: type Env = *app.Env (required)
routes/greeting/route.go   /greeting: func Route(env *app.Env) geta.Route
routes/env_test.go         func testEnv(testing.TB) Env
routes/zz_routes.go        written by geta sync
routes/zz_routes_test.go   written by geta sync
main.go                    geta.New and geta.Run
main_test.go               a test through getatest
```

```go
// app/env.go
package app

// Env is what every route receives: stores, clients, configuration.
type Env struct{ Greeting string }

func Open() *Env { return &Env{Greeting: "hello"} }
```

```go
// routes/env.go
package routes

import "example.com/hello/app"

// Env is the type the generated table passes every Route and Scope.
type Env = *app.Env
```

```go
// routes/greeting/route.go
package greeting

import (
	"context"
	"net/http"

	"example.com/hello/app"
	"github.com/koji-1009/geta"
)

// Route is /greeting.
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
// routes/env_test.go
package routes

import (
	"testing"

	"example.com/hello/app"
)

// testEnv is the Env zz_routes_test.go assembles the table with.
func testEnv(testing.TB) Env { return app.Open() }
```

```go
// main.go
package main

import (
	"context"
	"log"
	"net/http"

	"example.com/hello/app"
	"example.com/hello/routes"
	"github.com/koji-1009/geta"
)

func main() {
	a, err := geta.New(routes.Table(app.Open()))
	if err != nil {
		log.Fatal(err) // every assembly mistake, all at once
	}
	if err := geta.Run(context.Background(), &http.Server{Addr: ":8080", Handler: a}); err != nil {
		log.Fatal(err)
	}
}
```

```go
// main_test.go
package main

import (
	"testing"

	"example.com/hello/app"
	"example.com/hello/routes"
	"example.com/hello/routes/greeting"
	"github.com/koji-1009/geta/getatest"
)

func TestGreeting(t *testing.T) {
	c := getatest.New(t, routes.Table(app.Open())) // runs geta.New: an assembly error fails here
	res := c.Get("/greeting?name=Ada")
	if res.Status != 200 {
		t.Fatal(res.Status, res.Text())
	}
	if g := res.JSON[greeting.Greeting](); g.Text != "hello, Ada" {
		t.Fatal(g.Text)
	}
	if p := c.Get("/greeting").Problem(); p.Status != 400 { // name is required
		t.Fatal(p.Status)
	}
	getatest.Golden(t, c.App(), "openapi.json")
}
```

Then, from the module root:

```
go run github.com/koji-1009/geta/cmd/geta sync routes   # writes routes/zz_routes.go and routes/zz_routes_test.go
go test . -update                                       # writes openapi.json the first time
go test ./...                                           # every geta.New check, the test, the golden document
go run .                                                # serves on :8080
```

Rules:
- The root package (`routes/`) must declare `type Env` as an alias of the `Route` parameter type (`type Env = *app.Env`). Without it, the build fails with `undefined: Env`. The generated `zz_routes.go` declares `func Table(env Env) geta.Table`, which passes `env` to every `Route` and `Scope`.
- The Env type lives in its own package (`app`), because route packages cannot import `routes`, which imports them.
- The root package's name is the directory's name. Characters that cannot appear in an identifier become `_` (`my-api` becomes `my_api`). geta puts a `_` before a leading digit of any script (`1api` becomes `_1api`, `٣api` becomes `_٣api`), before a Go keyword (`type` becomes `_type`), and before a lone `_` (`__`). The root's own files use that package name.
- `-update` exists only in test packages that import getatest. `zz_routes_test.go` uses `getatest.New`, so `go test ./... -update` rewrites every golden file when all test packages import getatest. Other packages refuse the flag (`flag provided but not defined: -update`). In that case, pass `-update` only to the packages that call `Golden`.

## Packages

| Package | What |
| --- | --- |
| `geta` | Routes, operations, JSON, form, multipart, and raw bodies, scopes, middleware (with pluggable content codings in `geta.Compress`), automatic OPTIONS, the security gate, streams, upgrades, assembly (`geta.New`), OpenAPI 3.1 or 3.2, and `geta.Run` |
| `geta/getatest` | A test client over an in-memory network, and golden OpenAPI files ([Testing](testing.md)) |
| `geta/getaotel` | A separate module. OpenTelemetry on otelhttp ([OpenTelemetry](otel.md)) |
| `geta/getavet` | A separate module. An analyzer that catches mistakes before the program runs ([getavet](tools.md#getavet)) |
| `geta/cmd/geta` | `geta sync`, `geta check` ([The geta command](tools.md#the-geta-command)) |
