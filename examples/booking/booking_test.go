package main

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/model"
	"github.com/koji-1009/geta/examples/booking/routes"
	"github.com/koji-1009/geta/examples/booking/routes/bookings/booking_/history"
	"github.com/koji-1009/geta/getatest"
)

// The documents are committed; a change to either is a reviewed diff.
func TestDocuments(t *testing.T) {
	h := newHarness(t)
	getatest.Golden(t, h.anon.App(), "openapi.json")
	c32 := getatest.New(t, routes.Table(h.env), append(routes.Options(h.env), geta.WithOpenAPI(geta.OpenAPI32))...)
	getatest.Golden(t, c32.App(), "openapi.3.2.json")
}

func TestBookAndChange(t *testing.T) {
	h := newHarness(t)
	desk, member, viewer := h.as("frontdesk"), h.as("member"), h.as("viewer")

	// The gate: no token is a bare challenge; a token without the scope a
	// write needs is a 403 naming it.
	res := status(t, h.anon.Get("/rooms"), http.StatusUnauthorized)
	if res.Header.Get("WWW-Authenticate") != "Bearer" {
		t.Fatal(res.Header)
	}
	res = status(t, h.anon.Bearer("not.a.jwt").Get("/rooms"), http.StatusUnauthorized)
	if !strings.HasPrefix(res.Header.Get("WWW-Authenticate"), `Bearer error="invalid_token"`) {
		t.Fatal(res.Header)
	}
	newRoom := map[string]any{"name": "Kiku", "capacity": 4, "opens": "08:00:00+09:00", "closes": "18:00:00+09:00", "hourlyRate": "1200.00"}
	res = status(t, member.Post("/rooms", newRoom), http.StatusForbidden)
	if !strings.Contains(res.Header.Get("WWW-Authenticate"), `error="insufficient_scope", scope="rooms:write"`) {
		t.Fatal(res.Header)
	}

	// Rooms carry format types (uuid, time, ipv4, ipv6) and Money, a type
	// that writes its own JSON, checked first against its declared pattern.
	loc := h.room(desk, "Matsu")
	room := status(t, viewer.Get(loc), http.StatusOK).JSON[model.Room]()
	if room.Name != "Matsu" || room.HourlyRate.Cents() != 120000 || room.Panel.String() != "192.0.2.10" || room.Panel6.String() != "2001:db8::10" ||
		room.Opens.String() != "08:00:00+09:00" || loc != "/rooms/"+room.ID.String() {
		t.Fatalf("%+v", room)
	}
	for name, body := range map[string]map[string]any{
		"money as a number":     {"name": "X", "capacity": 1, "opens": "08:00:00Z", "closes": "09:00:00Z", "hourlyRate": 12.5},
		"money with one place":  {"name": "X", "capacity": 1, "opens": "08:00:00Z", "closes": "09:00:00Z", "hourlyRate": "12.5"},
		"a time with no offset": {"name": "X", "capacity": 1, "opens": "08:00:00", "closes": "09:00:00Z", "hourlyRate": "1.00"},
		"an ipv4 past 255":      {"name": "X", "capacity": 1, "opens": "08:00:00Z", "closes": "09:00:00Z", "hourlyRate": "1.00", "panel": "192.0.2.256"},
		"an ipv6 zone":          {"name": "X", "capacity": 1, "opens": "08:00:00Z", "closes": "09:00:00Z", "hourlyRate": "1.00", "panel6": "fe80::1%eth0"},
	} {
		if res := desk.Post("/rooms", body); res.Status != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, res.Status, res.Body)
		}
	}
	status(t, desk.Post("/rooms", map[string]any{"name": "X", "capacity": 1, "opens": "09:00:00Z", "closes": "08:00:00Z", "hourlyRate": "1.00"}),
		http.StatusUnprocessableEntity)
	status(t, viewer.Get("/rooms/not-a-uuid"), http.StatusBadRequest)
	problemType(t, viewer.Get("/rooms/0192d3a4-5b6c-7d8e-9f00-112233445566"), http.StatusNotFound, "/problems/room-not-found")

	// A booking: a date, a time of day with its offset, a duration, and
	// email addresses in this application's own format type.
	book := func(c *getatest.Client, title, start, length string) *getatest.Response {
		return c.Post(loc+"/bookings", map[string]any{"title": title, "date": "2026-10-05", "start": start, "length": length,
			"organizer": "ada@example.com", "attendees": []string{"bo@example.com"}})
	}
	res = status(t, book(member, "Standup", "09:00:00+09:00", "PT30M"), http.StatusCreated)
	b := res.JSON[model.Booking]()
	tag := res.Header.Get("ETag")
	if b.Price.String() != "600.00" || b.Status != model.Confirmed || b.Organizer.String() != "ada@example.com" ||
		res.Header.Get("Location") != "/bookings/"+b.ID.String() || tag != `"1"` {
		t.Fatalf("%v %+v", res.Header, b)
	}
	problemType(t, book(member, "Clash", "09:15:00+09:00", "PT1H"), http.StatusConflict, "/problems/overlap")
	// 08:15 at +08:00 is 09:15 at +09:00: offsets are compared as instants.
	problemType(t, book(member, "Clash", "08:15:00+08:00", "PT30M"), http.StatusConflict, "/problems/overlap")
	problemType(t, book(member, "Late", "17:30:00+09:00", "PT1H"), http.StatusUnprocessableEntity, "/problems/outside-hours")
	problemType(t, book(member, "Short", "12:00:00+09:00", "PT5M"), http.StatusUnprocessableEntity, "/problems/length")
	problemType(t, book(member, "Days", "12:00:00+09:00", "P1D"), http.StatusUnprocessableEntity, "/problems/length")
	status(t, book(viewer, "No scope", "12:00:00+09:00", "PT1H"), http.StatusForbidden)
	for name, body := range map[string]map[string]any{
		"an address this app does not take": {"title": "T", "date": "2026-10-05", "start": "12:00:00Z", "length": "PT1H", "organizer": "ada"},
		"no such day":                       {"title": "T", "date": "2026-02-30", "start": "12:00:00Z", "length": "PT1H", "organizer": "a@b.c"},
		"a duration with a gap":             {"title": "T", "date": "2026-10-05", "start": "12:00:00Z", "length": "P1Y2D", "organizer": "a@b.c"},
	} {
		if res := member.Post(loc+"/bookings", body); res.Status != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, res.Status, res.Body)
		}
	}
	list := status(t, viewer.Get(loc+"/bookings?date=2026-10-05"), http.StatusOK).Text()
	if strings.Count(list, `"id"`) != 1 {
		t.Fatal(list)
	}
	status(t, viewer.Get(loc+"/bookings?date=10/05/2026"), http.StatusBadRequest)

	// A fetch is conditional; a change must name the version it was made
	// against.
	at := "/bookings/" + b.ID.String()
	status(t, viewer.With("If-None-Match", tag).Get(at), http.StatusNotModified)
	status(t, member.Post(at+"/changes", map[string]any{"kind": "rename", "title": "Daily"}), http.StatusPreconditionRequired)
	res = status(t, member.With("If-Match", tag).Post(at+"/changes", map[string]any{"kind": "rename", "title": "Daily"}), http.StatusOK)
	next := res.Header.Get("ETag")
	if res.JSON[model.Booking]().Title != "Daily" || next == tag {
		t.Fatal(res.Header, res.Text())
	}
	status(t, member.With("If-Match", tag).Post(at+"/changes", map[string]any{"kind": "rename", "title": "Lost"}), http.StatusPreconditionFailed)
	// The body is a sealed type: its kind picks the variant it is checked
	// against.
	for name, body := range map[string]any{
		"an unknown kind":           map[string]any{"kind": "move", "title": "X"},
		"no kind":                   map[string]any{"title": "X"},
		"a member of another kind":  map[string]any{"kind": "rename", "reason": "X"},
		"a reschedule with no date": map[string]any{"kind": "reschedule", "start": "10:00:00+09:00"},
	} {
		if res := member.With("If-Match", next).Post(at+"/changes", body); res.Status != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, res.Status, res.Body)
		}
	}
	res = status(t, member.With("If-Match", next).Post(at+"/changes",
		map[string]any{"kind": "reschedule", "date": "2026-10-05", "start": "10:00:00+09:00", "length": "PT1H30M"}), http.StatusOK)
	if r := res.JSON[model.Booking](); r.Start.String() != "10:00:00+09:00" || r.Price.String() != "1800.00" {
		t.Fatal(res.Text())
	}
	next = res.Header.Get("ETag")
	res = status(t, member.With("If-Match", next).Post(at+"/changes", map[string]any{"kind": "cancel", "reason": "Holiday"}), http.StatusOK)
	next = res.Header.Get("ETag")
	problemType(t, member.With("If-Match", next).Post(at+"/changes", map[string]any{"kind": "rename", "title": "X"}),
		http.StatusConflict, "/problems/cancelled")
	// A cancelled booking frees its slot.
	status(t, book(member, "Again", "10:00:00+09:00", "PT1H"), http.StatusCreated)

	// The history answers each change as the variant it was made as, with
	// the members it wrote as JSON Pointers.
	hist := status(t, viewer.Get(at+"/history"), http.StatusOK).JSON[history.History]()
	var kinds, touched []string
	for _, e := range hist.Items {
		switch c := e.Change.(type) {
		case model.Rename:
			kinds = append(kinds, "rename:"+c.Title)
		case model.Reschedule:
			kinds = append(kinds, "reschedule:"+c.Start.String())
		case model.Cancel:
			kinds = append(kinds, "cancel:"+c.Reason)
		}
		for _, p := range e.Touched {
			touched = append(touched, p.String())
		}
		if e.By != "member" || e.At.Time().IsZero() {
			t.Fatalf("%+v", e)
		}
	}
	if !slices.Equal(kinds, []string{"rename:Daily", "reschedule:10:00:00+09:00", "cancel:Holiday"}) ||
		strings.Join(touched, " ") != "/title /date /start /length /price /status /cancelReason" {
		t.Fatal(kinds, touched)
	}
	problemType(t, viewer.Get("/bookings/0192d3a4-5b6c-7d8e-9f00-112233445566/history"), http.StatusNotFound, "/problems/booking-not-found")

	if me := status(t, member.Get("/me"), http.StatusOK).Text(); !strings.Contains(me, `"subject":"member","scopes":["bookings:write"]`) {
		t.Fatal(me)
	}
}

