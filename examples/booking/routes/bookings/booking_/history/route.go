package history

import (
	"context"
	"net/http"
	"uuid"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/app"
	"github.com/koji-1009/geta/examples/booking/failures"
	"github.com/koji-1009/geta/examples/booking/model"
)

// Route is /bookings/{booking}/history: every change, oldest first, each
// answered back as the variant it was made as.
func Route(env *app.Env) geta.Route {
	h := Handler{Bookings: env.Store}
	return geta.Route{
		Get: geta.Op(http.StatusOK, h.Get, geta.Doc{Summary: "A booking's changes", Failures: []geta.Failure{failures.BookingNotFound}}),
	}
}

type Store interface {
	History(ctx context.Context, id uuid.UUID) ([]model.Entry, error)
}

type Handler struct{ Bookings Store }

type GetIn struct {
	Booking uuid.UUID `path:"booking"`
}

// History is a booking's changes.
type History struct {
	Items []model.Entry `json:"items"`
}

func (h Handler) Get(ctx context.Context, in *GetIn) (*History, error) {
	es, err := h.Bookings.History(ctx, in.Booking)
	if err != nil {
		return nil, err
	}
	return &History{Items: es}, nil
}
