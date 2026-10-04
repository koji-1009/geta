package logout

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/bookmarks/app"
	"github.com/koji-1009/geta/examples/bookmarks/auth"
)

// Route is /logout: it ends the caller's session and removes the cookie.
func Route(env *app.Env) geta.Route {
	h := Handler{Sessions: env.Sessions, Secure: env.SecureCookies}
	return geta.Route{
		Post: geta.Op(http.StatusNoContent, h.Post, geta.Doc{Summary: "Log out, ending the session"}),
	}
}

type Sessions interface {
	Logout(ctx context.Context, sid string) error
}

type Handler struct {
	Sessions Sessions
	Secure   bool
}

// LoggedOut removes the session cookie: the same cookie, empty, with a
// negative MaxAge (Max-Age=0 on the wire). An envelope with no body field
// answers 204.
type LoggedOut struct {
	Session *http.Cookie `cookie:"sid"`
}

func (h Handler) Post(ctx context.Context, _ *struct{}) (*LoggedOut, error) {
	if err := h.Sessions.Logout(ctx, auth.Caller.Must(ctx).Session); err != nil {
		return nil, err
	}
	return &LoggedOut{Session: auth.Cookie("", -1, h.Secure)}, nil
}
