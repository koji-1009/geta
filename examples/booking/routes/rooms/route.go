package rooms

import (
	"context"
	"errors"
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/app"
	"github.com/koji-1009/geta/examples/booking/auth"
	"github.com/koji-1009/geta/examples/booking/model"
	"github.com/koji-1009/geta/examples/booking/store"
)

// Route is /rooms. Any caller with a token lists; creating a room needs the
// rooms:write scope.
func Route(env *app.Env) geta.Route {
	h := Handler{Rooms: env.Store}
	return geta.Route{
		Get: geta.Op(http.StatusOK, h.List, geta.Doc{
			Summary:     "List rooms",
			Description: "Every room, or those that match the filters given: all of them, when several are.",
		}),
		Post: geta.Op(http.StatusCreated, h.Create, geta.Doc{
			Summary:  "Create a room",
			Failures: []geta.Failure{geta.On(ErrHours, http.StatusUnprocessableEntity, "a room closes after it opens")},
			Scope:    auth.Require(auth.ScopeWriteRooms),
		}),
	}
}

// ErrHours is a room whose closing time is not after its opening time.
var ErrHours = errors.New("a room closes after it opens")

type Store interface {
	Rooms(ctx context.Context, f store.RoomFilter) ([]model.Room, error)
	CreateRoom(ctx context.Context, r model.Room) (*model.Room, error)
}

type Handler struct{ Rooms Store }

// ListIn filters the rooms. Each filter is optional, and its description is
// the doc tag beside it.
//
//   - feature is repeated: ?feature=projector&feature=video asks for rooms
//     with both. Its type is *[]string, every value of the parameter.
//   - capacity is a deepObject, a struct-typed query parameter:
//     ?capacity[min]=4&capacity[max]=10. A key CapacityRange does not name
//     (capacity[x]) is a 400.
//   - floor is an integer enum: a floor the building lacks is a 400.
//   - limit has a default, so a request may leave it out and the handler
//     still reads 20; the document lists it as not required, with its
//     default and examples.
type ListIn struct {
	Feature  *[]string      `query:"feature" doc:"Only rooms with every feature named; repeat the parameter to name several" schema:"maxItems=8"`
	Capacity *CapacityRange `query:"capacity" doc:"Only rooms whose capacity is within the range"`
	Floor    *int           `query:"floor" doc:"Only rooms on this floor" schema:"enum=1|2|3"`
	Limit    int            `query:"limit" doc:"At most this many rooms" schema:"minimum=1,maximum=100,default=20,examples=5|50"`
}

// CapacityRange is the capacity a room must have: at least Min people, at
// most Max. Its members are bound as form fields are.
type CapacityRange struct {
	Min *int `form:"min" doc:"At least this many people" schema:"minimum=1,maximum=500,examples=4"`
	Max *int `form:"max" doc:"At most this many people" schema:"minimum=1,maximum=500"`
}

// RoomList is every room that matched.
type RoomList struct {
	Items []model.Room `json:"items"`
}

func (h Handler) List(ctx context.Context, in *ListIn) (*RoomList, error) {
	f := store.RoomFilter{Floor: in.Floor, Limit: in.Limit}
	if in.Feature != nil {
		f.Features = *in.Feature
	}
	if in.Capacity != nil {
		f.MinCapacity, f.MaxCapacity = in.Capacity.Min, in.Capacity.Max
	}
	rs, err := h.Rooms.Rooms(ctx, f)
	if err != nil {
		return nil, err
	}
	return &RoomList{Items: rs}, nil
}

// NewRoom is a room as a client describes it; the server gives it its id.
type NewRoom struct {
	Name       string         `json:"name" schema:"minLength=1,maxLength=80"`
	Capacity   int            `json:"capacity" schema:"minimum=1,maximum=500"`
	Opens      geta.TimeOfDay `json:"opens"`
	Closes     geta.TimeOfDay `json:"closes"`
	HourlyRate model.Money    `json:"hourlyRate"`
	Panel      *geta.IPv4     `json:"panel,omitzero"`
	Panel6     *geta.IPv6     `json:"panel6,omitzero"`
	Features   *[]string      `json:"features,omitzero" doc:"What the room has; none when left out" schema:"maxItems=8,uniqueItems=true"`
	// Floor has a default: a request may leave it out, and the room is then
	// on floor 1.
	Floor int `json:"floor" doc:"The floor the room is on" schema:"enum=1|2|3,default=1"`
}

type CreateIn struct {
	Body NewRoom `body:"json"`
}

// RoomCreated is the new room and where it lives.
type RoomCreated struct {
	Location string     `header:"Location"`
	Room     model.Room `body:"json"`
}

func (h Handler) Create(ctx context.Context, in *CreateIn) (*RoomCreated, error) {
	b := in.Body
	// Any day will do to compare two times of day with their offsets.
	day, _ := geta.NewDate(2000, 1, 1)
	opens, err1 := b.Opens.On(day)
	closes, err2 := b.Closes.On(day)
	if err1 != nil || err2 != nil || !closes.After(opens) {
		return nil, ErrHours
	}
	features := []string{}
	if b.Features != nil {
		features = *b.Features
	}
	r, err := h.Rooms.CreateRoom(ctx, model.Room{Name: b.Name, Capacity: b.Capacity, Opens: b.Opens, Closes: b.Closes,
		HourlyRate: b.HourlyRate, Panel: b.Panel, Panel6: b.Panel6, Features: features, Floor: b.Floor})
	if err != nil {
		return nil, err
	}
	return &RoomCreated{Location: "/rooms/" + r.ID.String(), Room: *r}, nil
}
