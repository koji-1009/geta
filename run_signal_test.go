//go:build unix

package geta

import (
	"context"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"
)

// Run serves until the process receives SIGINT or SIGTERM, then
// shuts down and returns nil. Run catches the signal, so it does not end the
// test process.
func TestRunStopsOnASignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		l.Close()
		done := make(chan error, 1)
		go func() { done <- Run(context.Background(), &http.Server{Addr: addr, Handler: http.NotFoundHandler()}) }()
		// Once Run answers, it has registered for the signals: it does so
		// before it serves.
		var up bool
		for range 200 {
			if res, err := http.Get("http://" + addr + "/"); err == nil {
				res.Body.Close()
				up = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !up {
			t.Fatal("Run did not serve")
		}
		if err := syscall.Kill(syscall.Getpid(), sig); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%v: Run returned %v", sig, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%v: Run did not return", sig)
		}
	}
}
