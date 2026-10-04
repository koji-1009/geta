// Package metrics counts requests per route and status, labelled by the
// route's template, never by the raw path.
package metrics

import (
	"cmp"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/koji-1009/geta"
)

// Routes holds the counts, safe for concurrent use.
type Routes struct {
	mu     sync.Mutex
	counts map[key]*tally
}

type key struct {
	route  string
	status int
}

type tally struct {
	count int
	total time.Duration
}

// New returns an empty set of counts.
func New() *Routes { return &Routes{counts: map[key]*tally{}} }

// Unmatched is the route of a request no operation served: a 404, a 405, or
// a redirect to the clean path. A scanner's thousand URLs are one label.
const Unmatched = "unmatched"

// Middleware counts every request, in the root scope so that it sees the
// 404s and 405s too. geta.Observe, given the request's own context, reads
// how the App served it once the rest of the chain has returned: the method
// and the operation's template, after any root middleware rewrote the
// request. geta.Matched would read the match as it stands when called, which
// for a redirect to the clean path is the operation redirected to, though
// nothing served it.
//
// It is geta.Use, unordered: it changes nothing a later middleware reads,
// so it may sit anywhere in the chain. Placed first, it also counts the
// answers of every middleware after it: the CORS preflight's 204, the rate
// limit's 429, the gate's 401.
func (rs *Routes) Middleware() geta.Middleware {
	return geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, served := geta.Observe(r.Context())
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r)
			route := Unmatched
			if method, m, ok := served(); ok {
				route = method + " " + m.Template
			}
			rs.add(key{route, cmp.Or(sw.status, http.StatusOK)}, time.Since(start))
		})
	})
}

func (rs *Routes) add(k key, d time.Duration) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	t, ok := rs.counts[k]
	if !ok {
		t = &tally{}
		rs.counts[k] = t
	}
	t.count++
	t.total += d
}

// Count is the requests one route answered with one status.
type Count struct {
	Route  string `json:"route" doc:"The method and the route's template, or unmatched"`
	Status int    `json:"status"`
	Count  int    `json:"count"`
	// Seconds is the time the requests took, together.
	Seconds float64 `json:"seconds"`
}

// Snapshot is every count, by route and status.
func (rs *Routes) Snapshot() []Count {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]Count, 0, len(rs.counts))
	for k, t := range rs.counts {
		out = append(out, Count{Route: k.route, Status: k.status, Count: t.count, Seconds: t.total.Seconds()})
	}
	slices.SortFunc(out, func(a, b Count) int {
		return cmp.Or(cmp.Compare(a.Route, b.Route), cmp.Compare(a.Status, b.Status))
	})
	return out
}

// statusWriter keeps the status the response is written with. Unwrap lets
// http.ResponseController reach the writer beneath, for an event stream's
// Flush or an upgrade's Hijack.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 && code >= 200 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
