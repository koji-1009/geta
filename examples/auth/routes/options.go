package routes

import "github.com/koji-1009/geta"

// Options are what main, the tests and zz_routes_test.go assemble the app
// with.
func Options(env Env) []geta.Option {
	return []geta.Option{
		geta.WithLogger(env.Log),
		geta.WithInfo(geta.Info{Title: "geta auth example", Version: "0.1.0"}),
	}
}
