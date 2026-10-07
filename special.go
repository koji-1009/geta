package geta

import "reflect"

// specialOut is implemented by the output types that are not a JSON body.
type specialOut interface {
	plan(r *registry) (specialPlan, error)
}

// specialFor reports whether t is a special output, and plans it. Only
// geta's own types are: a type that embeds one has its method too, but is
// written as itself, so it is an output like any other.
func specialFor(r *registry, t reflect.Type) (specialPlan, bool, error) {
	if t.PkgPath() != reflect.TypeFor[Upgrade]().PkgPath() || !reflect.PointerTo(t).Implements(reflect.TypeFor[specialOut]()) {
		return nil, false, nil
	}
	sp, err := reflect.New(t).Interface().(specialOut).plan(r)
	return sp, true, err
}
