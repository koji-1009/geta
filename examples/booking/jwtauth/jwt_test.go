package jwtauth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/koji-1009/geta"
)

func validate(t *testing.T, tok string) (*Claims, error) {
	t.Helper()
	return validator(t, static(t)).Validate(tok)
}

func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}

func TestEveryAlgorithmVerifies(t *testing.T) {
	for alg, kid := range map[string]string{"RS256": "rs", "RS512": "rs", "ES256": "es", "ES384": "es384", "EdDSA": "ed"} {
		v := validator(t, static(t))
		c, err := v.Validate(token(t, alg, kid, nil))
		if err != nil || c.Subject != "ada" {
			t.Errorf("%s: %v", alg, err)
		}
	}
}

func TestFraming(t *testing.T) {
	good := token(t, "RS256", "rs", nil)
	parts := strings.Split(good, ".")
	for name, tok := range map[string]string{
		"two segments":  parts[0] + "." + parts[1],
		"five segments": good + ".a.b",
		"empty":         "",
		"padded":        parts[0] + "=." + parts[1] + "." + parts[2],
		"plus":          parts[0] + "." + parts[1] + "+." + parts[2],
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validate(t, tok)
			wantErr(t, err, jwt.ErrTokenMalformed)
		})
	}
}

// Only the allowlist's asymmetric algorithms verify; "none", HMAC, and
// critical extensions are refused before any key is used.
func TestHeaderPolicy(t *testing.T) {
	payload := jsonOf(claims(nil))
	for name, header := range map[string]string{
		"none":    `{"alg":"none","kid":"rs"}`,
		"HMAC":    `{"alg":"HS256","kid":"rs"}`,
		"unknown": `{"alg":"XX999","kid":"rs"}`,
		"PS256":   `{"alg":"PS256","kid":"rs"}`,
		"crit":    `{"alg":"RS256","kid":"rs","crit":["exp"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validate(t, sign(t, "RS256", []byte(header), payload)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	v := validator(t, static(t))
	v.Algorithms = []string{"RS256"}
	if _, err := v.Validate(token(t, "ES256", "es", nil)); err == nil {
		t.Fatal("an algorithm outside the allowlist verified")
	}
	// A key on the wrong curve for the algorithm.
	if _, err := validate(t, sign(t, "ES256", jsonOf(map[string]any{"alg": "ES256", "kid": "es384"}), payload)); err == nil {
		t.Fatal("an ES384 key verified ES256")
	}
	if (&Validator{Issuer: "i", Audience: "a", Keys: static(t), Algorithms: []string{"HS256"}}).Check() == nil {
		t.Fatal("HS256 allowed into the allowlist")
	}
}

func TestEmbeddedKeyHeadersAreIgnored(t *testing.T) {
	h := jsonOf(map[string]any{"alg": "RS256", "kid": "attacker", "jwk": map[string]any{"kty": "RSA"}, "jku": "https://evil.example"})
	_, err := validate(t, sign(t, "RS256", h, jsonOf(claims(nil))))
	wantErr(t, err, jwt.ErrTokenUnverifiable)
}

func TestTemporalClaims(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	v := validator(t, static(t))
	v.Now = func() time.Time { return now }
	v.Leeway = 30 * time.Second
	check := func(over map[string]any) error {
		_, err := v.Validate(token(t, "RS256", "rs", over))
		return err
	}
	wantErr(t, check(map[string]any{"exp": nil}), jwt.ErrTokenRequiredClaimMissing)
	wantErr(t, check(map[string]any{"exp": now.Unix() - 31}), jwt.ErrTokenExpired)
	if err := check(map[string]any{"exp": now.Unix() - 29}); err != nil {
		t.Fatal("within leeway:", err)
	}
	wantErr(t, check(map[string]any{"nbf": now.Unix() + 31}), jwt.ErrTokenNotValidYet)
	if err := check(map[string]any{"nbf": now.Unix() + 29}); err != nil {
		t.Fatal("nbf within leeway:", err)
	}
	wantErr(t, check(map[string]any{"exp": "tomorrow"}), jwt.ErrInvalidType)
}

func TestIssuerAndAudience(t *testing.T) {
	check := func(over map[string]any) error {
		_, err := validate(t, token(t, "RS256", "rs", over))
		return err
	}
	wantErr(t, check(map[string]any{"iss": "https://other.example"}), jwt.ErrTokenInvalidIssuer)
	wantErr(t, check(map[string]any{"iss": nil}), jwt.ErrTokenRequiredClaimMissing)
	wantErr(t, check(map[string]any{"aud": "other"}), jwt.ErrTokenInvalidAudience)
	wantErr(t, check(map[string]any{"aud": []any{"x", "y"}}), jwt.ErrTokenInvalidAudience)
	if err := check(map[string]any{"aud": []any{"x", "api"}}); err != nil {
		t.Fatal(err)
	}
}

func TestClaimsReachTheHandlerShape(t *testing.T) {
	c, err := validate(t, token(t, "RS256", "rs", map[string]any{"scope": "read write", "scp": []any{"write", "admin"}, "nbf": 1000}))
	if err != nil || strings.Join(c.Scopes, " ") != "read write admin" {
		t.Fatal(c, err)
	}
	if c.Issuer != "https://issuer.example" || strings.Join(c.Audience, ",") != "api" || c.Expiry.Unix() != 4102444800 || c.NotBefore.Unix() != 1000 {
		t.Fatalf("%+v", c)
	}
}

// When the Keys function holds no key at all, a token cannot be judged:
// that is the key source's outage, not the caller's fault.
func TestNoKeysHeldIsUnavailable(t *testing.T) {
	_, err := validator(t, empty()).Validate(token(t, "RS256", "rs", nil))
	if !errors.Is(err, geta.ErrUnavailable) || !errors.Is(err, ErrNoKeys) || errors.Is(err, jwt.ErrTokenUnverifiable) {
		t.Fatal(err)
	}
	if !strings.Contains(err.Error(), "the JWKS fetch failed") {
		t.Fatal("the Keyfunc's reason was lost:", err)
	}
	// The header policy still comes first: a refused alg or crit is the
	// caller's fault even while no key is held.
	for _, header := range []string{`{"alg":"HS256","kid":"rs"}`, `{"alg":"RS256","kid":"rs","crit":["exp"]}`} {
		_, err := validator(t, empty()).Validate(sign(t, "RS256", []byte(header), jsonOf(claims(nil))))
		if err == nil || errors.Is(err, geta.ErrUnavailable) {
			t.Fatalf("%s: %v", header, err)
		}
	}
	// With keys held, an unknown kid is the caller's fault.
	_, err = validate(t, token(t, "RS256", "nope", nil))
	if errors.Is(err, geta.ErrUnavailable) || !errors.Is(err, jwt.ErrTokenUnverifiable) {
		t.Fatal(err)
	}
}

func TestKeysIsRequired(t *testing.T) {
	_, err := (&Validator{Issuer: "i", Audience: "a"}).Validate(token(t, "RS256", "rs", nil))
	if err == nil || !strings.Contains(err.Error(), "Validator.Keys is required") {
		t.Fatal(err)
	}
}

// A key of the wrong type for the token's alg never verifies.
func TestKeyTypeMustMatchAlg(t *testing.T) {
	_, err := validate(t, sign(t, "RS256", jsonOf(map[string]any{"alg": "RS256", "kid": "es"}), jsonOf(claims(nil))))
	wantErr(t, err, jwt.ErrTokenSignatureInvalid)
	_, err = validate(t, sign(t, "EdDSA", jsonOf(map[string]any{"alg": "EdDSA", "kid": "rs"}), jsonOf(claims(nil))))
	wantErr(t, err, jwt.ErrTokenSignatureInvalid)
}
