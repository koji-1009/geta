package login

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/bookmarks/app"
	"github.com/koji-1009/geta/examples/bookmarks/auth"
)

// Route is /login. It is public: logging in is how a caller gets a session.
func Route(env *app.Env) geta.Route {
	h := Handler{Sessions: env.Sessions, Secure: env.SecureCookies}
	return geta.Route{
		Post: geta.Op(http.StatusOK, h.Post, geta.Doc{
			Summary:  "Log in, starting a cookie session",
			Security: []geta.Scheme{},
			Failures: []geta.Failure{geta.On(auth.ErrInvalidCredentials, http.StatusUnauthorized, "invalid credentials")},
		}),
	}
}

type Sessions interface {
	Login(ctx context.Context, user, password string) (string, error)
}

type Handler struct {
	Sessions Sessions
	Secure   bool
}

// Credentials are a JSON body, which the page sends with fetch. A JSON body
// is also a defence: an HTML form on another site can post only a form,
// which this operation answers 415, and a script there that sends JSON needs
// a preflight, which CORS refuses it.
type Credentials struct {
	User     string        `json:"user" schema:"minLength=1,maxLength=64"`
	Password geta.Password `json:"password" schema:"minLength=1,maxLength=256"`
}

type PostIn struct {
	Body Credentials `body:"json"`
}

// LoggedIn sets the session cookie and says who logged in.
type LoggedIn struct {
	Session *http.Cookie `cookie:"sid"`
	Body    User         `body:"json"`
}

type User struct {
	User string `json:"user"`
}

func (h Handler) Post(ctx context.Context, in *PostIn) (*LoggedIn, error) {
	sid, err := h.Sessions.Login(ctx, in.Body.User, string(in.Body.Password))
	if err != nil {
		return nil, err
	}
	return &LoggedIn{Session: auth.Cookie(sid, int(auth.TTL.Seconds()), h.Secure), Body: User{User: in.Body.User}}, nil
}
