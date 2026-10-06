//go:build unix

package geta

import (
	"context"
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
		l := newPipeListener()
		listenOn(t, l)
		done := make(chan error, 1)
		go func() { done <- Run(context.Background(), &http.Server{Handler: http.NotFoundHandler()}) }()
		// Once Run accepts, it has registered for the signals: it does so
		// before it serves.
		l.dial(t).Close()
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
