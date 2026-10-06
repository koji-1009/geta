package geta

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type tick struct {
	N int `json:"n"`
}

// When the server begins shutting down, open streams end, so they cannot
// hold the shutdown open.
func TestStreamsEndWhenTheServerDrains(t *testing.T) {
	h := func(ctx context.Context, _ *struct{}) (*Stream[tick], error) {
		return &Stream[tick]{Events: func(yield func(tick) bool) { <-ctx.Done() }}, nil
	}
	a, err := New(Table{Routes: []Entry{{Path: "/e", Route: Route{Get: Op(http.StatusOK, h, Doc{})}}}})
	if err != nil {
		t.Fatal(err)
	}
	drain := make(chan struct{})
	srv := httptest.NewUnstartedServer(a)
	srv.Config.BaseContext = func(net.Listener) context.Context {
		return context.WithValue(context.Background(), drainKey{}, drain)
	}
	srv.Start()
	defer srv.Close()
	res, err := http.Get(srv.URL + "/e")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	ended := make(chan struct{})
	go func() {
		bufio.NewReader(res.Body).ReadString(0)
		close(ended)
	}()
	close(drain)
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream outlived the drain")
	}
}

// listenOn makes Run listen on l, whatever the address.
func listenOn(t *testing.T, l net.Listener) {
	saved := listen
	t.Cleanup(func() { listen = saved })
	listen = func(string, string) (net.Listener, error) { return l, nil }
}

// Run serving stops when its context ends, and returns nil.
func TestRunStopsWhenItsContextEnds(t *testing.T) {
	l := newPipeListener()
	listenOn(t, l)
	ctx, cancel := context.WithCancel(context.Background())
	srv := &http.Server{Handler: http.NotFoundHandler()}
	done := make(chan error, 1)
	go func() { done <- Run(ctx, srv) }()
	l.dial(t).Close() // Run is serving
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}

// OPTIONS * reaches the handler through Run, where net/http would otherwise
// answer it itself; TestOptionsAsteriskThroughARealServer covers Serve.
func TestRunPassesOptionsAsteriskOn(t *testing.T) {
	l := newPipeListener()
	listenOn(t, l)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI == "*" {
			w.WriteHeader(http.StatusTeapot)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, &http.Server{Handler: h}) }()
	c := l.dial(t)
	if _, err := io.WriteString(c, "OPTIONS * HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	c.Close()
	cancel()
	if err := <-done; err != nil || res.StatusCode != http.StatusTeapot {
		t.Fatal(res.StatusCode, err)
	}
}

func TestRunReportsAListenFailure(t *testing.T) {
	err := Run(context.Background(), &http.Server{Addr: "256.0.0.1:1"})
	if err == nil || !strings.Contains(err.Error(), "256.0.0.1") {
		t.Fatal(err)
	}
}

// pipeListener hands out in-memory connections, so a server runs inside a
// synctest bubble.
type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

func (l *pipeListener) dial(t *testing.T) net.Conn {
	c, s := net.Pipe()
	select {
	case l.conns <- s:
	case <-l.done:
		t.Fatal("listener closed")
	}
	return c
}

// serveInBubble serves srv on a pipe listener the way Run does, without
// signals, and returns the listener and a stop function that shuts it down
// and reports Run's result.
func serveInBubble(t *testing.T, srv *http.Server) (*pipeListener, func() error) {
	l := newPipeListener()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveUntil(ctx, srv, ShutdownGrace, func() error { return srv.Serve(l) }) }()
	return l, func() error {
		cancel()
		return <-done
	}
}

// waitClosed reads c until the server closes it and returns how long that
// took.
func waitClosed(c net.Conn) time.Duration {
	start := time.Now()
	io.Copy(io.Discard, c)
	return time.Since(start)
}

