# booking

Meeting rooms and their bookings, behind an OpenID issuer's ES256 tokens. It depends on golang-jwt, keyfunc, jwkset, getaotel, coder/websocket, klauspost/compress, golang.org/x/time, and quic-go.

| Pattern | Where |
| --- | --- |
| Bearer JWTs verified by the example's own `jwtauth` package on golang-jwt (a `geta.Verifier`, its refusals as 401 challenges or a 503 while no key is held), keys discovered and refreshed by keyfunc; `jwtauth.RequireScopes` on each write, its scope stated in the write's bearer requirement through `Middleware.Scopes`. geta ships no JWT verifier: copy `jwtauth/` or write your own | `jwtauth/`, `auth/auth.go`, `issuer/issuer.go`, `routes/rooms/route.go` |
| A sealed type (`oneOf`) as a request body behind `geta.RequireConditional`, and in a history | `model/model.go`, `routes/bookings/booking_/changes/route.go` |
| Format types (uuid, date, time, duration, ipv4, ipv6, date-time, JSON Pointer), an application's `geta.FormatType`, a type with its own JSON narrowed by `WithSchema` | `model/` |
| Query parameters: a repeated one (`*[]string`), a deepObject filter (`capacity[min]`), an integer enum, a default, `doc` and `examples` | `routes/rooms/route.go` (`ListIn`) |
| `geta.OnAs` and problem types | `failures/failures.go` |
| zstd beside gzip, OpenTelemetry (getaotel), a rate limit, a concurrency cap, per-app `Limits` | `routes/scope.go`, `routes/options.go`, `coding/`, `telemetry/` |
| WebSocket over `geta.Upgrade{Accept: ...}` (coder/websocket) | `routes/rooms/room_/live/route.go` |
| HTTP/2 and HTTP/3 on one port | `transport/transport.go` |

## Run

```
cd examples/booking
go run . -addr 127.0.0.1:8080 -issuer-addr 127.0.0.1:8081 -debug-addr 127.0.0.1:8082
```

Without `-issuer`, a demo issuer starts in the same process. Its clients are `frontdesk` (rooms and all bookings), `member` (its own bookings), and `viewer` (read only). Each secret is `<client>-secret`:

```
TOKEN=$(curl -s -d grant_type=client_credentials -d client_id=frontdesk -d client_secret=frontdesk-secret http://127.0.0.1:8081/token | jq -r .access_token)
curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Kaede","capacity":12,"opens":"08:00:00Z","closes":"18:00:00Z","hourlyRate":"3000.00","floor":2,"features":["projector"]}' \
  http://127.0.0.1:8080/rooms
curl -sg -H "Authorization: Bearer $TOKEN" 'http://127.0.0.1:8080/rooms?feature=projector&capacity[min]=8&floor=2'
```

`-tls` serves HTTPS and HTTP/3 with a self-signed certificate. `/debug/spans` and `/debug/routes` on `-debug-addr` show the traces.

## Test

```
go -C examples/booking test ./...
go -C examples/booking test -run TestDocuments . -update   # rewrite openapi.json and openapi.3.2.json after a reviewed change
go -C examples/booking run github.com/koji-1009/geta/cmd/geta sync -check routes
```

## Client check

```
uvx openapi-python-client generate --path openapi.json --output-path $OUT --overwrite
go run . -addr 127.0.0.1:18180 -issuer-addr 127.0.0.1:18181 &
uv run --with $OUT --with httpx python clientcheck/roundtrip.py http://127.0.0.1:18180 http://127.0.0.1:18181
```

`clientcheck/panel.py` reads a room's live feed over WebSocket; its docstring has the commands.
