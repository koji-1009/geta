package me

import (
	"context"
	"net/http"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/app"
	"github.com/koji-1009/geta/examples/booking/jwtauth"
)

// Route is /me: what the gate read from the caller's token.
func Route(env *app.Env) geta.Route {
	return geta.Route{
		Get: geta.Op(http.StatusOK, get, geta.Doc{Summary: "The caller, as the token names it"}),
	}
}

// Me is the token's subject, scopes, and expiry.
type Me struct {
	Subject string    `json:"subject" schema:"maxLength=200"`
	Scopes  []string  `json:"scopes" schema:"maxItems=32"`
	Expires time.Time `json:"expires"`
}

func get(ctx context.Context, _ *struct{}) (*Me, error) {
	c := jwtauth.Principal.Must(ctx)
	return &Me{Subject: c.Subject, Scopes: c.Scopes, Expires: c.Expiry.UTC()}, nil
}
