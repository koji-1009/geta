// Package model is the booking service's domain: rooms, their bookings, and
// the changes a booking goes through. Every field's wire form and contract
// is in its type and tags; the document is read off them.
package model

import (
	"time"
	"uuid"

	"github.com/koji-1009/geta"
)

// Room is a meeting room. Its hours are times of day with the room's own
// offset ("08:00:00+09:00"); its door panel is a device on the network.
type Room struct {
	ID         uuid.UUID      `json:"id"`
	Name       string         `json:"name" schema:"minLength=1,maxLength=80"`
	Capacity   int            `json:"capacity" schema:"minimum=1,maximum=500"`
	Opens      geta.TimeOfDay `json:"opens"`
	Closes     geta.TimeOfDay `json:"closes"`
	HourlyRate Money          `json:"hourlyRate"`
	// Panel is the address of the display beside the door, which
	// subscribes to the room's live feed.
	Panel  *geta.IPv4 `json:"panel,omitzero"`
	Panel6 *geta.IPv6 `json:"panel6,omitzero"`
	// Features are what the room has, such as "projector" or "whiteboard".
	Features []string `json:"features" doc:"What the room has: projector, whiteboard, video, ..." schema:"maxItems=8,uniqueItems=true"`
	// Floor is the floor the room is on; the building has three.
	Floor int `json:"floor" schema:"enum=1|2|3"`
}

// Status is where a booking stands.
type Status string

const (
	Confirmed Status = "confirmed"
	Cancelled Status = "cancelled"
)

// Booking is one booking of a room: a day, a start, and a length.
type Booking struct {
	ID        uuid.UUID      `json:"id"`
	Room      uuid.UUID      `json:"room"`
	Title     string         `json:"title" schema:"minLength=1,maxLength=120"`
	Date      geta.Date      `json:"date"`
	Start     geta.TimeOfDay `json:"start"`
	Length    geta.Duration  `json:"length"`
	Organizer Email          `json:"organizer"`
	Attendees []Email        `json:"attendees" schema:"maxItems=50"`
	Price     Money          `json:"price"`
	Status    Status         `json:"status" schema:"enum=confirmed|cancelled"`
	// CancelReason is set once the booking is cancelled.
	CancelReason *string   `json:"cancelReason,omitzero" schema:"maxLength=200"`
	CreatedAt    time.Time `json:"createdAt"`
}

// Change is one change to a booking: a sealed type, told apart on the wire
// by its kind. A request's body is checked against the variant its kind
// names; the history answers changes back in the same form.
type Change interface{ isChange() }

// Rename gives the booking another title.
type Rename struct {
	Kind  string `json:"kind"`
	Title string `json:"title" schema:"minLength=1,maxLength=120"`
}

// Reschedule moves the booking, and may change its length.
type Reschedule struct {
	Kind   string         `json:"kind"`
	Date   geta.Date      `json:"date"`
	Start  geta.TimeOfDay `json:"start"`
	Length *geta.Duration `json:"length,omitzero"`
}

// Cancel cancels the booking, which then takes no other change.
type Cancel struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason" schema:"minLength=1,maxLength=200"`
}

func (Rename) isChange()     {}
func (Reschedule) isChange() {}
func (Cancel) isChange()     {}

// Changes declares Change to geta: main passes it to geta.New with
// geta.WithUnion, and a client decoding the history uses its JSONOptions.
var Changes = geta.Sealed[Change]("kind",
	geta.Case[Rename]("rename"),
	geta.Case[Reschedule]("reschedule"),
	geta.Case[Cancel]("cancel"),
)

// Entry is one change in a booking's history: when, by whom, what, and
// which members of the booking it wrote, as JSON Pointers ("/date").
type Entry struct {
	At      geta.DateTime      `json:"at"`
	By      string             `json:"by" schema:"maxLength=200"`
	Change  Change             `json:"change"`
	Touched []geta.JSONPointer `json:"touched" schema:"maxItems=8"`
}
