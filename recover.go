package geta

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recover turns a panic into a 500 and logs it with its stack. A panic after
// the response has started aborts the connection instead. http.ErrAbortHandler
// passes through. A nil log fails [New].
func Recover(log *slog.Logger) Middleware {
	m := Ordered(OrderRecover, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			o := &observer{ResponseWriter: w}
			defer func() {
				p := recover()
				if p == nil {
					return
				}
				if p == http.ErrAbortHandler {
					panic(p)
				}
				attrs := []slog.Attr{slog.String("method", r.Method), routeAttr(r.Context()),
					slog.String("stack", string(debug.Stack()))}
				if o.status != 0 || o.upgraded {
					log.LogAttrs(r.Context(), slog.LevelError, "geta: panic", append(attrs, slog.Any("panic", p))...)
					panic(http.ErrAbortHandler)
				}
				writeDefect(w, r, log, "geta: panic", p, attrs...)
			}()
			next.ServeHTTP(o, r)
		})
	})
	m.name = "recover"
	if log == nil {
		m.bad = errors.New("geta.Recover needs a logger; pass slog.New(slog.DiscardHandler) to record nothing")
	}
	return m
}
