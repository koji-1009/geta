package getatest_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

type Item struct {
	ID   string `json:"id" schema:"maxLength=16"`
	Name string `json:"name" schema:"minLength=1"`
}

type CreateIn struct {
	Body Item `body:"json"`
}

type Created struct {
	Location string `header:"Location"`
	Item     Item   `body:"json"`
}

func create(ctx context.Context, in *CreateIn) (*Created, error) {
	return &Created{Location: "/items/" + in.Body.ID, Item: in.Body}, nil
}

func table() geta.Table {
	return geta.Table{Routes: []geta.Entry{{
		Path:  "/items",
		Route: geta.Route{Post: geta.Op(http.StatusCreated, create, geta.Doc{Summary: "Create an item"})},
	}}}
}

// New assembles the table as geta.New does (an assembly mistake fails the
// test there) and serves it over an in-memory network. Every request fails
// the test if its status is not in the operation's document, or a JSON
// success body does not conform to its schema; JSON reads the body as a type,
// failing the test on a member the type lacks. In an application, the table
// is routes.Table(env), assembled with routes.Options(env)..., and the
// committed OpenAPI document is a golden file `go test -update` rewrites.
//
// This is a test, so it has a *testing.T; it is the body of a
// func TestCreate(t *testing.T) in a _test.go file.
func ExampleNew() {
	test := func(t *testing.T) {
		c := getatest.New(t, table())
		res := c.Post("/items", map[string]any{"id": "a1", "name": "lamp"})
		if res.Status != http.StatusCreated || res.Header.Get("Location") != "/items/a1" {
			t.Fatalf("create: %d %s", res.Status, res.Text())
		}
		if got := res.JSON[Item](); got.Name != "lamp" {
			t.Fatalf("created %+v", got)
		}
		// A body the contract refuses is a 400 problem naming each violation.
		if p := c.Post("/items", map[string]any{"id": "a2", "name": ""}).Problem(); p.Status != http.StatusBadRequest {
			t.Fatalf("an empty name: %+v", p)
		}
		getatest.Golden(t, c.App(), "testdata/openapi.json")
	}
	_ = test
}
