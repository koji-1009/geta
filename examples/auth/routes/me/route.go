package me

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/auth/app"
	"github.com/koji-1009/geta/examples/auth/auth"
)

// Route is /me, the session caller.
func Route(env *app.Env) geta.Route {
	return geta.Route{
		Get: geta.Op(http.StatusOK, get, geta.Doc{
			Summary:  "The session caller identity",
			Security: []geta.Scheme{auth.CookieAuth},
		}),
	}
}

type SessionRole struct {
	Role string `json:"role"`
}

func get(ctx context.Context, _ *struct{}) (*SessionRole, error) {
	return &SessionRole{Role: auth.Caller.Must(ctx).Role}, nil
}
