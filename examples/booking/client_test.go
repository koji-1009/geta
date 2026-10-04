package main

import (
	"errors"
	"net/http"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/model"
	"github.com/koji-1009/geta/examples/booking/routes/bookings/booking_"
	"github.com/koji-1009/geta/examples/booking/routes/bookings/booking_/changes"
	"github.com/koji-1009/geta/examples/booking/routes/bookings/booking_/history"
	"github.com/koji-1009/geta/examples/booking/routes/rooms"
	"github.com/koji-1009/geta/examples/booking/routes/rooms/room_/bookings"
	"github.com/koji-1009/geta/getaclient"
)

// The round trip of clientcheck/roundtrip.py, from Go with the handlers'
// own types: format types, Money, an application's format type, and a
// sealed change sent and read back, with no generated code.
func TestTypedClientRoundTrip(t *testing.T) {
	h := newHarness(t)
	desk := h.anon.Bearer(h.token("frontdesk")).Typed()
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	opens, err := geta.ParseTimeOfDay("08:00:00Z")
	must(err)
	closes, err := geta.ParseTimeOfDay("20:00:00Z")
	must(err)
	panel, err := geta.ParseIPv4("198.51.100.7")
	must(err)
	created, err := getaclient.Call[rooms.CreateIn, rooms.RoomCreated](ctx, desk, http.MethodPost, "/rooms", &rooms.CreateIn{Body: rooms.NewRoom{
		// Floor has a default, but a Go value always has the field: the
		// client sends what it holds, 0 when unset, and the enum refuses 0.
		Name: "Fuji", Capacity: 12, Opens: opens, Closes: closes, HourlyRate: model.Cents(99_95), Panel: &panel, Floor: 2,
	}})
	must(err)
	if created.Location != "/rooms/"+created.Room.ID.String() || created.Room.HourlyRate.String() != "99.95" || created.Room.Panel6 != nil ||
		created.Room.Floor != 2 || len(created.Room.Features) != 0 {
		t.Fatalf("%+v", created)
	}

	day, err := geta.ParseDate("2026-10-06")
	must(err)
	start, err := geta.ParseTimeOfDay("10:00:00Z")
	must(err)
	length, err := geta.ParseDuration("PT2H")
	must(err)
	ada, err := model.ParseEmail("ada@example.com")
	must(err)
	booked, err := getaclient.Call[bookings.CreateIn, bookings.Booked](ctx, desk, http.MethodPost, "/rooms/{room}/bookings", &bookings.CreateIn{
		Path: bookings.Path{Room: created.Room.ID},
		Body: bookings.NewBooking{Title: "Planning", Date: day, Start: start, Length: length, Organizer: ada},
	})
	must(err)
	if booked.Booking.Price.String() != "199.90" || booked.ETag != `"1"` || len(booked.Booking.Attendees) != 0 {
		t.Fatalf("%+v", booked)
	}
	_, err = getaclient.Call[bookings.CreateIn, bookings.Booked](ctx, desk, http.MethodPost, "/rooms/{room}/bookings", &bookings.CreateIn{
		Path: bookings.Path{Room: created.Room.ID},
		Body: bookings.NewBooking{Title: "Clash", Date: day, Start: start, Length: length, Organizer: ada},
	})
	if e, ok := errors.AsType[*getaclient.Error](err); !ok || e.Status != http.StatusConflict || e.Problem.Type != "/problems/overlap" {
		t.Fatal(err)
	}

	got, err := getaclient.Call[booking.GetIn, booking.Tagged](ctx, desk, http.MethodGet, "/bookings/{booking}", &booking.GetIn{Booking: booked.Booking.ID})
	must(err)
	later, err := geta.ParseTimeOfDay("13:00:00Z")
	must(err)
	changed, err := getaclient.Call[changes.PostIn, booking.Tagged](ctx, desk, http.MethodPost, "/bookings/{booking}/changes", &changes.PostIn{
		RequireConditional: geta.RequireConditional{Conditional: geta.Conditional{IfMatch: new(got.ETag)}},
		Booking:            booked.Booking.ID,
		Body:               model.Reschedule{Kind: "reschedule", Date: day, Start: later},
	})
	must(err)
	if changed.Booking.Start.String() != "13:00:00Z" || changed.ETag == got.ETag {
		t.Fatalf("%+v", changed)
	}
	_, err = getaclient.Call[changes.PostIn, booking.Tagged](ctx, desk, http.MethodPost, "/bookings/{booking}/changes", &changes.PostIn{
		Booking: booked.Booking.ID, Body: model.Rename{Kind: "rename", Title: "x"},
	})
	if e, ok := errors.AsType[*getaclient.Error](err); !ok || e.Status != http.StatusPreconditionRequired {
		t.Fatal(err)
	}

	hist, err := getaclient.Call[history.GetIn, history.History](ctx, desk, http.MethodGet, "/bookings/{booking}/history", &history.GetIn{Booking: booked.Booking.ID})
	must(err)
	if len(hist.Items) != 1 || hist.Items[0].By != "frontdesk" {
		t.Fatalf("%+v", hist)
	}
	if r, ok := hist.Items[0].Change.(model.Reschedule); !ok || r.Start.String() != "13:00:00Z" || r.Length != nil {
		t.Fatalf("%#v", hist.Items[0].Change)
	}
}
