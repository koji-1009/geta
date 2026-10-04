// Package routes is the booking service's route tree. Each directory below
// is one URL; zz_routes.go is the table geta sync writes from the tree.
package routes

import (
	"slices"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/app"
	"github.com/koji-1009/geta/examples/booking/auth"
	"github.com/koji-1009/geta/examples/booking/coding"
	"github.com/koji-1009/geta/examples/booking/ratelimit"
)

// Env is the one name the table refers to.
type Env = *app.Env

// Scope wraps the whole dispatch, 404 and 405 included, in the order
// geta.New checks: observe (tracing, then the access log), recover, shed
// (a rate per client address, the service's own limiter declaring its 429
// and Retry-After, then a cap on requests in flight), the
// deadline, negotiate (zstd or gzip, whichever the client prefers), the
// entity tag of every GET, and the gate, whose default is a bearer token
// from the issuer.
func Scope(env Env) geta.Scope {
	return slices.Concat(env.Observe, geta.Scope{
		geta.AccessLog(env.Log),
		geta.Recover(env.Log),
		ratelimit.PerAddr(env.RatePerMinute, time.Minute),
		geta.ConcurrencyLimit(env.MaxInFlight),
		geta.Timeout(env.RequestTimeout),
		geta.Compress(coding.Zstd, geta.GzipCoding()),
		geta.ETag(),
		auth.Gate(env.Issuer, env.Keys),
	})
}
