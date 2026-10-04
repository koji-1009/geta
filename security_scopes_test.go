package geta_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// Schemes of every type OpenAPI defines.
var (
	jwtScheme   = geta.Scheme{Name: "jwt", Type: "http", Scheme: "bearer", BearerFormat: "JWT", Description: "A signed token"}
	basicScheme = geta.Scheme{Name: "basic", Type: "http", Scheme: "basic"}
	keyScheme   = geta.APIKeyHeader("key", "X-API-Key")
	oidcScheme  = geta.Scheme{Name: "oidc", Type: "openIdConnect", OpenIDConnectURL: "https://id.example/.well-known/openid-configuration"}
	mtlsScheme  = geta.Scheme{Name: "mtls", Type: "mutualTLS", Description: "A client certificate"}
	petScopes   = map[string]string{"read:pets": "Read your pets", "write:pets": "Change your pets"}
	oauthScheme = geta.Scheme{Name: "oauth", Type: "oauth2", Flows: &geta.OAuthFlows{
		Implicit:          &geta.OAuthFlow{AuthorizationURL: "https://id.example/authorize", Scopes: petScopes},
		Password:          &geta.OAuthFlow{TokenURL: "https://id.example/token", RefreshURL: "https://id.example/refresh"},
		ClientCredentials: &geta.OAuthFlow{TokenURL: "http://127.0.0.1:8081/token", Scopes: map[string]string{"admin": "Everything"}},
		AuthorizationCode: &geta.OAuthFlow{AuthorizationURL: "https://id.example/authorize", TokenURL: "https://id.example/token", Scopes: petScopes},
	}}
	// What only a 3.2 document has a place for.
	deviceScheme = geta.Scheme{Name: "device", Type: "oauth2", Deprecated: true,
		OAuth2MetadataURL: "https://id.example/.well-known/oauth-authorization-server",
		Flows: &geta.OAuthFlows{DeviceAuthorization: &geta.OAuthFlow{DeviceAuthorizationURL: "https://id.example/device",
			TokenURL: "https://localhost/token", Scopes: map[string]string{"tv": "Watch"}}}}
)

// needs is an authorization middleware declaring the scopes it requires of
// scheme.
func needs(scheme geta.Scheme, scopes ...string) geta.Middleware {
	return geta.Ordered(geta.OrderAuthorize, noop).Answers(http.StatusForbidden, "A required scope is missing").Scopes(scheme, scopes...)
}

// schemesTable requires each type of scheme somewhere, with scopes stated
// where a middleware requires them.
func schemesTable(extra ...geta.Scheme) geta.Table {
	verifiers := map[string]geta.Verifier{}
	for _, s := range append([]geta.Scheme{jwtScheme, basicScheme, keyScheme, oidcScheme, mtlsScheme, oauthScheme}, extra...) {
		verifiers[s.Name] = admit
	}
	op := func(security []geta.Scheme, scope ...geta.Middleware) geta.Route {
		return geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: security, Scope: scope})}
	}
	tbl := geta.Table{
		Root: geta.Scope{geta.Secure(geta.Policy{Default: []geta.Scheme{jwtScheme, keyScheme}, Verifiers: verifiers})},
		Routes: []geta.Entry{
			// Two scopes of an oauth2 scheme, declared by two middleware.
			{Path: "/oauth", Route: op([]geta.Scheme{oauthScheme}, needs(oauthScheme, "read:pets"), needs(oauthScheme, "write:pets", "read:pets"))},
			// An openIdConnect scheme's scopes come from its discovery document.
			{Path: "/oidc", Route: op([]geta.Scheme{oidcScheme}, needs(oidcScheme, "openid", "profile"))},
			// Roles of an http scheme; the API key alternative, which the
			// middleware refuses, is left out.
			{Path: "/roles", Route: op([]geta.Scheme{basicScheme, keyScheme}, needs(basicScheme, "admin"))},
			// Two gates: the root's default and the directory's; only the
			// combinations holding jwt are stated.
			{Path: "/mixed", Route: op(nil, needs(jwtScheme, "write")),
				Scopes: []geta.Scope{{geta.Secure(geta.Policy{Default: []geta.Scheme{mtlsScheme}, Verifiers: verifiers})}}},
			// No scopes: every alternative of the root default, empty lists.
			{Path: "/plain", Route: op(nil)},
			{Path: "/public", Route: op([]geta.Scheme{})},
		},
	}
	if len(extra) > 0 {
		tbl.Routes = append(tbl.Routes, geta.Entry{Path: "/device", Route: op(extra, needs(extra[0], "tv"))})
	}
	return tbl
}

// A document holding every type of security scheme, oauth2 flows and their
// scopes, and requirements naming the scopes their middleware require, which
// openapi-spec-validator checks against the OpenAPI 3.1 meta-schema.
func TestSecuritySchemesGolden(t *testing.T) {
	getatest.Golden(t, accepts(t, schemesTable()), "testdata/security.openapi.json")
}

