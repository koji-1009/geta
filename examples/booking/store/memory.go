// Package store keeps rooms and bookings in memory, and holds the booking
// rules: a booking lies within its room's hours, overlaps no other
// confirmed booking of the room, and lasts whole minutes from 15 minutes to
// 8 hours. Its errors are the rows of the routes' failure tables.
package store

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"
	"uuid"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/model"
)

// Errors the store returns.
var (
	ErrRoomNotFound    = errors.New("store: room not found")
	ErrBookingNotFound = errors.New("store: booking not found")
	ErrOutsideHours    = errors.New("store: the booking is outside the room's hours")
	ErrLength          = errors.New("store: a booking lasts whole minutes, from 15 minutes to 8 hours")
	ErrCancelled       = errors.New("store: the booking is cancelled")
	// ErrNotOwner is a change by a caller who neither made the booking nor
	// may change any booking. Which booking is whose is known only once it
	// is loaded, so this is the store's to say, not a gate's.
	ErrNotOwner = errors.New("store: the booking is someone else's")
)

// OverlapError is a booking that would overlap another confirmed booking of
// the room. A failure row matches it by type (geta.OnAs).
type OverlapError struct{ With uuid.UUID }

func (e *OverlapError) Error() string { return "store: overlaps booking " + e.With.String() }

// Memory is the store, safe for concurrent use.
type Memory struct {
	mu       sync.Mutex
	now      func() time.Time
	rooms    map[uuid.UUID]model.Room
	bookings map[uuid.UUID]*record
}

type record struct {
	b model.Booking
	// owner is the subject that made the booking.
	owner   string
	version int
	history []model.Entry
}

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{now: time.Now, rooms: map[uuid.UUID]model.Room{}, bookings: map[uuid.UUID]*record{}}
}

// CreateRoom stores a room under a new id.
func (m *Memory) CreateRoom(ctx context.Context, r model.Room) (*model.Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.ID = uuid.NewV7()
	m.rooms[r.ID] = r
	return &r, nil
}

// RoomFilter narrows a listing of rooms; a zero field does not narrow it.
type RoomFilter struct {
	// Features are what a room must all have.
	Features []string
	// MinCapacity and MaxCapacity bound the room's capacity.
	MinCapacity, MaxCapacity *int
	Floor                    *int
	// Limit is the most rooms listed; 0 lists every one.
	Limit int
}

func (f RoomFilter) admits(r model.Room) bool {
	for _, want := range f.Features {
		if !slices.Contains(r.Features, want) {
			return false
		}
	}
	return (f.MinCapacity == nil || r.Capacity >= *f.MinCapacity) &&
		(f.MaxCapacity == nil || r.Capacity <= *f.MaxCapacity) &&
		(f.Floor == nil || r.Floor == *f.Floor)
}

// Rooms lists the rooms f admits by id, which orders them by creation.
func (m *Memory) Rooms(ctx context.Context, f RoomFilter) ([]model.Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Room, 0, len(m.rooms))
	for _, r := range m.rooms {
		if f.admits(r) {
			r.Features = slices.Clone(r.Features)
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b model.Room) int { return cmp.Compare(a.ID.String(), b.ID.String()) })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// Room returns one room.
func (m *Memory) Room(ctx context.Context, id uuid.UUID) (*model.Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[id]
	if !ok {
		return nil, ErrRoomNotFound
	}
	return &r, nil
}

// Book stores a new confirmed booking made by owner, pricing it by the
// room's rate, and returns it with its entity tag.
func (m *Memory) Book(ctx context.Context, owner string, b model.Booking) (*model.Booking, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	room, ok := m.rooms[b.Room]
	if !ok {
		return nil, "", ErrRoomNotFound
	}
	b.ID, b.Status, b.CreatedAt = uuid.NewV7(), model.Confirmed, m.now().UTC().Truncate(time.Second)
	if err := m.fits(room, &b); err != nil {
		return nil, "", err
	}
	rec := &record{b: b, owner: owner, version: 1}
	m.bookings[b.ID] = rec
	return &rec.b, rec.tag(), nil
}

// Bookings lists a room's bookings on a day, by start.
func (m *Memory) Bookings(ctx context.Context, room uuid.UUID, day geta.Date) ([]model.Booking, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rooms[room]; !ok {
		return nil, ErrRoomNotFound
	}
	out := []model.Booking{}
	for _, rec := range m.bookings {
		if rec.b.Room == room && rec.b.Date == day {
			out = append(out, rec.b)
		}
	}
	slices.SortFunc(out, func(a, b model.Booking) int {
		sa, _ := a.Start.On(a.Date)
		sb, _ := b.Start.On(b.Date)
		return sa.Compare(sb)
	})
	return out, nil
}

