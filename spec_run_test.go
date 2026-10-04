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
	"time"
)

// Rules of routing.md about Run and Serve that no other test asserts as the
// rule states them.

// The constants Run and Serve use.
func TestRoutingRunConstants(t *testing.T) {
	if ShutdownGrace != 30*time.Second || DefaultReadHeaderTimeout != 10*time.Second || DefaultIdleTimeout != 2*time.Minute {
		t.Fatalf("ShutdownGrace %v, DefaultReadHeaderTimeout %v, DefaultIdleTimeout %v", ShutdownGrace, DefaultReadHeaderTimeout, DefaultIdleTimeout)
	}
}

// With ReadTimeout zero, Serve fills ReadHeaderTimeout and IdleTimeout each
// on its own when it is zero, keeps a value the caller set (a negative one
// too), and sets no ReadTimeout or WriteTimeout; a non-zero ReadTimeout
// leaves both unset.
func TestRoutingServeFillsEachLimitAlone(t *testing.T) {
	for _, c := range []struct {
		srv                           *http.Server
		readHeader, idle, read, write time.Duration
	}{
		{&http.Server{}, DefaultReadHeaderTimeout, DefaultIdleTimeout, 0, 0},
		{&http.Server{ReadHeaderTimeout: 3 * time.Second}, 3 * time.Second, DefaultIdleTimeout, 0, 0},
		{&http.Server{IdleTimeout: 5 * time.Second}, DefaultReadHeaderTimeout, 5 * time.Second, 0, 0},
		{&http.Server{IdleTimeout: -1, ReadHeaderTimeout: -1}, -1, -1, 0, 0},
		{&http.Server{ReadTimeout: time.Minute}, 0, 0, time.Minute, 0},
		{&http.Server{ReadTimeout: time.Minute, IdleTimeout: 5 * time.Second}, 0, 5 * time.Second, time.Minute, 0},
	} {
		inReadHeader, inIdle, inRead := c.srv.ReadHeaderTimeout, c.srv.IdleTimeout, c.srv.ReadTimeout
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := Serve(ctx, c.srv, newPipeListener()); err != nil {
			t.Fatal(err)
		}
		s := c.srv
		if s.ReadHeaderTimeout != c.readHeader || s.IdleTimeout != c.idle || s.ReadTimeout != c.read || s.WriteTimeout != c.write {
			t.Errorf("ReadHeaderTimeout %v IdleTimeout %v ReadTimeout %v: served with %v, %v, %v, WriteTimeout %v",
				inReadHeader, inIdle, inRead, s.ReadHeaderTimeout, s.IdleTimeout, s.ReadTimeout, s.WriteTimeout)
		}
	}
}

// Serve serves on the listener it is given; srv.Addr, here an address no
// one could listen on, is not used.
func TestRoutingServeIgnoresAddr(t *testing.T) {
	l := newPipeListener()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, &http.Server{Addr: "256.0.0.1:1", Handler: http.NotFoundHandler()}, l) }()
	c := l.dial(t)
	if _, err := io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	c.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("%d", res.StatusCode)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// failingListener fails every Accept with err.
type failingListener struct{ err error }

func (l failingListener) Accept() (net.Conn, error) { return nil, l.err }
func (l failingListener) Close() error              { return nil }
func (l failingListener) Addr() net.Addr            { return pipeAddr{} }

// A serve failure before the context ends is Serve's result.
func TestRoutingServeReturnsAServeFailure(t *testing.T) {
	boom := errors.New("accept failed")
	done := make(chan error, 1)
	go func() {
		done <- Serve(context.Background(), &http.Server{Handler: http.NotFoundHandler()}, failingListener{boom})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, boom) {
			t.Fatalf("Serve returned %v, want the accept error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
}

// A server that disables HTTP/2 (a non-nil, empty TLSNextProto) is served
// over TLS with HTTP/1.1.
func TestRoutingServeTLSWithHTTP2Disabled(t *testing.T) {
	cert, pool := selfSigned(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler:      http.NotFoundHandler(),
		TLSConfig:    &tls.Config{Certificates: []tls.Certificate{cert}},
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, srv, l) }()
	if p := getProto(t, "https://"+l.Addr().String()+"/", pool, cancel, done); p != "HTTP/1.1" {
		t.Fatalf("negotiated %s, want HTTP/1.1", p)
	}
}
