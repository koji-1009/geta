// Package getaotel traces and measures a geta application with
// OpenTelemetry.
//
// The server span, its HTTP semantic-convention attributes, and the HTTP
// server metrics (http.server.request.duration,
// http.server.request.body.size, http.server.response.body.size) come from
// go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp. getaotel
// adds the geta side:
//
//   - the span name and http.route come from [geta.Matched], never the raw
//     path, so both stay low-cardinality;
//   - what a client chooses is not recorded as fact: no url.path (the raw
//     path), server.address and server.port only from [WithServerName] (not
//     the Host header), and client.address from the connection's peer (not
//     X-Forwarded-For);
//   - QUERY is recorded as QUERY, where otelhttp would write _OTHER;
//   - a protocol switch is recorded as 101;
//   - the middleware is ordered at [geta.OrderObserve].
//
// Put [Middleware] in the root scope so 404 and 405 are traced too:
//
//	table.Root = geta.Scope{getaotel.Middleware(), geta.AccessLog(log), geta.Recover(log)}
//
// With no option it uses the OpenTelemetry globals, which do nothing until
// the application sets them. In particular, an incoming traceparent is
// ignored until a global propagator is set. A typical setup exports spans
// over OTLP and serves metrics to Prometheus (otlptracehttp, the otel
// prometheus exporter, and promhttp; none is a dependency of getaotel):
//
//	spans, err := otlptracehttp.New(ctx) // OTEL_EXPORTER_OTLP_ENDPOINT
//	if err != nil { ... }
//	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(spans))
//	defer tp.Shutdown(context.Background())
//
//	prom, err := prometheus.New() // registers with prometheus.DefaultRegisterer
//	if err != nil { ... }
//	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(prom))
//	defer mp.Shutdown(context.Background())
//
//	otel.SetTracerProvider(tp)
//	otel.SetMeterProvider(mp)
//	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
//		propagation.TraceContext{}, propagation.Baggage{}))
//
//	table.Root = geta.Scope{getaotel.Middleware()}
//	// serve promhttp.Handler() on a separate listener, outside the table
//
// Or pass the providers explicitly instead of setting the globals:
//
//	getaotel.Middleware(getaotel.WithTracerProvider(tp), getaotel.WithMeterProvider(mp),
//		getaotel.WithPropagators(propagation.TraceContext{}))
package getaotel

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"

	"github.com/koji-1009/geta"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// Option configures [Middleware].
type Option func(*config)

type config struct {
	tp     trace.TracerProvider
	mp     metric.MeterProvider
	pr     propagation.TextMapPropagator
	server string
}

// WithServerName sets server.address and server.port on the span and the
// metrics, such as "api.example.com" or "api.example.com:8443". Without it
// they carry no server name: the request's Host is the client's to choose,
// and each distinct value would start a new metric series.
func WithServerName(name string) Option {
	return func(c *config) { c.server = name }
}

// WithTracerProvider sets where spans go. The default is the global
// provider, otel.GetTracerProvider.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(c *config) { c.tp = tp }
}

// WithMeterProvider sets where metrics go. The default is the global
// provider, otel.GetMeterProvider.
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(c *config) { c.mp = mp }
}

// WithPropagators sets how the incoming trace context is read from the
// request headers. The default is the global propagator,
// otel.GetTextMapPropagator.
func WithPropagators(p propagation.TextMapPropagator) Option {
	return func(c *config) { c.pr = p }
}

