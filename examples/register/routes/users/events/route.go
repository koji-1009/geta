package events

import (
	"context"
	"iter"
	"net/http"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/app"
	ev "github.com/koji-1009/geta/examples/register/events"
)

// Route is /users/events, a live feed of create, update, and delete. The
// gate runs before the stream opens: an anonymous subscriber gets a 401,
// never a stream.
func Route(env *app.Env) geta.Route {
	h := Handler{Feed: env.Events}
	return geta.Route{
		Get: geta.Op(http.StatusOK, h.Get, geta.Doc{
			Summary:     "Live feed of user create/update/delete events",
			Tags:        []string{"users", "events"},
			OperationID: "streamUserEvents",
		}),
	}
}

type Feed interface {
	Subscribe(ctx context.Context) iter.Seq[ev.UserEvent]
}

type Handler struct{ Feed Feed }

func (h Handler) Get(ctx context.Context, _ *struct{}) (*geta.Stream[ev.UserEvent], error) {
	return &geta.Stream[ev.UserEvent]{Events: h.Feed.Subscribe(ctx), KeepAlive: 15 * time.Second}, nil
}
