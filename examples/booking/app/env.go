// Package app builds the booking service's dependencies; the options its
// app is assembled with are routes.Options.
package app

import (
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/live"
	"github.com/koji-1009/geta/examples/booking/store"
)

// Env carries the dependencies every route draws from; routes take only
// what they use.
type Env struct {
	Store *store.Memory
	Live  *live.Hub
	Log   *slog.Logger

	// Issuer is the OpenID issuer whose tokens the gate accepts, and Keys
	// resolves their verification keys (auth.Discover).
	Issuer string
	Keys   jwt.Keyfunc

	// Observe is the root scope's first entries: getaotel's middleware in
	// the service, nothing in a test that does not trace.
	Observe geta.Scope

	// The root scope's shedding and deadline.
	RatePerMinute  int
	MaxInFlight    int
	RequestTimeout time.Duration
}

// Open builds an Env that trusts issuer's tokens, checked with keys. Logs
// are JSON lines on stderr.
func Open(issuer string, keys jwt.Keyfunc) *Env {
	return open(issuer, keys, os.Stderr)
}

// OpenQuiet is Open with logs discarded, for tests.
func OpenQuiet(issuer string, keys jwt.Keyfunc) *Env { return open(issuer, keys, io.Discard) }

func open(issuer string, keys jwt.Keyfunc, w io.Writer) *Env {
	return &Env{
		Store:          store.NewMemory(),
		Live:           live.NewHub(),
		Log:            slog.New(slog.NewJSONHandler(w, nil)),
		Issuer:         issuer,
		Keys:           keys,
		RatePerMinute:  600,
		MaxInFlight:    256,
		RequestTimeout: 5 * time.Second,
	}
}
