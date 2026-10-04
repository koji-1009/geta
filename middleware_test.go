package geta_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/koji-1009/geta"
)

// geta's middleware carry their ranks, so the app inherits them.

func TestBuiltinsCarryTheirOrder(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	for m, want := range map[*geta.Middleware]geta.Order{
		ptr(geta.AccessLog(log)):               geta.OrderObserve,
		ptr(geta.CORS(geta.AllowOrigins("*"))): geta.OrderCrossOrigin,
		ptr(geta.Recover(log)):                 geta.OrderRecover,
		ptr(geta.ConcurrencyLimit(1)):          geta.OrderShed,
		ptr(geta.Timeout(time.Second)):         geta.OrderDeadline,
		ptr(geta.Gzip()):                       geta.OrderNegotiate,
		ptr(geta.ETag()):                       geta.OrderValidate,
		ptr(geta.Secure(geta.Policy{})):        geta.OrderAuthenticate,
	} {
		if o, ok := m.Order(); !ok || o != want {
			t.Errorf("%v, want %v", o, want)
		}
	}
	if _, ok := geta.Use(noop).Order(); ok {
		t.Error("an adapted middleware has an order")
	}
}

func ptr[T any](v T) *T { return &v }

func TestRejectsEtagOutsideGzip(t *testing.T) {
	rejects(t, withRoot(one("/x", get(okHandler)), geta.ETag(), geta.Gzip()), "negotiate", "validate")
	accepts(t, withRoot(one("/x", get(okHandler)), geta.Gzip(), geta.ETag()))
}

func TestRejectsCORSInsideRecover(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	rejects(t, withRoot(one("/x", get(okHandler)), geta.Recover(log), geta.CORS(geta.AllowOrigins("*"))), "cross-origin", "recover")
}

func TestRejectsMiddlewareConstructionMistakes(t *testing.T) {
	for name, m := range map[string]geta.Middleware{
		"concurrency 0":      geta.ConcurrencyLimit(0),
		"timeout 0":          geta.Timeout(0),
		"cors no origin":     geta.CORS(),
		"cors * credentials": geta.CORS(geta.AllowOrigins("*"), geta.AllowCredentials()),
		"access log nil":     geta.AccessLog(nil),
		"recover nil":        geta.Recover(nil),
	} {
		t.Run(name, func(t *testing.T) { rejects(t, withRoot(one("/x", get(okHandler)), m), "middleware 0") })
	}
}

// A nil logger would panic at the first defect in place of its 500: WithLogger
// refuses it at assembly, as AccessLog and Recover do.
func TestANilLoggerIsRefused(t *testing.T) {
	_, err := geta.New(one("/x", get(okHandler)), geta.WithLogger(nil))
	if err == nil || !strings.Contains(err.Error(), "WithLogger: nil logger") {
		t.Fatal(err)
	}
}
