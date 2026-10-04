package health

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/app"
)

// Route is /health, a public liveness probe.
func Route(env *app.Env) geta.Route {
	return geta.Route{
		Get: geta.Op(http.StatusOK, get, geta.Doc{Summary: "Liveness probe", Security: []geta.Scheme{}}),
	}
}

// Health is the probe's answer.
type Health struct {
	Status string `json:"status" schema:"enum=ok"`
}

func get(context.Context, *struct{}) (*Health, error) { return &Health{Status: "ok"}, nil }
