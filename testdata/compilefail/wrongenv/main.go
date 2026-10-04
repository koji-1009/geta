// A table, as geta sync writes it, passing the Env to a Route that wants
// something else.
package main

import (
	"github.com/koji-1009/geta"

	r_health "github.com/koji-1009/geta/testdata/compilefail/wrongenv/health"
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
