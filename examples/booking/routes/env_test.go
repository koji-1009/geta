package routes

import (
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/koji-1009/geta/examples/booking/app"
	"github.com/koji-1009/geta/examples/booking/jwtauth"
)

// testEnv is the Env zz_routes_test.go assembles the table with: no key is
// held, which assembly never asks for.
func testEnv(testing.TB) Env {
	return app.OpenQuiet("https://issuer.example", func(*jwt.Token) (any, error) { return nil, jwtauth.ErrNoKeys })
}
