package health

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/app"
)

// Route is /health, a public liveness probe.
func Route(env *app.Env) geta.Route {
	return geta.Route{
		Get: geta.Op(http.StatusOK, get, geta.Doc{
			Summary:  "Liveness probe",
			Security: []geta.Scheme{},
		}),
	}
}

// Status is the probe's answer.
type Status struct {
	Status string `json:"status"`
}

func get(ctx context.Context, _ *struct{}) (*Status, error) {
	return &Status{Status: "ok"}, nil
}
