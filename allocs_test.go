//go:build !race

package geta_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A sealed body is read in one pass, in 41 allocations a request whether
// each discriminator comes first or last, as BenchmarkPostSealed and
// BenchmarkPostSealedLate report; more is a regression. The race detector
// makes sync.Pool drop what it holds, so this runs without it.
func TestPostSealedAllocations(t *testing.T) {
	h := benchSealedApp(t)
	for name, body := range map[string]string{
		"first": `{"id":"d1","main":{"kind":"square","side":2,"inner":{"kind":"circle","radius":1.5}},` +
			`"others":[{"kind":"circle","radius":3},{"kind":"square","side":4}]}`,
		"last": `{"id":"d1","main":{"side":2,"inner":{"radius":1.5,"kind":"circle"},"kind":"square"},` +
			`"others":[{"radius":3,"kind":"circle"},{"side":4,"kind":"square"}]}`,
	} {
		allocs := testing.AllocsPerRun(100, func() {
			req := httptest.NewRequest(http.MethodPost, "/drawings", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatal(rec.Code, rec.Body)
			}
		})
		if allocs > 41 {
			t.Errorf("discriminator %s: %v allocations a request, want at most 41", name, allocs)
		}
	}
}
