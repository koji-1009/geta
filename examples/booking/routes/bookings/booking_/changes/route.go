package changes

import (
	"context"
	"net/http"
	"slices"
	"time"
	"uuid"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/app"
	"github.com/koji-1009/geta/examples/booking/auth"
	"github.com/koji-1009/geta/examples/booking/failures"
	"github.com/koji-1009/geta/examples/booking/live"
	"github.com/koji-1009/geta/examples/booking/model"
	booking "github.com/koji-1009/geta/examples/booking/routes/bookings/booking_"
)

// Route is /bookings/{booking}/changes: rename, reschedule, or cancel a
// booking. A change must name the version it was made against in If-Match
// (428 without one, 412 once another change has landed), so two people
// editing one booking never overwrite each other unseen.
//
// Authorization is in two places. The gate in Doc.Scope requires
// bookings:write: it depends on the request alone, so it answers before the
// body is read. Whether this caller may change this booking (its owner, or
// a token with bookings:manage) depends on the booking, so the store checks
// it once the booking is loaded and returns store.ErrNotOwner, which the
// failure table answers 403 and documents.
func Route(env *app.Env) geta.Route {
	h := Handler{Bookings: env.Store, Live: env.Live}
	return geta.Route{
		Post: geta.Op(http.StatusOK, h.Post, geta.Doc{
			Summary:  "Change a booking",
			Failures: slices.Concat([]geta.Failure{failures.BookingNotFound, failures.NotOwner, failures.Cancelled}, failures.Schedule),
			Scope:    auth.Require(auth.ScopeWriteBookings),
		}),
	}
}

type Store interface {
	Apply(ctx context.Context, id uuid.UUID, by string, manage bool, c model.Change, check func(tag string) error) (*model.Booking, string, error)
}

type Publisher interface {
	Publish(room uuid.UUID, u live.Update)
}

type Handler struct {
	Bookings Store
	Live     Publisher
}

// PostIn is the change itself as the body: one of the variants of
// model.Change, chosen by its kind.
type PostIn struct {
	geta.RequireConditional
	Booking uuid.UUID    `path:"booking"`
	Body    model.Change `body:"json"`
}

func (h Handler) Post(ctx context.Context, in *PostIn) (*booking.Tagged, error) {
	manage := auth.Holds(ctx, auth.ScopeManageBookings)
	b, tag, err := h.Bookings.Apply(ctx, in.Booking, auth.Subject(ctx), manage, in.Body, func(current string) error {
		return in.Check(current, time.Time{})
	})
	if err != nil {
		return nil, err
	}
	kind := "changed"
	if b.Status == model.Cancelled {
		kind = "cancelled"
	}
	h.Live.Publish(b.Room, live.Update{Kind: kind, Booking: b.ID, Title: b.Title, Date: b.Date.String(), Start: b.Start.String()})
	return &booking.Tagged{ETag: tag, Booking: *b}, nil
}
