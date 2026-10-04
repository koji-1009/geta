// Package transport serves the booking app over TLS on TCP (HTTP/2 and
// HTTP/1.1, through geta.Serve) and over HTTP/3 on UDP (quic-go), announcing
// HTTP/3 to TCP clients with Alt-Svc. A *geta.App is an http.Handler, so
// quic-go serves it as it is; geta neither depends on quic-go nor sets
// Alt-Svc.
package transport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http"
	"time"

	"github.com/koji-1009/geta"
	"github.com/quic-go/quic-go/http3"
)

// SelfSigned returns a TLS config holding a fresh self-signed certificate
// for localhost and the loopback addresses, and a pool that trusts it.
func SelfSigned() (*tls.Config, *x509.CertPool, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "geta booking example"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	conf := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}}, MinVersion: tls.VersionTLS13}
	return conf, pool, nil
}

// Serve serves h on tcp over TLS and on udp over HTTP/3, until ctx ends or
// the process gets SIGINT or SIGTERM. geta.Serve drains the TCP server (open
// event streams end, requests in flight get geta.ShutdownGrace); the HTTP/3
// server is this function's to stop, and it gives that one the same grace.
func Serve(ctx context.Context, h http.Handler, tcp net.Listener, udp net.PacketConn, conf *tls.Config) error {
	h3 := &http3.Server{Handler: h, TLSConfig: http3.ConfigureTLSConfig(conf.Clone())}
	h3err := make(chan error, 1)
	go func() { h3err <- h3.Serve(udp) }()
	srv := &http.Server{TLSConfig: conf, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h3.SetQUICHeaders(w.Header()) // Alt-Svc: h3=":port"; ma=2592000
		h.ServeHTTP(w, r)
	})}
	err := geta.Serve(ctx, srv, tcp)
	stop, cancel := context.WithTimeout(context.Background(), geta.ShutdownGrace)
	defer cancel()
	if serr := h3.Shutdown(stop); serr != nil {
		h3.Close()
	}
	if herr := <-h3err; herr != nil && !errors.Is(herr, http.ErrServerClosed) && !errors.Is(herr, net.ErrClosed) {
		err = errors.Join(err, herr)
	}
	return err
}
