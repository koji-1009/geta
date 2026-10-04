package events

import (
	"context"
	"net/http"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/auth/app"
	"github.com/koji-1009/geta/examples/auth/auth"
)

// Route is /me/events: the caller's session feed. When /logout revokes the
// session, or it expires, the feed sends one revoked event and the server
// closes the connection itself; the client does not have to notice.
func Route(env *app.Env) geta.Route {
	h := Handler{Sessions: env.Sessions}
	return geta.Route{
		Get: geta.Op(http.StatusOK, h.Get, geta.Doc{
			Summary:     "The session status feed; closes when the session is revoked",
			Security:    []geta.Scheme{auth.CookieAuth},
			Tags:        []string{"sessions"},
			OperationID: "streamSessionEvents",
		}),
	}
}

type Sessions interface {
	Revoked(sid string) (<-chan struct{}, time.Duration)
}

type Handler struct{ Sessions Sessions }

// SessionEvent is one event of the feed.
type SessionEvent struct {
	Kind string `json:"kind" schema:"enum=revoked"`
}

func (e SessionEvent) EventName() string { return e.Kind }

func (h Handler) Get(ctx context.Context, _ *struct{}) (*geta.Stream[SessionEvent], error) {
	revoked, left := h.Sessions.Revoked(auth.Caller.Must(ctx).Session)
	return &geta.Stream[SessionEvent]{
		Events: func(yield func(SessionEvent) bool) {
			// The session ends at its expiry too, logged out or not.
			expiry := time.NewTimer(left)
			defer expiry.Stop()
			select {
			case <-revoked:
				yield(SessionEvent{Kind: "revoked"})
			case <-expiry.C:
				yield(SessionEvent{Kind: "revoked"})
			case <-ctx.Done():
			}
		},
		KeepAlive: 15 * time.Second,
	}, nil
}
