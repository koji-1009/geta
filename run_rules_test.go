package geta

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

// In-flight requests get ShutdownGrace after the context ends; then
// the remaining connections are closed, and Run reports that the grace ran
// out.
func TestShutdownGraceThenForcedClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-r.Context().Done() // holds the request until its connection closes
		})}
		l, stop := serveInBubble(t, srv)
		c := l.dial(t)
		if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		<-entered
		start := time.Now()
		closed := make(chan time.Duration, 1)
		go func() { closed <- waitClosed(c) }()
		err := stop()
		if d := time.Since(start); d != ShutdownGrace {
			t.Fatalf("the in-flight request was given %v, want ShutdownGrace (%v)", d, ShutdownGrace)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run returned %v after the grace ran out, want context.DeadlineExceeded", err)
		}
		if d := <-closed; d > ShutdownGrace {
			t.Fatalf("the connection stayed open %v", d)
		}
	})
}

// A request that finishes within the grace lets Run return nil.
func TestShutdownWaitsForAnInFlightRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-release
			io.WriteString(w, "done")
		})}
		l, stop := serveInBubble(t, srv)
		c := l.dial(t)
		if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		<-entered
		go func() {
			time.Sleep(ShutdownGrace / 2)
			close(release)
		}()
		res := make(chan string, 1)
		go func() {
			r, err := http.ReadResponse(bufio.NewReader(c), nil)
			if err != nil {
				res <- err.Error()
				return
			}
			b, _ := io.ReadAll(r.Body)
			res <- string(b)
		}()
		start := time.Now()
		if err := stop(); err != nil {
			t.Fatalf("Run returned %v after a clean shutdown", err)
		}
		// Shutdown polls for idle connections, so it returns within one poll
		// interval (at most 500ms) of the request's end.
		if d := time.Since(start); d < ShutdownGrace/2 || d > ShutdownGrace/2+time.Second {
			t.Fatalf("shutdown took %v, want the request's %v", d, ShutdownGrace/2)
		}
		if got := <-res; got != "done" {
			t.Fatalf("the in-flight request got %q", got)
		}
	})
}

// WithShutdownGrace replaces ShutdownGrace: an in-flight request that
// outlasts the given grace has its connection closed once it runs out, and
// Serve reports that it did. (Serve registers for signals, which a synctest
// bubble does not allow, so this runs on the clock, with a short grace.)
func TestWithShutdownGraceSetsTheGrace(t *testing.T) {
	const grace = 100 * time.Millisecond
	entered := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done() // holds the request until its connection closes
	})}
	l := newPipeListener()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, srv, l, WithShutdownGrace(grace)) }()
	c := l.dial(t)
	defer c.Close()
	if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	<-entered
	start := time.Now()
	cancel()
	err := <-done
	if d := time.Since(start); d < grace || d > ShutdownGrace/2 {
		t.Fatalf("the in-flight request was given %v, want the grace given (%v)", d, grace)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Serve returned %v after the grace ran out, want context.DeadlineExceeded", err)
	}
}

// A grace that is not positive is refused: Run listens on nothing and Serve
// serves nothing, closing its listener, and each returns the error.
func TestWithShutdownGraceRefusesANonPositiveGrace(t *testing.T) {
	listened := false
	saved := listen
	t.Cleanup(func() { listen = saved })
	listen = func(network, addr string) (net.Listener, error) {
		listened = true
		return nil, errors.New("not listening in a test")
	}
	for _, d := range []time.Duration{0, -time.Second} {
		want := "geta: WithShutdownGrace(" + d.String() + "): grace is not positive"
		if err := Run(context.Background(), &http.Server{}, WithShutdownGrace(d)); err == nil || err.Error() != want || listened {
			t.Errorf("Run with a grace of %v: %v (listened %v), want %q before listening", d, err, listened, want)
		}
		l := newPipeListener()
		if err := Serve(context.Background(), &http.Server{}, l, WithShutdownGrace(d)); err == nil || err.Error() != want {
			t.Errorf("Serve with a grace of %v: %v, want %q", d, err, want)
		}
		if _, err := l.Accept(); !errors.Is(err, net.ErrClosed) {
			t.Errorf("Serve with a grace of %v left its listener open: Accept returned %v", d, err)
		}
	}
}

