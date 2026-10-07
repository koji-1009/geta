# Testing

## Testing

```go
func TestGetUser(t *testing.T) {
	env := app.Open()
	c := getatest.New(t, routes.Table(env), routes.Options(env)...).Bearer("token")  // runs geta.New: assembly errors fail here
	res := c.Get("/users/1")
	if res.Status != 200 { t.Fatal(res.Status, res.Text()) }
	u := res.JSON[model.User]()        // fails the test on unknown members
	_ = c.Post("/users", map[string]any{"id": "2"})
	p := c.Get("/missing").Problem()   // geta.Problem
	_ = p
	s := c.Stream("/events")           // s.Next() reads one event
	_ = s
	getatest.Golden(t, c.App(), "openapi.json") // `go test . -update` rewrites it
	_ = u
}
```

The client:
- `c.Get(path)`, `c.Delete(path)`, `c.Post(path, body)`, `c.Put(path, body)`, `c.Patch(path, body)`, `c.Query(path, body)`: `c.Do` with that method.
- `c.Do(method, path, body) *getatest.Response`: `string`/`[]byte` bodies go as is, other non-nil bodies as JSON, typed `application/json` unless `c.With("Content-Type", ...)`.
- Form bodies: `c.Form(method, path, url.Values)`, `c.Multipart(method, path, url.Values, getatest.FilePart{Field, Filename, ContentType, Content})`. Raw body: `c.Content(method, path, contentType, body)`.
- `c.Send(req *http.Request)` adds client headers; the request's own headers win. In-memory connections take read deadlines: test a `Timeout`'s 408 with a stalling body (an `io.Pipe` reader a goroutine partly writes, closed by `t.Cleanup`). In `synctest.Test` the deadline passes without waiting.
- `c.Stream(path) *getatest.EventStream` sends `Accept: text/event-stream` unless the client sets one. `s.Next() (getatest.Event, bool)` reads one event (`Name`, `ID`, `Data`); `s.Comments` counts keep-alives; `s.Response` is the refusal if not streamed; `s.Close()` hangs up. `s.Err()` is nil after a stream's end, else what cut it off. `c.Get` and `Send` read a body to its end: read a stream that stays open with `Stream`.
- `c.Upgrade(path, protocol) *getatest.Upgraded` sends `Connection: Upgrade`, `Upgrade: protocol`. `u.Switched`: got 101. `u.Conn`: the switched `io.ReadWriteCloser`, closed at test end. `u.Response`: fully read on refusal.
- `Stream`, `Upgrade` send client headers once. A client `Accept` overrides `Stream`'s; `Upgrade`'s protocol overrides the client's.
- `c.With(key, value)`, `c.Bearer(token)` replace earlier values (`Bearer("b")` after `Bearer("a")` sends only `b`).
- `c.HTTP() *http.Client`: the underlying client, skipping client headers and document checks. `c.URL()`: base URL. `c.App()`: the assembled `*geta.App`.
- `c.DialContext` reaches the app at any address; `c.URL()` is `http://example.com`, a real host. Give it to self-dialing clients: `websocket.Dialer{NetDialContext: c.DialContext}` (gorilla/websocket). These skip client headers and document checks.
- `getatest.Serve(t, app)` serves an app you assembled.
- `*getatest.Response`: `Status`, `Header`, `Body`. `res.JSON[T]()` decodes, failing on unknown members. `res.Problem()` fails unless Content-Type is `application/problem+json`. `res.ProblemAs[P]()` reads a described problem. `res.Text()`: body string.

The test fails when the serving operation (after a root rewrite, the one served) answers:
- an undocumented status (declare middleware statuses with `.Answers`);
- a JSON success body its schema rejects;
- a `geta.OnAsProblem` row's problem outside its status's schema;
- an event (via `s.Next()`) its event type's schema rejects;
- a header with a declared schema (middleware `HeaderOf`, described row or output header field) whose value fails it (read as a request header of its type), or required and missing. Multi-line headers fail. Unchecked: geta's own `Allow`, 415 `Accept`, `Deprecation`, `Sunset`, `Set-Cookie`. Headers are checked whatever the body.

Bodies are checked when Content-Type begins `application/json` or `application/problem+json` without Content-Encoding (Compress-coded: unchecked). `Stream`, `Upgrade` check refusal bodies like `Send`. The checks are `App.Documented`, `App.Conforms`.

`getatest.Golden(t, app, path)` compares the document with the file. `-update` writes it, creating directories. Otherwise it fails naming the first differing line, or `-update` if the file is missing. The document is deterministic, so a committed openapi.json and git diff show what changed; to compare against a contract written elsewhere, use an OpenAPI diff tool such as oasdiff.

Unit-test handlers as methods with fake stores; time-dependent behaviour with `testing/synctest`.

