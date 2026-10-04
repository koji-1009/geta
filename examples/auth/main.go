// Command auth serves the auth example: declaration-driven authentication
// with a bearer token and a cookie session behind one gate.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/auth/app"
	"github.com/koji-1009/geta/examples/auth/routes"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()
	env := app.Open()
	a, err := geta.New(routes.Table(env), routes.Options(env)...)
	if err != nil {
		log.Fatal(err)
	}
	if err := geta.Run(context.Background(), &http.Server{Addr: *addr, Handler: a}); err != nil {
		log.Fatal(err)
	}
}
