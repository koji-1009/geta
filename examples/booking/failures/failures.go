// Package failures holds the failure rows the booking routes share. Rows
// that share a status carry a problem type each, so a client tells them
// apart by type, not by parsing the detail.
package failures

import (
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/store"
)

var (
	RoomNotFound    = geta.On(store.ErrRoomNotFound, http.StatusNotFound, "room not found").Type("/problems/room-not-found")
	BookingNotFound = geta.On(store.ErrBookingNotFound, http.StatusNotFound, "booking not found").Type("/problems/booking-not-found")

	// NotOwner is a change to a booking the caller did not make, by a token
	// without bookings:manage. The scope gate (bookings:write) runs before
	// the handler and sees only the request; whose booking it is is known
	// once the store has loaded it, so the store refuses and this row
	// answers 403.
	NotOwner = geta.On(store.ErrNotOwner, http.StatusForbidden, "the booking is someone else's").Type("/problems/not-owner")

	// Overlap matches a *store.OverlapError by type, wherever it is wrapped.
	Overlap   = geta.OnAs[*store.OverlapError](http.StatusConflict, "the room is booked then").Type("/problems/overlap")
	Cancelled = geta.On(store.ErrCancelled, http.StatusConflict, "the booking is cancelled").Type("/problems/cancelled")

	OutsideHours = geta.On(store.ErrOutsideHours, http.StatusUnprocessableEntity, "the booking is outside the room's hours").Type("/problems/outside-hours")
	Length       = geta.On(store.ErrLength, http.StatusUnprocessableEntity, "a booking lasts whole minutes, from 15 minutes to 8 hours").Type("/problems/length")
)

// Schedule are the rows of any write that places a booking in time.
var Schedule = []geta.Failure{Overlap, OutsideHours, Length}
