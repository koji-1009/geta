// A table, as geta sync writes it, pointing at a directory with no Route.
package main

import (
	"github.com/koji-1009/geta"

	r_health "github.com/koji-1009/geta/testdata/compilefail/missingroute/health"
)

type Env = *int

func Table(env Env) geta.Table {
	return geta.Table{
		Routes: []geta.Entry{
			{Path: "/health", Route: r_health.Route(env)},
		},
	}
}

func main() {}
