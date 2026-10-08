# Scopes and middleware

## Scopes and middleware

```go
func Scope(env Env) geta.Scope {
	return geta.Scope{
		geta.AccessLog(env.Log),
		geta.CORS(geta.AllowOrigins("*")),
		geta.Recover(env.Log),
		limiter, // your own rate limiter, at OrderShed (Rate limiting, below)
		geta.ConcurrencyLimit(256),
		geta.Timeout(10 * time.Second),
		geta.Gzip(),
		geta.ETag(),
		geta.Secure(policy),
	}
}
```

- Run order: outer directory to inner, top to bottom, then `Doc.Scope`. `routes/scope.go` (root scope) also wraps 404 and 405.
- Checked order: `OrderObserve < OrderCrossOrigin < OrderRecover < OrderShed < OrderDeadline < OrderNegotiate < OrderValidate < OrderAuthenticate < OrderAuthorize`. A descending chain fails `geta.New` naming both ends, across the root/directory seam too; equal and unordered entries pass. geta's middleware carry ranks: `ETag` outside `Gzip`, `CORS` inside `Recover` are refused.
- Plain net/http middleware: `geta.Use(fn)` (unordered) or `geta.Ordered(order, fn)`.
- Construction mistakes fail `geta.New` wherever the middleware sits (root, directory, `Doc.Scope`, `Doc.BeforeGate`), e.g. `ConcurrencyLimit` below 1, nil logger. A nil `*slog.Logger` to `geta.AccessLog`, `geta.Recover`, `geta.WithLogger` fails; `slog.New(slog.DiscardHandler)` records nothing.
- Pass on contexts derived from `r.Context()`. A root middleware passing another: logged 500 defect before any operation runs.

### Declaring what a middleware answers

