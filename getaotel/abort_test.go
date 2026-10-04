package getaotel_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaotel"
	"github.com/koji-1009/geta/getatest"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// abort aborts the response when X-Abort is "late" (after writing its status
// and part of the body, then panicking, which Recover turns into an abort)
// or "early" (before writing anything). "flushed" writes as "late" does and
// returns: the response is chunked, so only the abort breaks it.
var abort = geta.Use(func(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Abort") {
		case "late", "flushed":
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, "partial")
			http.NewResponseController(w).Flush()
			if r.Header.Get("X-Abort") == "flushed" {
				return
			}
			panic("late")
		case "early":
			panic(http.ErrAbortHandler)
		}
		next.ServeHTTP(w, r)
	})
})

// An aborted response is recorded as any other, by its route and the status
// it sent (500 when it sent none), and marked as an error with error.type
// "aborted" on the span and the duration metric; the client still reads a
// broken response.
func TestAnAbortedResponseIsRecorded(t *testing.T) {
	tb := table(nil)
	tb.Root = geta.Scope{geta.Recover(slog.New(slog.DiscardHandler)), abort}
	h := serve(t, tb)
	// The same response without the panic reads whole: the late case's broken
	// read is the abort's, not the framing's.
	req, _ := http.NewRequest("GET", h.c.URL()+"/users/7", nil)
	req.Header.Set("X-Abort", "flushed")
	res, err := h.c.HTTP().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || string(body) != "partial" || len(res.TransferEncoding) == 0 || res.TransferEncoding[0] != "chunked" {
		t.Fatalf("flushed: %q %v %v", body, res.TransferEncoding, err)
	}
	if s := h.span(); s.Status().Code != codes.Unset {
		t.Fatal(s.Status())
	}
	for _, c := range []struct {
		when   string
		status int64
	}{{"late", 200}, {"early", 500}} {
		req, _ := http.NewRequest("GET", h.c.URL()+"/users/7", nil)
		req.Header.Set("X-Abort", c.when)
		if res, err := h.c.HTTP().Do(req); err == nil {
			_, err = io.ReadAll(res.Body)
			res.Body.Close()
			if err == nil {
				t.Fatalf("%s: the client read a whole response", c.when)
			}
		}
		s := h.span()
		if s.Name() != "GET /users/{id}" || s.Status().Code != codes.Error {
			t.Errorf("%s: %q %v", c.when, s.Name(), s.Status())
		}
		if v, _ := attr(s, "http.route"); v.AsString() != "/users/{id}" {
			t.Errorf("%s: route %q", c.when, v.AsString())
		}
		if v, _ := attr(s, "error.type"); v.AsString() != "aborted" {
			t.Errorf("%s: error.type %q", c.when, v.AsString())
		}
		if got := status(t, s); got != c.status {
			t.Errorf("%s: status %d; want %d", c.when, got, c.status)
		}
	}
	found := map[int64]bool{}
	for _, set := range h.durations() {
		route, _ := set.Value("http.route")
		errType, _ := set.Value("error.type")
		code, _ := set.Value("http.response.status_code")
		if route.AsString() == "/users/{id}" && errType.AsString() == "aborted" {
			found[code.AsInt64()] = true
		}
	}
	if !found[200] || !found[500] {
		t.Fatalf("no duration point for the aborted responses: %v", h.durations())
	}
	// A request that is not aborted is not marked.
	if res := h.c.Get("/users/7"); res.Status != 200 {
		t.Fatal(res.Status)
	}
	if s := h.span(); s.Status().Code != codes.Unset {
		t.Fatal(s.Status())
	}
}

// panicky panics with the value X-Panic names: after writing its status
// and part of the body when X-Late is set, before writing anything otherwise.
var panicky = geta.Use(func(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := r.Header.Get("X-Panic")
		if v == "" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-Late") != "" {
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, "partial")
		}
		panicHere(v)
	})
})

// panicHere panics with v; the span's exception names it in its stack.
func panicHere(v string) { panic(v) }

