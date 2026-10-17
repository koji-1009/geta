package geta_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// The document is served by a route of the application's own, whose handler
// returns the assembled App's OpenAPI, as the guide writes it: the route is
// documented, with the operationId Doc names and an object body
// (geta.WithSchema), and what it answers is the document, as getatest checks
// against that document.
func TestTheDocumentIsServedByARoute(t *testing.T) {
	var a *geta.App
	tbl := one("/health", get(okHandler))
	tbl.Routes = append(tbl.Routes, geta.Entry{Path: "/openapi.json", Route: geta.Route{
		Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*json.RawMessage, error) {
			doc := json.RawMessage(a.OpenAPI())
			return &doc, nil
		}, geta.Doc{OperationID: "getOpenAPIDocument", Security: []geta.Scheme{}}),
	}})
	c := getatest.New(t, tbl, geta.WithSchema[json.RawMessage]("object", ""))
	a = c.App()
	res := c.Get("/openapi.json")
	var got, want any
	if err := json.Unmarshal(res.Body, &got); err != nil || res.Status != http.StatusOK {
		t.Fatal(res.Status, err)
	}
	if err := json.Unmarshal(a.OpenAPI(), &want); err != nil {
		t.Fatal(err)
	}
	if compact(t, got) != compact(t, want) {
		t.Errorf("served %s", res.Body)
	}
	m := doc(t, a)
	if at(t, m, "paths", "/openapi.json", "get", "operationId") != "getOpenAPIDocument" ||
		compact(t, at(t, m, "paths", "/openapi.json", "get", "responses", "200", "content", "application/json", "schema")) != `{"type":"object"}` {
		t.Error(compact(t, at(t, m, "paths", "/openapi.json")))
	}
}
