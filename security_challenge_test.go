package geta_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/koji-1009/geta"
)

// refuse is a verifier that admits nothing.
func refuse(*http.Request) (context.Context, error) { return nil, geta.ErrUnauthenticated }

// oauth is an oauth2 scheme named x with flows.
func oauth(flows geta.OAuthFlows) geta.Scheme {
	return geta.Scheme{Name: "x", Type: "oauth2", Flows: &flows}
}

// cc is an oauth2 scheme named x whose one flow is client credentials from
// token.
func cc(token string) geta.Scheme {
	return oauth(geta.OAuthFlows{ClientCredentials: &geta.OAuthFlow{TokenURL: token}})
}

// challenged is the 401 of a gate whose default is schemes, none admitting:
// its WWW-Authenticate values.
func challenged(t *testing.T, schemes ...geta.Scheme) []string {
	t.Helper()
	v := map[string]geta.Verifier{}
	for _, s := range schemes {
		v[s.Name] = refuse
	}
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.Secure(geta.Policy{Default: schemes, Verifiers: v})))
	r := do(t, a, "GET", "/x")
	if r.Code != 401 {
		t.Fatalf("%d", r.Code)
	}
	return r.Header().Values("WWW-Authenticate")
}

// Every 401 the gate answers carries a challenge for each refusing scheme
// (RFC 9110 section 15.5.2): an http scheme's name, Bearer for oauth2 and
// openIdConnect, and the type itself for apiKey (naming where the key goes)
// and mutualTLS.
func TestEveryRefusingSchemeChallenges(t *testing.T) {
	for _, c := range []struct {
		scheme geta.Scheme
		want   string
	}{
		{geta.Bearer, "Bearer"},
		{geta.Scheme{Name: "basic", Type: "http", Scheme: "basic"}, "Basic"},
		{geta.Scheme{Name: "Basic2", Type: "http", Scheme: "Basic"}, "Basic"},
		{geta.Scheme{Name: "digest", Type: "http", Scheme: "digest"}, "digest"},
		{geta.Scheme{Name: "oauth", Type: "oauth2", Flows: &geta.OAuthFlows{ClientCredentials: &geta.OAuthFlow{TokenURL: "https://id.example/token"}}}, "Bearer"},
		{geta.Scheme{Name: "oidc", Type: "openIdConnect", OpenIDConnectURL: "https://id.example/.well-known/openid-configuration"}, "Bearer"},
		{geta.APIKeyHeader("key", "X-API-Key"), `apiKey in="header", name="X-API-Key"`},
		{geta.Scheme{Name: "session", Type: "apiKey", In: "cookie", Param: "sid"}, `apiKey in="cookie", name="sid"`},
		{geta.Scheme{Name: "q", Type: "apiKey", In: "query", Param: `a"b\c`}, `apiKey in="query", name="a\"b\\c"`},
		{geta.Scheme{Name: "mtls", Type: "mutualTLS"}, "mutualTLS"},
	} {
		if got := challenged(t, c.scheme); !slices.Equal(got, []string{c.want}) {
			t.Errorf("%s: %q; want %q", c.scheme.Name, got, c.want)
		}
	}
	// Alternatives: one challenge each, in the order declared.
	if got := challenged(t, geta.APIKeyHeader("key", "X-API-Key"), geta.Bearer); !slices.Equal(got, []string{`apiKey in="header", name="X-API-Key"`, "Bearer"}) {
		t.Errorf("alternatives: %q", got)
	}
}

// emptyChallenge refuses with a Challenger that names no challenge.
type emptyChallenge struct{}

func (emptyChallenge) Error() string     { return "refused" }
func (emptyChallenge) Unwrap() error     { return geta.ErrUnauthenticated }
func (emptyChallenge) Challenge() string { return "" }

// A Challenger's own challenge replaces the scheme's; one that names none
// leaves the scheme's, so the 401 still carries one.
func TestAChallengerWithNoChallengeKeepsTheSchemes(t *testing.T) {
	a := accepts(t, withRoot(one("/x", get(okHandler)), geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer},
		Verifiers: map[string]geta.Verifier{"bearer": func(*http.Request) (context.Context, error) { return nil, emptyChallenge{} }}})))
	if got := do(t, a, "GET", "/x").Header().Values("WWW-Authenticate"); !slices.Equal(got, []string{"Bearer"}) {
		t.Fatal(got)
	}
}

