// A handler that returns its output by value instead of by pointer.
package main

import (
	"context"
	"net/http"

	"github.com/koji-1009/geta"
)

type In struct{}
type Out struct{ Name string }

func get(ctx context.Context, in *In) (Out, error) { return Out{}, nil }

var _ = geta.Route{Get: geta.Op(http.StatusOK, get, geta.Doc{})}

func main() {}
