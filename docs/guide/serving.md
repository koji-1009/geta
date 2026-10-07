# Assembly, serving, and the document

## Assembly and serving

```go
a, err := geta.New(routes.Table(env), geta.WithLogger(log), geta.WithInfo(geta.Info{Title: "API", Version: "1.0.0"}))
if err != nil { log.Fatal(err) }   // every assembly mistake is reported here, all at once
if err := geta.Run(ctx, &http.Server{Addr: ":8080", Handler: a}); err != nil { log.Fatal(err) }   // nil after a clean shutdown
```

### Run and Serve

- `geta.Run(ctx, srv)` serves until `ctx` ends or SIGINT/SIGTERM, then drains: streams end; in-flight requests get `geta.ShutdownGrace` (30s); connections close. Returns nil on clean shutdown, `context.DeadlineExceeded` when the grace ran out and the connections were closed, else the listen or serve error.
- `geta.WithShutdownGrace(d)` (Run or Serve): longer than the longest operation that should finish (the default cuts a `Doc.Timeout` past 30s), shorter than the platform's kill grace. Non-positive: refused, nothing served.
- Fills `ReadHeaderTimeout` (10s) and `IdleTimeout` (2m), each only when zero and `ReadTimeout` is zero. Set fields are kept; negative = no limit. Neither cuts open event streams.
- Never sets `ReadTimeout`/`WriteTimeout` (would cut streams and upgrades). Bound operations with `geta.Timeout`; without it nothing bounds a slow body sender. `geta.Timeout` bounds the time to a response, not a client's reading of it: a client that stops reading holds its connection until `srv.WriteTimeout` (set it in an App with no stream or upgrade) or a proxy ends it.
- Sets `DisableGeneralOptionsHandler`; keeps a caller's `BaseContext`.
- `TLSConfig` with `Certificates` or `GetCertificate`: TLS with HTTP/2 unless the server disables it. `GetConfigForClient`: HTTP/2 if the returned config offers `h2` in `NextProtos`.
- Empty `Addr`: `:https` with TLS, else `:http`.
- `geta.Serve(ctx, srv, listener)`: same on your listener; closes it on return.

### The document

- `a.OpenAPI()`: deterministic byte for byte, independent of table order. 3.1.0 default; 3.2.0 via `geta.WithOpenAPI(geta.OpenAPI32)`; other versions fail `geta.New`.
- 3.2 adds (3.1 unchanged): per-event stream description (`itemSchema`: `data` a string whose `contentSchema` is the event type; `event`/`id` required when the type names/identifies every event); a `summary` (reason phrase) per response; envelope cookies in its `Set-Cookie` schema; `Route.Query` as the path's `query`.
- A tag is a name in `Doc.Tags`; no 3.2 tag fields.
- Every status an operation can answer is listed ([Declaring what a middleware answers](middleware.md#declaring-what-a-middleware-answers)).
- Text must be UTF-8. Non-UTF-8 `Doc.Summary`, `Description`, `OperationID`, tag, or failure row detail fails `geta.New`, naming operation and field (`GET /t (file.go:12): Doc.Summary "caf\xe9" is not UTF-8`). Other text (`doc` tag, `WithInfo`) is named by JSON Pointer (`OpenAPI document text at /components/schemas/T/properties/a/description is not UTF-8: ...`).
- Without `Doc.OperationID`, an operation's ID is the lower-case method and each path segment capitalized: a parameter after `By`, a literal keeping its letters, digits, `.`, `-`, and `_` (`GET /users/{id}/posts` is `getUsersByIdPosts`, `GET /a-b` is `getA-b`, `GET /` is `getRoot`). Paths that differ only in other characters or in case (`/a~b` and `/ab`, `/aB` and `/a/b`) derive one ID.
- Duplicate operation IDs or schema names fail `geta.New`; give one of the operations a `Doc.OperationID`.
- Given `geta.Observe`'s `Match`, `App.Documented(m, status)` and `App.Conforms(m, status, header, body)` check a response against the document. `App.Types(method, template)` returns an operation's In and Out types. `Conforms` reports a non-JSON success body; it skips responses without their own schema (plain problem, raw body, upgrade, empty body).

### Other servers and HTTP/3

An App is an `http.Handler`; any server, quic-go's HTTP/3 included, serves it. geta does not depend on quic-go and sets no `Alt-Svc`; announce HTTP/3 from the TCP server's handler.

```go
h3 := &http3.Server{Addr: ":443", Handler: app, TLSConfig: http3.ConfigureTLSConfig(tlsConf)} // github.com/quic-go/quic-go/http3
go h3.ListenAndServe()
srv := &http.Server{Addr: ":443", TLSConfig: tlsConf, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	h3.SetQUICHeaders(w.Header()) // Alt-Svc: h3=":443"; ma=2592000
	app.ServeHTTP(w, r)
})}
err := geta.Run(ctx, srv)
```

`geta.Run` drains only its server; shutting down the HTTP/3 server and its streams is yours. `examples/booking` serves both on one port with `-tls` (`transport` package, quic-go v0.63.0).

