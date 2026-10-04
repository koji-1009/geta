package geta

import (
	"context"
	"fmt"
)

// Key carries one typed value through a request's context, such as the
// principal a verifier established. Build keys with [NewKey]; the zero Key
// is invalid and panics when used.
type Key[T any] struct{ id *keyID }

type keyID struct{ name string }

// NewKey returns a key distinct from every other key, whatever its name.
func NewKey[T any](name string) Key[T] {
	return Key[T]{&keyID{name}}
}

func (k Key[T]) check() {
	if k.id == nil {
		panic("geta: zero Key; use geta.NewKey")
	}
}

// With returns a context carrying v under k.
func (k Key[T]) With(ctx context.Context, v T) context.Context {
	k.check()
	return context.WithValue(ctx, k.id, v)
}

// Value returns the value under k and whether there is one.
func (k Key[T]) Value(ctx context.Context) (T, bool) {
	k.check()
	v, ok := ctx.Value(k.id).(T)
	return v, ok
}

// Must returns the value under k. Its absence is a defect: Must panics, and
// [Recover] answers 500.
func (k Key[T]) Must(ctx context.Context) T {
	v, ok := k.Value(ctx)
	if !ok {
		panic(fmt.Sprintf("geta: no value for key %q in the context", k.id.name))
	}
	return v
}

// String is the key's name.
func (k Key[T]) String() string {
	if k.id == nil {
		return "<zero geta.Key>"
	}
	return k.id.name
}
