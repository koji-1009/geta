package booking

import (
	"context"
	"net/http"
	"time"
	"uuid"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/app"
	"github.com/koji-1009/geta/examples/booking/failures"
	"github.com/koji-1009/geta/examples/booking/model"
)

// Route is /bookings/{booking}. A fetch may be conditional: a client holding
// the current version gets a 304.
func Route(env *app.Env) geta.Route {
	h := Handler{Bookings: env.Store}
	return geta.Route{
		Get: geta.Op(http.StatusOK, h.Get, geta.Doc{Summary: "Fetch a booking", Failures: []geta.Failure{failures.BookingNotFound}}),
	}
}

type Store interface {
	Booking(ctx context.Context, id uuid.UUID) (*model.Booking, string, error)
}

type Handler struct{ Bookings Store }

type GetIn struct {
	geta.Conditional
	Booking uuid.UUID `path:"booking"`
}

// Tagged is a booking and the entity tag of its version, which a change
// names in If-Match.
type Tagged struct {
	ETag    string        `header:"ETag"`
	Booking model.Booking `body:"json"`
}

func (h Handler) Get(ctx context.Context, in *GetIn) (*Tagged, error) {
	b, tag, err := h.Bookings.Booking(ctx, in.Booking)
	if err != nil {
		return nil, err
	}
	if err := in.Check(tag, time.Time{}); err != nil {
		return nil, err
	}
	return &Tagged{ETag: tag, Booking: *b}, nil
}
