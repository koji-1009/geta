package geta

import (
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// UnmatchedRoute is the route an access log records for a request that
// matched no operation. The raw path is never logged in its place.
const UnmatchedRoute = "<unmatched>"

// AccessLog records one line per request: method, route template, status,
// bytes, and duration. A response that streamed is marked streaming; one that
// switched protocols is marked upgrade. The raw path is never logged.
//
// The status is the one the client was sent; a handler that wrote nothing
// is a 200. A panic that passes through AccessLog is logged as aborted, with
// the status written before it or 500, and is re-raised unchanged. A nil log
// fails [New].
func AccessLog(log *slog.Logger) Middleware {
	m := Ordered(OrderObserve, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			o := &observer{ResponseWriter: w}
			returned := false
			defer func() {
				status := o.status
				if status == 0 {
					status = http.StatusOK
					if !returned {
						status = http.StatusInternalServerError
					}
				}
				attrs := []slog.Attr{
					methodAttr(r),
					routeAttr(r.Context()),
					slog.Int("status", status),
					slog.Int64("bytes", o.bytes),
					slog.Duration("duration", time.Since(start)),
				}
				if !returned {
					attrs = append(attrs, slog.Bool("aborted", true))
				}
				if o.streaming {
					attrs = append(attrs, slog.Bool("streaming", true))
				}
				if o.upgraded || o.status == http.StatusSwitchingProtocols {
					attrs = append(attrs, slog.Bool("upgrade", true))
				}
				log.LogAttrs(r.Context(), slog.LevelInfo, "request", attrs...)
			}()
			next.ServeHTTP(o, r)
			returned = true
		})
	})
	m.name = "access-log"
	if log == nil {
		m.bad = errors.New("geta.AccessLog needs a logger; pass slog.New(slog.DiscardHandler) to record nothing")
	}
	return m
}
