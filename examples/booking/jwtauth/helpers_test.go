package jwtauth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

var (
	keysOnce sync.Once
	rsaKey   *rsa.PrivateKey
	ecKey    *ecdsa.PrivateKey
	ec384    *ecdsa.PrivateKey
	edKey    ed25519.PrivateKey
)

func keys(t testing.TB) {
	keysOnce.Do(func() {
		var err error
		if rsaKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
		if ecKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
			panic(err)
		}
		if ec384, err = ecdsa.GenerateKey(elliptic.P384(), rand.Reader); err != nil {
			panic(err)
		}
		if _, edKey, err = ed25519.GenerateKey(rand.Reader); err != nil {
			panic(err)
		}
	})
}

func enc(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func jsonOf(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// sign builds a token from a raw header and payload.
func sign(t testing.TB, alg string, header, payload []byte) string {
	t.Helper()
	keys(t)
	input := enc(header) + "." + enc(payload)
	var sig []byte
	var err error
	switch alg {
	case "RS256":
		d := sha256.Sum256([]byte(input))
		sig, err = rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, d[:])
	case "RS512":
		d := sha512.Sum512([]byte(input))
		sig, err = rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA512, d[:])
	case "ES256":
		d := sha256.Sum256([]byte(input))
		sig = ecSig(ecKey, d[:], 32)
	case "ES384":
		d := sha512.Sum384([]byte(input))
		sig = ecSig(ec384, d[:], 48)
	case "EdDSA":
		sig = ed25519.Sign(edKey, []byte(input))
	default:
		sig = []byte("not-a-signature")
	}
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + enc(sig)
}

func ecSig(k *ecdsa.PrivateKey, digest []byte, size int) []byte {
	r, s, err := ecdsa.Sign(rand.Reader, k, digest)
	if err != nil {
		panic(err)
	}
	return append(r.FillBytes(make([]byte, size)), s.FillBytes(make([]byte, size))...)
}

// claims are valid ones, with overrides.
func claims(over map[string]any) map[string]any {
	c := map[string]any{"iss": "https://issuer.example", "aud": "api", "sub": "ada", "exp": 4102444800, "scope": "read write"}
	for k, v := range over {
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
	}
	return c
}

func token(t testing.TB, alg, kid string, over map[string]any) string {
	h := map[string]any{"alg": alg, "typ": "JWT"}
	if kid != "" {
		h["kid"] = kid
	}
	return sign(t, alg, jsonOf(h), jsonOf(claims(over)))
}

var errUnknownKid = errors.New("no key has this kid")

// static is a plain jwt.Keyfunc over the test public keys, keyed by kid,
// as an application with fixed keys would write it.
func static(t testing.TB) jwt.Keyfunc {
	keys(t)
	set := map[string]any{
		"rs":    &rsaKey.PublicKey,
		"es":    &ecKey.PublicKey,
		"es384": &ec384.PublicKey,
		"ed":    edKey.Public(),
	}
	return func(tok *jwt.Token) (any, error) {
		kid, _ := tok.Header["kid"].(string)
		if k, ok := set[kid]; ok {
			return k, nil
		}
		return nil, errUnknownKid
	}
}

// empty is a Keyfunc that holds no key yet, as one whose fetch failed.
func empty() jwt.Keyfunc {
	return func(*jwt.Token) (any, error) {
		return nil, fmt.Errorf("%w: the JWKS fetch failed", ErrNoKeys)
	}
}

func validator(t testing.TB, keys jwt.Keyfunc) *Validator {
	return &Validator{Issuer: "https://issuer.example", Audience: "api", Keys: keys,
		Algorithms: []string{"RS256", "RS512", "ES256", "ES384", "EdDSA"}}
}
