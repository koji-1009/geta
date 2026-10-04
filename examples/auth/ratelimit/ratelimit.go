// Package ratelimit is this example's rate limiter: a token bucket per key,
// held in this process's memory. That is enough for one instance; several
// instances behind a load balancer each count apart, so a deployment that
// runs more than one keeps its buckets in a store they share.
//
// geta has no rate limiter of its own: the store and the key are the
// application's policy. What geta needs from one is what it answers, so the
// document and CORS carry it: PerKey declares its 429 with Answers and its
// Retry-After with Header, an integer of at least 1 (geta.HeaderOf), since it
// writes delay-seconds.
package ratelimit

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/koji-1009/geta"
)

// ByRemoteAddr keys by the client's IP address: the host of RemoteAddr, or
// RemoteAddr as it is when it has no port.
func ByRemoteAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// PerKey admits up to n requests per key in a burst, refilling n tokens
// evenly over per. A refused request answers 429 with a Retry-After of the
// whole seconds until a token exists. It runs at geta.OrderShed: after
// Recover, before a deadline, and, in a Doc.BeforeGate, before the gate.
func PerKey(key func(*http.Request) string, n int, per time.Duration) geta.Middleware {
	if n < 1 || per < time.Duration(n) {
		// geta.New refuses it, naming this, as it does its own middleware's
		// mistakes.
		return geta.Invalid("rate-limit", fmt.Errorf("ratelimit.PerKey: %d per %v is no rate", n, per))
	}
	l := &limiter{capacity: float64(n), interval: per / time.Duration(n), buckets: map[string]*bucket{}, sweepAt: 1024}
	return geta.Ordered(geta.OrderShed, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if wait, ok := l.take(key(r), time.Now()); !ok {
				// At least 1, as declared: a wait take rounds down to no
				// nanosecond is still a wait.
				w.Header().Set("Retry-After", strconv.FormatInt(max(1, int64(math.Ceil(wait.Seconds()))), 10))
				geta.WriteProblem(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}).Answers(http.StatusTooManyRequests, "Rate limit exceeded").
		Header(http.StatusTooManyRequests, "Retry-After", "The whole seconds until the rate limit admits a request, sent with its 429 (RFC 9110 section 10.2.3)",
			geta.HeaderOf[int]("minimum=1"))
}

type limiter struct {
	mu       sync.Mutex
	capacity float64
	interval time.Duration
	buckets  map[string]*bucket
	sweepAt  int
}

type bucket struct {
	tokens float64
	at     time.Time
}

func (l *limiter) take(key string, now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		// A full bucket is the same as none: drop those that have refilled,
		// so a stream of new keys cannot grow the map past the keys that are
		// spending. The next sweep waits until the map doubles, which keeps
		// the cost constant per request.
		if len(l.buckets) >= l.sweepAt {
			for k, old := range l.buckets {
				if l.refill(old, now); old.tokens >= l.capacity {
					delete(l.buckets, k)
				}
			}
			l.sweepAt = max(1024, 2*len(l.buckets))
		}
		b = &bucket{tokens: l.capacity, at: now}
		l.buckets[key] = b
	}
	l.refill(b, now)
	if b.tokens < 1 {
		return time.Duration((1 - b.tokens) * float64(l.interval)), false
	}
	b.tokens--
	return 0, true
}

func (l *limiter) refill(b *bucket, now time.Time) {
	if el := now.Sub(b.at); el > 0 {
		b.tokens = min(l.capacity, b.tokens+float64(el)/float64(l.interval))
		b.at = now
	}
}
