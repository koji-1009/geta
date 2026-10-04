package getaotel_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaotel"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Mounted under a ServeMux pattern, a span is named by the method and geta's
// template of the operation served, or by the method alone where none
// serves; the outer pattern is never in the name.
func TestAMountedAppNamesItsSpansByTemplate(t *testing.T) {
	ended := make(chan sdktrace.ReadOnlySpan, 16)
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(ending{ended}))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader()))
	tb := table(nil)
	tb.Root = geta.Scope{getaotel.Middleware(getaotel.WithTracerProvider(tp), getaotel.WithMeterProvider(mp),
		getaotel.WithPropagators(propagation.TraceContext{}))}
	app, err := geta.New(tb, geta.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", app))
	srv := httptest.NewTestServer(t, mux)
	client := srv.Client()
	h := &harness{t: t, ended: ended}
	for path, name := range map[string]string{"/api/users/7": "GET /users/{id}", "/api/missing": "GET"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if s := h.span(); s.Name() != name {
			t.Errorf("%s: span %q, want %q", path, s.Name(), name)
		}
	}
}
