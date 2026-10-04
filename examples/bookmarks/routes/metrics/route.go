package metrics

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/bookmarks/app"
	"github.com/koji-1009/geta/examples/bookmarks/metrics"
)

// Route is /metrics, the counts the root scope's middleware keeps. It is
// public here for the demo; a deployment serves it on an address only its
// monitoring reaches, or behind a scheme of its own.
func Route(env *app.Env) geta.Route {
	h := Handler{Metrics: env.Metrics}
	return geta.Route{
		Get: geta.Op(http.StatusOK, h.Get, geta.Doc{Summary: "Requests per route and status", Security: []geta.Scheme{}}),
	}
}

type Snapshotter interface{ Snapshot() []metrics.Count }

type Handler struct{ Metrics Snapshotter }

type Counts struct {
	Items []metrics.Count `json:"items"`
}

func (h Handler) Get(ctx context.Context, _ *struct{}) (*Counts, error) {
	return &Counts{Items: h.Metrics.Snapshot()}, nil
}
