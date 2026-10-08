package getaotel_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaotel"
	"github.com/koji-1009/geta/getatest"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type empty struct{}

type userIn struct {
	ID string `path:"id"`
}

type user struct {
	ID string `json:"id"`
}

var errGone = errors.New("gone")

func getUser(_ context.Context, in *userIn) (*user, error) {
	switch in.ID {
	case "gone":
		return nil, errGone
	case "boom":
		return nil, errors.New("boom")
	}
	return &user{ID: in.ID}, nil
}

type tick struct {
	N int `json:"n"`
}

// ticks yields two events, holding the second until gate closes, so the
// first can only arrive if it was flushed.
func ticks(gate <-chan struct{}) func(context.Context, *empty) (*geta.Stream[tick], error) {
	return func(ctx context.Context, _ *empty) (*geta.Stream[tick], error) {
		return &geta.Stream[tick]{Events: func(yield func(tick) bool) {
			if !yield(tick{1}) {
				return
			}
			select {
			case <-gate:
			case <-ctx.Done():
				return
			}
			yield(tick{2})
		}}, nil
	}
}

func echo(context.Context, *empty) (*geta.Upgrade, error) {
	return &geta.Upgrade{
		Protocol: "echo",
		Serve: func(conn net.Conn, rw *bufio.ReadWriter) {
			line, _ := rw.ReadString('\n')
			rw.WriteString("echo: " + line)
			rw.Flush()
		},
	}, nil
}

// table is the app under test; gate releases the stream's second event.
func table(gate <-chan struct{}) geta.Table {
	return geta.Table{Routes: []geta.Entry{
		{Path: "/users/{id}", Route: geta.Route{Get: geta.Op(http.StatusOK, getUser, geta.Doc{
			Failures: []geta.Failure{geta.On(errGone, http.StatusNotFound, "no such user")},
		})}},
		{Path: "/ticks", Route: geta.Route{Get: geta.Op(http.StatusOK, ticks(gate), geta.Doc{})}},
		{Path: "/ws", Route: geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, echo, geta.Doc{})}},
	}}
}

// ending signals each span as it ends, so a test can wait for the server to
// finish a request it has already read the response of.
type ending struct{ ch chan sdktrace.ReadOnlySpan }

func (e ending) OnStart(context.Context, sdktrace.ReadWriteSpan) {}
func (e ending) OnEnd(s sdktrace.ReadOnlySpan)                   { e.ch <- s }
func (e ending) Shutdown(context.Context) error                  { return nil }
func (e ending) ForceFlush(context.Context) error                { return nil }

type harness struct {
	t      *testing.T
	c      *getatest.Client
	ended  chan sdktrace.ReadOnlySpan
	rec    *tracetest.SpanRecorder
	reader *sdkmetric.ManualReader
}

func serve(t *testing.T, tb geta.Table, opts ...geta.Option) *harness {
	t.Helper()
	return serveWith(t, tb, nil, opts...)
}

// serveWith is serve with options for the middleware.
func serveWith(t *testing.T, tb geta.Table, otelOpts []getaotel.Option, opts ...geta.Option) *harness {
	t.Helper()
	h := &harness{t: t, ended: make(chan sdktrace.ReadOnlySpan, 16), rec: tracetest.NewSpanRecorder(), reader: sdkmetric.NewManualReader()}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(h.rec), sdktrace.WithSpanProcessor(ending{h.ended}))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(h.reader))
	otelOpts = append([]getaotel.Option{getaotel.WithTracerProvider(tp), getaotel.WithMeterProvider(mp),
		getaotel.WithPropagators(propagation.TraceContext{})}, otelOpts...)
	tb.Root = append(geta.Scope{getaotel.Middleware(otelOpts...)}, tb.Root...)
	h.c = getatest.New(t, tb, append([]geta.Option{geta.WithLogger(slog.New(slog.DiscardHandler))}, opts...)...)
	return h
}