// The same in 3.2, with what only 3.2 has: a device authorization flow, an
// oauth2 scheme's metadata URL, and a deprecated scheme.
func TestSecuritySchemesGolden32(t *testing.T) {
	a, err := geta.New(schemesTable(deviceScheme), geta.WithOpenAPI(geta.OpenAPI32))
	if err != nil {
		t.Fatal(err)
	}
	getatest.Golden(t, a, "testdata/security.openapi.3.2.json")
}

// Each requirement states the scopes the operation's middleware require of
// its scheme; an alternative without that scheme is left out; one with no
// middleware asking scopes keeps empty lists.
func TestScopesAreStatedInTheRequirements(t *testing.T) {
	m := doc(t, accepts(t, schemesTable()))
	for path, want := range map[string]string{
		"/oauth":  `[{"oauth":["read:pets","write:pets"]}]`,
		"/oidc":   `[{"oidc":["openid","profile"]}]`,
		"/roles":  `[{"basic":["admin"]}]`,
		"/mixed":  `[{"jwt":["write"],"mtls":[]}]`,
		"/plain":  `[{"jwt":[]},{"key":[]}]`,
		"/public": `[]`,
	} {
		if got := compact(t, at(t, m, "paths", path, "get", "security")); got != want {
			t.Errorf("%s: %s; want %s", path, got, want)
		}
	}
	// The OPTIONS operation runs the root scope alone: no scopes.
	if got := compact(t, at(t, m, "paths", "/oauth", "options", "security")); got != `[{"jwt":[]},{"key":[]}]` {
		t.Error(got)
	}
	// A scope middleware in the root scope states its scopes on every
	// operation behind it, the OPTIONS operations among them.
	verifiers := map[string]geta.Verifier{"jwt": admit}
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.Secure(geta.Policy{Default: []geta.Scheme{jwtScheme}, Verifiers: verifiers}), needs(jwtScheme, "read")))
	m = doc(t, a)
	for _, method := range []string{"get", "options"} {
		if got := compact(t, at(t, m, "paths", "/x", method, "security")); got != `[{"jwt":["read"]}]` {
			t.Errorf("%s: %s", method, got)
		}
	}
}

// Leaving an alternative out of the document changes nothing the gate does:
// it still admits by the API key, and a preflight still allows its header.
func TestScopesChangeNoRuntimeBehaviour(t *testing.T) {
	tbl := schemesTable()
	tbl.Root = append(geta.Scope{geta.CORS(geta.AllowOrigins("https://app.example"))}, tbl.Root...)
	a := accepts(t, tbl)
	if c := do(t, a, "GET", "/roles", "X-API-Key", "k").Code; c != 200 {
		t.Fatal(c)
	}
	res := do(t, a, "OPTIONS", "/roles", "Origin", "https://app.example", "Access-Control-Request-Method", "GET")
	if h := res.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(h, "X-API-Key") || !strings.Contains(h, "Authorization") {
		t.Fatal(res.Code, h)
	}
}

// Scopes nothing could meet are refused: none, a scope that is no
// scope-token, an oauth2 scope no flow defines, scopes on an operation that
// requires nothing or on a scheme no gate before the middleware requires,
// another definition of the scheme, and scopes of two schemes no requirement
// holds together.
func TestScopeMistakesAreRefused(t *testing.T) {
	verifiers := map[string]geta.Verifier{"jwt": admit, "key": admit, "oauth": admit}
	gate := geta.Secure(geta.Policy{Default: []geta.Scheme{jwtScheme, keyScheme}, Verifiers: verifiers})
	route := func(security []geta.Scheme, scope ...geta.Middleware) geta.Table {
		return one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: security, Scope: scope})})
	}
	for want, tbl := range map[string]geta.Table{
		`Scopes of scheme "jwt" names no scope`:               withRoot(route(nil, needs(jwtScheme)), gate),
		`Scopes of scheme "jwt": "a b" is not a scope-token`:  withRoot(route(nil, needs(jwtScheme, "a b")), gate),
		`Scopes of scheme "jwt": "a\"b" is not a scope-token`: withRoot(route(nil, needs(jwtScheme, `a"b`)), gate),
		"Scopes names a scheme with no Name":                  withRoot(route(nil, needs(geta.Scheme{}, "a")), gate),
		`requires scope "delete:pets", which no flow of oauth2 scheme "oauth" defines`: withRoot(
			route([]geta.Scheme{oauthScheme}, needs(oauthScheme, "read:pets", "delete:pets")), gate),
		`requires scopes of scheme "jwt", which no earlier geta.Secure gate requires`:   withRoot(route([]geta.Scheme{}, needs(jwtScheme, "a")), gate),
		`requires scopes of scheme "oauth", which no earlier geta.Secure gate requires`: withRoot(route(nil, needs(oauthScheme, "read:pets")), gate),
		// A middleware with no rank may sit before the gate: it runs before
		// anything admitted the request.
		`middleware 0 (middleware) requires scopes of scheme "jwt", which no earlier geta.Secure gate requires`: withRoot(route(nil),
			geta.Use(noop).Scopes(jwtScheme, "a"), gate),
		`defines scheme "jwt" as`: withRoot(route(nil, needs(geta.Scheme{Name: "jwt", Type: "http", Scheme: "bearer"}, "a")), gate),
		"no security requirement holds schemes jwt and key, whose scopes its middleware require": withRoot(
			route(nil, needs(jwtScheme, "a"), needs(keyScheme, "b")), gate),
	} {
		t.Run(want, func(t *testing.T) { rejects(t, tbl, want) })
	}
	// In a Doc.BeforeGate, which runs before the gate.
	rejects(t, withRoot(one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{
		BeforeGate: geta.Scope{geta.Use(noop).Scopes(jwtScheme, "a")}})}), gate), `requires scopes of scheme "jwt", which no earlier geta.Secure gate requires`)
	// Two gates whose defaults hold jwt and key: the combination holds both.
	keyGate := geta.Secure(geta.Policy{Default: []geta.Scheme{keyScheme}, Verifiers: verifiers})
	jwtGate := geta.Secure(geta.Policy{Default: []geta.Scheme{jwtScheme}, Verifiers: verifiers})
	m := doc(t, accepts(t, withRoot(route(nil, needs(jwtScheme, "a"), needs(keyScheme, "b")), jwtGate, keyGate)))
	if got := compact(t, at(t, m, "paths", "/x", "get", "security")); got != `[{"jwt":["a"],"key":["b"]}]` {
		t.Error(got)
	}
}

