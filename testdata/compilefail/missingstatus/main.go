// An operation without its success status.
package main

import (
	"context"

	"github.com/koji-1009/geta"
)

type Out struct{ Name string }

func get(ctx context.Context, in *struct{}) (*Out, error) { return &Out{}, nil }

var _ = geta.Route{Get: geta.Op(get, geta.Doc{})}

func main() {}
