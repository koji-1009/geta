package routes

import (
	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/model"
)

// Options are what main, the tests and zz_routes_test.go assemble the app
// with: the sealed type of booking changes, the schema of Money (a type that
// writes its own JSON), a smaller body limit than the default, and the
// document's info. The document is OpenAPI 3.1; append
// geta.WithOpenAPI(geta.OpenAPI32) for 3.2.
func Options(env Env) []geta.Option {
	limits := geta.DefaultLimits
	limits.MaxBodyBytes = 64 << 10 // a booking is a few hundred bytes
	return []geta.Option{
		geta.WithLogger(env.Log),
		geta.WithInfo(geta.Info{Title: "geta booking example", Version: "0.1.0",
			Description: "Meeting rooms and their bookings, behind an OpenID issuer's tokens."}),
		geta.WithUnion(model.Changes),
		geta.WithSchema[model.Money]("string", "pattern="+model.MoneyPattern),
		geta.WithLimits(limits),
	}
}