// A scheme defined twice alike, flows built apart, is one definition; flows
// that differ are two.
func TestSchemesWithEqualFlowsAreOneDefinition(t *testing.T) {
	flows := func(scope string) *geta.OAuthFlows {
		return &geta.OAuthFlows{ClientCredentials: &geta.OAuthFlow{TokenURL: "https://id.example/token", Scopes: map[string]string{scope: ""}}}
	}
	a := geta.Scheme{Name: "o", Type: "oauth2", Flows: flows("a")}
	b := geta.Scheme{Name: "o", Type: "oauth2", Flows: flows("a")}
	v := map[string]geta.Verifier{"o": admit}
	two := func(x, y geta.Scheme) geta.Table {
		return geta.Table{Root: geta.Scope{geta.Secure(geta.Policy{Default: []geta.Scheme{x}, Verifiers: v})},
			Routes: []geta.Entry{{Path: "/y", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{y}})}}}}
	}
	accepts(t, two(a, b))
	rejects(t, two(a, geta.Scheme{Name: "o", Type: "oauth2", Flows: flows("b")}), `security scheme "o" has two definitions`)
	// The scopes a middleware requires are checked against the gate's
	// definition, equal though built apart.
	tbl := two(a, b)
	tbl.Root = append(tbl.Root, needs(b, "a"))
	got := compact(t, at(t, doc(t, accepts(t, tbl)), "paths", "/y", "get", "security"))
	if got != `[{"o":["a"]}]` {
		t.Error(got)
	}
}

// What only OpenAPI 3.2 has a place for is refused in a 3.1 document.
func TestSchemeMembersOf32AreRefusedIn31(t *testing.T) {
	flows := deviceScheme.Flows
	for want, s := range map[string]geta.Scheme{
		"OpenAPI 3.1 has no deviceAuthorization flow; use geta.OpenAPI32": {
			Name: "device", Type: "oauth2", Flows: flows},
		"OpenAPI 3.1 has no oauth2MetadataUrl; use geta.OpenAPI32": {Name: "device", Type: "oauth2", Flows: oauthScheme.Flows,
			OAuth2MetadataURL: "https://id.example/m"},
		"OpenAPI 3.1 has no deprecated security scheme; use geta.OpenAPI32": {Name: "device", Type: "mutualTLS", Deprecated: true},
	} {
		tbl := withRoot(one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{s}})}),
			geta.Secure(geta.Policy{Verifiers: map[string]geta.Verifier{"device": admit}}))
		rejects(t, tbl, want)
		if _, err := geta.New(tbl, geta.WithOpenAPI(geta.OpenAPI32)); err != nil {
			t.Errorf("3.2: %v", err)
		}
	}
	// 3.2 writes them.
	a, err := geta.New(schemesTable(deviceScheme), geta.WithOpenAPI(geta.OpenAPI32))
	if err != nil {
		t.Fatal(err)
	}
	got := compact(t, at(t, doc(t, a), "components", "securitySchemes", "device"))
	for _, part := range []string{`"deprecated":true`, `"oauth2MetadataUrl":"https://id.example/.well-known/oauth-authorization-server"`,
		`"deviceAuthorization":{"deviceAuthorizationUrl":"https://id.example/device","scopes":{"tv":"Watch"},"tokenUrl":"https://localhost/token"}`} {
		if !strings.Contains(got, part) {
			t.Errorf("lacks %s: %s", part, got)
		}
	}
	if s := compact(t, at(t, doc(t, a), "paths", "/device", "get", "security")); !slices.Contains([]string{`[{"device":["tv"]}]`}, s) {
		t.Error(s)
	}
}
