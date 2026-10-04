package getaotel_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaotel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// The exception event a panic is recorded with carries the panic's value.
func TestMWPanicExceptionCarriesTheValue(t *testing.T) {
	ended := make(chan sdktrace.ReadOnlySpan, 4)
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(ending{ended}))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader()))
	tb := table(nil)
	tb.Root = geta.Scope{getaotel.Middleware(getaotel.WithTracerProvider(tp), getaotel.WithMeterProvider(mp)), panicky}
	app, err := geta.New(tb, geta.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/users/7", nil)
	req.Header.Set("X-Panic", "boom")
	func() {
		defer func() { recover() }()
		app.ServeHTTP(httptest.NewRecorder(), req)
	}()
	s := (&harness{t: t, ended: ended}).span()
	var message string
	for _, e := range s.Events() {
		if e.Name != "exception" {
			continue
		}
		for _, a := range e.Attributes {
			if a.Key == "exception.message" {
				message = a.Value.AsString()
			}
		}
	}
	if message != "panic: boom" {
		t.Fatalf("exception.message %q", message)
	}
}
