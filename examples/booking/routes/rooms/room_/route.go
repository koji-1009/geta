package room

import (
	"context"
	"net/http"
	"uuid"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/app"
	"github.com/koji-1009/geta/examples/booking/failures"
	"github.com/koji-1009/geta/examples/booking/model"
)

// Route is /rooms/{room}. The room is a UUID: any other path segment is a
// 400 before the handler runs.
func Route(env *app.Env) geta.Route {
	h := Handler{Rooms: env.Store}
	return geta.Route{
		Get: geta.Op(http.StatusOK, h.Get, geta.Doc{Summary: "Fetch a room", Failures: []geta.Failure{failures.RoomNotFound}}),
	}
}

type Store interface {
	Room(ctx context.Context, id uuid.UUID) (*model.Room, error)
}

type Handler struct{ Rooms Store }

type GetIn struct {
	Room uuid.UUID `path:"room"`
}

func (h Handler) Get(ctx context.Context, in *GetIn) (*model.Room, error) {
	return h.Rooms.Room(ctx, in.Room)
}
