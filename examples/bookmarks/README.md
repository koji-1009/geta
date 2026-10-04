# bookmarks

An API that a single-page app on another origin calls with a session cookie. It runs behind a reverse proxy. It serves OpenAPI 3.2 because its QUERY operation needs it.

| Pattern | Where |
| --- | --- |
| Cookie sessions as the gate's default scheme (an apiKey in the `sid` cookie); login and logout setting and removing the cookie from an envelope | `auth/auth.go`, `routes/login/route.go`, `routes/logout/route.go` |
| CORS for one origin with credentials (`AllowCredentials`), beside net/http's `CrossOriginProtection` refusing other origins' writes | `routes/scope.go` |
| A rate limit keyed by the client address read through the proxies you run (`X-Forwarded-For` from the right, trusted prefixes only) | `clientip/clientip.go`, `ratelimit/ratelimit.go` (the application's own limiter, declaring its 429 and `Retry-After`), `routes/scope.go` |
| A `geta.Use` root middleware counting requests by template through `geta.Observe`, 404s and 405s under one label | `metrics/metrics.go`, `routes/metrics/route.go` |
| An input header parameter: `Idempotency-Key` on a POST, a retry answered with the stored result | `routes/bookmarks/route.go` (`CreateIn`), `idempotency/idempotency.go` |
| An input cookie parameter with an enum and a default | `routes/me/route.go` |
| QUERY with a JSON body, in an OpenAPI 3.2 document | `routes/bookmarks/route.go` (`Search`), `routes/options.go` |

## Run

```
go run ./examples/bookmarks -addr 127.0.0.1:8080 -origin http://localhost:5173
```

Flags:

- `-origin` is the page's origin.
- `-trusted-proxies 10.0.0.0/8,::1/128` lists the reverse proxies whose `X-Forwarded-For` entries the rate limit trusts. The default is none, so the limit keys by the peer address.
- `-insecure-cookies` leaves `Secure` off the session cookie, for plain HTTP away from localhost.

Users: `ada` / `ada-pass` and `bo` / `bo-pass`.

```
curl -s -c jar -H 'Content-Type: application/json' -d '{"user":"ada","password":"ada-pass"}' http://127.0.0.1:8080/login
curl -s -b jar -b lang=ja http://127.0.0.1:8080/me
curl -si -b jar -H 'Content-Type: application/json' -H 'Idempotency-Key: "3f0c9a7e-2b1d-4e8f-9a6b-5c4d3e2f1a0b"' \
  -d '{"url":"https://go.dev/","title":"Go","tags":["go"]}' http://127.0.0.1:8080/bookmarks   # send it twice: one bookmark
curl -s -b jar -X QUERY -H 'Content-Type: application/json' -d '{"text":"go","limit":5}' http://127.0.0.1:8080/bookmarks
curl -s http://127.0.0.1:8080/metrics
```

The page calls it as `fetch("http://127.0.0.1:8080/bookmarks", {credentials: "include"})`. The cookie is `SameSite=Lax`. A browser sends it from a page on the same site (https://app.example.com calling https://api.example.com). A page on another site needs `SameSite=None; Secure`.

## Test

```
go test ./examples/bookmarks/...
go test ./examples/bookmarks -run TestDocument -update   # rewrite openapi.json after a reviewed change
go run github.com/koji-1009/geta/cmd/geta sync -check examples/bookmarks/routes
```

There is no generated-client check. openapi-python-client reads only OpenAPI 3.1, and this document is 3.2.
