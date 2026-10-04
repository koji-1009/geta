package geta_test

import (
	"context"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
)

// A component's name is a key of the document's components, which OpenAPI
// restricts to ^[a-zA-Z0-9._-]+$. A type whose name would be another key is
// refused, so the document geta writes passes the meta-schema.

type 利用者 struct {
	Name string `json:"name"`
}

type box[T any] struct {
	V T `json:"v"`
}

type 形 interface{ is形() }

type dot struct {
	Kind string `json:"kind"`
}

func (dot) is形() {}

type holder struct {
	Shape 形 `json:"shape"`
}

func TestComponentNamesAreOnesOpenAPIAllows(t *testing.T) {
	rejects(t, one("/u", get(func(context.Context, *empty) (*利用者, error) { return nil, nil })),
		`type geta_test.利用者: component name "利用者"`, "^[a-zA-Z0-9._-]+$")
	// A generic type's argument that is no named type writes characters no
	// component name has.
	rejects(t, one("/b", get(func(context.Context, *empty) (*box[struct {
		X int `json:"x"`
	}], error) {
		return nil, nil
	})), `component name "box_struct{Xint\"json:\\\"x\\\"\"}"`)
	// A sealed type's name is a component's too.
	_, err := geta.New(one("/s", get(func(context.Context, *empty) (*holder, error) { return nil, nil })),
		geta.WithUnion(geta.Sealed[形]("kind", geta.Case[dot]("dot"))))
	if err == nil {
		t.Fatal("geta.New accepted a sealed type named 形")
	}
	if want := `type geta_test.形: component name "形"`; !strings.Contains(err.Error(), want) {
		t.Errorf("error lacks %q:\n%v", want, err)
	}
	// Names of letters, digits, '.', '_', and '-' pass, a generic one's
	// included.
	m := doc(t, accepts(t, one("/p", get(func(context.Context, *empty) (*box[ok], error) { return nil, nil }))))
	at(t, m, "components", "schemas", "box_ok")
}
