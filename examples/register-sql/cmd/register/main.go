// Command register serves the register example over SQLite: the same
// routes as examples/register, with the store chosen in main. It lives in
// this module because main is the one place the driver is imported.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register-sql/sqlite"
	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/routes"
	"github.com/koji-1009/geta/examples/register/store"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	path := flag.String("db", "register.db", "SQLite database file")
	flag.Parse()

	db, err := sqlite.Open(*path)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	users, err := store.NewSQL(context.Background(), sqlite.Users(db))
	if err != nil {
		log.Fatal(err)
	}
	env := app.Open()
	env.Users = users

	a, err := assemble(env)
	if err != nil {
		log.Fatal(err)
	}
	if err := geta.Run(context.Background(), &http.Server{Addr: *addr, Handler: a}); err != nil {
		log.Fatal(err)
	}
}

// assemble builds the app as examples/register does: its table with its
// options, routes.Options.
func assemble(env *app.Env) (*geta.App, error) {
	return geta.New(routes.Table(env), routes.Options(env)...)
}
