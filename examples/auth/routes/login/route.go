package login

import (
	"context"
	"net/http"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/auth/app"
	"github.com/koji-1009/geta/examples/auth/auth"
	"github.com/koji-1009/geta/examples/auth/ratelimit"
)

// Route is /login, the target of a login form. It is public: logging in is
// how a caller becomes authenticated.
//
// Login attempts are rate limited per client address, so a client cannot
// guess passwords at the speed of the network. The limiter is this
// example's own (package ratelimit), declaring its 429 and Retry-After; it
// is the operation's Doc.BeforeGate: geta runs it ahead of the root scope's
// gate, for a login alone, and documents its 429 here alone.
//
// Behind a reverse proxy or a load balancer, r.RemoteAddr is the proxy's
// address, and every client would share one bucket. There, key by the
// client address your proxy records instead: the last X-Forwarded-For
// entry (the one your own proxy appended), or the proxy's own header
// (X-Real-IP, CF-Connecting-IP). Read it only from a proxy you run: a
// header a client sends unchecked lets it pick a fresh key per attempt.
func Route(env *app.Env) geta.Route {
	h := Handler{Sessions: env.Sessions}
	return geta.Route{
		Post: geta.Op(http.StatusOK, h.Post, geta.Doc{
			Summary:    "Log in, starting a cookie session",
			Security:   []geta.Scheme{},
			Failures:   []geta.Failure{geta.On(auth.ErrInvalidCredentials, http.StatusUnauthorized, "invalid credentials")},
			BeforeGate: geta.Scope{ratelimit.PerKey(ratelimit.ByRemoteAddr, env.LoginsPerMinute, time.Minute)},
		}),
	}
}

type Sessions interface {
	Login(ctx context.Context, name, password string) (string, error)
}

type Handler struct{ Sessions Sessions }

// Credentials are the login form's fields, as a browser posts an HTML form
// (application/x-www-form-urlencoded). The password is a geta.Password:
// documented as format password, and printed as [redacted] by fmt and
// log/slog, so a log line that includes the input does not leak it.
type Credentials struct {
	Username string        `form:"username" schema:"minLength=1,maxLength=64"`
	Password geta.Password `form:"password" schema:"minLength=1,maxLength=256"`
}

type PostIn struct {
	Body Credentials `body:"form"`
}

type LoggedIn struct {
	Session *http.Cookie `cookie:"sid"`
	Body    Role         `body:"json"`
}

type Role struct {
	Role string `json:"role"`
}

func (h Handler) Post(ctx context.Context, in *PostIn) (*LoggedIn, error) {
	sid, err := h.Sessions.Login(ctx, in.Body.Username, string(in.Body.Password))
	if err != nil {
		return nil, err
	}
	// HttpOnly keeps the id from scripts; SameSite=Lax keeps it off
	// cross-site POSTs. Secure is left off because the demo serves plain
	// HTTP; production over TLS adds it. Max-Age is the session's own
	// lifetime, which the server enforces whatever the client keeps.
	c := &http.Cookie{Value: sid, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(auth.SessionTTL / time.Second)}
	return &LoggedIn{Session: c, Body: Role{Role: in.Body.Username}}, nil
}
