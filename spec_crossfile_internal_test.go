package geta

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"testing"
)

// Run, which binds srv.Addr itself, serves TLS as Serve does: with
// Certificates and HTTP/2, with HTTP/2 disabled by a non-nil empty
// TLSNextProto, and with GetConfigForClient, HTTP/2 when the config it
// returns offers h2.
func TestRunServesTLSAsServeDoes(t *testing.T) {
	cert, pool := selfSigned(t)
	for _, c := range []struct {
		name string
		srv  func() *http.Server
		want string
	}{
		{"Certificates", func() *http.Server {
			return &http.Server{TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}}
		}, "HTTP/2.0"},
		{"HTTP/2 disabled", func() *http.Server {
			return &http.Server{TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}},
				TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){}}
		}, "HTTP/1.1"},
		{"GetConfigForClient without h2", func() *http.Server {
			return &http.Server{TLSConfig: &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
				return &tls.Config{Certificates: []tls.Certificate{cert}}, nil
			}}}
		}, "HTTP/1.1"},
		{"GetConfigForClient with h2", func() *http.Server {
			return &http.Server{TLSConfig: &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
				return &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"}}, nil
			}}}
		}, "HTTP/2.0"},
	} {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		l.Close() // Run binds the address itself
		srv := c.srv()
		srv.Addr, srv.Handler = addr, http.NotFoundHandler()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Run(ctx, srv) }()
		if p := getProto(t, "https://"+addr+"/", pool, cancel, done); p != c.want {
			t.Errorf("%s: negotiated %s, want %s", c.name, p, c.want)
		}
		cancel()
	}
}
