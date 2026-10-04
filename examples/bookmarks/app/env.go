// Package app builds the bookmarks example's dependencies.
package app

import (
	"io"
	"log/slog"
	"net/netip"
	"os"
	"time"

	"github.com/koji-1009/geta/examples/bookmarks/auth"
	"github.com/koji-1009/geta/examples/bookmarks/idempotency"
	"github.com/koji-1009/geta/examples/bookmarks/metrics"
	"github.com/koji-1009/geta/examples/bookmarks/store"
)

// Env carries the dependencies every route draws from; routes take only
// what they use.
type Env struct {
	Sessions  *auth.Sessions
	Bookmarks *store.Memory
	// Created remembers the answer to each POST /bookmarks sent with an
	// Idempotency-Key, so a retry is answered, not written again.
	Created *idempotency.Keys[store.Bookmark]
	Metrics *metrics.Routes
	Log     *slog.Logger

	// PageOrigin is the origin of the single-page app that calls this API
	// with the session cookie, such as https://app.example.com. CORS lets
	// it, and it alone, send the cookie and read the answers.
	PageOrigin string
	// SecureCookies sets Secure on the session cookie: off only for a demo
	// on plain HTTP away from localhost.
	SecureCookies bool
	// TrustedProxies are the reverse proxies in front of the server, whose
	// X-Forwarded-For entries the rate limit believes (clientip.Key).
	TrustedProxies []netip.Prefix
	// RatePerMinute is how many requests one client address may make a
	// minute, in a burst or spread out.
	RatePerMinute int
}

// Open builds an Env whose page is at pageOrigin.
func Open(pageOrigin string) *Env { return open(pageOrigin, os.Stderr) }

// OpenQuiet is Open with logs discarded, for tests.
func OpenQuiet(pageOrigin string) *Env { return open(pageOrigin, io.Discard) }

func open(pageOrigin string, w io.Writer) *Env {
	return &Env{
		Sessions:      auth.NewSessions(),
		Bookmarks:     store.NewMemory(),
		Created:       idempotency.New[store.Bookmark](24 * time.Hour),
		Metrics:       metrics.New(),
		Log:           slog.New(slog.NewJSONHandler(w, nil)),
		PageOrigin:    pageOrigin,
		SecureCookies: true,
		RatePerMinute: 120,
	}
}
