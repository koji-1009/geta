package geta_test

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"

	"github.com/koji-1009/geta"
)

// statusRecorder keeps the status a response is written with. Unwrap lets
// http.ResponseController reach the writer beneath, for a stream's Flush or
// an upgrade's Hijack.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Middleware in the root scope runs for every request, 404 and 405 included,
// and reads what the request matched: geta.Matched gives the template and
// the operation's Doc, so a metric is labelled by "/users/{id}", never by the
// raw path, and a request that matched nothing gets one label of its own
// rather than one per URL a scanner tries. geta.Observe, given the request's
// own context, reads the operation the request was served as once it has
// been (after a root rewrite, the one rewritten to).
func ExampleMatched() {
	var mu sync.Mutex
	counts := map[string]int{}
	byRoute := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, served := geta.Observe(r.Context())
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			label := "unmatched"
			if method, m, ok := served(); ok {
				label = method + " " + m.Template
			}
			mu.Lock()
			counts[fmt.Sprintf("%s %d", label, rec.status)]++
			mu.Unlock()
		})
	})
	ok := func(ctx context.Context, in *struct {
		ID int `path:"id"`
	}) error {
		return nil
	}
	app, err := geta.New(geta.Table{
		Root:   geta.Scope{byRoute},
		Routes: []geta.Entry{{Path: "/items/{id}", Route: geta.Route{Delete: geta.OpNoBody(http.StatusNoContent, ok, geta.Doc{})}}},
	})
	if err != nil {
		panic(err)
	}
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodDelete, "/items/1", nil),
		httptest.NewRequest(http.MethodDelete, "/items/2", nil),
		httptest.NewRequest(http.MethodDelete, "/items/x", nil),
		httptest.NewRequest(http.MethodGet, "/items/1", nil),
		httptest.NewRequest(http.MethodGet, "/wp-admin", nil),
	} {
		app.ServeHTTP(httptest.NewRecorder(), req)
	}
	for _, k := range slices.Sorted(maps.Keys(counts)) {
		fmt.Println(k, counts[k])
	}
	// Output:
	// DELETE /items/{id} 204 2
	// DELETE /items/{id} 400 1
	// unmatched 404 1
	// unmatched 405 1
}
