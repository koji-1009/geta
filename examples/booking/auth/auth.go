// Package auth is the booking service's side of authentication: it trusts
// one OpenID issuer, verifies its ES256 access tokens with this example's
// jwtauth package, and fetches the issuer's keys with MicahParks/keyfunc.
// jwtauth fetches no keys itself; Keys is the jwt.Keyfunc this package
// builds.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/jwtauth"
)

// Audience is the aud every token for this service carries.
const Audience = "booking"

// Scopes the routes require.
const (
	ScopeWriteBookings = "bookings:write"
	ScopeWriteRooms    = "rooms:write"
	// ScopeManageBookings lets a caller change any booking, not only the
	// ones it made (the front desk). bookings:write alone changes only the
	// caller's own.
	ScopeManageBookings = "bookings:manage"
)

// Gate requires a bearer token wherever a route declares nothing; the token
// is a JWT of issuer, checked with keys. A Validator jwtauth refuses (no
// issuer, no keys) is a middleware geta.New refuses, naming the mistake.
func Gate(issuer string, keys jwt.Keyfunc) geta.Middleware {
	v := &jwtauth.Validator{Issuer: issuer, Audience: Audience, Algorithms: []string{"ES256"}, Keys: keys, Leeway: 30 * time.Second}
	verify, err := jwtauth.Verifier(v)
	if err != nil {
		return geta.Invalid("secure", err)
	}
	return geta.Secure(geta.Policy{
		Default:   []geta.Scheme{geta.Bearer},
		Verifiers: map[string]geta.Verifier{geta.Bearer.Name: verify},
	})
}

// Require answers 403 with error="insufficient_scope" unless the bearer
// token holds scope, which the document states in the operation's bearer
// requirement. It goes in an operation's Doc.Scope (a write on a URL anyone
// may read) or a directory's scope.
func Require(scope string) geta.Scope { return geta.Scope{jwtauth.RequireScopes(geta.Bearer, scope)} }

// Subject is the caller the gate admitted.
func Subject(ctx context.Context) string { return jwtauth.Principal.Must(ctx).Subject }

// Holds reports whether the caller's token holds scope. A rule that needs
// the loaded resource (who made this booking) cannot be a gate, which sees
// only the request: the handler passes what the caller holds to the store,
// and the store refuses with an error the failure table maps to 403.
func Holds(ctx context.Context, scope string) bool {
	return slices.Contains(jwtauth.Principal.Must(ctx).Scopes, scope)
}

// Discover reads issuer's discovery document, requires it to name the issuer
// exactly, and has keyfunc fetch and refresh the key set it points to. The
// key set must be served over https, or from a loopback address (the demo
// issuer). The Keyfunc it returns reports jwtauth.ErrNoKeys while no key is
// held (the issuer was unreachable), which the gate answers with 503, not 401.
func Discover(ctx context.Context, client *http.Client, issuer string) (jwt.Keyfunc, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery document for %s: %s", issuer, res.Status)
	}
	var meta struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&meta); err != nil {
		return nil, err
	}
	if meta.Issuer != issuer || !trusted(meta.JWKSURI) {
		return nil, fmt.Errorf("discovery document for %s names issuer %q and jwks_uri %q", issuer, meta.Issuer, meta.JWKSURI)
	}
	kf, err := keyfunc.NewDefaultOverrideCtx(ctx, []string{meta.JWKSURI}, keyfunc.Override{
		Client:          client,
		RefreshInterval: time.Minute,
		RefreshErrorHandlerFunc: func(string) func(context.Context, error) {
			return func(context.Context, error) {} // the 503s say it; the log need not
		},
	})
	if err != nil {
		return nil, err
	}
	return Held(ctx, kf), nil
}

// Held wraps kf so that it reports jwtauth.ErrNoKeys while kf's storage
// holds no key at all.
func Held(ctx context.Context, kf keyfunc.Keyfunc) jwt.Keyfunc {
	return func(t *jwt.Token) (any, error) {
		if held, err := kf.Storage().KeyReadAll(ctx); err != nil || len(held) == 0 {
			return nil, jwtauth.ErrNoKeys
		}
		return kf.Keyfunc(t)
	}
}

// trusted reports whether a key set URL may be fetched: https, or http on a
// loopback address.
func trusted(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	return u.Scheme == "http" && (host == "localhost" || ip != nil && ip.IsLoopback())
}
