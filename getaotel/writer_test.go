package getaotel_test

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaotel"
	"github.com/koji-1009/geta/getatest"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// conn reaches the connection beneath the middleware's writer: X-Conn
// "deadline" sets a write deadline through http.ResponseController and
// answers whether it could; "hijack" takes the connection, writes to the
// writer it took it from, and answers, on the connection, the error that
// write returned.
var conn = geta.Use(func(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Conn") {
		case "deadline":
			err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Minute))
			io.WriteString(w, fmt.Sprint(err))
		case "hijack":
			c, rw, err := http.NewResponseController(w).Hijack()
			if err != nil {
				io.WriteString(w, "no hijack: "+err.Error())
				return
			}
			defer c.Close()
			_, werr := w.Write([]byte("lost"))
			body := fmt.Sprint(werr)
			fmt.Fprintf(rw, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
			rw.Flush()
		default:
			next.ServeHTTP(w, r)
		}
	})
})

// The writer the middleware puts beneath otelhttp's unwraps to the server's:
// a write deadline set through http.ResponseController reaches the
// connection; once the connection is hijacked, a write to the writer is
// http.ErrHijacked, and nothing of it reaches the connection.
func TestTheWriterBeneathReachesTheConnection(t *testing.T) {
	tb := table(nil)
	tb.Root = geta.Scope{conn}
	h := serve(t, tb)

	req, _ := http.NewRequest(http.MethodGet, h.c.URL()+"/users/7", nil)
	req.Header.Set("X-Conn", "deadline")
	res, err := h.c.HTTP().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(body) != "<nil>" {
		t.Fatalf("SetWriteDeadline: %s", body)
	}
	h.span()

	req, _ = http.NewRequest(http.MethodGet, h.c.URL()+"/users/7", nil)
	req.Header.Set("X-Conn", "hijack")
	res, err = h.c.HTTP().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if want := http.ErrHijacked.Error(); string(body) != want {
		t.Fatalf("a write after the hijack: %q; want %q", body, want)
	}
	h.span()
}

var errNoHistogram = errors.New("no histogram for you")

// failingMeters is a meter provider whose histograms cannot be created.
type failingMeters struct{ noop.MeterProvider }

func (failingMeters) Meter(string, ...metric.MeterOption) metric.Meter { return failingMeter{} }

type failingMeter struct{ noop.Meter }

func (failingMeter) Float64Histogram(string, ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	return noop.Float64Histogram{}, errNoHistogram
}

func (failingMeter) Int64Histogram(string, ...metric.Int64HistogramOption) (metric.Int64Histogram, error) {
	return noop.Int64Histogram{}, errNoHistogram
}

type handled struct {
	mu   sync.Mutex
	errs []error
}

func (h *handled) Handle(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.errs = append(h.errs, err)
}

// A meter provider that cannot create the histograms is reported to
// OpenTelemetry's error handler, with its own error, and the requests are
// served all the same.
func TestAMeterProviderFailureIsHandledByOpenTelemetry(t *testing.T) {
	prev := otel.GetErrorHandler()
	t.Cleanup(func() { otel.SetErrorHandler(prev) })
	got := &handled{}
	otel.SetErrorHandler(got)

	tb := table(nil)
	tb.Root = geta.Scope{getaotel.Middleware(getaotel.WithMeterProvider(failingMeters{}))}
	c := getatest.New(t, tb, geta.WithLogger(slog.New(slog.DiscardHandler)))
	if res := c.Get("/users/7"); res.Status != http.StatusOK {
		t.Fatal(res.Status, res.Text())
	}
	got.mu.Lock()
	defer got.mu.Unlock()
	if len(got.errs) == 0 {
		t.Fatal("no error handled")
	}
	for _, err := range got.errs {
		if !errors.Is(err, errNoHistogram) {
			t.Errorf("handled %v", err)
		}
	}
}