// bookings:write lets a caller change its own bookings; another's needs
// bookings:manage. The refusal comes from the store, which alone knows whose
// booking it is, through the failure table, and writes nothing.
func TestChangeOnlyOwnBooking(t *testing.T) {
	h := newHarness(t)
	desk, member, guest := h.as("frontdesk"), h.as("member"), h.as("guest")
	loc := h.room(desk, "Ume")
	res := status(t, member.Post(loc+"/bookings", map[string]any{"title": "Mine", "date": "2026-10-05", "start": "09:00:00+09:00",
		"length": "PT30M", "organizer": "ada@example.com"}), http.StatusCreated)
	at, tag := res.Header.Get("Location"), res.Header.Get("ETag")

	for name, body := range map[string]map[string]any{
		"rename": {"kind": "rename", "title": "Taken"},
		"cancel": {"kind": "cancel", "reason": "Not mine to cancel"},
	} {
		res := guest.With("If-Match", tag).Post(at+"/changes", body)
		if res.Status != http.StatusForbidden || res.Problem().Type != "/problems/not-owner" {
			t.Errorf("%s by another member: %d %s", name, res.Status, res.Body)
		}
	}
	// Refused whatever version it names: ownership is decided first.
	problemType(t, guest.With("If-Match", `"9"`).Post(at+"/changes", map[string]any{"kind": "rename", "title": "Taken"}),
		http.StatusForbidden, "/problems/not-owner")
	if b := status(t, member.Get(at), http.StatusOK); b.Header.Get("ETag") != tag || b.JSON[model.Booking]().Title != "Mine" {
		t.Fatal(b.Header, b.Text())
	}

	// The front desk may change any booking; the owner its own.
	res = status(t, desk.With("If-Match", tag).Post(at+"/changes", map[string]any{"kind": "rename", "title": "Desk"}), http.StatusOK)
	res = status(t, member.With("If-Match", res.Header.Get("ETag")).Post(at+"/changes", map[string]any{"kind": "cancel", "reason": "Done"}), http.StatusOK)
	if res.JSON[model.Booking]().Status != model.Cancelled {
		t.Fatal(res.Text())
	}
}