// Middleware traces each request with a server span and records the HTTP
// server metrics, ordered at [geta.OrderObserve].
//
// For a request served by an operation, the span is named "METHOD
// /template" and the span and metrics carry http.route = template. A
// request no operation served (404, 405) is named by its method alone and
// has no route. A method outside the standard set is named "HTTP". Method
// and route are taken after the chain runs, so they reflect any root
// middleware inside this one that rewrites them; a HEAD served by a GET
// operation stays HEAD, as in [geta.AccessLog]. The route is the template
// even when the app is mounted under a ServeMux pattern.
//
// A 5xx marks the span as an error. A response aborted with
// http.ErrAbortHandler is recorded with the status it sent (500 if none),
// marked as an error with error.type "aborted", and stays aborted. Any
// other panic is recorded the same way with error.type "panic" and an
// exception event carrying its value and stack, then re-panics with the
// same value.
//
// Event streams and protocol switches pass through: the wrapped writer
// still flushes and hijacks.
func Middleware(opts ...Option) geta.Middleware {
	var c config
	for _, o := range opts {
		o(&c)
	}
	// otelhttp treats a nil provider or propagator as the global one. The
	// meter provider is wrapped so the metrics record QUERY.
	mp := c.mp
	if mp == nil {
		mp = otel.GetMeterProvider()
	}
	instrument := otelhttp.NewMiddleware("", otelhttp.WithTracerProvider(c.tp), otelhttp.WithMeterProvider(queryMeters{mp}),
		otelhttp.WithPropagators(c.pr), otelhttp.WithSpanNameFormatter(spanName))
	return geta.Ordered(geta.OrderObserve, func(next http.Handler) http.Handler {
		inner := instrument(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s := switched(w)
			// The chain sees the request as it arrived.
			r.Pattern, r.URL, r.Host = s.pattern, s.url, s.host
			peer(r)
			abort, p := serve(next, w, r)
			served(r)
			// otelhttp reads the metrics' server.address from r.Host.
			r.Host = c.server
			switch {
			case p != nil:
				panicked(w, r, s, p)
			case abort:
				aborted(w, r, s)
			case s.hijacked:
				// otelhttp did not see the 101 written on the hijacked
				// connection; tell it. switcher drops the call.
				w.WriteHeader(http.StatusSwitchingProtocols)
			}
		}))
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s := &switcher{ResponseWriter: w, pattern: r.Pattern, url: r.URL, host: r.Host}
			// served leaves the method here for queryMeters.
			r = r.WithContext(context.WithValue(r.Context(), servedKey{}, new(servedMethod)))
			// otelhttp records at span start what it is given, and what it
			// records cannot be removed later. It is given no pattern (served
			// records the operation's route instead), no path (url.path would
			// carry the raw one), and the server name as the Host. r is
			// already a copy.
			r.Pattern = ""
			u := *r.URL
			u.Path, u.RawPath = "", ""
			r.URL = &u
			r.Host = c.server
			inner.ServeHTTP(s, r)
			switch {
			case s.panic != nil:
				panic(s.panic.value)
			case s.aborted:
				panic(http.ErrAbortHandler)
			}
		})
	})
}

// serve runs next and reports whether it panicked with http.ErrAbortHandler
// (abort) or another value (p). The panic is recovered so otelhttp, which
// records metrics only when its handler returns, still records the request.
// The exception event is recorded here, while the panicking frames are
// still on the stack. Middleware re-panics after otelhttp returns.
func serve(next http.Handler, w http.ResponseWriter, r *http.Request) (abort bool, p *panicValue) {
	defer func() {
		v := recover()
		switch {
		case v == nil:
		case v == http.ErrAbortHandler:
			abort = true
		default:
			p = &panicValue{v}
			trace.SpanFromContext(r.Context()).RecordError(fmt.Errorf("panic: %v", v), trace.WithStackTrace(true))
		}
	}()
	next.ServeHTTP(w, r)
	return false, nil
}

// panicValue is what a handler panicked with.
type panicValue struct{ value any }

