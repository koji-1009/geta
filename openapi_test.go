package geta_test

import (
	"bytes"
	"context"
	"math/rand/v2"
	"net/http"
	"testing"

	"github.com/koji-1009/geta"
)

type address struct {
	City string `json:"city" schema:"minLength=1"`
}

type person struct {
	Name    string            `json:"name" schema:"maxLength=10"`
	Home    address           `json:"home"`
	Past    []address         `json:"past"`
	Labels  map[string]string `json:"labels"`
	Partner *person           `json:"partner,omitzero"`
}

type personIn struct {
	ID   int64  `path:"id"`
	Q    string `query:"id"` // a query parameter may share a path parameter's name
	Body person `body:"json"`
}

var errMissing = &lookupErr{}

func richTable() geta.Table {
	gate := geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer}, Verifiers: map[string]geta.Verifier{"bearer": admit}})
	put := func(ctx context.Context, in *personIn) (*person, error) { return &in.Body, nil }
	del := func(ctx context.Context, in *idIn) error { return nil }
	return geta.Table{
		Root: geta.Scope{gate},
		Routes: []geta.Entry{
			{Path: "/people/{id}", Route: geta.Route{
				Put: geta.Op(http.StatusOK, put, geta.Doc{
					Summary: "Replace", Description: "Replaces a person.", Tags: []string{"people", "admin"},
					Failures: []geta.Failure{geta.On(errNotFound, 404, "no such person"), geta.OnAs[*lookupErr](404, "")},
				}),
				Delete: geta.OpNoBody(http.StatusNoContent, del, geta.Doc{OperationID: "removePerson", Tags: []string{"people"}}),
			}},
			{Path: "/health", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{}})}},
			{Path: "/weird", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{
				Failures: []geta.Failure{geta.On(errNotFound, 499, ""), geta.On(errMissing, 401, "token expired")},
			})}},
		},
	}
}

func TestDocumentShape(t *testing.T) {
	m := doc(t, accepts(t, richTable()))
	if at(t, m, "openapi") != "3.1.0" {
		t.Fatal(m["openapi"])
	}
	put := at(t, m, "paths", "/people/{id}", "put")
	if got := compact(t, at(t, put, "parameters")); got != `[{"in":"path","name":"id","required":true,"schema":{"format":"int64","type":"integer"}},{"in":"query","name":"id","required":true,"schema":{"maxLength":4096,"type":"string"}}]` {
		t.Fatal(got)
	}
	// person is read and written, and its request schema states backstops
	// its response schema does not: the request refers to person-Input.
	if got := compact(t, at(t, put, "requestBody")); got != `{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/person-Input"}}},"required":true}` {
		t.Fatal(got)
	}
	if got := compact(t, at(t, put, "responses", "200", "content")); got != `{"application/json":{"schema":{"$ref":"#/components/schemas/person"}}}` {
		t.Fatal(got)
	}
	if at(t, put, "operationId") != "putPeopleId" || at(t, put, "summary") != "Replace" || at(t, put, "description") != "Replaces a person." {
		t.Fatal(put)
	}
	responses := at(t, put, "responses").(map[string]any)
	for status, desc := range map[string]string{
		"200": "OK",
		"400": "The request does not match its contract",
		"401": "Unauthenticated",
		"404": "no such person; Not Found",
		"413": "The request body is larger than 1048576 bytes (Limits.MaxBodyBytes)",
		"415": "The request has content of a media type the operation does not take, or no Content-Type, or content in a content coding (Content-Encoding)",
		"500": "A defect in the server, such as an error no failure row matches, output that cannot be encoded, or a panic",
		"503": "The credential source is unavailable",
		"504": "The deadline passed before a response",
	} {
		if got := at(t, responses, status, "description"); got != desc {
			t.Errorf("%s: %q, want %q", status, got, desc)
		}
	}
	if len(responses) != 9 {
		t.Errorf("responses: %v", responses)
	}
	if got := compact(t, at(t, put, "security")); got != `[{"bearer":[]}]` {
		t.Fatal(got)
	}

	del := at(t, m, "paths", "/people/{id}", "delete")
	if at(t, del, "operationId") != "removePerson" {
		t.Fatal(del)
	}
	if got := compact(t, at(t, del, "responses", "204")); got != `{"description":"No Content"}` {
		t.Fatal(got)
	}

	health := at(t, m, "paths", "/health", "get")
	if got := compact(t, at(t, health, "security")); got != `[]` {
		t.Fatal(got)
	}
	if _, has := at(t, health, "responses").(map[string]any)["401"]; has {
		t.Fatal("a public operation lists the gate's 401")
	}
	for _, k := range []string{"summary", "description", "tags", "parameters", "requestBody"} {
		if _, has := health.(map[string]any)[k]; has {
			t.Errorf("undeclared %s present", k)
		}
	}

	weird := at(t, m, "paths", "/weird", "get", "responses")
	if at(t, weird, "499", "description") != "Status 499" || at(t, weird, "401", "description") != "token expired; Unauthenticated" {
		t.Fatal(weird)
	}

	if got := compact(t, at(t, m, "tags")); got != `[{"name":"admin"},{"name":"people"}]` {
		t.Fatal(got)
	}
	if got := compact(t, at(t, m, "components", "securitySchemes")); got != `{"bearer":{"scheme":"bearer","type":"http"}}` {
		t.Fatal(got)
	}
	schemas := at(t, m, "components", "schemas").(map[string]any)
	for _, name := range []string{"person", "person-Input", "address", "address-Input", "Problem", "ok"} {
		if _, ok := schemas[name]; !ok {
			t.Errorf("schema %s not collected", name)
		}
	}
	if len(schemas) != 6 {
		t.Errorf("schemas: %v", schemas)
	}
	// The request's: the backstops, and the request schemas of the nested
	// components, itself among them.
	if got := compact(t, schemas["person-Input"]); got != `{"additionalProperties":false,"properties":{"home":{"$ref":"#/components/schemas/address-Input"},"labels":{"additionalProperties":{"maxLength":4096,"type":"string"},"maxProperties":8192,"propertyNames":{"maxLength":4096},"type":"object"},"name":{"maxLength":10,"type":"string"},"partner":{"$ref":"#/components/schemas/person-Input"},"past":{"items":{"$ref":"#/components/schemas/address-Input"},"maxItems":8192,"type":"array"}},"required":["name","home","past","labels"],"type":"object"}` {
		t.Fatal(got)
	}
	if got := compact(t, schemas["address-Input"]); got != `{"additionalProperties":false,"properties":{"city":{"maxLength":4096,"minLength":1,"type":"string"}},"required":["city"],"type":"object"}` {
		t.Fatal(got)
	}
	// The response's: what the author declared, and no backstop.
	if got := compact(t, schemas["person"]); got != `{"additionalProperties":false,"properties":{"home":{"$ref":"#/components/schemas/address"},"labels":{"additionalProperties":{"type":"string"},"type":"object"},"name":{"maxLength":10,"type":"string"},"partner":{"$ref":"#/components/schemas/person"},"past":{"items":{"$ref":"#/components/schemas/address"},"type":"array"}},"required":["name","home","past","labels"],"type":"object"}` {
		t.Fatal(got)
	}
	if got := compact(t, schemas["address"]); got != `{"additionalProperties":false,"properties":{"city":{"minLength":1,"type":"string"}},"required":["city"],"type":"object"}` {
		t.Fatal(got)
	}
}