type searchIn struct {
	Body struct {
		Term string `json:"term"`
	} `body:"json"`
}

// QUERY is a method the HTTP semantic conventions name a span by: its span is
// QUERY /template, its method QUERY. otelhttp's metrics, which this package
// does not write, still record it as _OTHER.
func TestAQueryIsNamedByItsTemplate(t *testing.T) {
	search := func(_ context.Context, in *searchIn) (*user, error) { return &user{ID: in.Body.Term}, nil }
	h := serve(t, geta.Table{Routes: []geta.Entry{
		{Path: "/search", Route: geta.Route{Query: geta.Op(http.StatusOK, search, geta.Doc{})}},
	}}, geta.WithOpenAPI(geta.OpenAPI32))
	if res := h.c.Query("/search", map[string]string{"term": "x"}); res.Status != 200 {
		t.Fatal(res.Status, res.Text())
	}
	s := h.span()
	if s.Name() != "QUERY /search" {
		t.Fatal(s.Name())
	}
	if v, _ := attr(s, "http.request.method"); v.AsString() != "QUERY" {
		t.Fatalf("http.request.method %q", v.AsString())
	}
	sets := h.durations()
	if len(sets) != 1 {
		t.Fatalf("%d duration series", len(sets))
	}
	route, _ := sets[0].Value("http.route")
	method, _ := sets[0].Value("http.request.method")
	if route.AsString() != "/search" || method.AsString() != "QUERY" {
		t.Fatal(sets[0].Encoded(attribute.DefaultEncoder()))
	}
	// The body-size histograms say QUERY too, and a GET beside it keeps its
	// own method: the correction is the QUERY request's alone.
	if res := h.c.Get("/search"); res.Status != http.StatusMethodNotAllowed {
		t.Fatal(res.Status)
	}
	h.span()
	var rm metricdata.ResourceMetrics
	if err := h.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			var sets []attribute.Set
			switch d := m.Data.(type) {
			case metricdata.Histogram[int64]:
				for _, p := range d.DataPoints {
					sets = append(sets, p.Attributes)
				}
			case metricdata.Histogram[float64]:
				for _, p := range d.DataPoints {
					sets = append(sets, p.Attributes)
				}
			}
			for _, s := range sets {
				v, _ := s.Value("http.request.method")
				seen[m.Name+" "+v.AsString()] = true
			}
		}
	}
	for _, want := range []string{
		"http.server.request.duration QUERY", "http.server.request.body.size QUERY", "http.server.response.body.size QUERY",
		"http.server.request.duration GET",
	} {
		if !seen[want] {
			t.Errorf("no series %q among %v", want, seen)
		}
	}
	for k := range seen {
		if strings.HasSuffix(k, "_OTHER") {
			t.Errorf("series %q", k)
		}
	}
}

// span waits for the server's span of the request just made.
func (h *harness) span() sdktrace.ReadOnlySpan {
	h.t.Helper()
	select {
	case s := <-h.ended:
		return s
	case <-time.After(5 * time.Second):
		h.t.Fatal("no span ended")
		return nil
	}
}

func attr(s sdktrace.ReadOnlySpan, key attribute.Key) (attribute.Value, bool) {
	for _, kv := range s.Attributes() {
		if kv.Key == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func status(t *testing.T, s sdktrace.ReadOnlySpan) int64 {
	t.Helper()
	v, ok := attr(s, "http.response.status_code")
	if !ok {
		t.Fatalf("span %q has no http.response.status_code: %v", s.Name(), s.Attributes())
	}
	return v.AsInt64()
}

// durations returns the attribute sets of http.server.request.duration.
func (h *harness) durations() []attribute.Set {
	h.t.Helper()
	var rm metricdata.ResourceMetrics
	if err := h.reader.Collect(context.Background(), &rm); err != nil {
		h.t.Fatal(err)
	}
	var sets []attribute.Set
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "http.server.request.duration" {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				h.t.Fatalf("http.server.request.duration is %T", m.Data)
			}
			for _, dp := range hist.DataPoints {
				sets = append(sets, dp.Attributes)
			}
		}
	}
	return sets
}

