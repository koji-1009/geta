package admin

import (
	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/auth/app"
	"github.com/koji-1009/geta/examples/auth/auth"
)

// Scope guards everything under /admin. Nothing here names the prefix; the
// directory does.
func Scope(env *app.Env) geta.Scope {
	return geta.Scope{auth.RequireRole("admin")}
}
