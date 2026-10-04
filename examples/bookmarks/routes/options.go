package routes

import "github.com/koji-1009/geta"

// Options are what main, the tests and zz_routes_test.go assemble the app
// with. The document is OpenAPI 3.2: QUERY /bookmarks is the path's query
// operation, which 3.1 has no place for, so geta.New refuses the table
// without it.
func Options(env Env) []geta.Option {
	return []geta.Option{
		geta.WithLogger(env.Log),
		geta.WithInfo(geta.Info{Title: "geta bookmarks example", Version: "0.1.0",
			Description: "Bookmarks for a single-page app on another origin, behind a session cookie."}),
		geta.WithOpenAPI(geta.OpenAPI32),
	}
}
