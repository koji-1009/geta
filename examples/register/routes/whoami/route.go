package whoami

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/auth"
)

// Route is /whoami. Bearer is declared, not inherited: the handler needs a
// principal, and only the bearer verifier sets one.
func Route(env *app.Env) geta.Route {
	return geta.Route{
		Get: geta.Op(http.StatusOK, get, geta.Doc{
			Summary:  "The authenticated caller",
			Security: []geta.Scheme{geta.Bearer},
		}),
	}
}

type Caller struct {
	ID    string `json:"id"`
	Admin bool   `json:"admin"`
}

func get(ctx context.Context, _ *struct{}) (*Caller, error) {
	p := auth.Caller.Must(ctx)
	return &Caller{ID: p.ID, Admin: p.Admin}, nil
}
