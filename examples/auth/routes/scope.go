// Package routes is the auth example's route tree.
package routes

import (
	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/auth/app"
	"github.com/koji-1009/geta/examples/auth/auth"
)

// Env is the one name the table refers to.
type Env = *app.Env

// Scope wraps the whole dispatch: one gate for every route, 404s included.
// The login's rate limit is /login's own (its Doc.BeforeGate), which geta
// runs here, ahead of the gate, for a login alone.
func Scope(env Env) geta.Scope {
	return geta.Scope{
		geta.Recover(env.Log),
		geta.Secure(auth.Policy(env.Sessions)),
	}
}
