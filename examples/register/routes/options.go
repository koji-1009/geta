package routes

import "github.com/koji-1009/geta"

// Options are what every assembly of the app passes geta.New: main, the
// tests, zz_routes_test.go, and the SQLite command in examples/register-sql.
func Options(env Env) []geta.Option {
	return []geta.Option{
		geta.WithLogger(env.Log),
		geta.WithInfo(geta.Info{Title: "geta register example", Version: "0.1.0"}),
	}
}
