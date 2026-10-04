package getaotel_test

import (
	"net/http"
	"testing"

	"go.opentelemetry.io/otel/attribute"
)

// OPTIONS on a served path is the OPTIONS operation geta serves there: the
// span and the metrics carry its template, as for any operation.
func TestOptionsIsNamedByItsTemplate(t *testing.T) {
	h := serve(t, table(nil))
	if res := h.c.Do(http.MethodOptions, "/users/7", nil); res.Status != http.StatusNoContent {
		t.Fatal(res.Status)
	}
	s := h.span()
	if s.Name() != "OPTIONS /users/{id}" {
		t.Fatalf("span %q", s.Name())
	}
	if v, _ := attr(s, "http.route"); v.AsString() != "/users/{id}" {
		t.Fatalf("http.route %q", v.AsString())
	}
	if status(t, s) != http.StatusNoContent {
		t.Fatal(status(t, s))
	}
	sets := h.durations()
	if len(sets) != 1 {
		t.Fatalf("%d duration series", len(sets))
	}
	route, _ := sets[0].Value("http.route")
	method, _ := sets[0].Value("http.request.method")
	if route.AsString() != "/users/{id}" || method.AsString() != http.MethodOptions {
		t.Fatal(sets[0].Encoded(attribute.DefaultEncoder()))
	}
}
