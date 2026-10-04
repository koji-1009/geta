// Command register serves the register example.
package main

import (
	"cmp"
	"context"
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/routes"
)

func main() {
	addr := flag.String("addr", ":"+cmp.Or(os.Getenv("PORT"), "8080"), "listen address")
	flag.Parse()
	env := app.Open()

	a, err := geta.New(routes.Table(env), routes.Options(env)...)
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: *addr, Handler: a}
	if err := geta.Run(context.Background(), srv); err != nil {
		log.Fatal(err)
	}
}
