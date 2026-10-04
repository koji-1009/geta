// Package jwtauth verifies bearer JWTs for geta's security gate. It is the
// booking example's own code, not part of geta: an application copies it,
// or writes its own, as it sees fit.
//
// Parsing, signature verification, and registered-claim checks are done by
// github.com/golang-jwt/jwt/v5. jwtauth never fetches or refreshes keys:
// the application supplies a jwt.Keyfunc, for example from
// github.com/MicahParks/keyfunc for a JWKS or OpenID issuer, or one
// returning a static key. jwtauth adds the geta side: the allowed
// algorithms, the claims handlers read, and how a refusal becomes a 401,
// 503, or 500.
package jwtauth

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/koji-1009/geta"
)

// supported are the asymmetric algorithms an allowlist may name. "none" and
// HMAC are excluded to rule out key confusion.
var supported = []string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512", "EdDSA"}

// errCritical refuses any "crit" header: jwtauth understands no extensions
// (RFC 7515 §4.1.11).
var errCritical = errors.New("unsupported critical header extensions")

// ErrNoKeys is returned (or wrapped) by a [Validator.Keys] function that
// holds no signing key at all, for example before its key set is first
// fetched. Validate then reports geta.ErrUnavailable, which the gate answers
// with 503. Any other Keys error, such as an unknown kid, is a 401.
var ErrNoKeys = errors.New("jwtauth:no signing keys")

// Claims are the validated claims of a token.
type Claims struct {
	Subject   string
	Issuer    string
	Audience  []string
	Expiry    time.Time
	NotBefore time.Time
	IssuedAt  time.Time
	// Scopes is the union of the space-delimited "scope" claim and the
	// "scp" array.
	Scopes []string
	// Raw is the whole payload, numbers as json.Number.
	Raw map[string]any
}

// Validator checks tokens against one issuer and audience.
type Validator struct {
	Issuer   string
	Audience string
	// Algorithms is the allowlist; empty means RS256 and ES256.
	Algorithms []string
	// Keys resolves a token's verification key (or a
	// jwt.VerificationKeySet). It is called only after the header's alg
	// passes the allowlist and the header has no "crit". Pass, for example,
	// a MicahParks/keyfunc value's Keyfunc method, or a function returning
	// a static public key. Return (or wrap) [ErrNoKeys] when no key is held.
	Keys jwt.Keyfunc
	// Leeway tolerates clock skew on exp, nbf, and iat.
	Leeway time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Check reports a misconfigured v: nil, a missing Issuer, Audience, or
// Keys, or an algorithm outside RS256/384/512, ES256/384/512, and EdDSA.
// [Verifier] and [Validator.Validate] return the same error.
func (v *Validator) Check() error {
	switch {
	case v == nil:
		return errors.New("jwtauth:nil Validator")
	case v.Issuer == "":
		return errors.New("jwtauth:Validator.Issuer is required")
	case v.Audience == "":
		return errors.New("jwtauth:Validator.Audience is required")
	case v.Keys == nil:
		return errors.New("jwtauth:Validator.Keys is required")
	}
	for _, a := range v.Algorithms {
		if !slices.Contains(supported, a) {
			return fmt.Errorf("jwtauth:unsupported algorithm %q", a)
		}
	}
	return nil
}

func (v *Validator) allowed() []string {
	if len(v.Algorithms) == 0 {
		return []string{"RS256", "ES256"}
	}
	return v.Algorithms
}

// Validate checks token and returns its claims. A refusal wraps one of
// golang-jwt's ErrToken values. If Keys reported [ErrNoKeys], the error
// instead wraps geta.ErrUnavailable and Keys' error.
func (v *Validator) Validate(token string) (*Claims, error) {
	if err := v.Check(); err != nil {
		return nil, err
	}
	opts := []jwt.ParserOption{
		jwt.WithValidMethods(v.allowed()),
		jwt.WithIssuer(v.Issuer),
		jwt.WithAudience(v.Audience),
		jwt.WithExpirationRequired(),
		// Refuse a token issued in the future (beyond Leeway).
		jwt.WithIssuedAt(),
		jwt.WithLeeway(v.Leeway),
		jwt.WithJSONNumber(),
		jwt.WithStrictDecoding(),
	}
	if v.Now != nil {
		opts = append(opts, jwt.WithTimeFunc(v.Now))
	}
	var noKeys error
	claims := jwt.MapClaims{}
	_, err := jwt.NewParser(opts...).ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, has := t.Header["crit"]; has {
			return nil, errCritical
		}
		key, kerr := v.Keys(t)
		if errors.Is(kerr, ErrNoKeys) {
			noKeys = kerr
		}
		return key, kerr
	})
	if noKeys != nil {
		return nil, fmt.Errorf("%w: %w", geta.ErrUnavailable, noKeys)
	}
	if err != nil {
		return nil, err
	}
	return claimsOf(claims), nil
}

// claimsOf reads the claims golang-jwt has already validated.
func claimsOf(m jwt.MapClaims) *Claims {
	c := &Claims{Raw: m}
	c.Subject, _ = m.GetSubject()
	c.Issuer, _ = m.GetIssuer()
	c.Audience, _ = m.GetAudience()
	for _, d := range []struct {
		get func() (*jwt.NumericDate, error)
		to  *time.Time
	}{{m.GetExpirationTime, &c.Expiry}, {m.GetNotBefore, &c.NotBefore}, {m.GetIssuedAt, &c.IssuedAt}} {
		if t, err := d.get(); err == nil && t != nil {
			*d.to = t.Time
		}
	}
	if scope, ok := m["scope"].(string); ok {
		c.Scopes = append(c.Scopes, strings.Fields(scope)...)
	}
	if scp, ok := m["scp"].([]any); ok {
		for _, s := range scp {
			if str, ok := s.(string); ok && !slices.Contains(c.Scopes, str) {
				c.Scopes = append(c.Scopes, str)
			}
		}
	}
	return c
}
