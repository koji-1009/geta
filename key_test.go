package geta_test

import (
	"context"
	"testing"

	"github.com/koji-1009/geta"
)

var principal = geta.NewKey[string]("principal")

func TestKeyRoundTrip(t *testing.T) {
	ctx := principal.With(context.Background(), "ada")
	if v, ok := principal.Value(ctx); !ok || v != "ada" {
		t.Fatal(v, ok)
	}
	other := geta.NewKey[string]("principal")
	if _, ok := other.Value(ctx); ok {
		t.Fatal("two keys with one name collide")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("the zero key did not panic")
		}
	}()
	var zero geta.Key[string]
	zero.Value(ctx)
}
