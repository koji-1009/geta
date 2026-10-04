// Command bookmarks serves the bookmarks example: an API a single-page app on
// another origin calls with a session cookie, behind a reverse proxy.
//
//	go run ./examples/bookmarks -addr 127.0.0.1:8080 -origin http://localhost:5173
//	curl -si -c jar -H 'Content-Type: application/json' -d '{"user":"ada","password":"ada-pass"}' http://127.0.0.1:8080/login
//	curl -s -b jar http://127.0.0.1:8080/me
package main

import (
	"cmp"
	"context"
	"flag"
	"log"
	"net/http"
	"net/netip"
	"os"
	"strings"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/bookmarks/app"
	"github.com/koji-1009/geta/examples/bookmarks/routes"
)

func main() {
	addr := flag.String("addr", ":"+cmp.Or(os.Getenv("PORT"), "8080"), "listen address")
	origin := flag.String("origin", "http://localhost:5173", "origin of the page that calls the API with its cookie")
	proxies := flag.String("trusted-proxies", "", "comma-separated prefixes of the reverse proxies in front of the server, such as 10.0.0.0/8; empty trusts none")
	insecure := flag.Bool("insecure-cookies", false, "leave Secure off the session cookie, for plain HTTP away from localhost")
	flag.Parse()

	env := app.Open(*origin)
	env.SecureCookies = !*insecure
	for p := range strings.SplitSeq(*proxies, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(p)
		if err != nil {
			log.Fatalf("-trusted-proxies: %v", err)
		}
		env.TrustedProxies = append(env.TrustedProxies, prefix)
	}
	a, err := geta.New(routes.Table(env), routes.Options(env)...)
	if err != nil {
		log.Fatal(err)
	}
	if err := geta.Run(context.Background(), &http.Server{Addr: *addr, Handler: a}); err != nil {
		log.Fatal(err)
	}
}
