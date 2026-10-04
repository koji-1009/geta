// Package routes is the register example's route tree. Each directory below
// is one URL; zz_routes.go is the table geta sync writes from the tree.
package routes

import (
	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/auth"
)

// Env is the one name the table refers to.
type Env = *app.Env

// Scope wraps the whole dispatch, 404 and 405 included. The order is a
// value: geta.New rejects this list if two entries are out of order. CORS
// needs no header list: a page on another origin may send the headers each
// operation declares (Authorization, If-Match, Content-Type) and read those
// it answers with (ETag).
func Scope(env Env) geta.Scope {
	return geta.Scope{
		geta.AccessLog(env.Log),
		geta.CORS(geta.AllowOrigins("*")),
		geta.Recover(env.Log),
		geta.Timeout(env.RequestTimeout),
		geta.Gzip(),
		geta.Secure(auth.Policy()),
	}
}
