package userimport

import (
	"context"
	"net/http"
	"slices"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/auth"
	"github.com/koji-1009/geta/examples/register/failures"
	"github.com/koji-1009/geta/examples/register/model"
	"github.com/koji-1009/geta/examples/register/store"
)

// Route is /users/import: many users in one request, all or none.
func Route(env *app.Env) geta.Route {
	h := Handler{Users: env.Users}
	return geta.Route{
		Post: geta.Op(http.StatusCreated, h.Post, geta.Doc{
			Summary: "Create many users at once, all or none",
			Failures: slices.Concat(
				[]geta.Failure{
					geta.On(store.ErrConflict, http.StatusConflict, "a user in the batch already exists, or appears twice").Type("/problems/user-exists"),
					failures.RoleChange,
				},
				failures.Store,
			),
			// Like the other user writes, only an administrator imports.
			Scope: geta.Scope{auth.RequireAdmin()},
		}),
	}
}

type Store interface {
	CreateMany(ctx context.Context, us []model.User) error
}

type Handler struct{ Users Store }

type Batch struct {
	Users []model.User `json:"users" schema:"minItems=1,maxItems=100"`
}

type PostIn struct {
	Body Batch `body:"json"`
}

type Imported struct {
	Created int `json:"created"`
}

func (h Handler) Post(ctx context.Context, in *PostIn) (*Imported, error) {
	// A new user is a member; only the admin route makes an admin. One
	// other role refuses the whole batch.
	for _, u := range in.Body.Users {
		if u.Role != model.RoleMember {
			return nil, store.ErrRoleChange
		}
	}
	if err := h.Users.CreateMany(ctx, in.Body.Users); err != nil {
		return nil, err
	}
	return &Imported{Created: len(in.Body.Users)}, nil
}