// Across alternatives the first scheme that admits lets the request through;
// with none admitting, a verifier's defect answers 500, else an unavailable
// source 503 with no challenge, else 401.
func TestAlternativesDecideInOrderOfGravity(t *testing.T) {
	a := secured(nil, route("/x", []geta.Scheme{geta.Bearer, apiKey}))
	for _, c := range []struct {
		bearer, key string
		want        int
		challenge   bool
	}{
		{"Bearer down", "", 503, false},
		{"Bearer down", "k", 200, false},
		{"Bearer bug", "", 500, false},
		{"Bearer bug", "k", 200, false},
		{"Bearer nope", "", 401, true},
		{"Bearer ok", "", 200, false},
	} {
		r := do(t, a, "GET", "/x", "Authorization", c.bearer, "X-API-Key", c.key)
		if r.Code != c.want || (r.Header().Get("WWW-Authenticate") != "") != c.challenge {
			t.Errorf("%q %q: %d %q", c.bearer, c.key, r.Code, r.Header().Values("WWW-Authenticate"))
		}
	}
	// A defect wins over an unavailable source.
	both := geta.Scheme{Name: "other", Type: "http", Scheme: "other"}
	a = accepts(t, withRoot(one("/x", get(okHandler)), geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer, both},
		Verifiers: map[string]geta.Verifier{
			"bearer": func(*http.Request) (context.Context, error) { return nil, geta.ErrUnavailable },
			"other":  func(*http.Request) (context.Context, error) { return nil, errors.New("bug") },
		}})))
	if c := do(t, a, "GET", "/x").Code; c != 500 {
		t.Fatal(c)
	}
}

