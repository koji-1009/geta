package main

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/routes"
	"github.com/koji-1009/geta/examples/booking/transport"
	"github.com/quic-go/quic-go/http3"
)

// The app served as main serves it with -tls: HTTP/2 over TLS on TCP,
// announcing HTTP/3, and HTTP/3 on UDP, on one port; a quic-go client gets
// the same answers over HTTP/3, the gate and geta's OPTIONS included.
func TestHTTP2AndHTTP3(t *testing.T) {
	h := newHarness(t)
	a, err := geta.New(routes.Table(h.env), routes.Options(h.env)...)
	if err != nil {
		t.Fatal(err)
	}
	conf, pool, err := transport.SelfSigned()
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenPacket("udp", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- transport.Serve(ctx, a, tcp, udp, conf) }()
	t.Cleanup(func() {
		cancel()
		if err := <-served; err != nil {
			t.Error(err)
		}
	})
	base := "https://" + tcp.Addr().String()
	token := h.token("viewer")

	h2 := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}, ForceAttemptHTTP2: true}}
	defer h2.CloseIdleConnections()
	h3 := &http.Client{Transport: &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	defer h3.Transport.(*http3.Transport).Close()

	do := func(c *http.Client, method, path, bearer string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), method, base+path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		res, err := c.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}

	res, body := do(h2, http.MethodGet, "/health", "")
	if res.Proto != "HTTP/2.0" || res.StatusCode != http.StatusOK || body != `{"status":"ok"}` ||
		!strings.HasPrefix(res.Header.Get("Alt-Svc"), `h3=":`) {
		t.Fatalf("%s %d %s %v", res.Proto, res.StatusCode, body, res.Header)
	}
	res, body = do(h3, http.MethodGet, "/health", "")
	if res.Proto != "HTTP/3.0" || res.StatusCode != http.StatusOK || body != `{"status":"ok"}` {
		t.Fatalf("%s %d %s", res.Proto, res.StatusCode, body)
	}
	if res, _ = do(h3, http.MethodGet, "/rooms", ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatal(res.StatusCode)
	}
	if res, body = do(h3, http.MethodGet, "/rooms", token); res.StatusCode != http.StatusOK || body != `{"items":[]}` {
		t.Fatal(res.StatusCode, body)
	}
	if res, _ = do(h3, http.MethodOptions, "/rooms", token); res.StatusCode != http.StatusNoContent || res.Header.Get("Allow") != "GET, HEAD, OPTIONS, POST" {
		t.Fatal(res.StatusCode, res.Header)
	}
}
