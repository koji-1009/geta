package jwtauth_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/jwtauth"
)

// jwtauth never fetches keys; the application passes a jwt.Keyfunc. For an
// OpenID issuer, build one with github.com/MicahParks/keyfunc at startup and
// report jwtauth.ErrNoKeys while its key set is still empty, so the gate
// answers 503 rather than 401:
//
//	// Read jwks_uri from https://issuer.example/.well-known/openid-configuration
//	// yourself, checking that its "issuer" equals your issuer exactly.
//	kf, err := keyfunc.NewDefaultCtx(ctx, []string{jwksURI})
//	if err != nil {
//		return err
//	}
//	keys := func(t *jwt.Token) (any, error) {
//		if held, err := kf.Storage().KeyReadAll(ctx); err != nil || len(held) == 0 {
//			return nil, jwtauth.ErrNoKeys
//		}
//		return kf.Keyfunc(t)
//	}
//	v := &jwtauth.Validator{Issuer: issuer, Audience: "api", Keys: keys}
//
// With a fixed key, the Keyfunc just returns it, as below.
func ExampleVerifier() {
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	v := &jwtauth.Validator{
		Issuer:   "https://issuer.example",
		Audience: "api",
		Keys: func(t *jwt.Token) (any, error) {
			if t.Header["kid"] != "k1" {
				return nil, errors.New("unknown kid")
			}
			return &priv.PublicKey, nil
		},
	}
	verify, err := jwtauth.Verifier(v) // a Validator missing Issuer, Audience, or Keys is refused here
	if err != nil {
		panic(err)
	}
	policy := geta.Policy{
		Default:   []geta.Scheme{geta.Bearer},
		Verifiers: map[string]geta.Verifier{"bearer": verify},
	}
	_ = geta.Scope{geta.Secure(policy)}

	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": "https://issuer.example", "aud": "api", "sub": "ada", "exp": 4102444800,
	})
	tok.Header["kid"] = "k1"
	signed, _ := tok.SignedString(priv)
	c, err := v.Validate(signed)
	fmt.Println(c.Subject, err)
	// Output: ada <nil>
}
