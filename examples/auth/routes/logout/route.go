package logout

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/auth/app"
	"github.com/koji-1009/geta/examples/auth/auth"
)

// Route is /logout. It ends the session and expires the cookie.
func Route(env *app.Env) geta.Route {
	h := Handler{Sessions: env.Sessions}
	return geta.Route{
		Post: geta.Op(http.StatusOK, h.Post, geta.Doc{
			Summary:  "Log out, ending the cookie session",
			Security: []geta.Scheme{auth.CookieAuth},
		}),
	}
}

type Sessions interface {
	Logout(ctx context.Context, sid string) error
}

type Handler struct{ Sessions Sessions }

type LoggedOut struct {
	Session *http.Cookie `cookie:"sid"`
	Body    Done         `body:"json"`
}

type Done struct {
	OK bool `json:"ok"`
}

func (h Handler) Post(ctx context.Context, _ *struct{}) (*LoggedOut, error) {
	if err := h.Sessions.Logout(ctx, auth.Caller.Must(ctx).Session); err != nil {
		return nil, err
	}
	// A cookie is deleted by its name and path, not its value.
	c := &http.Cookie{Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1}
	return &LoggedOut{Session: c, Body: Done{OK: true}}, nil
}
