package role

import (
	"context"
	"net/http"
	"slices"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/failures"
	"github.com/koji-1009/geta/examples/register/model"
)

// Route is /admin/users/{id}/role. The /admin scope already requires an
// administrator.
func Route(env *app.Env) geta.Route {
	h := Handler{Users: env.Users}
	return geta.Route{
		Put: geta.Op(http.StatusOK, h.Put, geta.Doc{
			Summary: "Change a user's role",
			Failures: slices.Concat(
				[]geta.Failure{
					failures.UserNotFound,
					failures.LastAdmin,
				},
				failures.Store,
			),
		}),
	}
}

type Store interface {
	SetRole(ctx context.Context, id string, role model.Role) (*model.User, error)
}

type Handler struct{ Users Store }

type RoleChange struct {
	Role model.Role `json:"role" schema:"enum=admin|member"`
}

type PutIn struct {
	ID   string     `path:"id" schema:"maxLength=64"`
	Body RoleChange `body:"json"`
}

func (h Handler) Put(ctx context.Context, in *PutIn) (*model.User, error) {
	return h.Users.SetRole(ctx, in.ID, in.Body.Role)
}
