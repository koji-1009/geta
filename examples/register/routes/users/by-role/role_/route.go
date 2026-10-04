package byrole

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/failures"
	"github.com/koji-1009/geta/examples/register/model"
	"github.com/koji-1009/geta/examples/register/store"
)

// Route is /users/by-role/{role}: the same listing as /users?role=, with the
// role in the path, so an unknown role is a 400 before the handler runs. It
// pages as /users does (limit up to 100, 20 by default): a listing with no
// bound answers as many users as the store holds in one response.
func Route(env *app.Env) geta.Route {
	h := Handler{Users: env.Users}
	return geta.Route{
		Get: geta.Op(http.StatusOK, h.List, geta.Doc{
			Summary:  "List users of a role",
			Failures: failures.Store,
		}),
	}
}

type Store interface {
	List(ctx context.Context, f store.Filter) ([]model.User, int, error)
}

type Handler struct{ Users Store }

type ListIn struct {
	Role   model.Role `path:"role" schema:"enum=admin|member"`
	Limit  *int       `query:"limit" schema:"minimum=1,maximum=100"`
	Offset *int       `query:"offset" schema:"minimum=0"`
}

func (h Handler) List(ctx context.Context, in *ListIn) (*model.UserList, error) {
	f := store.Filter{Role: &in.Role, Limit: 20}
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
