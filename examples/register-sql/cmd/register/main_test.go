package main

import (
	"testing"

	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/getatest"
)

// The SQLite command serves the register example's app: its document is
// examples/register's, info included, whatever store is behind it.
func TestSameAppAsTheRegisterExample(t *testing.T) {
	env := app.Open()
	a, err := assemble(env)
	if err != nil {
		t.Fatal(err)
	}
	getatest.Golden(t, a, "../../../../examples/register/openapi.json")
}