// A client that never finishes its headers is disconnected after
// DefaultReadHeaderTimeout, and an idle keep-alive connection after
// DefaultIdleTimeout; a limit the caller set is kept.
func TestRunBoundsSlowHeadersAndIdleConnections(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := &http.Server{Handler: http.NotFoundHandler()}
		l, stop := serveInBubble(t, srv)
		defer func() {
			if err := stop(); err != nil {
				t.Error(err)
			}
		}()

		c := l.dial(t)
		if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n"); err != nil {
			t.Fatal(err)
		}
		if d := waitClosed(c); d != DefaultReadHeaderTimeout {
			t.Fatalf("slow headers held the connection for %v, want %v", d, DefaultReadHeaderTimeout)
		}

		c = l.dial(t)
		if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		br := bufio.NewReader(c)
		res, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		start := time.Now()
		io.Copy(io.Discard, br)
		if d := time.Since(start); d != DefaultIdleTimeout {
			t.Fatalf("an idle connection stayed open for %v, want %v", d, DefaultIdleTimeout)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		srv := &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: 3 * time.Second, IdleTimeout: -1}
		l, stop := serveInBubble(t, srv)
		defer stop()
		c := l.dial(t)
		if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n"); err != nil {
			t.Fatal(err)
		}
		if d := waitClosed(c); d != 3*time.Second {
			t.Fatalf("the caller's ReadHeaderTimeout was not kept: held for %v", d)
		}
		if srv.IdleTimeout != -1 {
			t.Fatalf("the caller's IdleTimeout was replaced by %v", srv.IdleTimeout)
		}
	})
	srv := &http.Server{ReadTimeout: time.Minute}
	withDefaults(srv)
	if srv.ReadHeaderTimeout != 0 || srv.IdleTimeout != 0 {
		t.Fatalf("a server with ReadTimeout got defaults: %v, %v", srv.ReadHeaderTimeout, srv.IdleTimeout)
	}
}

// The limits Run sets do not cut an open event stream: an event sent long
// after both have passed still arrives.
func TestRunLimitsDoNotCutAStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ticks := make(chan tick)
		h := func(ctx context.Context, _ *struct{}) (*Stream[tick], error) {
			return &Stream[tick]{Events: func(yield func(tick) bool) {
				for {
					select {
					case v := <-ticks:
						if !yield(v) {
							return
						}
					case <-ctx.Done():
						return
					}
				}
			}}, nil
		}
		a, err := New(Table{Routes: []Entry{{Path: "/e", Route: Route{Get: Op(http.StatusOK, h, Doc{})}}}})
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: a}
		l, stop := serveInBubble(t, srv)
		c := l.dial(t)
		if _, err := io.WriteString(c, "GET /e HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		br := bufio.NewReader(c)
		res, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusOK {
			t.Fatal(res.Status)
		}
		time.Sleep(10 * (DefaultReadHeaderTimeout + DefaultIdleTimeout))
		ticks <- tick{N: 1}
		body := bufio.NewReader(res.Body)
		line, err := body.ReadString('\n')
		if err != nil || line != "data: {\"n\":1}\n" {
			t.Fatalf("%q, %v", line, err)
		}
		rest := make(chan struct{})
		go func() {
			io.Copy(io.Discard, br)
			close(rest)
		}()
		if err := stop(); err != nil {
			t.Fatal(err)
		}
		<-rest
	})
}

// selfSigned returns a server certificate for 127.0.0.1 and a pool that
// trusts it.
func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "geta test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// getProto requests url over TLS trusting pool, retrying until the server
// is up, then stops the server with cancel, checks its result on done, and
// returns the response's protocol.
func getProto(t *testing.T, url string, pool *x509.CertPool, cancel context.CancelFunc, done <-chan error) string {
	t.Helper()
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}, ForceAttemptHTTP2: true}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	var res *http.Response
	var err error
	for range 100 {
		if res, err = client.Get(url); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.TLS == nil {
		t.Fatal("not served over TLS")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not stop")
	}
	return res.Proto
}

// A server whose TLSConfig carries a certificate is served over TLS, and
// HTTP/2 is negotiated.
func TestServeNegotiatesHTTP2OverTLS(t *testing.T) {
	cert, pool := selfSigned(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.NotFoundHandler(), TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, srv, l) }()
	if p := getProto(t, "https://"+l.Addr().String()+"/", pool, cancel, done); p != "HTTP/2.0" {
		t.Fatalf("negotiated %s, want HTTP/2.0", p)
	}
}

// Run, which binds srv.Addr itself, serves TLS the same way.
func TestRunServesTLS(t *testing.T) {
	cert, pool := selfSigned(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close() // Run binds the address itself
	srv := &http.Server{Addr: addr, Handler: http.NotFoundHandler(), TLSConfig: &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil },
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, srv) }()
	if p := getProto(t, "https://"+addr+"/", pool, cancel, done); p != "HTTP/2.0" {
		t.Fatalf("negotiated %s, want HTTP/2.0", p)
	}
}
