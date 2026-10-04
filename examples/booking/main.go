// Command booking serves the booking example: meeting rooms and their
// bookings behind an OpenID issuer's ES256 tokens, traced with
// OpenTelemetry, coded in zstd or gzip, and, with -tls, served over HTTP/2
// and HTTP/3.
//
// With no -issuer, it starts a demo issuer in the same process and prints
// how to get a token from it:
//
//	go run . -addr 127.0.0.1:8080 -issuer-addr 127.0.0.1:8081 -debug-addr 127.0.0.1:8082
//	curl -s -d grant_type=client_credentials -d client_id=frontdesk -d client_secret=frontdesk-secret http://127.0.0.1:8081/token
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/app"
	"github.com/koji-1009/geta/examples/booking/auth"
	"github.com/koji-1009/geta/examples/booking/issuer"
	"github.com/koji-1009/geta/examples/booking/routes"
	"github.com/koji-1009/geta/examples/booking/telemetry"
	"github.com/koji-1009/geta/examples/booking/transport"
)

func main() {
	addr := flag.String("addr", ":"+cmp.Or(os.Getenv("PORT"), "8080"), "listen address")
	issuerURL := flag.String("issuer", "", "OpenID issuer to trust; empty starts a demo issuer in this process")
	issuerAddr := flag.String("issuer-addr", "127.0.0.1:0", "listen address of the demo issuer")
	debugAddr := flag.String("debug-addr", "", "listen address of /debug/spans and /debug/routes; empty serves none")
	useTLS := flag.Bool("tls", false, "serve TLS (HTTP/2) on TCP and HTTP/3 on UDP at -addr, with a self-signed certificate")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *issuerURL == "" {
		u, err := demoIssuer(*issuerAddr)
		if err != nil {
			log.Fatal(err)
		}
		*issuerURL = u
	}
	keys, err := auth.Discover(ctx, &http.Client{Timeout: 10 * time.Second}, *issuerURL)
	if err != nil {
		log.Fatal(err)
	}

	env := app.Open(*issuerURL, keys)
	tel := telemetry.New()
	defer tel.Shutdown(context.Background())
	env.Observe = geta.Scope{tel.Middleware()}

	a, err := geta.New(routes.Table(env), routes.Options(env)...)
	if err != nil {
		log.Fatal(err)
	}
	if *debugAddr != "" {
		go func() { log.Print(http.ListenAndServe(*debugAddr, tel.Handler())) }()
	}

	if !*useTLS {
		err = geta.Run(ctx, &http.Server{Addr: *addr, Handler: a})
	} else {
		err = serveTLS(ctx, a, *addr)
	}
	if err != nil {
		log.Fatal(err)
	}
}

// serveTLS serves a over TLS on TCP and HTTP/3 on UDP, on one port.
func serveTLS(ctx context.Context, a http.Handler, addr string) error {
	conf, _, err := transport.SelfSigned()
	if err != nil {
		return err
	}
	tcp, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	udp, err := net.ListenPacket("udp", tcp.Addr().String())
	if err != nil {
		tcp.Close()
		return err
	}
	log.Printf("serving https://%s over HTTP/2 and HTTP/3", tcp.Addr())
	return transport.Serve(ctx, a, tcp, udp, conf)
}

// demoIssuer starts the demo issuer with three clients: frontdesk may
// create rooms, book them, and change any booking; member may book and
// change its own bookings; viewer may only read.
func demoIssuer(addr string) (string, error) {
	iss, err := issuer.New(auth.Audience, map[string]issuer.Client{
		"frontdesk": {Secret: "frontdesk-secret", Scopes: []string{auth.ScopeWriteRooms, auth.ScopeWriteBookings, auth.ScopeManageBookings}},
		"member":    {Secret: "member-secret", Scopes: []string{auth.ScopeWriteBookings}},
		"viewer":    {Secret: "viewer-secret"},
	})
	if err != nil {
		return "", err
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	iss.URL = "http://" + l.Addr().String()
	go func() {
		err := http.Serve(l, iss.Handler())
		if !errors.Is(err, net.ErrClosed) {
			log.Print(err)
		}
	}()
	log.Printf("demo issuer at %s (clients frontdesk, member, viewer; secret <client>-secret)", iss.URL)
	return iss.URL, nil
}
