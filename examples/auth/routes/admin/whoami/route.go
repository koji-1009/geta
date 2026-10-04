package whoami

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/auth/app"
	"github.com/koji-1009/geta/examples/auth/auth"
)

// Route is /admin/whoami. Bearer is declared, not inherited: the handler
// reads a principal, and only a verifier sets one.
func Route(env *app.Env) geta.Route {
	return geta.Route{
		Get: geta.Op(http.StatusOK, get, geta.Doc{Summary: "The caller identity", Security: []geta.Scheme{geta.Bearer}}),
	}
}

type Identity struct {
	Role string `json:"role"`
}

func get(ctx context.Context, _ *struct{}) (*Identity, error) {
	return &Identity{Role: auth.Caller.Must(ctx).Role}, nil
}
