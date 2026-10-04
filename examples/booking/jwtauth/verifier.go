package jwtauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/koji-1009/geta"
)

// Principal carries the validated claims from the gate to handlers.
var Principal = geta.NewKey[*Claims]("jwtauth.claims")

// Verifier returns a geta.Verifier for the bearer scheme that validates the
// token with v and puts the claims under [Principal]. It returns the error
// of [Validator.Check] for a misconfigured v.
//
// A request without bearer credentials gets a 401 with a bare challenge; a
// bad token gets a 401 with error="invalid_token" and golang-jwt's reason.
// [ErrNoKeys] from Keys is a 503. A Validator misconfigured after this call
// is a 500.
func Verifier(v *Validator) (geta.Verifier, error) {
	if err := v.Check(); err != nil {
		return nil, err
	}
	return func(r *http.Request) (context.Context, error) {
		h := r.Header.Get("Authorization")
		scheme, token, _ := strings.Cut(h, " ")
		if h == "" || !strings.EqualFold(scheme, "bearer") {
			return nil, geta.ErrUnauthenticated
		}
		token = strings.TrimSpace(token)
		if token == "" || strings.ContainsAny(token, " \t") {
			return nil, invalid(errors.New("the Authorization header carries no single bearer token"))
		}
		c, err := v.Validate(token)
		switch {
		case err == nil:
			return Principal.With(r.Context(), c), nil
		case errors.Is(err, geta.ErrUnavailable):
			return nil, err
		}
		// The most specific reason first: a claim failure also wraps
		// ErrTokenInvalidClaims.
		for _, reason := range []error{jwt.ErrTokenMalformed, jwt.ErrTokenSignatureInvalid, jwt.ErrTokenUnverifiable,
			jwt.ErrTokenRequiredClaimMissing, jwt.ErrTokenExpired, jwt.ErrTokenNotValidYet, jwt.ErrTokenUsedBeforeIssued,
			jwt.ErrTokenInvalidIssuer, jwt.ErrTokenInvalidAudience, jwt.ErrTokenInvalidClaims} {
			if errors.Is(err, reason) {
				return nil, invalid(reason)
			}
		}
		return nil, err
	}, nil
}

// rejection is a 401 with an RFC 6750 challenge.
type rejection struct {
	reason error
}

func invalid(reason error) error { return &rejection{reason} }

func (r *rejection) Error() string { return "invalid_token: " + r.reason.Error() }

func (r *rejection) Unwrap() []error { return []error{geta.ErrUnauthenticated, r.reason} }

// Challenge is the WWW-Authenticate value the gate sends.
func (r *rejection) Challenge() string {
	return fmt.Sprintf(`Bearer error="invalid_token", error_description=%q`, r.reason.Error())
}

// RequireScopes answers 403 with error="insufficient_scope" unless the
// caller's token holds every scope. It reads [Principal] and is ordered at
// geta.OrderAuthorize, inside the gate. scheme is the scheme whose verifier
// is [Verifier]; the document lists the scopes under that scheme's
// requirement on every operation behind the middleware
// ([geta.Middleware.Scopes]).
//
// A caller admitted by another scheme (an API key, a session cookie) has no
// scopes and gets a 403 without a bearer challenge.
//
// geta.New reports these as construction errors: a scheme that is not http
// bearer, oauth2, or openIdConnect; no scopes; a scope that is not an RFC
// 6750 scope-token; and scopes geta.New refuses for the document.
func RequireScopes(scheme geta.Scheme, scopes ...string) geta.Middleware {
	switch {
	case scheme.Type == "http" && strings.EqualFold(scheme.Scheme, "bearer"), scheme.Type == "oauth2", scheme.Type == "openIdConnect":
	default:
		return geta.Invalid("require-scopes", fmt.Errorf("jwtauth.RequireScopes:scheme %q (type %s) carries no bearer token", scheme.Name, scheme.Type))
	}
	if len(scopes) == 0 {
		return geta.Invalid("require-scopes", errors.New("jwtauth.RequireScopes:no scopes"))
	}
	for _, s := range scopes {
		if !scopeToken(s) {
			return geta.Invalid("require-scopes", fmt.Errorf("jwtauth.RequireScopes:scope %q is not a scope token", s))
		}
	}
	challenge := fmt.Sprintf(`Bearer error="insufficient_scope", scope="%s"`, strings.Join(scopes, " "))
	return geta.Ordered(geta.OrderAuthorize, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, ok := Principal.Value(r.Context())
			switch {
			case !ok:
				geta.WriteProblem(w, http.StatusForbidden, "the caller was not authenticated with a bearer token")
				return
			case !hasAll(c.Scopes, scopes):
				w.Header().Set("WWW-Authenticate", challenge)
				geta.WriteProblem(w, http.StatusForbidden, "the token lacks a required scope")
				return
			}
			next.ServeHTTP(w, r)
		})
	}).Answers(http.StatusForbidden, "The token lacks a required scope").
		Answers(http.StatusForbidden, "The caller was not authenticated with a bearer token").
		Header(http.StatusForbidden, "WWW-Authenticate", `The scopes the token lacks, as an RFC 6750 insufficient_scope challenge`).
		Scopes(scheme, scopes...)
}

// scopeToken reports whether s is an RFC 6750 scope-token:
// 1*( %x21 / %x23-5B / %x5D-7E ).
func scopeToken(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if c := s[i]; c < 0x21 || c > 0x7e || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}

func hasAll(have, want []string) bool {
	for _, s := range want {
		if !slices.Contains(have, s) {
			return false
		}
	}
	return true
}
