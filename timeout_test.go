package geta_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/koji-1009/geta"
)

// Timeout: the deadline is a context; the 504 comes from the handler's
// return, and it still carries CORS headers.

func TestTimeoutIs504WithCORS(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		wait := func(ctx context.Context, _ *empty) (*ok, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("no deadline on the context")
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		tbl := withRoot(one("/x", get(wait)), geta.CORS(geta.AllowOrigins("*")), geta.Timeout(10*time.Second))
		rec := do(t, accepts(t, tbl), "GET", "/x", header("Origin", "https://a.example")...)
		if rec.Code != 504 || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("%d %v", rec.Code, rec.Header())
		}
		if el := time.Since(start); el != 10*time.Second {
			t.Fatalf("took %v of virtual time", el)
		}
	})
}

func TestTimeoutLeavesAFastHandlerAlone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tbl := withRoot(one("/x", get(okHandler)), geta.Timeout(time.Second))
		if c := do(t, accepts(t, tbl), "GET", "/x").Code; c != 200 {
			t.Fatal(c)
		}
	})
}

// A context derived from a Timeout context reports Canceled when the
// deadline passes; that is still a 504.
func TestDerivedContextDeadlineIs504(t *testing.T) {
	h := func(ctx context.Context, _ *empty) (*ok, error) {
		child, cancel := context.WithTimeout(ctx, time.Hour)
		defer cancel()
		<-child.Done()
		return nil, child.Err()
	}
	app, err := geta.New(one("/slow", get(h), geta.Scope{geta.Timeout(20 * time.Millisecond)}), geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/slow", nil))
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status %d, want 504", rec.Code)
	}
}

// A client that goes away under Timeout, seen through a derived context, is
// still a client that went away: nothing is written.
func TestDerivedContextClientGoneWritesNothing(t *testing.T) {
	h := func(ctx context.Context, _ *empty) (*ok, error) {
		child, cancel := context.WithCancel(ctx)
		defer cancel()
		<-child.Done()
		return nil, child.Err()
	}
	app, err := geta.New(one("/x", get(h), geta.Scope{geta.Timeout(time.Hour)}), geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/x", nil))
	if rec.Body.Len() != 0 || len(rec.Header()) != 0 {
		t.Fatalf("wrote %v %q", rec.Header(), rec.Body)
	}
}
