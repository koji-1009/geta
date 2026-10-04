package routes

import (
	"testing"

	"github.com/koji-1009/geta/examples/auth/app"
)

// testEnv is the Env zz_routes_test.go assembles the table with.
func testEnv(testing.TB) Env { return app.Open() }
