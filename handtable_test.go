package geta_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
)

type handItemIn struct {
	ID string `path:"id" schema:"minLength=1"`
}

type handItem struct {
	ID string `json:"id"`
}

type handHealth struct {
	OK bool `json:"ok"`
}

// A table written by hand, with no routes tree and no geta sync, assembles
// through geta.New as the generated one does: its path parameter is bound by
// the operation's input, its directory-style scope wraps the entry it is
// listed on and no other, and the root scope wraps every request.
func TestHandWrittenTableAssemblesAndServes(t *testing.T) {
	stamp := func(name string) geta.Middleware {
		return geta.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Add("X-Scopes", name)
				next.ServeHTTP(w, r)
			})
		})
	}
	table := geta.Table{
		Root: geta.Scope{stamp("root")},
		Routes: []geta.Entry{
			{
				Path: "/health",
				Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *struct{}) (*handHealth, error) {
					return &handHealth{OK: true}, nil
				}, geta.Doc{Summary: "Health"})},
			},
			{
				Path: "/items/{id}",
				Route: geta.Route{Get: geta.Op(http.StatusOK, func(_ context.Context, in *handItemIn) (*handItem, error) {
					return &handItem{ID: in.ID}, nil
				}, geta.Doc{Summary: "One item"})},
				Scopes: []geta.Scope{{stamp("items")}},
			},
		},
	}
	app, err := geta.New(table)
	if err != nil {
		t.Fatal(err)
	}
	get := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec
	}
	rec := get("/items/7")
	if body := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || body != `{"id":"7"}` {
		t.Fatal(rec.Code, body)
	}
	if got := rec.Header().Values("X-Scopes"); len(got) != 2 || got[0] != "root" || got[1] != "items" {
		t.Fatalf("X-Scopes = %q, want root then items", got)
	}
	rec = get("/health")
	if got := rec.Header().Values("X-Scopes"); rec.Code != http.StatusOK || len(got) != 1 || got[0] != "root" {
		t.Fatal(rec.Code, got)
	}
	if rec = get("/nowhere"); rec.Code != http.StatusNotFound || rec.Header().Get("X-Scopes") != "root" {
		t.Fatal(rec.Code, rec.Header().Values("X-Scopes"))
	}

	// geta.New refuses a path the operation's input does not bind.
	table.Routes[1].Path = "/items/{name}"
	if _, err := geta.New(table); err == nil {
		t.Fatal("geta.New accepted a path parameter the input does not bind")
	}
}
