package admin

import (
	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/auth"
)

// Scope guards everything under /admin: the gate answers "who are you",
// this answers "may you".
func Scope(env *app.Env) geta.Scope {
	return geta.Scope{auth.RequireAdmin()}
}
