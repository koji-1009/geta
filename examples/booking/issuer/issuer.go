// Package issuer is a stand-in for the OpenID provider the booking service
// trusts, small enough to run in the same process for a demo or a test: it
// signs ES256 access tokens for registered clients (the OAuth 2.0 client
// credentials grant, RFC 6749 section 4.4) and publishes its discovery
// document and its JSON Web Key Set. In production this is Keycloak, Auth0,
// Entra ID, or any other issuer; the booking service only ever reads the
// discovery document and the key set, as it would theirs.
//
// It is plain net/http, not a geta app: it is the other party.
package issuer

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/MicahParks/jwkset"
	"github.com/golang-jwt/jwt/v5"
)

// Client is a registered OAuth client: its secret and the scopes it may ask
// for.
type Client struct {
	Secret string
	Scopes []string
}

// Issuer signs tokens with one ES256 key.
type Issuer struct {
	// URL is the issuer identifier, the iss of every token and the base of
	// its endpoints. Set it once the listener's address is known.
	URL string
	// Audience is the aud of every token.
	Audience string
	// TTL is how long a token lives.
	TTL time.Duration

	key     *ecdsa.PrivateKey
	kid     string
	jwks    []byte
	clients map[string]Client

	mu      sync.Mutex
	offline bool
}

// New generates a signing key and registers clients.
func New(audience string, clients map[string]Client) (*Issuer, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	kid := rand.Text()[:16]
	jwk, err := jwkset.NewJWKFromKey(&key.PublicKey, jwkset.JWKOptions{Metadata: jwkset.JWKMetadataOptions{
		KID: kid, ALG: jwkset.AlgES256, USE: jwkset.UseSig,
	}})
	if err != nil {
		return nil, err
	}
	set := jwkset.NewMemoryStorage()
	if err := set.KeyWrite(context.Background(), jwk); err != nil {
		return nil, err
	}
	jwks, err := set.JSONPublic(context.Background())
	if err != nil {
		return nil, err
	}
	return &Issuer{Audience: audience, TTL: 15 * time.Minute, key: key, kid: kid, jwks: jwks, clients: clients}, nil
}

// SetOffline makes the key set endpoint answer 503, as an issuer that is
// down would.
func (i *Issuer) SetOffline(off bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.offline = off
}

func (i *Issuer) isOffline() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.offline
}

// Mint signs a token for subject with scopes.
func (i *Issuer) Mint(subject string, scopes []string) (string, error) {
	now := time.Now()
	t := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss":   i.URL,
		"aud":   i.Audience,
		"sub":   subject,
		"iat":   now.Unix(),
		"exp":   now.Add(i.TTL).Unix(),
		"scope": strings.Join(scopes, " "),
	})
	t.Header["kid"] = i.kid
	return t.SignedString(i.key)
}

// Handler serves the discovery document, the key set, and the token
// endpoint.
func (i *Issuer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                                i.URL,
			"jwks_uri":                              i.URL + "/jwks.json",
			"token_endpoint":                        i.URL + "/token",
			"grant_types_supported":                 []string{"client_credentials"},
			"id_token_signing_alg_values_supported": []string{"ES256"},
		})
	})
	mux.HandleFunc("GET /jwks.json", func(w http.ResponseWriter, r *http.Request) {
		if i.isOffline() {
			http.Error(w, "offline", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/jwk-set+json")
		w.Write(i.jwks)
	})
	mux.HandleFunc("POST /token", i.token)
	return mux
}

// token is the client credentials grant: client_id and client_secret in the
// form (client_secret_post), scope a space-separated subset of the client's.
func (i *Issuer) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if r.PostForm.Get("grant_type") != "client_credentials" {
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	id, secret := r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	c, ok := i.clients[id]
	if !ok || subtle.ConstantTimeCompare([]byte(c.Secret), []byte(secret)) != 1 {
		oauthError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	scopes := c.Scopes
	if s := r.PostForm.Get("scope"); s != "" {
		scopes = strings.Fields(s)
		for _, s := range scopes {
			if !slices.Contains(c.Scopes, s) {
				oauthError(w, http.StatusBadRequest, "invalid_scope")
				return
			}
		}
	}
	tok, err := i.Mint(id, scopes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": tok,
		"token_type":   "Bearer",
		"expires_in":   int(i.TTL / time.Second),
		"scope":        strings.Join(scopes, " "),
	})
}

func oauthError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, fmt.Sprint(err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}
