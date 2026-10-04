package ping

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/app"
)

// Route is /admin/ping, an admin-only liveness check.
func Route(env *app.Env) geta.Route {
	return geta.Route{
		Get: geta.Op(http.StatusOK, get, geta.Doc{Summary: "Admin-only liveness check"}),
	}
}

type Pong struct {
	Pong bool `json:"pong"`
}

func get(ctx context.Context, _ *struct{}) (*Pong, error) { return &Pong{Pong: true}, nil }
