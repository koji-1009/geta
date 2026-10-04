package geta_test

import (
	"context"
	"iter"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/koji-1009/geta"
)

// ConcurrencyLimit: admit, shed, release.

func TestConcurrencyLimitShedsAndReleases(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		var entered sync.WaitGroup
		slow := func(ctx context.Context, _ *empty) (*ok, error) {
			entered.Done()
			<-gate
			return &ok{true}, nil
		}
		boom := func(ctx context.Context, _ *empty) (*ok, error) { panic("boom") }
		tbl := geta.Table{
			Root: geta.Scope{geta.Recover(slog.New(slog.DiscardHandler)), geta.ConcurrencyLimit(2)},
			Routes: []geta.Entry{
				{Path: "/slow", Route: get(slow)},
				{Path: "/fast", Route: get(okHandler)},
				{Path: "/boom", Route: get(boom)},
			},
		}
		a := accepts(t, tbl)
		results := make(chan int, 2)
		entered.Add(2)
		for range 2 {
			go func() { results <- do(t, a, "GET", "/slow").Code }()
		}
		entered.Wait()
		shed := do(t, a, "GET", "/fast")
		if shed.Code != 503 || shed.Header().Get("Retry-After") != "" || !strings.Contains(shed.Body.String(), "server at capacity") {
			t.Fatalf("%d %v %s", shed.Code, shed.Header(), shed.Body)
		}
		close(gate)
		if first, second := <-results, <-results; first != 200 || second != 200 {
			t.Fatal("held requests failed")
		}
		// A panicking handler frees its slot too.
		for range 3 {
			if c := do(t, a, "GET", "/boom").Code; c != 500 {
				t.Fatal(c)
			}
		}
		if c := do(t, a, "GET", "/fast").Code; c != 200 {
			t.Fatal("slots leaked:", c)
		}
	})
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed chan struct{}
	once    atomic.Bool
}

func (f *flushRecorder) Flush() {
	f.ResponseRecorder.Flush()
	if f.once.CompareAndSwap(false, true) {
		close(f.flushed)
	}
}

// With two ConcurrencyLimits in a chain, a stream frees both slots.
func TestNestedConcurrencyLimitsFreeEverySlotForAStream(t *testing.T) {
	release := make(chan struct{})
	stream := func(ctx context.Context, _ *empty) (*geta.Stream[ok], error) {
		return &geta.Stream[ok]{Events: iter.Seq[ok](func(yield func(ok) bool) {
			<-release
		})}, nil
	}
	app, err := geta.New(geta.Table{
		Root: geta.Scope{geta.ConcurrencyLimit(1)},
		Routes: []geta.Entry{
			{Path: "/s", Route: get(stream), Scopes: []geta.Scope{{geta.ConcurrencyLimit(1)}}},
			{Path: "/x", Route: get(okHandler), Scopes: []geta.Scope{{geta.ConcurrencyLimit(1)}}},
		},
	}, geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	fw := &flushRecorder{ResponseRecorder: httptest.NewRecorder(), flushed: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.ServeHTTP(fw, httptest.NewRequest(http.MethodGet, "/s", nil))
	}()
	<-fw.flushed
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	close(release)
	<-done
	if rec.Code != 200 {
		t.Fatalf("GET /x while the stream is open: %d, want 200 (a stream holds no slot)", rec.Code)
	}
}
