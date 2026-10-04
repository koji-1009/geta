package geta_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// getatest: the conformance check on every response, and the client's
// headers.

// getatest fails a test whose response carries a status the document does
// not list for the operation, so the document and the wire stay one set.
func TestGetatestFailsOnAnUndocumentedStatus(t *testing.T) {
	teapot := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			geta.WriteProblem(w, http.StatusTeapot, "")
		})
	}
	rec := &recorder{TB: t}
	c := getatest.New(rec, withRoot(one("/x", get(okHandler)), geta.Use(teapot)))
	if res := c.Get("/x"); res.Status != http.StatusTeapot {
		t.Fatal(res.Status)
	}
	if len(rec.errs) != 1 || !strings.Contains(rec.errs[0], "status 418 is not documented") {
		t.Fatalf("%q", rec.errs)
	}

	// Declared, it is documented and passes.
	rec = &recorder{TB: t}
	c = getatest.New(rec, withRoot(one("/x", get(okHandler)), geta.Use(teapot).Answers(http.StatusTeapot, "Short and stout")))
	c.Get("/x")
	if len(rec.errs) != 0 {
		t.Fatalf("%q", rec.errs)
	}

	// No operation reached: nothing to check.
	rec = &recorder{TB: t}
	getatest.New(rec, one("/x", get(okHandler))).Get("/nope")
	if len(rec.errs) != 0 {
		t.Fatalf("%q", rec.errs)
	}
}

// createdBody and countBody are two bodies with nothing in common.
type createdBody struct {
	ID string `json:"id"`
}

type countBody struct {
	Count int `json:"count"`
}

// overrideTable has a root method override in front of operations that answer
// different statuses and bodies.
func overrideTable(scopes ...geta.Scope) geta.Table {
	return geta.Table{
		Root: geta.Scope{
			geta.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Getatest-Request") != "" {
						http.Error(w, "getatest's header reached the app", http.StatusTeapot)
						return
					}
					next.ServeHTTP(w, r)
				})
			}),
			methodOverride,
		},
		Routes: []geta.Entry{
			{Path: "/items", Scopes: scopes, Route: geta.Route{
				Post:   geta.Op(http.StatusCreated, func(context.Context, *empty) (*createdBody, error) { return &createdBody{"a"}, nil }, geta.Doc{}),
				Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error { return nil }, geta.Doc{}),
			}},
			{Path: "/things", Route: geta.Route{
				Post: geta.Op(http.StatusOK, func(context.Context, *empty) (*createdBody, error) { return &createdBody{"a"}, nil }, geta.Doc{}),
				Put:  geta.Op(http.StatusOK, func(context.Context, *empty) (*countBody, error) { return &countBody{1}, nil }, geta.Doc{}),
			}},
		},
	}
}

// getatest checks a response against the operation that served it: a root
// method override that turns a POST into a DELETE answers the DELETE's 204,
// a PUT's body is the PUT's schema, and an override to a method nothing
// serves is a 405 with nothing to check.
func TestGetatestChecksTheOperationServed(t *testing.T) {
	c := getatest.New(t, overrideTable(), geta.WithLogger(quietLogger()))
	cases := []struct {
		path, override string
		want           int
	}{
		{"/items", "", http.StatusCreated},
		{"/items", "DELETE", http.StatusNoContent},
		{"/things", "PUT", http.StatusOK},
		{"/items", "PATCH", http.StatusMethodNotAllowed},
	}
	for _, cs := range cases {
		cl := c
		if cs.override != "" {
			cl = c.With("X-HTTP-Method-Override", cs.override)
		}
		if res := cl.Post(cs.path, nil); res.Status != cs.want {
			t.Errorf("POST %s as %q: %d; want %d: %s", cs.path, cs.override, res.Status, cs.want, res.Body)
		}
	}
}

// A status the operation served does not list still fails, and the failure
// names that operation, not the one the request arrived for.
func TestGetatestNamesTheOperationServed(t *testing.T) {
	teapot := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusTeapot)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	tb := &recorder{TB: t}
	c := getatest.New(tb, overrideTable(geta.Scope{teapot}), geta.WithLogger(quietLogger()))
	if res := c.With("X-HTTP-Method-Override", "DELETE").Post("/items", nil); res.Status != http.StatusTeapot {
		t.Fatalf("%d", res.Status)
	}
	if len(tb.errs) != 1 || !strings.Contains(tb.errs[0], "is not documented for DELETE /items") || !strings.Contains(tb.errs[0], "served as DELETE") {
		t.Fatalf("getatest reported %q", tb.errs)
	}
}