func TestTheMiddlewareObserves(t *testing.T) {
	if o, ok := getaotel.Middleware().Order(); !ok || o != geta.OrderObserve {
		t.Fatal(o, ok)
	}
}

func TestAMatchedOperationIsNamedByItsTemplate(t *testing.T) {
	h := serve(t, table(nil))
	if res := h.c.Get("/users/7"); res.Status != 200 {
		t.Fatal(res.Status)
	}
	s := h.span()
	if s.Name() != "GET /users/{id}" || s.SpanKind() != trace.SpanKindServer {
		t.Fatal(s.Name(), s.SpanKind())
	}
	if v, _ := attr(s, "http.route"); v.AsString() != "/users/{id}" {
		t.Fatalf("http.route %q", v.AsString())
	}
	if v, _ := attr(s, "http.request.method"); v.AsString() != "GET" {
		t.Fatalf("http.request.method %q", v.AsString())
	}
	if status(t, s) != 200 || s.Status().Code != codes.Unset {
		t.Fatal(status(t, s), s.Status())
	}
	sets := h.durations()
	if len(sets) != 1 {
		t.Fatalf("%d duration series", len(sets))
	}
	route, _ := sets[0].Value("http.route")
	code, _ := sets[0].Value("http.response.status_code")
	method, _ := sets[0].Value("http.request.method")
	if route.AsString() != "/users/{id}" || code.AsInt64() != 200 || method.AsString() != "GET" {
		t.Fatal(sets[0].Encoded(attribute.DefaultEncoder()))
	}
}

// What the client chooses is not recorded: neither the raw path nor the Host
// nor X-Forwarded-For reaches the span or the metrics, and two Hosts are one
// series. The handler still sees the request as it arrived.
func TestWhatTheClientChoosesIsNotRecorded(t *testing.T) {
	for _, server := range []string{"", "api.example:8443"} {
		var seen []string
		tb := table(nil)
		tb.Root = geta.Scope{geta.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = append(seen, r.Host+r.URL.Path)
				next.ServeHTTP(w, r)
			})
		})}
		var opts []getaotel.Option
		if server != "" {
			opts = append(opts, getaotel.WithServerName(server))
		}
		h := serveWith(t, tb, opts)
		for _, host := range []string{"a.example", "b.example"} {
			req, err := http.NewRequest(http.MethodGet, h.c.URL()+"/users/"+host, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = host
			req.Header.Set("X-Forwarded-For", "203.0.113.9")
			if res := h.c.Send(req); res.Status != 200 {
				t.Fatal(res.Status)
			}
			s := h.span()
			for _, kv := range s.Attributes() {
				if v := kv.Value.Emit(); strings.Contains(v, host) || strings.Contains(v, "203.0.113.9") {
					t.Errorf("span attribute %v", kv)
				}
			}
			if v, _ := attr(s, "server.address"); v.AsString() != strings.Split(server, ":")[0] {
				t.Errorf("server.address %q", v.AsString())
			}
			client, _ := attr(s, "client.address")
			if peer, _ := attr(s, "network.peer.address"); client.AsString() == "" || client.AsString() != peer.AsString() {
				t.Errorf("client.address %q, peer %q", client.AsString(), peer.AsString())
			}
		}
		if want := []string{"a.example/users/a.example", "b.example/users/b.example"}; !slices.Equal(seen, want) {
			t.Errorf("the chain saw %q", seen)
		}
		sets := h.durations()
		if len(sets) != 1 {
			t.Fatalf("server %q: %d duration series", server, len(sets))
		}
		addr, _ := sets[0].Value("server.address")
		port, hasPort := sets[0].Value("server.port")
		if addr.AsString() != strings.Split(server, ":")[0] || hasPort != (server != "") || hasPort && port.AsInt64() != 8443 {
			t.Errorf("server %q: %s", server, sets[0].Encoded(attribute.DefaultEncoder()))
		}
	}
}