- `.Answers(status, "reason")` documents a status the middleware can answer. 1xx or outside 100–599 fails `geta.New`, naming middleware and position (`root scope: middleware 1 (middleware): Answers(103, "Hints"): 103 is an informational (1xx) status`).
- Documented as:
  - 4xx/5xx: a problem; failure rows' reasons first, then every other cause (middleware `.Answers`, geta's own 400/413/415/500/504); none replaces another.
  - 2xx/3xx the operation answers as success: reason after the status text (`OK; Served from the cache`).
  - Other 2xx/3xx (http-to-https `.Answers(308, "...")`, a 202): no content; description the reasons joined by `; `; headers from `.Header` (the redirect's `Location`); body unchecked.
  - 304: only on GET operations with success 200, as `geta.ETag`'s.
- `.Header(status, "Retry-After", "description")` declares a header with that status: documented, optional, string, on that status of every operation behind the middleware (`options` operations included), exposed by `geta.CORS`. Non-token name, `Content-Type`, or status not in `.Answers` fails `geta.New`.
- Typed: add `geta.HeaderOf[T](constraints)`: `.Header(429, "X-RateLimit-Remaining", "", geta.HeaderOf[uint32](""))`, `.Header(429, "Retry-After", "...", geta.HeaderOf[int]("minimum=1"))`. T: string, number, boolean, or `encoding.TextMarshaler` (`time.Time` is date-time), not a pointer. Constraints follow response schema tag rules; enum member, default, example must be header field values. Anything else, zero `geta.HeaderType`, or a second `HeaderOf` on one `.Header` fails `geta.New`.
- No runtime writing or checking. In tests `App.Conforms` holds it to the schema: `Retry-After: 0` against `HeaderOf[int]("minimum=1")` fails (`a header does not match the documented schema: Retry-After: 0 is less than minimum 1`). Absent header passes, as does any value of an untyped `.Header`.
- A response states a header once, any spelling (first described row's; required where every cause sets it). All `HeaderOf` of one name on a status (described row's, output header field, other middleware's) must agree, or `geta.New` fails naming both. Untyped `.Header` yields to typed. geta's own 415 `Accept` headers replace a row's.
- geta's own declare theirs: `Secure`'s `WWW-Authenticate` on 401 (not on operations requiring nothing).
- `.Scopes(scheme, "read:pets", ...)` states required scopes (Security).

### Rewrites and the match

- `geta.Matched(ctx)` gives any middleware the matched template and `Doc`.
- A root middleware rewriting method or path makes geta match again; later gates, the handler, and `Matched` see the served operation. Unserved target: 404, or 405 with the new path's `Allow`. Rewritten OPTIONS answers the new path. POST rewritten to QUERY: served by the QUERY operation.
- Between a rewrite and the next gate `Matched` reads the arrival's match; from that gate on, the operation reached.
- Rewrite before the root `geta.Secure`. A later rewrite reaching an operation the root gates have a scheme for, other than the one checked: 500 defect, no handler, any method. With two root gates, a move between them is refused for any operation either checks. Moves to a public operation, or none, are served. The 500 is in every operation's document.
- Dispatch fixes the operation: rewrites in a directory scope or `Doc.Scope` change neither what is served nor what a gate there checks.
- A root middleware after the gate moving to an operation whose `Doc.BeforeGate` did not run: 500 defect.
- `geta.Observe(ctx)` returns a context and `read()` reporting method and `Match` served (root rewrite's target; method of a 404/405; ok false for 404, 405, redirect). Wrapping an App: `ctx, read := geta.Observe(r.Context()); app.ServeHTTP(w, r.WithContext(ctx)); method, m, ok := read()`.

### Timeout

- `geta.Timeout(d)` sets a context deadline `d` away; it stops nothing. A handler watching its context returns `context.DeadlineExceeded`: 504 (CORS headers kept). One not watching runs to the end; its response is sent. An earlier (outer) deadline is kept.
- At OrderDeadline, outside compression, validators, and gate: it bounds verifier work too. A stream or protocol switch lifts it.
- Body window: the deadline length (shortest Timeout in the chain, `Doc.Timeout` counted) from the first body read. A body (any kind, chunked too) not arrived by then: 408, handler not run, concurrency slot freed. A raw `io.Reader` read past it errors; that error returned by the handler is the 408. Time before reading does not shorten the window.
- Mechanism: connection read deadline (`http.ResponseController.SetReadDeadline`), cleared once the body is read whole; never cuts a response, stream, or switched connection. Your response writer without `Unwrap` takes no deadline; the body reads unbounded.
- Exactly the operations reading a body behind a `Timeout` document the 408.
- `Doc.Timeout` sets one operation's length, longer or shorter (`Doc{Timeout: 5 * time.Minute}` upload, `100 * time.Millisecond` lookup); every chained `geta.Timeout` uses it from where it runs. `geta.Timeout` cannot follow `Compress`, `ETag`, `Secure` (lower rank): use `Doc.Timeout`, not an inner `Timeout`; for a directory, same value on each operation. After a root rewrite the served operation's length counts, unless the deadline passed.
- `geta.New` refuses negative `Doc.Timeout`, and positive without `geta.Timeout` in the chain.

### Built-in middleware

- `geta.Recover(log)`: panic (`Key.Must` included) becomes 500 with logged stack and CORS headers. Panic after the response started (streamed body included): logged, connection aborted. `http.ErrAbortHandler` passes unchanged.
- `geta.ConcurrencyLimit(n)`: sheds with 503, no `Retry-After`, documented. Slots freed on return and panic; every nested limit's slot freed when a stream starts or a connection switches.
- `geta.AccessLog(log)`: logs method and route template served (after root rewrite), never the raw path, and status sent (net/http's 200 when nothing wrote). A clean-path 308 logs the template it leads to, as getaotel names its span. A method is logged cut to 128 bytes, in every log line geta writes: a request no operation serves may carry any token as its method. Streams and upgrades marked. A passing panic (`http.ErrAbortHandler`, or uncaught by an inner `Recover`): logged `aborted=true` with the status written before it (500 if none), and goes on unchanged. An inner `Recover`'s 500 logs as any status.
- geta's own client errors (400 of binding, precondition list, or asterisk form; 408; `Conditional`'s 412, 428, and the 412 geta answers where no `Conditional` is embedded; 413; 415; 426): logged at Debug on the App's logger (`WithLogger`, else `slog.Default()`) as "geta: request refused" with `method`, `route`, `status`, `detail`, plus for violations `violations` (count listed), `in` (places, e.g. `query,body`), and `omitted` (count not listed, when any). No request values or violation text. Not logged: 404, 405, middleware answers, 5xx. A defect's 500 logs at Error.
- `AccessLog`, `Recover`, `ETag`, `Compress` writers unwrap: `http.ResponseController`'s `SetWriteDeadline`, `SetReadDeadline`, `EnableFullDuplex` reach the server's writer.

### CORS

- `geta.CORS(geta.AllowOrigins(...))`: root scope only (OPTIONS runs the root scope alone); `geta.New` refuses it in a directory scope, `Doc.Scope`, `Doc.BeforeGate`. An origin is written as a browser sends it (`https://app.example`, `http://localhost:5173`); `geta.New` refuses a path, a trailing slash, upper case, or user info, which no `Origin` would match.
- Wildcard and specific origins (`Vary: Origin` for allowed and refused), credentials (`AllowCredentials`). A preflight (QUERY's behind a gate too) needs no credentials, spends no root rate-limiter token. An unwritten response is completed as any other.
- Preflight `Access-Control-Allow-Methods`: the path's `Allow` less HEAD and OPTIONS; none for an unserved path. `AllowMethods` only adds.
- Preflight `Access-Control-Allow-Headers`: the asked operation's request headers: header parameters (`Conditional`'s four included), `Authorization` or an apiKey header for its schemes, `Content-Type` with a body.
- `Access-Control-Expose-Headers`: the answering operation's headers: declared output headers, described rows' headers, `ETag` behind `geta.ETag`, chained middleware `.Header`s (`Retry-After`, `Doc.BeforeGate`'s included), `WWW-Authenticate` where a scheme is required (401 included), `Deprecation`/`Sunset`, `Allow` on plain cross-origin OPTIONS. Never `Set-Cookie`.
- `AllowHeaders`/`ExposeHeaders` only add; a request no operation serves gets only those.
- `geta.PreflightMaxAge(d)` sends `Access-Control-Max-Age`; `d` not a positive whole number of seconds fails `geta.New`. Without it none is sent.

### Compression and ETag

- `geta.Compress(codings...)` codes text-like bodies of at least `geta.CompressThreshold` (1024) bytes in the client's preferred coding. `geta.Gzip()` = `geta.Compress(geta.GzipCoding())`; `GzipCoding` is the only shipped coding.
- `geta.Coding{Name, NewWriter}`: any other coding. `NewWriter(w)` returns an encoder into `w`, called once per coded response: one `Write` of the whole body, one `Close`. Any error: body sent uncoded with the identity tag.
- Negotiation (RFC 9110 §12.5.3): among accepted codings (named with q > 0, or unnamed under `*` q > 0; `x-gzip` names gzip), highest q wins, your order breaks ties, identity wins only with higher q.
- Weights only as RFC 9110 §12.4.2 writes them: OWS `;` OWS `q=`, `q` either case, 0 with up to three decimals or 1 with up to three zeros. Anything else after a coding (`q=2`, `q=abc`, `q = 1`, no-break space, another parameter) refuses what the element names, like `q=0`. Codings: tokens, ASCII case-insensitive only (the Kelvin sign names no `k`).
- Identity refused (`identity;q=0`, or `*;q=0` without identity): every body coded, any size or type. None of your codings accepted: uncoded, never 406.
- Uncoded: streams, 204, 206 (its `Content-Range` counts uncoded bytes), 304, already-encoded bodies. All but streams vary on `Accept-Encoding` (204, 304, empty included); streams carry no `Vary`. HEAD gets GET's header.
- `geta.New` refuses: no coding; coding without `NewWriter`; indistinguishable names (not a token, `identity`, `*`, `x-gzip` (name it gzip), ending in `-identity`, duplicate, one ending in `-` plus another's name, e.g. `x-br` beside `br`). Your own coding named `gzip` replacing `GzipCoding()` is the gzip sent.
- Each coded form gets its own strong tag (`"v1"` → `"v1-gzip"`, `"v1-zstd"`, `"v1-br"`), read back as `"v1"` in `If-Match`/`If-None-Match` before anything inside sees the request: `Conditional.Check` and `geta.ETag` compare your own tag; a client holding a coded form gets its 304 and writes with its tag. Weak tags unchanged. Your tag that would read as a coded form's (ending in `-` plus a coding name) gets `-identity` appended. Uncoded, small, streamed, or empty responses carry the identity tag; empty ones also vary on `Accept-Encoding`.
- `geta.ETag()` tags GET and HEAD 200s only (keeps a handler's own tag), answers 304, documented on GET operations. Reads `If-None-Match` as `Check`: entity-tag list (comma in quotes stays), all lines one list; anything else 400, documented beside the 304. On a GET whose input embeds no `Conditional`, it evaluates every precondition against the tag it sends, in RFC 9110 §13.2.2's order: `If-Match` other than that tag (or `*`) 412, malformed 400 ([Conditional requests](conditional.md)). The document states its ETag, optional, on the GET's 200 and 304, and `If-None-Match` as an optional header parameter (CORS allows it). A stream passes through untouched, so its document states none of these.
- zstd from your own dependencies (geta imports none):

```go
var zstdPool sync.Pool // github.com/klauspost/compress/zstd

type pooledZstd struct{ *zstd.Encoder }

func (e pooledZstd) Close() error { err := e.Encoder.Close(); zstdPool.Put(e.Encoder); return err }

var zstdCoding = geta.Coding{Name: "zstd", NewWriter: func(w io.Writer) (io.WriteCloser, error) {
	if e, ok := zstdPool.Get().(*zstd.Encoder); ok {
		e.Reset(w)
		return pooledZstd{e}, nil
	}
	e, err := zstd.NewWriter(w, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	return pooledZstd{e}, nil
}}

// in the root scope, before ETag:
geta.Compress(zstdCoding, geta.GzipCoding()), geta.ETag(),
```

`examples/booking` serves zstd beside gzip this way (`coding` package), on klauspost/compress v1.20.1.

### Rate limiting

geta has no rate limiter; count storage and request keys are your policy. Write middleware at `OrderShed` (after `Recover`, before deadline and gate) declaring its 429 and `Retry-After`:

```go
func Limit(allow func(r *http.Request) (wait time.Duration, ok bool)) geta.Middleware {
	return geta.Ordered(geta.OrderShed, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if wait, ok := allow(r); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
				geta.WriteProblem(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}).Answers(http.StatusTooManyRequests, "Rate limit exceeded").
		Header(http.StatusTooManyRequests, "Retry-After", "The whole seconds until the rate limit admits a request",
			geta.HeaderOf[int]("minimum=1"))
}
```

- `allow` is your store and key: in-memory token bucket per client address (`ratelimit` packages of `examples/auth`, `examples/bookmarks`; `examples/booking`'s over `golang.org/x/time/rate`), or a store shared by your instances.
- Behind a reverse proxy `r.RemoteAddr` is the proxy's; key by the client address your proxies record (`examples/bookmarks` `clientip` reads `X-Forwarded-For` from the right, trusted prefixes only).
- `geta.HeaderOf[int]("minimum=1")` documents `Retry-After` as `{"type":"integer","format":"int64","minimum":1}`; `max(1, ...)` keeps to it. Without it: documented as a string.
- A limit needed before credentials are checked (a login's) goes in `Doc.BeforeGate`.

### One operation's middleware: `Doc.Scope`

`Doc.Scope` runs after the URL's directory scopes, for that operation only, before input binding. Use it for a rule one method has:

```go
admin := geta.Scope{auth.RequireAdmin()} // geta.Ordered(geta.OrderAuthorize, ...) answering 403, declared with .Answers(403, "Not an administrator")
return geta.Route{
	Get: geta.Op(http.StatusOK, h.Get, geta.Doc{Summary: "Fetch a user", Failures: common}),
	Put: geta.Op(http.StatusOK, h.Put, geta.Doc{Summary: "Replace a user", Failures: common, Scope: admin}),
}
```

- A refused caller gets its status (403) before the body is read, not a 400.
- Order checked with the whole chain. Its `.Answers` documented on that operation only (a directory scope's on every method of its URL). A `geta.Secure` in it secures that operation only.
- Check authorization this way, not in the handler.

### One operation's middleware before the gate: `Doc.BeforeGate`

`Doc.Scope` runs after the gate, so a rate limiter there (OrderShed, below OrderAuthenticate) fails `geta.New`. Rules needed before credentials are checked go in `Doc.BeforeGate`:

```go
Post: geta.Op(http.StatusOK, h.Post, geta.Doc{
	Security:   []geta.Scheme{},
	BeforeGate: geta.Scope{loginLimit}, // your limiter (Rate limiting, above), declaring .Answers(429, ...) and .Header(429, "Retry-After", ...)
}),
```

- Runs where the operation's first `geta.Secure` runs (root scope for that operation alone, or the directory scope holding the gate; no gate: after the root scope). Order checked against what it joins (`Recover, [BeforeGate], Secure` passes; root `Timeout` before the gate fails).
- On a protected operation it limits strangers before credentials (429, not 401).
- After a root rewrite before the gate, the target's BeforeGate runs. Beside a `Doc.Scope` gate: just before that gate. Redirects and OPTIONS run none.
- Its answers are documented, and headers CORS-exposed, on that operation alone. A gate in it fails `geta.New`.
- `examples/auth` limits `/login` this way (`routes/login/route.go`).

