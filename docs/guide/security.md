# Security

## Security

```go
policy := geta.Policy{
	Default:   []geta.Scheme{geta.Bearer},
	Verifiers: map[string]geta.Verifier{
		geta.Bearer.Name: func(r *http.Request) (context.Context, error) {
			// return auth.Caller.With(r.Context(), principal), nil on success
			return nil, geta.ErrUnauthenticated // 401; geta.ErrUnavailable is 503
		},
	},
}
```

- Verifier error: nil admits; `geta.ErrUnauthenticated` (or wrapping it) 401; `geta.ErrUnavailable` (or wrapping it) 503; other: logged 500 defect with `instance`, error not in body.
- Schemes are alternatives; the first admitting admits. If none: defect, then unavailable, then 401.
- `Doc.Security`: nil = policy default; `[]geta.Scheme{}` = public; list = alternatives. Several `Secure` gates must all admit; nil `Doc.Security` then needs one scheme per gate's default, in one requirement.
- Behind a root gate, unknown URLs are 401 to strangers.
- A verifier's context need not derive from the request's; `Secure` keeps geta's match.
- Principal: `var Caller = geta.NewKey[Principal]("caller")`; `Caller.Value(ctx)` or `Caller.Must(ctx)`. Keys are distinct by identity; the zero key panics.
- Authorization: middleware at `geta.OrderAuthorize` answering 403, in a directory scope or `Doc.Scope`.

### Schemes

`geta.New` refuses:

- a declared scheme without verifier (a root gate's default too, routes or not);
- a protected operation without `geta.Secure` in its chain;
- two different definitions of one name (identical members, flows included, are one);
- `Type` not `http`/`apiKey`/`oauth2`/`openIdConnect`/`mutualTLS`; no `Name` or `Type`; a `Name` outside `^[a-zA-Z0-9._-]+$`, which keys the document's `securitySchemes` (`"api key"`);
- `http` `Scheme` not a token;
- `apiKey` `In` not `header`/`query`/`cookie`, or a `Param` it cannot carry (header/cookie name not a token; query name with `&`, `=`, `;`, `#`, `+`, `%`, whitespace);
- `oauth2` without `Flows` or a flow lacking its grant's URLs; `openIdConnect` without `OpenIDConnectURL`;
- another type's member. Members: `http` `Scheme` (+`BearerFormat` with bearer); `apiKey` `In`, `Param`; `oauth2` `Flows`, `OAuth2MetadataURL`; `openIdConnect` `OpenIDConnectURL`; `mutualTLS` none; all `Description`, `Deprecated`.

oauth2: `geta.Scheme{Name: "oauth", Type: "oauth2", Flows: &geta.OAuthFlows{AuthorizationCode: &geta.OAuthFlow{AuthorizationURL: "https://id.example/authorize", TokenURL: "https://id.example/token", Scopes: map[string]string{"read:pets": "Read your pets"}}}}`.

- Flows take exactly their grant's URLs: `Implicit` `AuthorizationURL`; `Password`, `ClientCredentials` `TokenURL`; `AuthorizationCode` both; `DeviceAuthorization` `DeviceAuthorizationURL` + `TokenURL`; any `RefreshURL`.
- `Scopes` maps scope-token to description; `{}` if empty.
- URLs (flows', `OpenIDConnectURL`, `OAuth2MetadataURL`): absolute https, no fragment; http only on loopback (`localhost`, `127.0.0.1`, `::1`).
- `DeviceAuthorization`, `OAuth2MetadataURL`, `Deprecated`: OpenAPI 3.2 only; 3.1 refuses them.
- 401 carries one documented `WWW-Authenticate` challenge per refusing scheme; 503 none. Challenge: the verifier error's own (`geta.Challenger`); else `http` its scheme name (`Bearer`, `Basic`, as declared); `oauth2`, `openIdConnect` `Bearer`; `apiKey` `apiKey in="header", name="X-API-Key"`; `mutualTLS` `mutualTLS`.

### Scopes in the document

- Scope-requiring middleware declares them: `geta.Ordered(geta.OrderAuthorize, fn).Answers(403, "A scope is missing").Scopes(oauth, "read:pets")`.
- Operations behind it list them in that scheme's requirement (`{"oauth": ["read:pets"]}`); two such middleware add up. oauth2/openIdConnect: scopes; other types: roles.
- Alternatives lacking that scheme (API key beside bearer) leave those operations' requirements; runtime is unchanged (gates check them, CORS allows their header).
- `geta.New` refuses: no scope; a non-scope-token; a scope no oauth2 flow defines; scopes on a scheme no earlier gate requires of the operation (public operation, middleware before the gate, `Doc.BeforeGate`); a scheme definition differing from the gate's; scopes of two schemes no requirement holds together.

### Cookie sessions

`examples/auth`: cookie sessions. `examples/bookmarks`: a single-page app on another origin:

- CSRF: `http.CrossOriginProtection` beside `geta.CORS` with `AllowCredentials`.
- Client address behind proxies: `X-Forwarded-For` read from the right, trusted prefixes only, keying its own rate limiter.
- Idempotency: `Idempotency-Key` header parameter on a POST; retries get the stored result.

### JWT

geta ships no JWT verifier: a token is checked by the application's own `geta.Verifier`. `examples/booking/jwtauth` is one to copy, on golang-jwt/jwt/v5. It shows:

- A `geta.Verifier` that admits a valid bearer JWT and puts its claims under a `geta.Key`: only RS*/ES*/EdDSA, `exp`, issuer, and audience required, `crit` refused, `Leeway` on `exp`, `nbf`, and `iat`.
- Its refusals in geta's terms: no bearer credentials is `geta.ErrUnauthenticated` (401, bare challenge); a bad token an error wrapping `geta.ErrUnauthenticated` whose `Challenge()` (`geta.Challenger`) is `Bearer error="invalid_token", error_description="<reason>"`; a Keyfunc holding no key yet is `geta.ErrUnavailable` (503, no challenge).
- A misconfigured validator turned into `geta.Invalid("secure", err)` in the gate's scope, so `geta.New` reports it (`auth.Gate`).
- `RequireScopes(scheme, scopes...)`: a `geta.Ordered(geta.OrderAuthorize, fn)` middleware answering 403 `insufficient_scope`, declaring its 403s with `Answers`, its challenge with `Header`, and its scopes with `Middleware.Scopes` (Scopes in the document), in each write's `Doc.Scope`.
- OpenID keys via `github.com/MicahParks/keyfunc/v3` (`auth.Discover`): the discovery document read by the application, its `issuer` required to equal the configured one exactly, keyfunc fetching and refreshing the JWKS, against an in-process ES256 issuer.

