package users

import (
	"context"
	"net/http"
	"slices"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/auth"
	"github.com/koji-1009/geta/examples/register/events"
	"github.com/koji-1009/geta/examples/register/failures"
	"github.com/koji-1009/geta/examples/register/model"
	"github.com/koji-1009/geta/examples/register/store"
)

// Route is /users.
func Route(env *app.Env) geta.Route {
	h := Handler{Users: env.Users, Events: env.Events}
	return geta.Route{
		Get: geta.Op(http.StatusOK, h.List, geta.Doc{
			Summary:  "List users",
			Failures: failures.Store,
		}),
		// Anyone may list; only an administrator writes.
		Post: geta.Op(http.StatusCreated, h.Create, geta.Doc{
			Summary:  "Create a user",
			Failures: slices.Concat([]geta.Failure{failures.UserExists, failures.RoleChange}, failures.Store),
			Scope:    geta.Scope{auth.RequireAdmin()},
		}),
	}
}

// Store is what this route needs from the store.
type Store interface {
	List(ctx context.Context, f store.Filter) ([]model.User, int, error)
	Create(ctx context.Context, u model.User) error
}

// Publisher announces a change on the live feed.
type Publisher interface{ Publish(events.UserEvent) }

type Handler struct {
	Users  Store
	Events Publisher
}

type ListIn struct {
	Limit  *int        `query:"limit" schema:"minimum=1,maximum=100"`
	Offset *int        `query:"offset" schema:"minimum=0"`
	Role   *model.Role `query:"role" schema:"enum=admin|member"`
}

func (h Handler) List(ctx context.Context, in *ListIn) (*model.UserList, error) {
	f := store.Filter{Role: in.Role, Limit: 20}
	if in.Limit != nil {
		f.Limit = *in.Limit
	}
	if in.Offset != nil {
		f.Offset = *in.Offset
	}
	users, total, err := h.Users.List(ctx, f)
	if err != nil {
		return nil, err
	}
	return &model.UserList{Items: users, Total: total}, nil
}

type CreateIn struct {
	Body model.User `body:"json"`
}

// Created is a 201 with no body: the new user is at Location.
type Created struct {
	Location string `header:"Location"`
}

func (h Handler) Create(ctx context.Context, in *CreateIn) (*Created, error) {
	// A new user is a member; only the admin route makes an admin.
	if in.Body.Role != model.RoleMember {
		return nil, store.ErrRoleChange
	}
	if err := h.Users.Create(ctx, in.Body); err != nil {
		return nil, err
	}
	// Announced only after the write succeeded: a duplicate never reaches
	// the feed.
	h.Events.Publish(events.UserEvent{Kind: "created", ID: in.Body.ID})
	return &Created{Location: "/users/" + in.Body.ID}, nil
}
