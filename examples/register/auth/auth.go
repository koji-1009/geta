// Package auth is the register example's demo authentication: two bearer
// tokens. A real app verifies a JWT here (examples/booking does it with its
// jwtauth package and an issuer's ES256 key set) without touching anything
// else.
package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/koji-1009/geta"
)

// Principal is the authenticated caller.
type Principal struct {
	ID    string
	Admin bool
}

// Caller carries the principal from the gate to handlers and middleware.
var Caller = geta.NewKey[Principal]("principal")

var tokens = map[string]Principal{
	"t-admin": {ID: "ada", Admin: true},
	"t-user":  {ID: "bo"},
}

// Policy requires a bearer token wherever a route declares nothing.
func Policy() geta.Policy {
	return geta.Policy{
		Default: []geta.Scheme{geta.Bearer},
		Verifiers: map[string]geta.Verifier{
			geta.Bearer.Name: func(r *http.Request) (context.Context, error) {
				token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
				p, known := tokens[token]
				if !ok || !known {
					return nil, geta.ErrUnauthenticated
				}
				return Caller.With(r.Context(), p), nil
			},
		},
	}
}

// RequireAdmin answers 403 to a caller who is not an administrator. A
// directory's scope applies it to every method beneath it, as /admin does; an
// operation's own scope (geta.Doc.Scope) applies it to one method of a URL
// whose other methods any caller may use, as the writes on /users do. Either
// way it runs before the input is bound, so a caller who may not write is
// refused before the request's contract is checked.
func RequireAdmin() geta.Middleware {
	return geta.Ordered(geta.OrderAuthorize, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if p, ok := Caller.Value(r.Context()); !ok || !p.Admin {
				geta.WriteProblem(w, http.StatusForbidden, "admin only")
				return
			}
			next.ServeHTTP(w, r)
		})
	}).Answers(http.StatusForbidden, "Not an administrator")
}
