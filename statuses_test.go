package geta_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

type stored struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type upsertIn struct {
	ID   string `path:"id"`
	Name string `query:"name"`
}

// upserted answers 201 when the item was created and 200 (the field left
// zero) when it was replaced.
type upserted struct {
	Status   int     `status:"200|201"`
	Location *string `header:"Location"`
	Body     stored  `body:"json"`
}

type dropIn struct {
	ID    string `path:"id"`
	Later *bool  `query:"later"`
}

type queued struct {
	Status int `status:"202|204"`
}

func upsertTable(seen map[string]bool) geta.Table {
	return geta.Table{Routes: []geta.Entry{
		{Path: "/items/{id}", Route: geta.Route{
			Put: geta.Op(http.StatusOK, func(_ context.Context, in *upsertIn) (*upserted, error) {
				out := &upserted{Body: stored{in.ID, in.Name}}
				switch {
				case in.Name == "undeclared":
					out.Status = http.StatusAccepted
				case !seen[in.ID]:
					seen[in.ID] = true
					loc := "/items/" + in.ID
					out.Status, out.Location = http.StatusCreated, &loc
				}
				return out, nil
			}, geta.Doc{}),
			Delete: geta.Op(http.StatusNoContent, func(_ context.Context, in *dropIn) (*queued, error) {
				if in.Later != nil && *in.Later {
					return &queued{Status: http.StatusAccepted}, nil
				}
				return &queued{}, nil
			}, geta.Doc{}),
		}},
	}}
}

// An output's status field lets the handler choose among the success
// statuses it declares: zero answers the operation's own, a declared one is
// sent, and one it does not declare is a defect, a 500. getatest holds each
// to the document.
func TestHandlersChooseAmongDeclaredStatuses(t *testing.T) {
	c := getatest.New(t, upsertTable(map[string]bool{}), geta.WithLogger(quietLogger()))
	res := c.Put("/items/a?name=ann", nil)
	if res.Status != http.StatusCreated || res.Header.Get("Location") != "/items/a" || res.JSON[stored]().Name != "ann" {
		t.Fatal(res.Status, res.Header, res.Text())
	}
	res = c.Put("/items/a?name=bea", nil)
	if res.Status != http.StatusOK || res.Header.Get("Location") != "" {
		t.Fatal(res.Status, res.Header, res.Text())
	}
	if res := c.Put("/items/a?name=undeclared", nil); res.Status != http.StatusInternalServerError {
		t.Fatal(res.Status, res.Text())
	}
	if res := c.Delete("/items/a"); res.Status != http.StatusNoContent {
		t.Fatal(res.Status)
	}
	if res := c.Delete("/items/a?later=true"); res.Status != http.StatusAccepted {
		t.Fatal(res.Status)
	}
}

// Every status the field declares is in the document, each with the
// output's schema and headers.
func TestEveryDeclaredStatusIsDocumented(t *testing.T) {
	m := doc(t, accepts(t, upsertTable(nil)))
	for _, s := range []string{"200", "201"} {
		if got := compact(t, at(t, m, "paths", "/items/{id}", "put", "responses", s, "content")); got != `{"application/json":{"schema":{"$ref":"#/components/schemas/stored"}}}` {
			t.Errorf("%s: %s", s, got)
		}
		if got := at(t, m, "paths", "/items/{id}", "put", "responses", s, "headers", "Location", "required"); got != false {
			t.Errorf("%s Location required %v", s, got)
		}
	}
	responses := at(t, m, "paths", "/items/{id}", "delete", "responses").(map[string]any)
	for _, s := range []string{"202", "204"} {
		if _, ok := responses[s]; !ok {
			t.Errorf("delete lacks %s", s)
		}
	}
	if _, ok := responses["200"]; ok {
		t.Error("delete lists 200")
	}
}

// What a status field could state falsely is refused, by geta.New and
// getavet alike: the operation's status outside it, a status that carries
// no body beside a body, a status that is not a 2xx or 3xx (or is 304), one
// given twice, a field that is no int, two fields.
func TestStatusFieldMistakesAreRefused(t *testing.T) {
	type noBody struct {
		Status int    `status:"200|204"`
		Body   stored `body:"json"`
	}
	type notModified struct {
		Status int `status:"200|304"`
	}
	type twice struct {
		Status int `status:"200|200"`
	}
	type notInt struct {
		Status string `status:"200|201"`
	}
	type two struct {
		Status int `status:"200|201"`
		Again  int `status:"200"`
	}
	type word struct {
		Status int `status:"ok"`
	}
	for want, tbl := range map[string]geta.Table{
		`success status 202 is not in status tag "200|201"`: one("/x/{id}", geta.Route{Put: geta.Op(http.StatusAccepted,
			func(context.Context, *upsertIn) (*upserted, error) { return nil, nil }, geta.Doc{})}),
		"success status 204 takes no body, but the output has one":            one("/x", get(func(context.Context, *empty) (*noBody, error) { return nil, nil })),
		`status tag "200|304": 304 is not a 2xx or 3xx status other than 304`: one("/x", get(func(context.Context, *empty) (*notModified, error) { return nil, nil })),
		`status tag "200|200" names 200 twice`:                                one("/x", get(func(context.Context, *empty) (*twice, error) { return nil, nil })),
		"Status: a status field is an int, not string":                        one("/x", get(func(context.Context, *empty) (*notInt, error) { return nil, nil })),
		"Again: a second status field":                                        one("/x", get(func(context.Context, *empty) (*two, error) { return nil, nil })),
		`status tag "ok": "ok" is not a status code`:                          one("/x", get(func(context.Context, *empty) (*word, error) { return nil, nil })),
	} {
		_, err := geta.New(tbl)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v\nwant %q", err, want)
		}
	}
}