// A panic other than http.ErrAbortHandler, with no Recover inside the
// middleware, is recorded before it goes on: the status sent, or 500 when
// none was, the span an error with error.type "panic" and an exception event
// whose stack is the panic's, and the duration metric carrying both. It goes
// on with its own value, for the server (or a Recover outside) to handle.
func TestAPanicWithoutRecoverIsRecordedAndGoesOn(t *testing.T) {
	h := &harness{t: t, ended: make(chan sdktrace.ReadOnlySpan, 16), rec: tracetest.NewSpanRecorder(), reader: sdkmetric.NewManualReader()}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(h.rec), sdktrace.WithSpanProcessor(ending{h.ended}))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(h.reader))
	tb := table(nil)
	tb.Root = geta.Scope{getaotel.Middleware(getaotel.WithTracerProvider(tp), getaotel.WithMeterProvider(mp)), panicky}
	app, err := geta.New(tb, geta.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		late   bool
		status int64
	}{{false, 500}, {true, 200}} {
		req := httptest.NewRequest(http.MethodGet, "/users/7", nil)
		req.Header.Set("X-Panic", "boom")
		if c.late {
			req.Header.Set("X-Late", "1")
		}
		rec := httptest.NewRecorder()
		got := func() (p any) {
			defer func() { p = recover() }()
			app.ServeHTTP(rec, req)
			return nil
		}()
		if got != "boom" {
			t.Fatalf("late %v: the panic went on as %#v", c.late, got)
		}
		// The middleware writes nothing of its own: no 500 reaches the
		// connection, which the server closes.
		if want := map[bool]string{true: "partial"}[c.late]; rec.Body.String() != want {
			t.Fatalf("late %v: %d %q", c.late, rec.Code, rec.Body)
		}
		s := h.span()
		if s.Name() != "GET /users/{id}" || s.Status().Code != codes.Error {
			t.Errorf("late %v: %q %v", c.late, s.Name(), s.Status())
		}
		if v, _ := attr(s, "error.type"); v.AsString() != "panic" {
			t.Errorf("late %v: error.type %q", c.late, v.AsString())
		}
		if got := status(t, s); got != c.status {
			t.Errorf("late %v: status %d; want %d", c.late, got, c.status)
		}
		var stack string
		for _, e := range s.Events() {
			for _, a := range e.Attributes {
				if a.Key == "exception.stacktrace" {
					stack = a.Value.AsString()
				}
			}
		}
		if !strings.Contains(stack, "getaotel_test.panicHere") {
			t.Errorf("late %v: the exception's stack is not the panic's:\n%s", c.late, stack)
		}
	}
	found := map[int64]bool{}
	for _, set := range h.durations() {
		route, _ := set.Value("http.route")
		errType, _ := set.Value("error.type")
		code, _ := set.Value("http.response.status_code")
		if route.AsString() == "/users/{id}" && errType.AsString() == "panic" {
			found[code.AsInt64()] = true
		}
	}
	if !found[200] || !found[500] {
		t.Fatalf("no duration point for the panics: %v", h.durations())
	}
	// A request that does not panic is not marked.
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/users/7", nil))
	if s := h.span(); rec.Code != 200 || s.Status().Code != codes.Unset {
		t.Fatal(rec.Code, s.Status())
	}
}

// After a rewrite to a method outside the standard set, the span records
// _OTHER and the original method, and is named HTTP.
func TestARewriteToAnUnknownMethodIsOther(t *testing.T) {
	tb := table(nil)
	tb.Root = geta.Scope{rewrite}
	h := serve(t, tb)
	if res := h.c.With("X-HTTP-Method-Override", "PURGE").Post("/users/7", nil); res.Status != 405 {
		t.Fatal(res.Status)
	}
	s := h.span()
	if s.Name() != "HTTP" {
		t.Fatal(s.Name())
	}
	if v, _ := attr(s, "http.request.method"); v.AsString() != "_OTHER" {
		t.Fatal(v.AsString())
	}
	if v, _ := attr(s, "http.request.method_original"); v.AsString() != "PURGE" {
		t.Fatal(v.AsString())
	}
}

// With no option, the middleware uses the OpenTelemetry globals: the tracer
// provider, the meter provider, and the propagator.
func TestWithNoOptionTheGlobalsAreUsed(t *testing.T) {
	tp0, mp0, pr0 := otel.GetTracerProvider(), otel.GetMeterProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(tp0)
		otel.SetMeterProvider(mp0)
		otel.SetTextMapPropagator(pr0)
	})
	rec := tracetest.NewSpanRecorder()
	ended := make(chan sdktrace.ReadOnlySpan, 4)
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec), sdktrace.WithSpanProcessor(ending{ended})))
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	tb := table(nil)
	tb.Root = geta.Scope{getaotel.Middleware()}
	c := getatest.New(t, tb)
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	if res := c.With("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01").Get("/users/7"); res.Status != 200 {
		t.Fatal(res.Status)
	}
	h := &harness{t: t, ended: ended, reader: reader}
	s := h.span()
	if s.Name() != "GET /users/{id}" || s.SpanContext().TraceID().String() != traceID {
		t.Fatal(s.Name(), s.SpanContext().TraceID())
	}
	if len(h.durations()) != 1 {
		t.Fatal(h.durations())
	}
}