type flag struct {
	On bool  `json:"on"`
	N  int32 `json:"n" schema:"maximum=9"`
}

type flagIn struct {
	Body flag `body:"json"`
}

type note struct {
	Text string `json:"text"`
	Flag flag   `json:"flag"`
}

type noteIn struct {
	Body note `body:"json"`
}

type memo struct {
	Text string `json:"text"`
	Flag flag   `json:"flag"`
}

// A component whose request and response schemas are the same is one
// component, read and written; one only a request reads states the
// backstops under its plain name, and one only a response writes states
// none. The author's bounds and the type's own (an int32's) are stated on
// both sides.
func TestComponentsSplitOnlyWhereTheSidesDiffer(t *testing.T) {
	echo := func(ctx context.Context, in *flagIn) (*flag, error) { return &in.Body, nil }
	read := func(ctx context.Context, in *noteIn) (*flag, error) { return &in.Body.Flag, nil }
	written := func(ctx context.Context, in *empty) (*memo, error) { return &memo{}, nil }
	m := doc(t, accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/echo", Route: geta.Route{Post: geta.Op(http.StatusOK, echo, geta.Doc{})}},
		{Path: "/note", Route: geta.Route{Post: geta.Op(http.StatusOK, read, geta.Doc{})}},
		{Path: "/memo", Route: geta.Route{Get: geta.Op(http.StatusOK, written, geta.Doc{})}},
	}}))
	schemas := at(t, m, "components", "schemas").(map[string]any)
	if len(schemas) != 4 {
		t.Errorf("schemas: %v", schemas)
	}
	if got := compact(t, schemas["flag"]); got != `{"additionalProperties":false,"properties":{"n":{"format":"int32","maximum":9,"type":"integer"},"on":{"type":"boolean"}},"required":["on","n"],"type":"object"}` {
		t.Fatal(got)
	}
	if got := compact(t, schemas["note"]); got != `{"additionalProperties":false,"properties":{"flag":{"$ref":"#/components/schemas/flag"},"text":{"maxLength":4096,"type":"string"}},"required":["text","flag"],"type":"object"}` {
		t.Fatal(got)
	}
	if got := compact(t, schemas["memo"]); got != `{"additionalProperties":false,"properties":{"flag":{"$ref":"#/components/schemas/flag"},"text":{"type":"string"}},"required":["text","flag"],"type":"object"}` {
		t.Fatal(got)
	}
	for _, p := range []string{"/echo", "/note"} {
		if got := compact(t, at(t, m, "paths", p, "post", "responses", "200", "content", "application/json", "schema")); got != `{"$ref":"#/components/schemas/flag"}` {
			t.Fatal(p, got)
		}
	}
}

// The document is a function of the table's content, not of its order.
func TestDocumentIgnoresTableOrder(t *testing.T) {
	want := accepts(t, richTable()).OpenAPI()
	for i := range 20 {
		tbl := richTable()
		r := rand.New(rand.NewPCG(uint64(i), 7))
		r.Shuffle(len(tbl.Routes), func(a, b int) { tbl.Routes[a], tbl.Routes[b] = tbl.Routes[b], tbl.Routes[a] })
		if got := accepts(t, tbl).OpenAPI(); !bytes.Equal(got, want) {
			t.Fatalf("order %d changed the document", i)
		}
	}
}

func TestReservedProblemSchemaName(t *testing.T) {
	type Problem struct {
		X string `json:"x"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*Problem, error) { return nil, nil })), `"Problem" is reserved`)
}