// Booking returns one booking and its entity tag.
func (m *Memory) Booking(ctx context.Context, id uuid.UUID) (*model.Booking, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.bookings[id]
	if !ok {
		return nil, "", ErrBookingNotFound
	}
	b := rec.b
	return &b, rec.tag(), nil
}

// History returns a booking's changes, oldest first.
func (m *Memory) History(ctx context.Context, id uuid.UUID) ([]model.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.bookings[id]
	if !ok {
		return nil, ErrBookingNotFound
	}
	return slices.Clone(rec.history), nil
}

// Apply applies a change made by by. Only the booking's owner may change it,
// unless manage is set (by may change every booking); anyone else gets
// ErrNotOwner, before the precondition is looked at (the caller may not act,
// whatever version it holds). check is called with the
// booking's current entity tag before anything is written, under the same
// lock, so no other write lands between the check and this one; its error
// is returned as is.
func (m *Memory) Apply(ctx context.Context, id uuid.UUID, by string, manage bool, c model.Change, check func(tag string) error) (*model.Booking, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.bookings[id]
	if !ok {
		return nil, "", ErrBookingNotFound
	}
	if !manage && rec.owner != by {
		return nil, "", ErrNotOwner
	}
	if err := check(rec.tag()); err != nil {
		return nil, "", err
	}
	if rec.b.Status == model.Cancelled {
		return nil, "", ErrCancelled
	}
	next := rec.b
	var touched []geta.JSONPointer
	switch c := c.(type) {
	case model.Rename:
		next.Title = c.Title
		touched = []geta.JSONPointer{geta.NewJSONPointer("title")}
	case model.Reschedule:
		next.Date, next.Start = c.Date, c.Start
		touched = []geta.JSONPointer{geta.NewJSONPointer("date"), geta.NewJSONPointer("start")}
		if c.Length != nil {
			next.Length = *c.Length
			touched = append(touched, geta.NewJSONPointer("length"), geta.NewJSONPointer("price"))
		}
		if err := m.fits(m.rooms[next.Room], &next); err != nil {
			return nil, "", err
		}
	case model.Cancel:
		next.Status, next.CancelReason = model.Cancelled, &c.Reason
		touched = []geta.JSONPointer{geta.NewJSONPointer("status"), geta.NewJSONPointer("cancelReason")}
	default:
		return nil, "", fmt.Errorf("store: unknown change %T", c)
	}
	at, err := geta.DateTimeOf(m.now().UTC().Truncate(time.Second))
	if err != nil {
		return nil, "", err
	}
	rec.b = next
	rec.version++
	rec.history = append(rec.history, model.Entry{At: at, By: by, Change: c, Touched: touched})
	b := rec.b
	return &b, rec.tag(), nil
}

// fits checks b against the room's hours, its length rule, and the room's
// other confirmed bookings, and prices it.
func (m *Memory) fits(room model.Room, b *model.Booking) error {
	length, err := b.Length.Std()
	if err != nil || length < 15*time.Minute || length > 8*time.Hour || length%time.Minute != 0 {
		return ErrLength
	}
	start, err := b.Start.On(b.Date)
	if err != nil {
		return ErrOutsideHours
	}
	end := start.Add(length)
	opens, err1 := room.Opens.On(b.Date)
	closes, err2 := room.Closes.On(b.Date)
	if err1 != nil || err2 != nil || start.Before(opens) || end.After(closes) {
		return ErrOutsideHours
	}
	for _, other := range m.bookings {
		o := other.b
		if o.ID == b.ID || o.Room != b.Room || o.Status != model.Confirmed {
			continue
		}
		os, _ := o.Start.On(o.Date)
		ol, _ := o.Length.Std()
		if start.Before(os.Add(ol)) && os.Before(end) {
			return &OverlapError{With: o.ID}
		}
	}
	minutes := int64(length / time.Minute)
	b.Price = model.Cents((room.HourlyRate.Cents()*minutes + 59) / 60)
	return nil
}

// tag is the entity tag of the booking's current version.
func (r *record) tag() string { return `"` + strconv.Itoa(r.version) + `"` }
