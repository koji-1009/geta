# auth

Authentication by declaration. A bearer token and a cookie session sit behind one `geta.Secure` gate, and each route declares which it takes. Authorization is middleware after the gate, so 401 and 403 stay apart.

| Pattern | Where |
| --- | --- |
| One policy with two schemes (bearer, an apiKey cookie), the default a bearer token | `auth/auth.go` |
| A public route (`Security: []geta.Scheme{}`), a cookie-only route, a bearer-only route | `routes/public/route.go`, `routes/me/route.go`, `routes/admin/whoami/route.go` |
| A role check at `geta.OrderAuthorize` answering 403, for a whole directory | `auth/auth.go` (`RequireRole`), `routes/admin/scope.go` |
| A login form (`body:"form"`) whose password is a `geta.Password`, setting the session cookie from an envelope | `routes/login/route.go` |
| A rate limit on login attempts: the application's own limiter in the login's `Doc.BeforeGate`, declaring its 429 and `Retry-After` | `ratelimit/ratelimit.go`, `routes/login/route.go`, `TestLoginIsRateLimited` |
| An event stream that ends when its session is revoked | `routes/me/events/route.go` |
| The OpenAPI 3.1 and 3.2 documents of one table | `auth_test.go` (`TestDocumentCarriesTheDeclarations`, `TestDocument32`) |

## Run

```
go run ./examples/auth -addr 127.0.0.1:8080
```

Bearer tokens `admin-token` and `member-token`; logins `admin` / `admin-pass` and `member` / `member-pass`.

```
curl -s -H 'Authorization: Bearer admin-token' http://127.0.0.1:8080/admin/whoami
curl -si -c jar -d username=member -d password=member-pass http://127.0.0.1:8080/login
curl -s -b jar http://127.0.0.1:8080/me
curl -sN -b jar http://127.0.0.1:8080/me/events
```

## Test

```
go test ./examples/auth/...
go test ./examples/auth -run 'TestDocument' -update   # rewrite openapi.json and openapi.3.2.json after a reviewed change
go run github.com/koji-1009/geta/cmd/geta sync -check examples/auth/routes
```

## Client check

```
uvx openapi-python-client generate --path openapi.json --output-path $OUT --overwrite
go run . -addr 127.0.0.1:18280 &
uv run --with $OUT --with httpx python clientcheck/roundtrip.py http://127.0.0.1:18280
```
