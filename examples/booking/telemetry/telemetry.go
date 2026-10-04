// Package telemetry keeps the service's spans and metrics in memory and
// shows them on a debug handler served on its own listener, outside the
// route table. A deployment exports them instead (OTLP for spans,
// Prometheus for metrics); getaotel takes any provider.
package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaotel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Span is what the debug handler shows of one ended span.
type Span struct {
	Name    string `json:"name"`
	TraceID string `json:"traceId"`
	Parent  string `json:"parent,omitempty"`
	Route   string `json:"route,omitempty"`
	Status  int64  `json:"status"`
	Error   bool   `json:"error"`
}

// Telemetry holds the providers and what they recorded.
type Telemetry struct {
	Tracer *sdktrace.TracerProvider
	Meter  *sdkmetric.MeterProvider
	reader *sdkmetric.ManualReader

	mu    sync.Mutex
	spans []Span
	ended chan Span
}

// keep bounds the spans held.
const keep = 1000

// New builds in-memory providers.
func New() *Telemetry {
	t := &Telemetry{reader: sdkmetric.NewManualReader()}
	t.Tracer = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(processor{t}))
	t.Meter = sdkmetric.NewMeterProvider(sdkmetric.WithReader(t.reader))
	return t
}

// Middleware is getaotel's middleware on these providers, continuing an
// incoming W3C traceparent.
func (t *Telemetry) Middleware() geta.Middleware {
	return getaotel.Middleware(getaotel.WithTracerProvider(t.Tracer), getaotel.WithMeterProvider(t.Meter),
		getaotel.WithPropagators(propagation.TraceContext{}))
}

// Watch returns a channel that receives every span ended from now on, for
// a test that waits for one.
func (t *Telemetry) Watch() <-chan Span {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ended = make(chan Span, 64)
	return t.ended
}

// Spans returns the spans ended so far, oldest first, and forgets them.
func (t *Telemetry) Spans() []Span {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.spans
	t.spans = nil
	return s
}

// Routes returns the http.route values of the request duration metric, with
// their request counts.
func (t *Telemetry) Routes(ctx context.Context) (map[string]uint64, error) {
	var rm metricdata.ResourceMetrics
	if err := t.reader.Collect(ctx, &rm); err != nil {
		return nil, err
	}
	out := map[string]uint64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			h, ok := m.Data.(metricdata.Histogram[float64])
			if m.Name != "http.server.request.duration" || !ok {
				continue
			}
			for _, p := range h.DataPoints {
				route, _ := p.Attributes.Value(attribute.Key("http.route"))
				out[route.AsString()] += p.Count
			}
		}
	}
	return out, nil
}

// Handler serves GET /debug/spans and GET /debug/routes.
func (t *Telemetry) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/spans", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, t.Spans())
	})
	mux.HandleFunc("GET /debug/routes", func(w http.ResponseWriter, r *http.Request) {
		routes, err := t.Routes(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, routes)
	})
	return mux
}

// Shutdown flushes and stops the providers.
func (t *Telemetry) Shutdown(ctx context.Context) {
	t.Tracer.Shutdown(ctx)
	t.Meter.Shutdown(ctx)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// processor records each ended span.
type processor struct{ t *Telemetry }

func (p processor) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

func (p processor) OnEnd(s sdktrace.ReadOnlySpan) {
	span := Span{Name: s.Name(), TraceID: s.SpanContext().TraceID().String(), Error: s.Status().Code == codes.Error}
	if s.Parent().IsValid() {
		span.Parent = s.Parent().SpanID().String()
	}
	for _, a := range s.Attributes() {
		switch a.Key {
		case "http.route":
			span.Route = a.Value.AsString()
		case "http.response.status_code":
			span.Status = a.Value.AsInt64()
		}
	}
	p.t.mu.Lock()
	defer p.t.mu.Unlock()
	if len(p.t.spans) == keep {
		p.t.spans = p.t.spans[1:]
	}
	p.t.spans = append(p.t.spans, span)
	if p.t.ended != nil {
		select {
		case p.t.ended <- span:
		default:
		}
	}
}

func (p processor) Shutdown(context.Context) error   { return nil }
func (p processor) ForceFlush(context.Context) error { return nil }
