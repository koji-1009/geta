// Package app builds the auth example's dependencies.
package app

import (
	"log/slog"
	"os"

	"github.com/koji-1009/geta/examples/auth/auth"
)

// Env needs no database: auth is orthogonal to persistence.
type Env struct {
	Sessions *auth.Sessions
	Log      *slog.Logger
	// LoginsPerMinute is how many login attempts one client address may
	// make a minute, in a burst or spread out.
	LoginsPerMinute int
}

// Open builds an Env.
func Open() *Env {
	return &Env{Sessions: auth.NewSessions(), Log: slog.New(slog.NewJSONHandler(os.Stderr, nil)), LoginsPerMinute: 10}
}
