package geta

import (
	"context"
	"fmt"
	"net/http"
	"sync"
)

type slotKey struct{}

// releaseSlot frees the request's concurrency slots early, as an open stream
// or switched connection does.
func releaseSlot(ctx context.Context) {
	if f, ok := ctx.Value(slotKey{}).(func()); ok {
		f()
	}
}

// ConcurrencyLimit sheds requests past n in flight with 503. A shed request
// takes no slot; a slot is freed when its handler returns, panics, or starts
// a stream. The 503 carries no Retry-After. [New] refuses an n below 1.
func ConcurrencyLimit(n int) Middleware {
	var bad error
	if n < 1 {
		bad = fmt.Errorf("geta.ConcurrencyLimit %d is less than 1", n)
	}
	sem := make(chan struct{}, max(n, 1))
	m := Ordered(OrderShed, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case sem <- struct{}{}:
			default:
				writeProblem(w, r, http.StatusServiceUnavailable, "server at capacity", nil)
				return
			}
			release := sync.OnceFunc(func() { <-sem })
			defer release()
			// Chain to an outer limit so releaseSlot frees every slot.
			all := release
			if outer, ok := r.Context().Value(slotKey{}).(func()); ok {
				all = func() {
					release()
					outer()
				}
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), slotKey{}, all)))
		})
	})
	m.name = "concurrency-limit"
	m.bad = bad
	m.answers = []answer{{status: http.StatusServiceUnavailable, reason: "Server at capacity"}}
	return m
}
