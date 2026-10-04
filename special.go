package geta

import "reflect"

// specialOut is implemented by the output types that are not a JSON body.
type specialOut interface {
	plan(r *registry) (specialPlan, error)
}

// specialFor reports whether t is a special output, and plans it.
func specialFor(r *registry, t reflect.Type) (specialPlan, bool, error) {
	if !reflect.PointerTo(t).Implements(reflect.TypeFor[specialOut]()) {
		return nil, false, nil
	}
	sp, err := reflect.New(t).Interface().(specialOut).plan(r)
	return sp, true, err
}
