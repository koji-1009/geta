package getatest

import (
	"context"
	"net/http"
	"testing"

	"github.com/koji-1009/geta"
)

type optShape interface{ isOptShape() }

type optCircle struct {
	Kind   string  `json:"kind"`
	Radius float64 `json:"radius"`
}

func (optCircle) isOptShape() {}

var optShapes = geta.Sealed[optShape]("kind", geta.Case[optCircle]("circle"))

type optProblem struct {
	Shape optShape `json:"shape"`
}

type optError struct{}

func (*optError) Error() string { return "bad shape" }

var optRow = geta.OnAsProblem(http.StatusUnprocessableEntity, "bad shape", func(*optError) optProblem {
	return optProblem{Shape: optCircle{Kind: "circle", Radius: 2}}
})

// A refusal that Stream or Upgrade hands back reads a described problem
// holding a sealed type with ProblemAs, as Send's does.
func TestStreamAndUpgradeRefusalsReadSealedTypes(t *testing.T) {
	c := New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/s", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*geta.Stream[string], error) {
			return nil, &optError{}
		}, geta.Doc{Failures: []geta.Failure{optRow}})}},
		{Path: "/u", Route: geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, func(context.Context, *struct{}) (*geta.Upgrade, error) {
			return nil, &optError{}
		}, geta.Doc{Failures: []geta.Failure{optRow}})}},
	}}, geta.WithUnion(optShapes))
	for name, r := range map[string]*Response{
		"stream":  c.Stream("/s").Response,
		"upgrade": c.Upgrade("/u", "echo").Response,
	} {
		if r.Status != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d %s", name, r.Status, r.Body)
		}
		if got, ok := r.ProblemAs[optProblem]().Shape.(optCircle); !ok || got.Radius != 2 {
			t.Errorf("%s: %#v", name, got)
		}
	}
}
