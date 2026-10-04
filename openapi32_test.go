package geta_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
)

type plainEvent struct {
	N int `json:"n"`
}

type cookieOut struct {
	Session *http.Cookie `cookie:"sid"`
	Theme   *http.Cookie `cookie:"theme"`
	Body    ok           `body:"json"`
}

func versionTable() geta.Table {
	named := func(ctx context.Context, _ *empty) (*geta.Stream[change], error) { return &geta.Stream[change]{}, nil }
	plain := func(ctx context.Context, _ *empty) (*geta.Stream[plainEvent], error) {
		return &geta.Stream[plainEvent]{}, nil
	}
	cookies := func(ctx context.Context, _ *empty) (*cookieOut, error) { return &cookieOut{}, nil }
	return geta.Table{Routes: []geta.Entry{
		{Path: "/named", Route: get(named)},
		{Path: "/plain", Route: get(plain)},
		{Path: "/cookies", Route: get(cookies)},
	}}
}

func versionDoc(t *testing.T, opts ...geta.Option) map[string]any {
	t.Helper()
	a, err := geta.New(versionTable(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	return doc(t, a)
}

// 3.1 stays the default, and selecting it says the same as saying nothing.
func TestOpenAPIDefaultsTo31(t *testing.T) {
	a, err := geta.New(versionTable())
	if err != nil {
		t.Fatal(err)
	}
	b, err := geta.New(versionTable(), geta.WithOpenAPI(geta.OpenAPI31))
	if err != nil {
		t.Fatal(err)
	}
	if string(a.OpenAPI()) != string(b.OpenAPI()) {
		t.Fatal("WithOpenAPI(OpenAPI31) changed the document")
	}
	m := doc(t, a)
	if at(t, m, "openapi") != "3.1.0" {
		t.Fatal(m["openapi"])
	}
	if got := compact(t, at(t, m, "paths", "/named", "get", "responses", "200", "content")); got != `{"text/event-stream":{"schema":{"$ref":"#/components/schemas/change"}}}` {
		t.Fatal(got)
	}
	if _, has := at(t, m, "paths", "/named", "get", "responses", "200").(map[string]any)["summary"]; has {
		t.Fatal("a 3.1 response has a summary")
	}
}

func TestOpenAPI32DescribesEventsWithItemSchema(t *testing.T) {
	m := versionDoc(t, geta.WithOpenAPI(geta.OpenAPI32))
	if at(t, m, "openapi") != "3.2.0" {
		t.Fatal(m["openapi"])
	}
	// change names and identifies every event: both fields are required.
	if got := compact(t, at(t, m, "paths", "/named", "get", "responses", "200", "content")); got != `{"text/event-stream":{"itemSchema":{"properties":{"data":{"contentMediaType":"application/json","contentSchema":{"$ref":"#/components/schemas/change"},"type":"string"},"event":{"type":"string"},"id":{"type":"string"}},"required":["data","event","id"],"type":"object"}}}` {
		t.Fatal(got)
	}
	// plainEvent does neither: geta writes data alone.
	if got := compact(t, at(t, m, "paths", "/plain", "get", "responses", "200", "content")); got != `{"text/event-stream":{"itemSchema":{"properties":{"data":{"contentMediaType":"application/json","contentSchema":{"$ref":"#/components/schemas/plainEvent"},"type":"string"}},"required":["data"],"type":"object"}}}` {
		t.Fatal(got)
	}
}

func TestOpenAPI32GivesResponsesASummary(t *testing.T) {
	m := versionDoc(t, geta.WithOpenAPI(geta.OpenAPI32))
	responses := at(t, m, "paths", "/cookies", "get", "responses").(map[string]any)
	for status, summary := range map[string]string{"200": "OK", "500": "Internal Server Error", "504": "Gateway Timeout"} {
		if got := at(t, responses, status, "summary"); got != summary {
			t.Errorf("%s: summary %q, want %q", status, got, summary)
		}
	}
	// The description still lists the causes.
	if got := at(t, responses, "500", "description").(string); !strings.Contains(got, "A defect in the server") {
		t.Fatal(got)
	}
	if got := at(t, m, "paths", "/named", "options", "responses", "204", "summary"); got != "No Content" {
		t.Fatal(got)
	}
}

func TestOpenAPI32NamesTheCookiesSet(t *testing.T) {
	m := versionDoc(t, geta.WithOpenAPI(geta.OpenAPI32))
	if got := compact(t, at(t, m, "paths", "/cookies", "get", "responses", "200", "headers", "Set-Cookie")); got != `{"description":"Sets sid, theme","explode":true,"schema":{"properties":{"sid":{"type":"string"},"theme":{"type":"string"}},"type":"object"},"style":"simple"}` {
		t.Fatal(got)
	}
	m31 := versionDoc(t)
	if got := compact(t, at(t, m31, "paths", "/cookies", "get", "responses", "200", "headers", "Set-Cookie")); got != `{"description":"Sets sid, theme","schema":{"type":"string"}}` {
		t.Fatal(got)
	}
}

func TestOpenAPIRefusesAnUnknownVersion(t *testing.T) {
	for _, v := range []geta.OpenAPIVersion{"3.0.3", "3.1", "3.3.0", ""} {
		_, err := geta.New(versionTable(), geta.WithOpenAPI(v))
		if err == nil || !strings.Contains(err.Error(), "WithOpenAPI: unsupported version") {
			t.Errorf("%q: %v", v, err)
		}
	}
}