// A User-Agent or a method past 128 bytes is recorded cut; the chain sees
// both whole.
func TestLongClientValuesAreRecordedCut(t *testing.T) {
	var seen string
	tb := table(nil)
	tb.Root = geta.Scope{geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = r.UserAgent()
			next.ServeHTTP(w, r)
		})
	})}
	h := serve(t, tb)
	ua := strings.Repeat("u", 1000)
	if res := h.c.With("User-Agent", ua).Get("/users/7"); res.Status != 200 || seen != ua {
		t.Fatal(res.Status, len(seen))
	}
	if v, _ := attr(h.span(), "user_agent.original"); v.AsString() != strings.Repeat("u", 128)+"…" {
		t.Fatalf("user_agent.original: %d bytes", len(v.AsString()))
	}
	// The cut splits no rune.
	h.c.With("User-Agent", "a"+strings.Repeat("é", 500)).Get("/users/7")
	if v, _ := attr(h.span(), "user_agent.original"); v.AsString() != "a"+strings.Repeat("é", 63)+"…" {
		t.Fatalf("user_agent.original: %q", v.AsString())
	}
	req, _ := http.NewRequest(strings.Repeat("M", 1000), h.c.URL()+"/users/7", nil)
	h.c.Send(req)
	if v, _ := attr(h.span(), "http.request.method_original"); v.AsString() != strings.Repeat("M", 128)+"…" {
		t.Fatalf("http.request.method_original: %d bytes", len(v.AsString()))
	}
}

// Two paths of one template are one series: the raw path never reaches a
// metric attribute.
func TestPathsOfOneTemplateShareASeries(t *testing.T) {
	h := serve(t, table(nil))
	h.c.Get("/users/1")
	h.span()
	h.c.Get("/users/2")
	h.span()
	sets := h.durations()
	if len(sets) != 1 {
		t.Fatalf("%d duration series for one template", len(sets))
	}
	for _, kv := range sets[0].ToSlice() {
		if strings.Contains(kv.Value.String(), "/users/1") || strings.Contains(kv.Value.String(), "/users/2") {
			t.Fatalf("the raw path is a metric attribute: %v", kv)
		}
	}
}

func TestAnIncomingTraceparentIsContinued(t *testing.T) {
	h := serve(t, table(nil))
	const traceID, parentID = "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	h.c.With("traceparent", "00-"+traceID+"-"+parentID+"-01").Get("/users/7")
	s := h.span()
	if got := s.SpanContext().TraceID().String(); got != traceID {
		t.Fatalf("trace %s", got)
	}
	if p := s.Parent(); p.SpanID().String() != parentID || !p.IsRemote() {
		t.Fatal(p.SpanID(), p.IsRemote())
	}
}

func TestAnUnmatchedRequestIsNamedByItsMethodAlone(t *testing.T) {
	h := serve(t, table(nil))
	cases := []struct {
		method, path, name string
		status             int
	}{
		{"GET", "/nowhere/42", "GET", 404},
		{"DELETE", "/users/7", "DELETE", 405},
		{"BREW", "/users/7", "HTTP", 405},
	}
	for _, c := range cases {
		if res := h.c.Do(c.method, c.path, nil); res.Status != c.status {
			t.Fatal(c.method, c.path, res.Status)
		}
		s := h.span()
		if s.Name() != c.name {
			t.Fatalf("%s %s: span %q", c.method, c.path, s.Name())
		}
		if v, ok := attr(s, "http.route"); ok {
			t.Fatalf("%s %s: http.route %q", c.method, c.path, v.AsString())
		}
		if got := status(t, s); got != int64(c.status) {
			t.Fatalf("%s %s: status %d", c.method, c.path, got)
		}
	}
	for _, set := range h.durations() {
		if v, ok := set.Value("http.route"); ok {
			t.Fatalf("an unmatched request has route %q", v.AsString())
		}
	}
}

