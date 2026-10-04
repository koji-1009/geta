package geta_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/koji-1009/geta"
)

// Principal is who the gate admitted; Caller carries it to the handlers and
// to the middleware after the gate.
type Principal struct {
	Name  string
	Admin bool
}

var Caller = geta.NewKey[Principal]("caller")

// A stand-in token table: a real application verifies a JWT here (examples/booking/jwtauth).
var bearerTokens = map[string]Principal{"t-admin": {Name: "ada", Admin: true}, "t-user": {Name: "bo"}}

type WhoAmI struct {
	Name string `json:"name"`
}

func whoIsCalling(ctx context.Context, _ *struct{}) (*WhoAmI, error) {
	return &WhoAmI{Name: Caller.Must(ctx).Name}, nil
}

// requireAdmin is authorization: ordinary middleware after the gate,
// answering 403, declared so the document lists it.
func requireAdmin() geta.Middleware {
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

// The gate is secure by default: an operation that declares nothing needs the
// policy's default scheme, an empty Security makes one public, and a URL that
// matches nothing is a 401 to a stranger, not a 404 that would map the API.
// The gate answers "who are you" (401); authorization, "may you", is
// middleware after it (403), here in one operation's own Doc.Scope.
func ExampleSecure() {
	policy := geta.Policy{
		Default: []geta.Scheme{geta.Bearer},
		Verifiers: map[string]geta.Verifier{
			geta.Bearer.Name: func(r *http.Request) (context.Context, error) {
				token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
				p, known := bearerTokens[token]
				if !ok || !known {
					return nil, geta.ErrUnauthenticated // 401; geta.ErrUnavailable is a 503
				}
				return Caller.With(r.Context(), p), nil
			},
		},
	}
	ok := func(ctx context.Context, _ *struct{}) error { return nil }
	app, err := geta.New(geta.Table{
		Root: geta.Scope{geta.Secure(policy)},
		Routes: []geta.Entry{
			{Path: "/health", Route: geta.Route{Get: geta.OpNoBody(http.StatusNoContent, ok, geta.Doc{Security: []geta.Scheme{}})}},
			{Path: "/whoami", Route: geta.Route{Get: geta.Op(http.StatusOK, whoIsCalling, geta.Doc{})}},
			{Path: "/admin/ping", Route: geta.Route{Post: geta.OpNoBody(http.StatusNoContent, ok, geta.Doc{
				Scope: geta.Scope{requireAdmin()},
			})}},
		},
	})
	if err != nil {
		panic(err)
	}
	for _, c := range []struct{ method, path, token string }{
		{"GET", "/health", ""},
		{"GET", "/whoami", ""},
		{"GET", "/whoami", "t-user"},
		{"POST", "/admin/ping", "t-user"},
		{"POST", "/admin/ping", "t-admin"},
		{"GET", "/nowhere", ""},
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		fmt.Printf("%s %s as %q: %d %q\n", c.method, c.path, c.token, rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
	// Output:
	// GET /health as "": 204 ""
	// GET /whoami as "": 401 "Bearer"
	// GET /whoami as "t-user": 200 ""
	// POST /admin/ping as "t-user": 403 ""
	// POST /admin/ping as "t-admin": 204 ""
	// GET /nowhere as "": 401 "Bearer"
}