// geta.New refuses a scheme it could neither document nor challenge for: no
// Name or Type, a type OpenAPI does not define, an http scheme naming no
// authentication scheme, an apiKey with no place or name.
func TestRejectsIncompleteSchemes(t *testing.T) {
	for _, c := range []struct {
		scheme geta.Scheme
		want   string
	}{
		{geta.Scheme{Type: "http", Scheme: "bearer"}, "has no Name or Type"},
		{geta.Scheme{Name: "x"}, "has no Name or Type"},
		{geta.Scheme{Name: "x", Type: "custom"}, `security scheme "x": unknown Type "custom"`},
		{geta.Scheme{Name: "x", Type: "http"}, `http scheme: Scheme "" is not a token`},
		{geta.Scheme{Name: "x", Type: "http", Scheme: "a b"}, `http scheme: Scheme "a b" is not a token`},
		{geta.Scheme{Name: "x", Type: "apiKey", In: "body", Param: "k"}, `apiKey scheme: In "body" is not "header", "query", or "cookie"`},
		{geta.Scheme{Name: "x", Type: "apiKey", In: "header"}, `apiKey scheme: Param "" is not a valid header name`},
		{geta.Scheme{Name: "x", Type: "apiKey", In: "header", Param: "a b"}, `apiKey scheme: Param "a b" is not a valid header name`},
		{geta.Scheme{Name: "x", Type: "apiKey", In: "query", Param: "a\x01"}, `apiKey scheme: Param "a\x01" is not a valid query name`},
		// A cookie's name is a token, as an input cookie's is; a query's name
		// is one a client sends as the document states it.
		{geta.Scheme{Name: "x", Type: "apiKey", In: "cookie", Param: "a b"}, `apiKey scheme: Param "a b" is not a valid cookie name`},
		{geta.Scheme{Name: "x", Type: "apiKey", In: "cookie", Param: "a;b"}, `apiKey scheme: Param "a;b" is not a valid cookie name`},
		{geta.Scheme{Name: "x", Type: "apiKey", In: "query", Param: "a&b=c"}, `apiKey scheme: Param "a&b=c" is not a valid query name`},
		{geta.Scheme{Name: "x", Type: "apiKey", In: "query", Param: "a b"}, `apiKey scheme: Param "a b" is not a valid query name`},
		{geta.Scheme{Name: "x", Type: "apiKey", In: "query", Param: "a#b"}, `apiKey scheme: Param "a#b" is not a valid query name`},
		{geta.Scheme{Name: "x", Type: "apiKey", In: "query", Param: "a+b"}, `apiKey scheme: Param "a+b" is not a valid query name`},
		{geta.Scheme{Name: "x", Type: "apiKey", In: "query", Param: "a%20b"}, `apiKey scheme: Param "a%20b" is not a valid query name`},
		// OpenAPI requires an oauth2's flows and an openIdConnect's URL.
		{geta.Scheme{Name: "x", Type: "oauth2"}, "oauth2 scheme: missing Flows"},
		{geta.Scheme{Name: "x", Type: "oauth2", Flows: &geta.OAuthFlows{}}, "oauth2 scheme: Flows has no flow"},
		{geta.Scheme{Name: "x", Type: "openIdConnect"}, "openIdConnect scheme: missing OpenIDConnectURL"},
		// Each flow takes the URLs its grant has, and no other.
		{oauth(geta.OAuthFlows{Implicit: &geta.OAuthFlow{}}), "implicit flow: missing AuthorizationURL"},
		{oauth(geta.OAuthFlows{Implicit: &geta.OAuthFlow{AuthorizationURL: "https://a.example/auth", TokenURL: "https://a.example/token"}}),
			"implicit flow: TokenURL does not apply"},
		{oauth(geta.OAuthFlows{Password: &geta.OAuthFlow{}}), "password flow: missing TokenURL"},
		{oauth(geta.OAuthFlows{Password: &geta.OAuthFlow{TokenURL: "https://a.example/token", AuthorizationURL: "https://a.example/auth"}}),
			"password flow: AuthorizationURL does not apply"},
		{oauth(geta.OAuthFlows{ClientCredentials: &geta.OAuthFlow{}}), "clientCredentials flow: missing TokenURL"},
		{oauth(geta.OAuthFlows{AuthorizationCode: &geta.OAuthFlow{TokenURL: "https://a.example/token"}}), "authorizationCode flow: missing AuthorizationURL"},
		{oauth(geta.OAuthFlows{AuthorizationCode: &geta.OAuthFlow{AuthorizationURL: "https://a.example/auth"}}), "authorizationCode flow: missing TokenURL"},
		{oauth(geta.OAuthFlows{AuthorizationCode: &geta.OAuthFlow{AuthorizationURL: "https://a.example/auth", TokenURL: "https://a.example/token",
			DeviceAuthorizationURL: "https://a.example/device"}}), "authorizationCode flow: DeviceAuthorizationURL does not apply"},
		{oauth(geta.OAuthFlows{DeviceAuthorization: &geta.OAuthFlow{TokenURL: "https://a.example/token"}}), "deviceAuthorization flow: missing DeviceAuthorizationURL"},
		// A URL is absolute, https (http on a loopback host), with no fragment.
		{cc("/token"), `TokenURL "/token" is not an absolute URL`},
		{cc("token.example/t"), `TokenURL "token.example/t" is not an absolute URL`},
		{cc("http://a.example/token"), `TokenURL "http://a.example/token" is not https`},
		{cc("ftp://a.example/token"), `TokenURL "ftp://a.example/token" is not https`},
		{cc("https://a.example/token#x"), "has a fragment"},
		{cc("https://a.example/to ken"), "contains whitespace"},
		{oauth(geta.OAuthFlows{ClientCredentials: &geta.OAuthFlow{TokenURL: "https://a.example/token", RefreshURL: "refresh"}}),
			`clientCredentials flow's RefreshURL "refresh" is not an absolute URL`},
		{geta.Scheme{Name: "x", Type: "openIdConnect", OpenIDConnectURL: "/.well-known/openid-configuration"}, `OpenIDConnectURL "/.well-known/openid-configuration" is not an absolute URL`},
		{geta.Scheme{Name: "x", Type: "oauth2", Flows: cc("https://a.example/t").Flows, OAuth2MetadataURL: "http://a.example/m"}, `OAuth2MetadataURL "http://a.example/m" is not https`},
		// A scope is a scope-token.
		{oauth(geta.OAuthFlows{ClientCredentials: &geta.OAuthFlow{TokenURL: "https://a.example/token", Scopes: map[string]string{"a b": "x"}}}),
			`clientCredentials flow: scope "a b" is not a scope-token`},
		// A member of another type than the scheme's.
		{geta.Scheme{Name: "x", Type: "apiKey", In: "header", Param: "K", Scheme: "bearer"}, "apiKey scheme: Scheme does not apply"},
		{geta.Scheme{Name: "x", Type: "apiKey", In: "header", Param: "K", BearerFormat: "JWT"}, "apiKey scheme: BearerFormat does not apply"},
		{geta.Scheme{Name: "x", Type: "http", Scheme: "basic", BearerFormat: "JWT"}, `http scheme: BearerFormat "JWT" with Scheme "basic", not bearer`},
		{geta.Scheme{Name: "x", Type: "http", Scheme: "bearer", In: "header"}, "http scheme: In does not apply"},
		{geta.Scheme{Name: "x", Type: "http", Scheme: "bearer", Param: "K"}, "http scheme: Param does not apply"},
		{geta.Scheme{Name: "x", Type: "http", Scheme: "bearer", Flows: cc("https://a.example/t").Flows}, "http scheme: Flows does not apply"},
		{geta.Scheme{Name: "x", Type: "http", Scheme: "bearer", OpenIDConnectURL: "https://a.example/c"}, "http scheme: OpenIDConnectURL does not apply"},
		{geta.Scheme{Name: "x", Type: "oauth2", Flows: cc("https://a.example/t").Flows, OpenIDConnectURL: "https://a.example/c"}, "oauth2 scheme: OpenIDConnectURL does not apply"},
		{geta.Scheme{Name: "x", Type: "openIdConnect", OpenIDConnectURL: "https://a.example/c", Flows: cc("https://a.example/t").Flows}, "openIdConnect scheme: Flows does not apply"},
		{geta.Scheme{Name: "x", Type: "openIdConnect", OpenIDConnectURL: "https://a.example/c", OAuth2MetadataURL: "https://a.example/m"}, "openIdConnect scheme: OAuth2MetadataURL does not apply"},
		{geta.Scheme{Name: "x", Type: "mutualTLS", Scheme: "bearer"}, "mutualTLS scheme: Scheme does not apply"},
		{geta.Scheme{Name: "x", Type: "mutualTLS", In: "header", Param: "K"}, "mutualTLS scheme: In does not apply"},
	} {
		t.Run(c.want, func(t *testing.T) {
			gate := geta.Secure(geta.Policy{Verifiers: map[string]geta.Verifier{c.scheme.Name: refuse}})
			rejects(t, withRoot(one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{c.scheme}})}), gate), c.want)
			// A gate's default is held to the same.
			rejects(t, withRoot(one("/x", get(okHandler)), geta.Secure(geta.Policy{Default: []geta.Scheme{c.scheme},
				Verifiers: map[string]geta.Verifier{c.scheme.Name: refuse}})), c.want)
		})
	}
}

// A root gate's default naming a scheme with no verifier is refused even
// when the table has no routes, where every request would reach it.
func TestRejectsRootDefaultWithoutVerifierWithNoRoutes(t *testing.T) {
	rejects(t, geta.Table{Root: geta.Scope{geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer}})}},
		`root scope: requires scheme "bearer", but the gate's policy has no verifier for it`)
	// With a verifier, the route-less table assembles and holds unknown URLs to the default.
	a := accepts(t, geta.Table{Root: geta.Scope{geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer},
		Verifiers: map[string]geta.Verifier{"bearer": bearerVerifier}})}})
	if c := do(t, a, "GET", "/x").Code; c != 401 {
		t.Fatal(c)
	}
	if c := do(t, a, "GET", "/x", "Authorization", "Bearer ok").Code; c != 404 {
		t.Fatal(c)
	}
}
