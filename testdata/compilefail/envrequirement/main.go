// A route requires a store method the Env's store does not have.
package main

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type ReadOnly struct{}

func (ReadOnly) Find(ctx context.Context, id string) (string, error) { return "", nil }

type Env struct{ Users ReadOnly }

// Store is what this route needs: it also deletes.
type Store interface {
	Find(ctx context.Context, id string) (string, error)
	Delete(ctx context.Context, id string) error
}

type Handler struct{ Users Store }

func (h Handler) Delete(ctx context.Context, in *struct{}) error { return nil }

func Route(env *Env) geta.Route {
	h := Handler{Users: env.Users}
	return geta.Route{Delete: geta.OpNoBody(http.StatusNoContent, h.Delete, geta.Doc{})}
}

func main() {}
