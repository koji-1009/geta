package jwtauth

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

type me struct {
	Sub    string   `json:"sub"`
	Scopes []string `json:"scopes"`
}

func meHandler(ctx context.Context, _ *struct{}) (*me, error) {
	c := Principal.Must(ctx)
	return &me{Sub: c.Subject, Scopes: c.Scopes}, nil
}

func app(t *testing.T, keys jwt.Keyfunc) *getatest.Client {
	verify, err := Verifier(validator(t, keys))
	if err != nil {
		t.Fatal(err)
	}
	tbl := geta.Table{
		Root: geta.Scope{geta.Secure(geta.Policy{
			Default:   []geta.Scheme{geta.Bearer},
			Verifiers: map[string]geta.Verifier{"bearer": verify},
		})},
		Routes: []geta.Entry{
			{Path: "/me", Route: geta.Route{Get: geta.Op(http.StatusOK, meHandler, geta.Doc{})}},
			{Path: "/admin", Route: geta.Route{Get: geta.Op(http.StatusOK, meHandler, geta.Doc{})},
				Scopes: []geta.Scope{{RequireScopes(geta.Bearer, "admin")}}},
		},
	}
	return getatest.New(t, tbl)
}

func TestNoBearerCredentialsGetABareChallenge(t *testing.T) {
	c := app(t, static(t))
	for _, h := range []string{"", "Basic dXNlcjpwYXNz"} {
		res := c.With("Authorization", h).Get("/me")
		if h == "" {
			res = c.Get("/me")
		}
		if res.Status != 401 || res.Header.Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%q: %d %q", h, res.Status, res.Header.Get("WWW-Authenticate"))
		}
	}
}

func TestBadTokensGetInvalidToken(t *testing.T) {
	c := app(t, static(t))
	cases := map[string]string{
		"Bearer ":    "no single bearer token",
		"Bearer a b": "no single bearer token",
		"Bearer " + token(t, "RS256", "rs", map[string]any{"exp": 1}):                           "token is expired",
		"Bearer " + token(t, "RS256", "rs", map[string]any{"exp": nil}):                         "token is missing required claim",
		"Bearer " + token(t, "RS256", "rs", map[string]any{"nbf": 4102444700}):                  "token is not valid yet",
		"Bearer " + token(t, "RS256", "rs", map[string]any{"iss": "https://x.example"}):         "token has invalid issuer",
		"Bearer " + token(t, "RS256", "rs", map[string]any{"aud": "other"}):                     "token has invalid audience",
		"Bearer " + token(t, "RS256", "nope", nil):                                              "token is unverifiable",
		"Bearer " + token(t, "PS256", "rs", nil):                                                "token signature is invalid",
		"Bearer " + strings.Replace(token(t, "RS256", "rs", nil), ".", ".e30", 1)[:20]:          "token is malformed",
		"Bearer " + token(t, "RS256", "rs", nil)[:len(token(t, "RS256", "rs", nil))-4] + "AAAA": "token signature is invalid",
	}
	for h, want := range cases {
		res := c.With("Authorization", h).Get("/me")
		wa := res.Header.Get("WWW-Authenticate")
		if res.Status != 401 || !strings.HasPrefix(wa, `Bearer error="invalid_token"`) || !strings.Contains(wa, want) {
			t.Errorf("%.40q: %d %q", h, res.Status, wa)
		}
	}
}

func TestAValidTokenReachesTheHandler(t *testing.T) {
	c := app(t, static(t))
	res := c.With("Authorization", "bearer "+token(t, "ES256", "es", nil)).Get("/me")
	if res.Status != 200 || res.Text() != `{"sub":"ada","scopes":["read","write"]}` {
		t.Fatal(res.Status, res.Text())
	}
}

func TestNoKeysHeldIs503(t *testing.T) {
	res := app(t, empty()).Bearer(token(t, "RS256", "rs", nil)).Get("/me")
	if res.Status != 503 || res.Header.Get("WWW-Authenticate") != "" {
		t.Fatal(res.Status, res.Header)
	}
}

