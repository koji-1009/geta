package tag

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/failures"
	"github.com/koji-1009/geta/examples/register/model"
)

// Route is /users/{id}/tags/{index}.
func Route(env *app.Env) geta.Route {
	h := Handler{Users: env.Users}
	return geta.Route{
		Get: geta.Op(http.StatusOK, h.Get, geta.Doc{
			Summary: "Read one tag by index",
			Failures: slices.Concat([]geta.Failure{
				failures.UserNotFound,
				geta.On(ErrOutOfRange, http.StatusNotFound, "tag index out of range"),
			}, failures.Store),
		}),
	}
}

var ErrOutOfRange = errors.New("tag index out of range")

type Store interface {
	Find(ctx context.Context, id string) (*model.User, error)
}

type Handler struct{ Users Store }

type GetIn struct {
	ID    string `path:"id" schema:"maxLength=64"`
	Index int    `path:"index" schema:"minimum=0"`
}

type Tag struct {
	Tag string `json:"tag"`
}

func (h Handler) Get(ctx context.Context, in *GetIn) (*Tag, error) {
	u, err := h.Users.Find(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if in.Index >= len(u.Tags) {
		return nil, ErrOutOfRange
	}
	return &Tag{Tag: u.Tags[in.Index]}, nil
}
