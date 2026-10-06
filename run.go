package geta

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// ShutdownGrace is how long [Run] and [Serve] wait for in-flight requests
// after a signal before closing the remaining connections, unless
// [WithShutdownGrace] gives another. It is not derived from the App's
// Timeouts; set it longer than the longest operation that should finish and
// shorter than the platform's own shutdown grace.
const ShutdownGrace = 30 * time.Second

// RunOption configures [Run] and [Serve].
type RunOption func(*runConfig)

type runConfig struct {
	grace time.Duration
}

// WithShutdownGrace sets how long [Run] and [Serve] wait for in-flight
// requests once shutdown begins, in place of [ShutdownGrace]. If d is not
// positive, Run and Serve return an error and serve nothing.
func WithShutdownGrace(d time.Duration) RunOption {
	return func(c *runConfig) { c.grace = d }
}

// runOptions applies opts to the defaults and validates the result.
func runOptions(opts []RunOption) (runConfig, error) {
	c := runConfig{grace: ShutdownGrace}
	for _, o := range opts {
		o(&c)
	}
	if c.grace <= 0 {
		return c, fmt.Errorf("geta: WithShutdownGrace(%v): grace is not positive", c.grace)
	}
	return c, nil
}

// DefaultReadHeaderTimeout is the ReadHeaderTimeout [Run] and [Serve] give a
// server whose ReadHeaderTimeout and ReadTimeout are both zero, so a client
// sending headers slowly cannot hold a connection forever. On a TLS server it
// also bounds the handshake.
const DefaultReadHeaderTimeout = 10 * time.Second

// DefaultIdleTimeout is the IdleTimeout [Run] and [Serve] give a server
// whose IdleTimeout and ReadTimeout are both zero. It is longer than common
// load balancers' idle timeouts (60 seconds on AWS ALB), so the proxy, not
// the server, retires a pooled connection. A connection with a request in
// progress, an open event stream included, is not idle.
const DefaultIdleTimeout = 2 * time.Minute

type drainKey struct{}

// draining returns a channel closed when the server begins shutting down,
// so an open stream can end instead of holding the shutdown open.
func draining(ctx context.Context) <-chan struct{} {
	ch, _ := ctx.Value(drainKey{}).(chan struct{})
	return ch
}

// Run serves srv on srv.Addr until ctx is done or the process receives
// SIGINT or SIGTERM, then stops accepting connections and lets in-flight
// requests finish, for at most [ShutdownGrace] or the grace
// [WithShutdownGrace] gives. Open event streams end at once. Run is a
// convenience: an App is an http.Handler, and any server can serve it.
//
// Run returns nil once every request has finished, and
// context.DeadlineExceeded when the grace ran out and Run closed the
// connections still open; otherwise it returns the error that stopped it
// listening or serving.
//
// When srv.TLSConfig has a certificate (Certificates, GetCertificate, or
// GetConfigForClient), Run serves TLS, with HTTP/2 negotiated unless srv
// disables it. An empty srv.Addr is then ":https", otherwise ":http". With
// GetConfigForClient, HTTP/2 is negotiated when the returned config's
// NextProtos offers "h2".
//
// Run fills two limits the caller left at zero: [DefaultReadHeaderTimeout]
// and [DefaultIdleTimeout]; neither is set when srv.ReadTimeout is. A
// negative value means no limit. Run sets no ReadTimeout or WriteTimeout,
// which would cut event streams and upgraded connections; bound an operation
// with [Timeout] instead.
//
// Run sets DisableGeneralOptionsHandler, so OPTIONS * reaches the handler
// rather than net/http's own answer.
func Run(ctx context.Context, srv *http.Server, opts ...RunOption) error {
	if _, err := runOptions(opts); err != nil {
		return err
	}
	addr := srv.Addr
	if addr == "" {
		addr = ":http"
		if servesTLS(srv) {
			addr = ":https"
		}
	}
	l, err := listen("tcp", addr)
	if err != nil {
		return err
	}
	return Serve(ctx, srv, l, opts...)
}

// listen is net.Listen; a test replaces it to see the address Run binds.
var listen = net.Listen

// Serve is [Run] on a listener the caller already holds, such as one on
// ":0" or a socket handed over by the system. srv.Addr is not used. Serve
// closes l when it returns, even on an invalid option.
func Serve(ctx context.Context, srv *http.Server, l net.Listener, opts ...RunOption) error {
	c, err := runOptions(opts)
	if err != nil {
		l.Close()
		return err
	}
	if servesTLS(srv) {
		return run(ctx, srv, c.grace, func() error { return srv.ServeTLS(l, "", "") })
	}
	return run(ctx, srv, c.grace, func() error { return srv.Serve(l) })
}

// servesTLS reports whether srv carries its own certificate, as net/http
// decides when ServeTLS is given no files.
func servesTLS(srv *http.Server) bool {
	c := srv.TLSConfig
	return c != nil && (len(c.Certificates) > 0 || c.GetCertificate != nil || c.GetConfigForClient != nil)
}

func run(ctx context.Context, srv *http.Server, grace time.Duration, serve func() error) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serveUntil(ctx, srv, grace, serve)
}

// withDefaults fills the limits the caller left unset; see [Run].
func withDefaults(srv *http.Server) {
	if srv.ReadTimeout != 0 {
		return // it already bounds headers and idle connections
	}
	if srv.ReadHeaderTimeout == 0 {
		srv.ReadHeaderTimeout = DefaultReadHeaderTimeout
	}
	if srv.IdleTimeout == 0 {
		srv.IdleTimeout = DefaultIdleTimeout
	}
}

// serveUntil runs serve until it fails or ctx is done, then shuts srv down,
// giving in-flight requests grace. It handles no signals, so tests can run it
// in a synctest bubble.
func serveUntil(ctx context.Context, srv *http.Server, grace time.Duration, serve func() error) error {
	withDefaults(srv)
	srv.DisableGeneralOptionsHandler = true
	drain := make(chan struct{})
	base := srv.BaseContext
	srv.BaseContext = func(l net.Listener) context.Context {
		c := context.Background()
		if base != nil {
			c = base(l)
		}
		return context.WithValue(c, drainKey{}, drain)
	}
	srv.RegisterOnShutdown(func() { close(drain) })
	errc := make(chan error, 1)
	go func() { errc <- serve() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	err := srv.Shutdown(sctx)
	if errors.Is(err, context.DeadlineExceeded) {
		srv.Close()
	}
	if serr := <-errc; !errors.Is(serr, http.ErrServerClosed) {
		return serr
	}
	return err
}
