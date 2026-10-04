// One URL answering GET twice.
package main

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type Out struct{ Name string }

func a(ctx context.Context, in *struct{}) (*Out, error) { return &Out{}, nil }
func b(ctx context.Context, in *struct{}) (*Out, error) { return &Out{}, nil }

var _ = geta.Route{
	Get: geta.Op(http.StatusOK, a, geta.Doc{}),
	Get: geta.Op(http.StatusOK, b, geta.Doc{}),
}

func main() {}
