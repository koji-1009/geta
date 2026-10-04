package bookmark

import (
	"context"
	"net/http"
	"uuid"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/bookmarks/app"
	"github.com/koji-1009/geta/examples/bookmarks/auth"
	"github.com/koji-1009/geta/examples/bookmarks/store"
)

// Route is /bookmarks/{bookmark}, one of the caller's bookmarks. Another
// user's is not found, as one that does not exist is.
func Route(env *app.Env) geta.Route {
	h := Handler{Bookmarks: env.Bookmarks}
	return geta.Route{
		Get: geta.Op(http.StatusOK, h.Get, geta.Doc{
			Summary:  "Fetch a bookmark",
			Failures: []geta.Failure{geta.On(store.ErrNotFound, http.StatusNotFound, "bookmark not found")},
		}),
	}
}

type Store interface {
	Find(ctx context.Context, owner string, id uuid.UUID) (store.Bookmark, error)
}

type Handler struct{ Bookmarks Store }

type GetIn struct {
	Bookmark uuid.UUID `path:"bookmark"`
}

func (h Handler) Get(ctx context.Context, in *GetIn) (*store.Bookmark, error) {
	b, err := h.Bookmarks.Find(ctx, auth.Caller.Must(ctx).User, in.Bookmark)
	if err != nil {
		return nil, err
	}
	return &b, nil
}
