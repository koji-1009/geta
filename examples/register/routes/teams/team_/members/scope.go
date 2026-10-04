package members

import (
	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/auth"
)

// Scope guards everything under /teams/{team}/members: every operation
// there changes a team's membership, which only an administrator may do.
// The rule depends on the caller alone, so it is a gate, not a handler's
// check.
func Scope(env *app.Env) geta.Scope {
	return geta.Scope{auth.RequireAdmin()}
}
