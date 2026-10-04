// Package app builds the register example's dependencies.
package app

import (
	"log/slog"
	"os"
	"time"

	"github.com/koji-1009/geta/examples/register/events"
	"github.com/koji-1009/geta/examples/register/store"
	"github.com/koji-1009/geta/examples/register/teams"
)

// Env carries the dependencies every route draws from. It is shared by all
// goroutines, so everything in it is safe for concurrent use. Routes never
// see it whole: each takes only what it uses.
type Env struct {
	Users          store.Users
	Teams          *teams.Memory
	Events         *events.Hub
	Log            *slog.Logger
	RequestTimeout time.Duration
}

// Open builds an Env. Logs are JSON lines on stderr.
func Open() *Env {
	return &Env{
		Users:          store.NewMemory(),
		Teams:          teams.NewMemory(),
		Events:         events.NewHub(),
		Log:            slog.New(slog.NewJSONHandler(os.Stderr, nil)),
		RequestTimeout: 10 * time.Second,
	}
}
