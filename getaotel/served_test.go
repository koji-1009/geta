package getaotel_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaotel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// What a span and a metric name is the request as served: after a root
// rewrite, under a ServeMux mount, and for a HEAD a GET serves.

// rewrite is a root middleware that turns a POST into the method its
// X-HTTP-Method-Override names, and moves the path to the one X-Move names.
var rewrite = geta.Use(func(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m := r.Header.Get("X-HTTP-Method-Override"); m != "" && r.Method == http.MethodPost {
			r = r.Clone(r.Context())
			r.Method = m
		}
		if to := r.Header.Get("X-Move"); to != "" {
			r = r.Clone(r.Context())
			r.URL.Path = to
		}
		next.ServeHTTP(w, r)
	})
})

// A root middleware inside this one that changes the method or the path:
// the span and the metrics name the operation served, not the one the
// request arrived for.
func TestARewrittenRequestIsNamedByWhatWasServed(t *testing.T) {
	tb := table(nil)
	tb.Root = geta.Scope{rewrite}
	tb.Routes = append(tb.Routes, geta.Entry{Path: "/items", Route: geta.Route{
		Post:   geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error { return nil }, geta.Doc{}),
		Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error { return nil }, geta.Doc{}),
	}})
	h := serve(t, tb)
	cases := []struct {
		header, value, method, path string
		status                      int
		name, route, served         string
	}{
		{"X-HTTP-Method-Override", "DELETE", "POST", "/items", 204, "DELETE /items", "/items", "DELETE"},
		{"X-Move", "/users/7", "GET", "/nowhere", 200, "GET /users/{id}", "/users/{id}", "GET"},
	}
	for _, c := range cases {
		if res := h.c.With(c.header, c.value).Do(c.method, c.path, nil); res.Status != c.status {
			t.Fatal(c.method, c.path, res.Status)
		}
		s := h.span()
		if s.Name() != c.name {
			t.Errorf("%s %s: span %q; want %q", c.method, c.path, s.Name(), c.name)
		}
		if v, _ := attr(s, "http.route"); v.AsString() != c.route {
			t.Errorf("%s %s: http.route %q", c.method, c.path, v.AsString())
		}
		if v, _ := attr(s, "http.request.method"); v.AsString() != c.served {
			t.Errorf("%s %s: http.request.method %q", c.method, c.path, v.AsString())
		}
	}
	sets := h.durations()
	if len(sets) != len(cases) {
		t.Fatalf("%d duration series; want %d", len(sets), len(cases))
	}
	for _, c := range cases {
		found := false
		for _, set := range sets {
			route, _ := set.Value("http.route")
			method, _ := set.Value("http.request.method")
			if route.AsString() == c.route && method.AsString() == c.served {
				found = true
			}
		}
		if !found {
			for _, set := range sets {
				t.Log(set.Encoded(attribute.DefaultEncoder()))
			}
			t.Errorf("no series for %s %s", c.served, c.route)
		}
	}
}

// A root middleware that turns a request into one no operation serves: the
// span and the metrics carry the method geta answered 405 for, as the access
// log does, not the one the request arrived with.
func TestARewriteToNoOperationIsNamedByTheMethodServed(t *testing.T) {
	tb := table(nil)
	tb.Root = geta.Scope{rewrite}
	tb.Routes = append(tb.Routes, geta.Entry{Path: "/items", Route: geta.Route{
		Post:   geta.OpNoBody(http.StatusCreated, func(context.Context, *empty) error { return nil }, geta.Doc{}),
		Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error { return nil }, geta.Doc{}),
	}})
	h := serve(t, tb)
	if res := h.c.With("X-HTTP-Method-Override", "PATCH").Post("/items", nil); res.Status != http.StatusMethodNotAllowed {
		t.Fatal(res.Status)
	}
	s := h.span()
	if s.Name() != "PATCH" {
		t.Errorf("span %q; want PATCH", s.Name())
	}
	if v, _ := attr(s, "http.request.method"); v.AsString() != "PATCH" {
		t.Errorf("http.request.method %q", v.AsString())
	}
	sets := h.durations()
	if len(sets) != 1 {
		t.Fatalf("%d duration series", len(sets))
	}
	if method, _ := sets[0].Value("http.request.method"); method.AsString() != "PATCH" {
		t.Errorf("metric method %q; want PATCH", method.AsString())
	}
	if v, ok := sets[0].Value("http.route"); ok {
		t.Errorf("metric route %q", v.AsString())
	}
}

