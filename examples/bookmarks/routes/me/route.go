package me

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/bookmarks/app"
	"github.com/koji-1009/geta/examples/bookmarks/auth"
)

// Route is /me, the caller of the session.
func Route(env *app.Env) geta.Route {
	return geta.Route{
		Get: geta.Op(http.StatusOK, get, geta.Doc{Summary: "Who the session is"}),
	}
}

// GetIn reads a cookie parameter: lang, the language the page shows, which
// the page keeps in a cookie of its own. Its type and schema hold it as any
// parameter: a language the app has no greeting in is a 400, and a request
// without the cookie reads its default.
type GetIn struct {
	Lang string `cookie:"lang" doc:"The language of the greeting" schema:"enum=en|ja,default=en"`
}

type Me struct {
	User     string `json:"user"`
	Greeting string `json:"greeting"`
}

var greetings = map[string]string{"en": "Hello, ", "ja": "こんにちは、"}

func get(ctx context.Context, in *GetIn) (*Me, error) {
	user := auth.Caller.Must(ctx).User
	return &Me{User: user, Greeting: greetings[in.Lang] + user}, nil
}
