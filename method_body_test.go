package geta_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/internal/vet"
)

type methodBodyItem struct {
	Name string `json:"name"`
}

type methodBodyForm struct {
	Name string `form:"name"`
}

type methodBodyJSON struct {
	Body methodBodyItem `body:"json"`
}

type methodBodyOptional struct {
	Body *methodBodyItem `body:"json"`
}

type methodBodyFormIn struct {
	Body methodBodyForm `body:"form"`
}

type methodBodyMultipart struct {
	Body methodBodyForm `body:"multipart"`
}

type methodBodyRaw struct {
	Body []byte `body:"text/csv"`
}

type methodBodyEmbedded struct{ methodBodyJSON }

func methodBodyOp[In any]() geta.Operation {
	return geta.OpNoBody(http.StatusNoContent, func(context.Context, *In) error { return nil }, geta.Doc{})
}

// A body on a GET (and so on the HEAD geta answers with it) or a DELETE is
// refused, whatever its encoding, required or not, embedded or not: content
// there has no defined meaning (RFC 9110 §9.3.1, §9.3.5) and OpenAPI says to
// avoid a requestBody there. The message points to QUERY or POST. The
// methods whose content has a meaning take one.
func TestABodyOnGetOrDeleteIsRefused(t *testing.T) {
	ops := map[string]geta.Operation{
		"json":      methodBodyOp[methodBodyJSON](),
		"optional":  methodBodyOp[methodBodyOptional](),
		"form":      methodBodyOp[methodBodyFormIn](),
		"multipart": methodBodyOp[methodBodyMultipart](),
		"raw":       methodBodyOp[methodBodyRaw](),
		"embedded":  methodBodyOp[methodBodyEmbedded](),
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			_, err := geta.New(one("/x", geta.Route{Get: op}))
			if err == nil || !strings.Contains(err.Error(), "GET /x") ||
				!strings.Contains(err.Error(), "the input has a body, which a GET request does not take") ||
				!strings.Contains(err.Error(), "use Query or Post") {
				t.Fatalf("GET: %v", err)
			}
			_, err = geta.New(one("/x", geta.Route{Delete: op}))
			if err == nil || !strings.Contains(err.Error(), "DELETE /x") ||
				!strings.Contains(err.Error(), "the input has a body, which a DELETE request does not take") ||
				!strings.Contains(err.Error(), "use Post") {
				t.Fatalf("DELETE: %v", err)
			}
			if _, err := geta.New(one("/x", geta.Route{Post: op, Put: op, Patch: op, Query: op}), geta.WithOpenAPI(geta.OpenAPI32)); err != nil {
				t.Fatal(err)
			}
		})
	}
	// Parameters alone are what a GET and a DELETE take.
	if _, err := geta.New(one("/x", geta.Route{Get: methodBodyOp[queryIn](), Delete: methodBodyOp[queryIn]()})); err != nil {
		t.Fatal(err)
	}
	if err := vet.CheckMethodBody(http.MethodHead); err == nil {
		t.Fatal("CheckMethodBody accepted HEAD")
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, geta.MethodQuery} {
		if err := vet.CheckMethodBody(m); err != nil {
			t.Fatal(m, err)
		}
	}
}