// panicked marks the span and metrics with error.type "panic" before
// otelhttp records them, and records a 500 if no status was sent. Nothing
// is written to the client.
func panicked(w http.ResponseWriter, r *http.Request, s *switcher, p *panicValue) {
	errType := semconv.ErrorTypeKey.String("panic")
	span := trace.SpanFromContext(r.Context())
	span.SetAttributes(errType)
	span.SetStatus(codes.Error, "the handler panicked")
	if l, ok := otelhttp.LabelerFromContext(r.Context()); ok {
		l.Add(errType)
	}
	s.panic = p
	if !s.wrote {
		// Tell otelhttp the status; switcher drops the call.
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// aborted marks the span and metrics with error.type "aborted" before
// otelhttp records them, and records a 500 if no status was sent.
func aborted(w http.ResponseWriter, r *http.Request, s *switcher) {
	errType := semconv.ErrorTypeKey.String("aborted")
	span := trace.SpanFromContext(r.Context())
	span.SetAttributes(errType)
	span.SetStatus(codes.Error, "the response was aborted")
	if l, ok := otelhttp.LabelerFromContext(r.Context()); ok {
		l.Add(errType)
	}
	s.aborted = true
	if !s.wrote {
		// Tell otelhttp the status; switcher drops the call.
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// standard are the methods the HTTP semantic conventions know, QUERY
// included since semconv 1.38. Others are named "HTTP" to bound span names.
var standard = map[string]bool{
	http.MethodConnect: true, http.MethodDelete: true, http.MethodGet: true, http.MethodHead: true,
	http.MethodOptions: true, http.MethodPatch: true, http.MethodPost: true, http.MethodPut: true,
	http.MethodTrace: true, geta.MethodQuery: true,
}

// served records the method and route the request was served as, after the
// chain has run. r is otelhttp's copy, whose Method and Pattern it reads for
// the metrics after this handler returns. otelhttp's route from Pattern
// overrides the Labeler's, so Pattern is set to the template; otherwise a
// ServeMux mount pattern would put every operation in one series.
func served(r *http.Request) {
	span := trace.SpanFromContext(r.Context())
	method := r.Method
	// A HEAD served by a GET operation stays HEAD.
	_, read := geta.Observe(r.Context())
	if m, _, _ := read(); m != "" {
		method = m
	}
	if mt, ok := geta.Matched(r.Context()); ok {
		route := semconv.HTTPRoute(mt.Template)
		span.SetAttributes(route)
		if l, ok := otelhttp.LabelerFromContext(r.Context()); ok {
			l.Add(route)
		}
		r.Pattern = mt.Method + " " + mt.Template
	} else {
		r.Pattern = ""
	}
	// otelhttp records QUERY as _OTHER, overriding any Labeler attribute.
	// Leave the method for queryMeters to restore in the metrics.
	if sm, ok := r.Context().Value(servedKey{}).(*servedMethod); ok && method == geta.MethodQuery {
		sm.method = method
	}
	if r.Method != method || method == geta.MethodQuery {
		r.Method = method
		if standard[method] {
			span.SetAttributes(semconv.HTTPRequestMethodKey.String(method))
		} else {
			span.SetAttributes(semconv.HTTPRequestMethodKey.String("_OTHER"), semconv.HTTPRequestMethodOriginal(method))
		}
	}
	span.SetName(spanName("", r))
}

// peer sets client.address to the connection's peer where otelhttp took it
// from X-Forwarded-For, which any client can send. An application behind a
// proxy it trusts can set client.address itself.
func peer(r *http.Request) {
	if r.Header.Get("X-Forwarded-For") == "" {
		return
	}
	addr := r.RemoteAddr
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}
	trace.SpanFromContext(r.Context()).SetAttributes(semconv.ClientAddress(addr))
}

// spanName is "METHOD /template" for a request served by an operation, and
// the method alone otherwise. The raw path is never used.
func spanName(_ string, r *http.Request) string {
	method := r.Method
	mt, matched := geta.Matched(r.Context())
	if !standard[method] {
		method = "HTTP"
	}
	if matched {
		return method + " " + mt.Template
	}
	return method
}

// switcher sits under otelhttp's writer. It notes a hijack, after which a
// WriteHeader that only informs otelhttp of the status is dropped, and it
// carries the request's pattern past otelhttp. It implements Unwrap for
// http.ResponseController.
type switcher struct {
	http.ResponseWriter
	hijacked bool
	aborted  bool        // the response was aborted: nothing more reaches it
	panic    *panicValue // the handler panicked: nothing more reaches it
	wrote    bool        // the status went out
	// The request's pattern, URL, and Host as they arrived.
	pattern string
	url     *url.URL
	host    string
}

func (s *switcher) WriteHeader(code int) {
	if s.hijacked || s.aborted || s.panic != nil {
		return
	}
	if code >= 200 {
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *switcher) Write(b []byte) (int, error) {
	if s.hijacked {
		return 0, http.ErrHijacked
	}
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

func (s *switcher) Flush() { s.FlushError() }

func (s *switcher) FlushError() error {
	s.wrote = true
	return http.NewResponseController(s.ResponseWriter).Flush()
}

func (s *switcher) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, rw, err := http.NewResponseController(s.ResponseWriter).Hijack()
	if err == nil {
		s.hijacked = true
	}
	return c, rw, err
}

func (s *switcher) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// servedMethod carries, in the request's context, a method otelhttp would
// record as _OTHER (QUERY).
type servedMethod struct{ method string }

type servedKey struct{}

// queryMeters wraps the application's meter provider so its histograms
// replace otelhttp's _OTHER with the served method (QUERY). otelhttp writes
// http.request.method last, so only the recording can correct it.
type queryMeters struct{ metric.MeterProvider }

func (p queryMeters) Meter(name string, opts ...metric.MeterOption) metric.Meter {
	return queryMeter{p.MeterProvider.Meter(name, opts...)}
}

type queryMeter struct{ metric.Meter }

func (m queryMeter) Float64Histogram(name string, opts ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	h, err := m.Meter.Float64Histogram(name, opts...)
	if err != nil {
		return h, err
	}
	return float64Histogram{h}, nil
}

func (m queryMeter) Int64Histogram(name string, opts ...metric.Int64HistogramOption) (metric.Int64Histogram, error) {
	h, err := m.Meter.Int64Histogram(name, opts...)
	if err != nil {
		return h, err
	}
	return int64Histogram{h}, nil
}

type float64Histogram struct{ metric.Float64Histogram }

func (h float64Histogram) Record(ctx context.Context, v float64, opts ...metric.RecordOption) {
	h.Float64Histogram.Record(ctx, v, servedOptions(ctx, opts)...)
}

type int64Histogram struct{ metric.Int64Histogram }

func (h int64Histogram) Record(ctx context.Context, v int64, opts ...metric.RecordOption) {
	h.Int64Histogram.Record(ctx, v, servedOptions(ctx, opts)...)
}

// servedOptions returns opts with http.request.method set to the method
// served left in ctx, or opts unchanged if there is none.
func servedOptions(ctx context.Context, opts []metric.RecordOption) []metric.RecordOption {
	sm, _ := ctx.Value(servedKey{}).(*servedMethod)
	if sm == nil || sm.method == "" {
		return opts
	}
	set := metric.NewRecordConfig(opts).Attributes()
	kvs := set.ToSlice()
	for i, kv := range kvs {
		if kv.Key == semconv.HTTPRequestMethodKey {
			kvs[i] = semconv.HTTPRequestMethodKey.String(sm.method)
		}
	}
	return []metric.RecordOption{metric.WithAttributeSet(attribute.NewSet(kvs...))}
}

// switched unwraps otelhttp's writer down to the switcher. It panics if a
// writer on the way does not unwrap, which would show on the first request.
func switched(w http.ResponseWriter) *switcher {
	for {
		if s, ok := w.(*switcher); ok {
			return s
		}
		w = w.(interface{ Unwrap() http.ResponseWriter }).Unwrap()
	}
}
