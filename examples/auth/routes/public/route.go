package public

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/auth/app"
)

// Route is /public. It is public because it says so: an empty Security, not
// an absent one, which would inherit the bearer default and answer 401.
func Route(env *app.Env) geta.Route {
	return geta.Route{
		Get: geta.Op(http.StatusOK, get, geta.Doc{Summary: "Public", Security: []geta.Scheme{}}),
	}
}

type Message struct {
	Message string `json:"message"`
}

func get(ctx context.Context, _ *struct{}) (*Message, error) {
	return &Message{Message: "anyone can read this"}, nil
}