// Mounted under a ServeMux pattern, the app's metrics still carry each
// template as http.route, and an unmatched request carries none: the outer
// pattern never becomes the route, so operations do not share one series.
func TestAMountedAppMeasuresByTemplate(t *testing.T) {
	ended := make(chan sdktrace.ReadOnlySpan, 16)
	reader := sdkmetric.NewManualReader()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(tracetest.NewSpanRecorder()), sdktrace.WithSpanProcessor(ending{ended}))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	tb := table(nil)
	tb.Root = geta.Scope{getaotel.Middleware(getaotel.WithTracerProvider(tp), getaotel.WithMeterProvider(mp),
		getaotel.WithPropagators(propagation.TraceContext{})), rewrite}
	app, err := geta.New(tb, geta.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", app))
	srv := httptest.NewTestServer(t, mux)
	client := srv.Client() // sets srv.URL
	h := &harness{t: t, ended: ended, reader: reader}
	cases := []struct {
		path, move string
		status     int
	}{
		{"/api/users/7", "", http.StatusOK},
		{"/api/nowhere", "/users/8", http.StatusOK},
		{"/api/missing", "", http.StatusNotFound},
		{"/api/ticks", "/users/9", http.StatusOK},
	}
	for _, c := range cases {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+c.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if c.move != "" {
			req.Header.Set("X-Move", c.move)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != c.status {
			t.Fatalf("%s: %d", c.path, res.StatusCode)
		}
		h.span()
	}
	sets := h.durations()
	byRoute := map[string]int{}
	for _, set := range sets {
		route, _ := set.Value("http.route")
		byRoute[route.AsString()]++
	}
	if len(sets) != 2 || byRoute["/users/{id}"] != 1 || byRoute[""] != 1 {
		for _, set := range sets {
			t.Log(set.Encoded(attribute.DefaultEncoder()))
		}
		t.Fatalf("series by route: %v; want one for /users/{id} and one with no route", byRoute)
	}
}

// A root middleware that moves a request off its operation: the span carries
// no route, not the one the request arrived for.
func TestARewriteToNoOperationHasNoRoute(t *testing.T) {
	tb := table(nil)
	tb.Root = geta.Scope{rewrite}
	h := serve(t, tb)
	if res := h.c.With("X-Move", "/nowhere").Get("/users/7"); res.Status != http.StatusNotFound {
		t.Fatal(res.Status)
	}
	s := h.span()
	if s.Name() != "GET" {
		t.Errorf("span %q; want GET", s.Name())
	}
	if v, ok := attr(s, "http.route"); ok {
		t.Errorf("http.route %q", v.AsString())
	}
	for _, set := range h.durations() {
		if v, ok := set.Value("http.route"); ok {
			t.Errorf("metric route %q", v.AsString())
		}
	}
}

// Mounted under a ServeMux pattern, a span carries the template of the
// operation served, and an unmatched request's span carries none: the outer
// pattern is never the span's route.
func TestAMountedAppTracesByTemplate(t *testing.T) {
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
	client := srv.Client() // sets srv.URL
	h := &harness{t: t, ended: ended}
	cases := []struct {
		method, path string
		status       int
		route        string
	}{
		{http.MethodGet, "/api/users/7", http.StatusOK, "/users/{id}"},
		{http.MethodGet, "/api/missing", http.StatusNotFound, ""},
		{http.MethodDelete, "/api/users/7", http.StatusMethodNotAllowed, ""},
	}
	for _, c := range cases {
		req, err := http.NewRequestWithContext(t.Context(), c.method, srv.URL+c.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != c.status {
			t.Fatalf("%s %s: %d", c.method, c.path, res.StatusCode)
		}
		s := h.span()
		v, ok := attr(s, "http.route")
		if c.route == "" && ok {
			t.Errorf("%s %s: http.route %q; want none", c.method, c.path, v.AsString())
		}
		if c.route != "" && v.AsString() != c.route {
			t.Errorf("%s %s: http.route %q; want %q", c.method, c.path, v.AsString(), c.route)
		}
	}
}

// A HEAD served by a GET operation is a HEAD on the span and the metric, as
// the access log has it, not merged into the GET series.
func TestAHeadIsRecordedAsHead(t *testing.T) {
	h := serve(t, table(nil))
	if res := h.c.Do(http.MethodHead, "/users/7", nil); res.Status != http.StatusOK {
		t.Fatal(res.Status)
	}
	s := h.span()
	if s.Name() != "HEAD /users/{id}" {
		t.Errorf("span %q; want HEAD /users/{id}", s.Name())
	}
	if v, _ := attr(s, "http.request.method"); v.AsString() != "HEAD" {
		t.Errorf("http.request.method %q", v.AsString())
	}
	if v, _ := attr(s, "http.route"); v.AsString() != "/users/{id}" {
		t.Errorf("http.route %q", v.AsString())
	}
	h.c.Get("/users/7")
	h.span()
	sets := h.durations()
	byMethod := map[string]int{}
	for _, set := range sets {
		method, _ := set.Value("http.request.method")
		byMethod[method.AsString()]++
	}
	if len(sets) != 2 || byMethod["HEAD"] != 1 || byMethod["GET"] != 1 {
		for _, set := range sets {
			t.Log(set.Encoded(attribute.DefaultEncoder()))
		}
		t.Fatalf("series by method: %v; want one HEAD and one GET", byMethod)
	}
}
