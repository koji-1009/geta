package userstats

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/failures"
	"github.com/koji-1009/geta/examples/register/model"
	"github.com/koji-1009/geta/examples/register/store"
)

// Route is /stats/users.
func Route(env *app.Env) geta.Route {
	h := Handler{Users: env.Users}
	return geta.Route{
		Get: geta.Op(http.StatusOK, h.Get, geta.Doc{
			Summary:  "Count users and admins",
			Failures: failures.Store,
		}),
	}
}

type Store interface {
	List(ctx context.Context, f store.Filter) ([]model.User, int, error)
}

type Handler struct{ Users Store }

type UserStats struct {
	Total  int `json:"total"`
	Admins int `json:"admins"`
}

func (h Handler) Get(ctx context.Context, _ *struct{}) (*UserStats, error) {
	_, total, err := h.Users.List(ctx, store.Filter{})
	if err != nil {
		return nil, err
	}
	admin := model.RoleAdmin
	_, admins, err := h.Users.List(ctx, store.Filter{Role: &admin})
	if err != nil {
		return nil, err
	}
	return &UserStats{Total: total, Admins: admins}, nil
}
