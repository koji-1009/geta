package jwtauth

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// RequireScopes runs at geta.OrderAuthorize, so it sits inside the gate.
func TestRequireScopesAuthorizes(t *testing.T) {
	if o, ok := RequireScopes(geta.Bearer, "admin").Order(); !ok || o != geta.OrderAuthorize {
		t.Fatal(o, ok)
	}
	verify, err := Verifier(validator(t, static(t)))
	if err != nil {
		t.Fatal(err)
	}
	gate := geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer}, Verifiers: map[string]geta.Verifier{"bearer": verify}})
	tbl := geta.Table{Root: geta.Scope{RequireScopes(geta.Bearer, "admin"), gate},
		Routes: []geta.Entry{{Path: "/x", Route: geta.Route{Get: geta.Op(http.StatusOK, meHandler, geta.Doc{})}}}}
	if _, err := geta.New(tbl); err == nil || !strings.Contains(err.Error(), "authenticate(8000) runs after authorize(9000)") {
		t.Fatal(err)
	}
}

// A Validator that cannot judge a token is refused before anything serves:
// Verifier returns Check's error, and an application turns it into a
// middleware geta.New refuses.
func TestAMisconfiguredValidatorIsRefused(t *testing.T) {
	keys := static(t)
	for want, v := range map[string]*Validator{
		"nil Validator":                  nil,
		"Validator.Issuer is required":   {Audience: "api", Keys: keys},
		"Validator.Audience is required": {Issuer: "https://issuer.example", Keys: keys},
		"Validator.Keys is required":     {Issuer: "https://issuer.example", Audience: "api"},
		`unsupported algorithm "HS256"`: {Issuer: "https://issuer.example", Audience: "api", Keys: keys,
			Algorithms: []string{"HS256"}},
	} {
		verify, err := Verifier(v)
		if verify != nil || err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
		if err := v.Check(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Check, %s: %v", want, err)
		}
		if _, err := v.Validate(token(t, "RS256", "rs", nil)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate, %s: %v", want, err)
		}
		tbl := geta.Table{Root: geta.Scope{geta.Invalid("secure", err)},
			Routes: []geta.Entry{{Path: "/x", Route: geta.Route{Get: geta.Op(http.StatusOK, meHandler, geta.Doc{Security: []geta.Scheme{}})}}}}
		if _, err := geta.New(tbl); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("geta.New, %s: %v", want, err)
		}
	}
	// A Validator changed into a misconfigured one after Verifier accepted it
	// is a defect the gate answers 500, to a caller with a token.
	v := validator(t, keys)
	verify, err := Verifier(v)
	if err != nil {
		t.Fatal(err)
	}
	v.Issuer = ""
	c := getatest.New(t, geta.Table{
		Root:   geta.Scope{geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer}, Verifiers: map[string]geta.Verifier{"bearer": verify}})},
		Routes: []geta.Entry{{Path: "/me", Route: geta.Route{Get: geta.Op(http.StatusOK, meHandler, geta.Doc{})}}},
	}, geta.WithLogger(slog.New(slog.DiscardHandler)))
	if res := c.Bearer(token(t, "RS256", "rs", nil)).Get("/me"); res.Status != 500 {
		t.Fatal(res.Status)
	}
}

// With no Algorithms, RS256 and ES256 alone verify.
func TestDefaultAlgorithms(t *testing.T) {
	v := &Validator{Issuer: "https://issuer.example", Audience: "api", Keys: static(t)}
	for alg, kid := range map[string]string{"RS256": "rs", "ES256": "es"} {
		if _, err := v.Validate(token(t, alg, kid, nil)); err != nil {
			t.Errorf("%s: %v", alg, err)
		}
	}
	for alg, kid := range map[string]string{"RS512": "rs", "ES384": "es384", "EdDSA": "ed"} {
		if _, err := v.Validate(token(t, alg, kid, nil)); err == nil {
			t.Errorf("%s verified", alg)
		}
	}
}

// A token issued in the future, past Leeway, is refused as used before it
// was issued; within Leeway, and in the past, it is taken, and its iat
// reaches the claims.
func TestIssuedAtIsChecked(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	v := validator(t, static(t))
	v.Now = func() time.Time { return now }
	v.Leeway = 30 * time.Second
	check := func(iat any) (*Claims, error) {
		return v.Validate(token(t, "RS256", "rs", map[string]any{"iat": iat}))
	}
	wantErr(t, func() error { _, err := check(now.Unix() + 31); return err }(), jwt.ErrTokenUsedBeforeIssued)
	if c, err := check(now.Unix() + 29); err != nil || c.IssuedAt.Unix() != now.Unix()+29 {
		t.Fatal(c, err)
	}
	if _, err := check(now.Unix() - 3600); err != nil {
		t.Fatal(err)
	}
	// Through the gate: 401 invalid_token with the reason.
	c := app(t, static(t))
	res := c.Bearer(token(t, "RS256", "rs", map[string]any{"iat": time.Now().Add(time.Hour).Unix()})).Get("/me")
	if wa := res.Header.Get("WWW-Authenticate"); res.Status != 401 || !strings.Contains(wa, "token used before issued") {
		t.Fatal(res.Status, wa)
	}
}