// A serve error other than the server being closed, met while shutting
// down, is Run's result.
func TestServeErrorDuringShutdownIsReported(t *testing.T) {
	boom := errors.New("boom")
	release := make(chan struct{})
	srv := &http.Server{}
	srv.RegisterOnShutdown(func() { close(release) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := serveUntil(ctx, srv, ShutdownGrace, func() error {
		<-release
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want the serve error", err)
	}
}

// Run listens on srv.Addr, or, when it is empty, on :https for a
// server with a certificate and on :http otherwise.
func TestRunListensOnTheDefaultAddress(t *testing.T) {
	var got string
	refused := errors.New("not listening in a test")
	saved := listen
	t.Cleanup(func() { listen = saved })
	listen = func(network, addr string) (net.Listener, error) {
		got = addr
		return nil, refused
	}
	cert, _ := selfSigned(t)
	for _, c := range []struct {
		srv  *http.Server
		want string
	}{
		{&http.Server{}, ":http"},
		{&http.Server{TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}}, ":https"},
		{&http.Server{TLSConfig: &tls.Config{GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil }}}, ":https"},
		{&http.Server{TLSConfig: &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) { return nil, nil }}}, ":https"},
		{&http.Server{TLSConfig: &tls.Config{}}, ":http"}, // no certificate: not TLS
		{&http.Server{Addr: "127.0.0.1:8443", TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}}, "127.0.0.1:8443"},
	} {
		got = ""
		if err := Run(context.Background(), c.srv); !errors.Is(err, refused) || got != c.want {
			t.Errorf("Addr %q TLS %v: listened on %q (%v), want %q", c.srv.Addr, c.srv.TLSConfig != nil, got, err, c.want)
		}
	}
}

// Serve closes the listener when it returns. (Serve registers for
// signals, which a synctest bubble does not allow, so this runs outside one.)
func TestServeClosesTheListener(t *testing.T) {
	l := newPipeListener()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, &http.Server{Handler: http.NotFoundHandler()}, l) }()
	c := l.dial(t) // Serve is accepting
	c.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := l.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after Serve returned: %v, want net.ErrClosed", err)
	}
}

type baseKey struct{}

// Run and Serve keep a BaseContext the caller set, adding the drain
// signal to the context it returns.
func TestRunKeepsTheCallersBaseContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		got := make(chan string, 1)
		srv := &http.Server{
			BaseContext: func(net.Listener) context.Context {
				return context.WithValue(context.Background(), baseKey{}, "caller's")
			},
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				v, _ := r.Context().Value(baseKey{}).(string)
				if draining(r.Context()) == nil {
					v += " without the drain signal"
				}
				got <- v
			}),
		}
		l, stop := serveInBubble(t, srv)
		defer stop()
		c := l.dial(t)
		if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		if v := <-got; v != "caller's" {
			t.Fatalf("the request's context carries %q, want the caller's value and the drain signal", v)
		}
		c.Close()
	})
}

// A server whose TLSConfig has GetConfigForClient alone is served
// over TLS, with HTTP/2 negotiated when the config it returns offers it.
func TestServeTLSWithGetConfigForClient(t *testing.T) {
	cert, pool := selfSigned(t)
	for _, c := range []struct {
		protos []string
		want   string
	}{
		{nil, "HTTP/1.1"},
		{[]string{"h2", "http/1.1"}, "HTTP/2.0"},
	} {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: http.NotFoundHandler(), TLSConfig: &tls.Config{
			GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
				return &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: c.protos}, nil
			},
		}}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Serve(ctx, srv, l) }()
		if p := getProto(t, "https://"+l.Addr().String()+"/", pool, cancel, done); p != c.want {
			t.Errorf("NextProtos %q: negotiated %s, want %s", c.protos, p, c.want)
		}
		cancel()
	}
}
