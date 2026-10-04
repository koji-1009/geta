package getatest

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/koji-1009/geta"
)

// A stream's record is taken by the client while the handler still runs;
// once both are done with it, none is left behind.
func TestServingsKeepNoRecordOfAFinishedStream(t *testing.T) {
	release := make(chan struct{})
	stream := geta.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			http.NewResponseController(w).Flush()
			<-release
			io.WriteString(w, "data: \"x\"\n\n")
			http.NewResponseController(w).Flush()
		})
	})
	// The operation is a stream, so the event the middleware writes for it
	// is one its document describes.
	c := New(t, geta.Table{Root: geta.Scope{stream}, Routes: []geta.Entry{{Path: "/x", Route: geta.Route{
		Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*geta.Stream[string], error) { return nil, nil }, geta.Doc{}),
	}}}})
	for range 3 {
		s := c.Stream("/x")
		if s.Response.Status != http.StatusOK {
			t.Fatal(s.Response.Status)
		}
		release <- struct{}{}
		if _, ok := s.Next(); !ok {
			t.Fatal("no event")
		}
		// The stream ends once the handler has returned.
		if _, ok := s.Next(); ok {
			t.Fatal("a second event")
		}
	}
	c.log.mu.Lock()
	defer c.log.mu.Unlock()
	if n := len(c.log.byID); n != 0 {
		t.Fatalf("%d records left", n)
	}
}

// A request the client cancels after it reached the handler leaves no
// record, whether the handler recorded it before the client gave up or after.
func TestServingsKeepNoRecordOfACancelledRequest(t *testing.T) {
	reached := make(chan struct{})
	finished := make(chan struct{})
	wait := geta.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() { finished <- struct{}{} }()
			if r.Header.Get("Early") != "" {
				w.WriteHeader(http.StatusOK) // recorded, not yet sent
			}
			reached <- struct{}{}
			<-r.Context().Done()
			w.WriteHeader(http.StatusOK)
		})
	})
	c := New(t, geta.Table{Root: geta.Scope{wait}, Routes: []geta.Entry{{Path: "/x", Route: geta.Route{
		Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*struct{}, error) { return &struct{}{}, nil }, geta.Doc{}),
	}}}})
	rt := documenting{c: c, next: c.hc.Transport}
	for _, early := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL()+"/x", nil)
		if err != nil {
			t.Fatal(err)
		}
		if early {
			req.Header.Set("Early", "1")
		}
		go func() {
			<-reached
			cancel()
		}()
		if _, err := rt.RoundTrip(req); err == nil {
			t.Fatal("the cancelled request succeeded")
		}
		<-finished
		// The servings handler finishes the record just after the app
		// returns.
		deadline := time.Now().Add(5 * time.Second)
		for {
			c.log.mu.Lock()
			n := len(c.log.byID)
			c.log.mu.Unlock()
			if n == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("early %v: %d records left", early, n)
			}
			time.Sleep(time.Millisecond)
		}
	}
}