// keyVerifier admits a request carrying X-API-Key: k.
func keyVerifier(r *http.Request) (context.Context, error) {
	if r.Header.Get("X-API-Key") == "k" {
		return nil, nil
	}
	return nil, geta.ErrUnauthenticated
}

// both is an app whose operations take a bearer token or an API key.
func both(t *testing.T) *getatest.Client {
	verify, err := Verifier(validator(t, static(t)))
	if err != nil {
		t.Fatal(err)
	}
	key := geta.APIKeyHeader("key", "X-API-Key")
	sec := []geta.Scheme{geta.Bearer, key}
	return getatest.New(t, geta.Table{
		Root: geta.Scope{geta.Secure(geta.Policy{Default: sec, Verifiers: map[string]geta.Verifier{"bearer": verify, "key": keyVerifier}})},
		Routes: []geta.Entry{
			{Path: "/open", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*me, error) { return &me{}, nil }, geta.Doc{})}},
			{Path: "/admin", Route: geta.Route{Get: geta.Op(http.StatusOK, meHandler, geta.Doc{})}, Scopes: []geta.Scope{{RequireScopes(geta.Bearer, "admin")}}},
		},
	})
}

// Beside another scheme, a missing or bad bearer token is no refusal while
// the other admits; with none admitting, the 401 carries the bearer
// challenge, bare or invalid_token, beside the other's.
func TestBearerBesideAnotherScheme(t *testing.T) {
	c := both(t)
	if res := c.With("X-API-Key", "k").Get("/open"); res.Status != 200 {
		t.Fatal(res.Status)
	}
	if res := c.With("X-API-Key", "k").Bearer("garbage").Get("/open"); res.Status != 200 {
		t.Fatal(res.Status)
	}
	res := c.Bearer("garbage").Get("/open")
	if wa := res.Header.Values("WWW-Authenticate"); res.Status != 401 || len(wa) != 2 ||
		!strings.HasPrefix(wa[0], `Bearer error="invalid_token"`) || wa[1] != `apiKey in="header", name="X-API-Key"` {
		t.Fatal(res.Status, wa)
	}
	res = c.Get("/open")
	if wa := res.Header.Values("WWW-Authenticate"); res.Status != 401 || len(wa) != 2 || wa[0] != "Bearer" {
		t.Fatal(res.Status, wa)
	}
}

// RequireScopes for a caller the gate admitted by another scheme: no token,
// so no scopes, and a 403 with no bearer challenge; both causes, and the
// challenge header, are documented on the 403.
func TestRequireScopesForAnotherScheme(t *testing.T) {
	c := both(t)
	res := c.With("X-API-Key", "k").Get("/admin")
	if res.Status != 403 || res.Header.Get("WWW-Authenticate") != "" ||
		res.Problem().Detail != "the caller was not authenticated with a bearer token" {
		t.Fatal(res.Status, res.Header, res.Text())
	}
	res = c.Bearer(token(t, "RS256", "rs", nil)).Get("/admin")
	if res.Status != 403 || res.Header.Get("WWW-Authenticate") != `Bearer error="insufficient_scope", scope="admin"` {
		t.Fatal(res.Status, res.Header)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Security  []map[string][]string `json:"security"`
			Responses map[string]struct {
				Description string `json:"description"`
				Headers     map[string]struct {
					Required *bool `json:"required"`
				} `json:"headers"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(c.App().OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	r403 := doc.Paths["/admin"]["get"].Responses["403"]
	if r403.Description != "The token lacks a required scope; The caller was not authenticated with a bearer token" {
		t.Fatal(r403.Description)
	}
	if h, ok := r403.Headers["WWW-Authenticate"]; !ok || h.Required == nil || *h.Required {
		t.Fatal(r403.Headers)
	}
	// The document states the scope on the bearer requirement and leaves out
	// the API key alternative, which RequireScopes refuses; /open keeps both.
	if s := doc.Paths["/admin"]["get"].Security; len(s) != 1 || len(s[0]) != 1 || len(s[0]["bearer"]) != 1 || s[0]["bearer"][0] != "admin" {
		t.Fatal(s)
	}
	if s := doc.Paths["/open"]["get"].Security; len(s) != 2 {
		t.Fatal(s)
	}
}