func TestRequireScopes(t *testing.T) {
	c := app(t, static(t))
	res := c.Bearer(token(t, "RS256", "rs", nil)).Get("/admin")
	if res.Status != 403 || res.Header.Get("WWW-Authenticate") != `Bearer error="insufficient_scope", scope="admin"` {
		t.Fatal(res.Status, res.Header)
	}
	if res := c.Bearer(token(t, "RS256", "rs", map[string]any{"scp": []any{"admin"}})).Get("/admin"); res.Status != 200 {
		t.Fatal(res.Status)
	}
	gate := geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer}, Verifiers: map[string]geta.Verifier{"bearer": keyVerifier}})
	behind := func(m geta.Middleware) geta.Table {
		return geta.Table{Root: geta.Scope{gate}, Routes: []geta.Entry{{Path: "/x", Route: geta.Route{Get: geta.Op(http.StatusOK, meHandler, geta.Doc{})},
			Scopes: []geta.Scope{{m}}}}}
	}
	// A scope is an RFC 6750 scope-token: %x21 / %x23-5B / %x5D-7E.
	for _, bad := range [][]string{nil, {`a"b`}, {"a b"}, {""}, {`a\b`}, {"a\x00b"}, {"a\x7fb"}, {"a\x1bb"}, {"café"}, {"ok", "a\x01"}} {
		if _, err := geta.New(behind(RequireScopes(geta.Bearer, bad...))); err == nil || !strings.Contains(err.Error(), "RequireScopes") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := geta.New(behind(RequireScopes(geta.Bearer, "read:users", "!#[]~", "a/b.c-d_e"))); err != nil {
		t.Error(err)
	}
	// A scheme whose credentials are no bearer token carries no scopes.
	for _, s := range []geta.Scheme{geta.APIKeyHeader("key", "X-API-Key"), {Name: "basic", Type: "http", Scheme: "basic"}, {Name: "mtls", Type: "mutualTLS"}} {
		if _, err := geta.New(behind(RequireScopes(s, "admin"))); err == nil || !strings.Contains(err.Error(), "carries no bearer token") {
			t.Errorf("%s: %v", s.Name, err)
		}
	}
	// geta.New refuses scopes on an operation whose gate does not require
	// the scheme, and an oauth2 scope its flows do not define.
	public := geta.Table{Root: geta.Scope{gate}, Routes: []geta.Entry{{Path: "/x",
		Route:  geta.Route{Get: geta.Op(http.StatusOK, meHandler, geta.Doc{Security: []geta.Scheme{}})},
		Scopes: []geta.Scope{{RequireScopes(geta.Bearer, "admin")}}}}}
	if _, err := geta.New(public); err == nil || !strings.Contains(err.Error(), `requires scopes of scheme "bearer", which no earlier geta.Secure gate requires`) {
		t.Error(err)
	}
	oauth := geta.Scheme{Name: "bearer", Type: "oauth2", Flows: &geta.OAuthFlows{ClientCredentials: &geta.OAuthFlow{
		TokenURL: "https://issuer.example/token", Scopes: map[string]string{"read": "Read"}}}}
	og := geta.Secure(geta.Policy{Default: []geta.Scheme{oauth}, Verifiers: map[string]geta.Verifier{"bearer": keyVerifier}})
	tbl := geta.Table{Root: geta.Scope{og}, Routes: []geta.Entry{{Path: "/x", Route: geta.Route{Get: geta.Op(http.StatusOK, meHandler, geta.Doc{})},
		Scopes: []geta.Scope{{RequireScopes(oauth, "admin")}}}}}
	if _, err := geta.New(tbl); err == nil || !strings.Contains(err.Error(), `requires scope "admin", which no flow of oauth2 scheme "bearer" defines`) {
		t.Error(err)
	}
}

// The scopes RequireScopes requires are stated in the requirement of its
// scheme, so a client generated from the document asks for them.
func TestRequireScopesAreDocumented(t *testing.T) {
	var doc struct {
		Paths map[string]map[string]struct {
			Security []map[string][]string `json:"security"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(app(t, static(t)).App().OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	if got := doc.Paths["/admin"]["get"].Security; len(got) != 1 || !slices.Equal(got[0]["bearer"], []string{"admin"}) {
		t.Fatal(got)
	}
	if got := doc.Paths["/me"]["get"].Security; len(got) != 1 || got[0]["bearer"] == nil || len(got[0]["bearer"]) != 0 {
		t.Fatal(got)
	}
}
