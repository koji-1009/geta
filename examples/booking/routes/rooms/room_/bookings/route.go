package bookings

import (
	"context"
	"net/http"
	"slices"
	"uuid"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/app"
	"github.com/koji-1009/geta/examples/booking/auth"
	"github.com/koji-1009/geta/examples/booking/failures"
	"github.com/koji-1009/geta/examples/booking/live"
	"github.com/koji-1009/geta/examples/booking/model"
)

// Route is /rooms/{room}/bookings: a room's bookings on a day, and booking
// it. Booking needs the bookings:write scope.
func Route(env *app.Env) geta.Route {
	h := Handler{Bookings: env.Store, Live: env.Live}
	return geta.Route{
		Get: geta.Op(http.StatusOK, h.List, geta.Doc{
			Summary:  "List a room's bookings on a day",
			Failures: []geta.Failure{failures.RoomNotFound},
		}),
		Post: geta.Op(http.StatusCreated, h.Create, geta.Doc{
			Summary:  "Book a room",
			Failures: slices.Concat([]geta.Failure{failures.RoomNotFound}, failures.Schedule),
			Scope:    auth.Require(auth.ScopeWriteBookings),
		}),
	}
}

type Store interface {
	Bookings(ctx context.Context, room uuid.UUID, day geta.Date) ([]model.Booking, error)
	Book(ctx context.Context, owner string, b model.Booking) (*model.Booking, string, error)
}

// Publisher tells the room's panels.
type Publisher interface {
	Publish(room uuid.UUID, u live.Update)
}

type Handler struct {
	Bookings Store
	Live     Publisher
}

type Path struct {
	Room uuid.UUID `path:"room"`
}

type ListIn struct {
	Path
	Date geta.Date `query:"date"`
}

// BookingList is a room's bookings on one day, by start.
type BookingList struct {
	Items []model.Booking `json:"items"`
}

func (h Handler) List(ctx context.Context, in *ListIn) (*BookingList, error) {
	bs, err := h.Bookings.Bookings(ctx, in.Room, in.Date)
	if err != nil {
		return nil, err
	}
	return &BookingList{Items: bs}, nil
}

// NewBooking is a booking as a client asks for it.
type NewBooking struct {
	Title     string         `json:"title" schema:"minLength=1,maxLength=120"`
	Date      geta.Date      `json:"date"`
	Start     geta.TimeOfDay `json:"start"`
	Length    geta.Duration  `json:"length"`
	Organizer model.Email    `json:"organizer"`
	Attendees *[]model.Email `json:"attendees,omitzero" schema:"maxItems=50"`
}

type CreateIn struct {
	Path
	Body NewBooking `body:"json"`
}

// Booked is the new booking, where it lives, and its entity tag.
type Booked struct {
	Location string        `header:"Location"`
	ETag     string        `header:"ETag"`
	Booking  model.Booking `body:"json"`
}

func (h Handler) Create(ctx context.Context, in *CreateIn) (*Booked, error) {
	nb := in.Body
	attendees := []model.Email{}
	if nb.Attendees != nil {
		attendees = *nb.Attendees
	}
	b, tag, err := h.Bookings.Book(ctx, auth.Subject(ctx), model.Booking{Room: in.Room, Title: nb.Title, Date: nb.Date, Start: nb.Start,
		Length: nb.Length, Organizer: nb.Organizer, Attendees: attendees})
	if err != nil {
		return nil, err
	}
	h.Live.Publish(in.Room, live.Update{Kind: "booked", Booking: b.ID, Title: b.Title, Date: b.Date.String(), Start: b.Start.String()})
	return &Booked{Location: "/bookings/" + b.ID.String(), ETag: tag, Booking: *b}, nil
}
