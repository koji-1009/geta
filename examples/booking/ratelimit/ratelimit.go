// Package ratelimit is the booking service's rate limiter: a
// golang.org/x/time/rate token bucket per client address, held in this
// process's memory. That is enough for one instance; several instances
// behind a load balancer each count apart, so a deployment that runs more
// than one keeps its buckets in a store they share.
//
// geta has no rate limiter of its own: the store and the key are the
// application's policy. What geta needs from one is what it answers, so the
// document and CORS carry it: PerAddr declares its 429 with Answers and its
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
	"golang.org/x/time/rate"
)

// PerAddr admits up to n requests per client address in a burst, refilling
// n tokens evenly over per. The address is the host of RemoteAddr, or
// RemoteAddr as it is when it has no port; behind a proxy, key by what the
// proxy forwards instead. A refused request answers 429 with a Retry-After
// of the whole seconds until a token exists. It runs at geta.OrderShed:
// after Recover, before a deadline and the gate.
func PerAddr(n int, per time.Duration) geta.Middleware {
	if n < 1 || per < time.Duration(n) {
		// geta.New refuses it, naming this, as it does its own middleware's
		// mistakes.
		return geta.Invalid("rate-limit", fmt.Errorf("ratelimit.PerAddr: %d per %v is no rate", n, per))
	}
	l := &limiter{every: rate.Every(per / time.Duration(n)), burst: n, keys: map[string]*rate.Limiter{}, sweepAt: 1024}
	return geta.Ordered(geta.OrderShed, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if wait, ok := l.take(addr(r), time.Now()); !ok {
				w.Header().Set("Retry-After", strconv.FormatInt(int64(math.Ceil(wait.Seconds())), 10))
				geta.WriteProblem(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}).Answers(http.StatusTooManyRequests, "Rate limit exceeded").
		Header(http.StatusTooManyRequests, "Retry-After", "The whole seconds until the rate limit admits a request, sent with its 429 (RFC 9110 section 10.2.3)",
			geta.HeaderOf[int]("minimum=1"))
}

func addr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type limiter struct {
	mu      sync.Mutex
	every   rate.Limit
	burst   int
	keys    map[string]*rate.Limiter
	sweepAt int
}

// take spends a token of key's bucket, or reports how long until one
// exists.
func (l *limiter) take(key string, now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lim, ok := l.keys[key]
	if !ok {
		// A full bucket is the same as none: drop those that have refilled,
		// so a stream of new addresses cannot grow the map past the ones
		// that are spending. The next sweep waits until the map doubles,
		// which keeps the cost constant per request.
		if len(l.keys) >= l.sweepAt {
			for k, old := range l.keys {
				if old.TokensAt(now) >= float64(l.burst) {
					delete(l.keys, k)
				}
			}
			l.sweepAt = max(1024, 2*len(l.keys))
		}
		lim = rate.NewLimiter(l.every, l.burst)
		l.keys[key] = lim
	}
	res := lim.ReserveN(now, 1)
	if wait := res.DelayFrom(now); wait > 0 {
		res.CancelAt(now)
		return wait, false
	}
	return 0, true
}