// A path that is not clean is redirected to its clean form; geta.Matched
// reports what the clean path will match, so the redirect carries that
// template, as the access log's route does.
func TestARedirectIsNamedByTheTemplateItLeadsTo(t *testing.T) {
	h := serve(t, table(nil))
	res := h.c.Get("/users/./7")
	if res.Status != http.StatusPermanentRedirect || res.Header.Get("Location") != "/users/7" {
		t.Fatal(res.Status, res.Header)
	}
	s := h.span()
	if s.Name() != "GET /users/{id}" || status(t, s) != 308 {
		t.Fatal(s.Name(), status(t, s))
	}
}

func TestOnlyA5xxIsAnError(t *testing.T) {
	h := serve(t, table(nil))
	if res := h.c.Get("/users/gone"); res.Status != 404 {
		t.Fatal(res.Status)
	}
	s := h.span()
	if status(t, s) != 404 || s.Status().Code != codes.Unset {
		t.Fatal(status(t, s), s.Status())
	}
	if v, _ := attr(s, "http.route"); v.AsString() != "/users/{id}" {
		t.Fatalf("a failed operation lost its route: %q", v.AsString())
	}
	if res := h.c.Get("/users/boom"); res.Status != 500 {
		t.Fatal(res.Status)
	}
	s = h.span()
	if status(t, s) != 500 || s.Status().Code != codes.Error {
		t.Fatal(status(t, s), s.Status())
	}
}

func TestAStreamFlushesThroughTheMiddleware(t *testing.T) {
	gate := make(chan struct{})
	h := serve(t, table(gate))
	st := h.c.Stream("/ticks")
	if st.Response.Status != 200 {
		t.Fatal(st.Response.Status)
	}
	// The source waits for gate after the first event: reading it proves
	// the event was flushed while the handler was still running.
	first, ok := st.Next()
	if !ok || first.Data != `{"n":1}` {
		t.Fatal(first, ok)
	}
	close(gate)
	second, ok := st.Next()
	if !ok || second.Data != `{"n":2}` {
		t.Fatal(second, ok)
	}
	if _, ok := st.Next(); ok {
		t.Fatal("a third event")
	}
	s := h.span()
	if s.Name() != "GET /ticks" || status(t, s) != 200 {
		t.Fatal(s.Name(), status(t, s))
	}
}

func TestAnUpgradeSwitchesThroughTheMiddleware(t *testing.T) {
	h := serve(t, table(nil))
	u := h.c.Upgrade("/ws", "echo")
	if !u.Switched {
		t.Fatal(u.Response.Status, u.Response.Text())
	}
	io.WriteString(u.Conn, "hi\n")
	line, _ := bufio.NewReader(u.Conn).ReadString('\n')
	if line != "echo: hi\n" {
		t.Fatalf("%q", line)
	}
	s := h.span()
	if s.Name() != "GET /ws" || status(t, s) != 101 || s.Status().Code != codes.Unset {
		t.Fatal(s.Name(), status(t, s), s.Status())
	}
	sets := h.durations()
	if len(sets) != 1 {
		t.Fatalf("%d duration series", len(sets))
	}
	if code, _ := sets[0].Value("http.response.status_code"); code.AsInt64() != 101 {
		t.Fatalf("an upgrade measured as %d", code.AsInt64())
	}
	// A refused upgrade is an ordinary response.
	if u := h.c.Upgrade("/ws", ""); u.Switched || u.Response.Status != 426 {
		t.Fatal(u.Switched, u.Response.Status)
	}
	if s := h.span(); status(t, s) != 426 {
		t.Fatal(status(t, s))
	}
}