// getatest: a second Bearer replaces the first rather than sending both,
// so the server reads the caller the test named.
func TestClientWithReplaces(t *testing.T) {
	var mu sync.Mutex
	var got [][]string
	capture := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			got = append(got, r.Header.Values("Authorization"))
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	})
	c := getatest.New(t, geta.Table{Root: geta.Scope{capture, geta.Secure(scopePolicy())}, Routes: []geta.Entry{route("/me", nil)}})
	root := c.Bearer("root")
	user := root.Bearer("ok")
	if res := user.Get("/me"); res.Text() != `{"id":"ada"}` {
		t.Fatalf("Bearer on a client with a token: %d %s", res.Status, res.Body)
	}
	if res := root.Get("/me"); res.Text() != `{"id":"root"}` {
		t.Fatalf("the first client changed: %d %s", res.Status, res.Body)
	}
	if res := root.With("Authorization", "Bearer ok").Get("/me"); res.Text() != `{"id":"ada"}` {
		t.Fatalf("With on a client with a token: %d %s", res.Status, res.Body)
	}
	want := [][]string{{"Bearer ok"}, {"Bearer root"}, {"Bearer ok"}}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("Authorization sent: %q", got)
	}
}

// Stream and Upgrade send the client's headers as Send does: once each, a
// client's Accept in place of Stream's, and Upgrade's protocol in place of
// the client's.
func TestStreamAndUpgradeSendTheClientsHeadersOnce(t *testing.T) {
	var mu sync.Mutex
	var seen []http.Header
	record := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen = append(seen, r.Header.Clone())
			mu.Unlock()
			w.WriteHeader(http.StatusTeapot)
		})
	}).Answers(http.StatusTeapot, "Short and stout")
	c := getatest.New(t, withRoot(one("/x", get(okHandler)), record)).With("X-Trace", "t")
	c.With("Accept", "application/json").Stream("/x")
	c.Stream("/x")
	c.With("Upgrade", "h2c").Upgrade("/x", "websocket")
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 {
		t.Fatal(len(seen))
	}
	for i, want := range []map[string][]string{
		{"Accept": {"application/json"}, "X-Trace": {"t"}},
		{"Accept": {"text/event-stream"}, "X-Trace": {"t"}},
		{"Upgrade": {"websocket"}, "X-Trace": {"t"}},
	} {
		for k, v := range want {
			if got := seen[i].Values(k); !slices.Equal(got, v) {
				t.Errorf("request %d: %s %q, want %q", i, k, got, v)
			}
		}
	}
}

// A request that sets a header itself sends its own value in place of the
// client's, once: the server never sees the client's default beside it.
func TestSendPrefersTheRequestsOwnHeader(t *testing.T) {
	var mu sync.Mutex
	var seen [][]string
	record := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen = append(seen, r.Header.Values("Authorization"))
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	})
	c := getatest.New(t, geta.Table{
		Root: geta.Scope{record},
		Routes: []geta.Entry{{Path: "/items", Route: geta.Route{
			Get: geta.Op(http.StatusOK, okHandler, geta.Doc{}),
		}}},
	}).Bearer("client")

	own, err := http.NewRequestWithContext(t.Context(), http.MethodGet, c.URL()+"/items", nil)
	if err != nil {
		t.Fatal(err)
	}
	own.Header.Set("Authorization", "Bearer request")
	if res := c.Send(own); res.Status != http.StatusOK {
		t.Fatalf("own header: %d %s", res.Status, res.Body)
	}
	plain, err := http.NewRequestWithContext(t.Context(), http.MethodGet, c.URL()+"/items", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res := c.Send(plain); res.Status != http.StatusOK {
		t.Fatalf("no header: %d %s", res.Status, res.Body)
	}

	mu.Lock()
	defer mu.Unlock()
	want := [][]string{{"Bearer request"}, {"Bearer client"}}
	if !slices.EqualFunc(seen, want, slices.Equal) {
		t.Fatalf("Authorization seen: %q, want %q", seen, want)
	}
}
