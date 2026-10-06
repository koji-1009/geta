package geta_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// An operation's own scope: middleware one method of a URL runs and the
// others do not, inside the URL's directory scopes and before binding.

// scopePolicy admits "Bearer root" as root and "Bearer ok" as ada.
func scopePolicy() geta.Policy {
	return geta.Policy{
		Default: []geta.Scheme{geta.Bearer},
		Verifiers: map[string]geta.Verifier{
			"bearer": func(r *http.Request) (context.Context, error) {
				switch r.Header.Get("Authorization") {
				case "Bearer root":
					return caller.With(r.Context(), who{"root"}), nil
				case "Bearer ok":
					return caller.With(r.Context(), who{"ada"}), nil
				}
				return nil, geta.ErrUnauthenticated
			},
			"apiKey": apiKeyVerifier,
		},
	}
}

// scopeAdmin answers 403 to anyone but root.
func scopeAdmin() geta.Middleware {
	return geta.Ordered(geta.OrderAuthorize, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if u, ok := caller.Value(r.Context()); !ok || u.ID != "root" {
				geta.WriteProblem(w, http.StatusForbidden, "admin only")
				return
			}
			next.ServeHTTP(w, r)
		})
	}).Answers(http.StatusForbidden, "Not an administrator")
}

type scopeBody struct {
	Name string `json:"name" schema:"minLength=1"`
}

type scopeIn struct {
	Body scopeBody `body:"json"`
}

// scopeTable is /items: an open read and a write whose doc carries scope.
func scopeTable(calls *atomic.Int64, scope geta.Scope, rows ...geta.Failure) geta.Table {
	create := func(ctx context.Context, in *scopeIn) (*ok, error) {
		calls.Add(1)
		if in.Body.Name == "fail" {
			return nil, errNotFound
		}
		return &ok{true}, nil
	}
	return geta.Table{
		Root: geta.Scope{geta.Secure(scopePolicy())},
		Routes: []geta.Entry{{Path: "/items", Route: geta.Route{
			Get:  geta.Op(http.StatusOK, okHandler, geta.Doc{}),
			Post: geta.Op(http.StatusCreated, create, geta.Doc{Failures: rows, Scope: scope}),
		}}},
	}
}

// A caller the operation's scope refuses is refused before the body is
// bound: an invalid body from a non-admin is a 403, not a 400, and the
// handler never runs. The other method of the URL stays open.
func TestOperationScopeRefusesBeforeBinding(t *testing.T) {
	var calls atomic.Int64
	c := getatest.New(t, scopeTable(&calls, geta.Scope{scopeAdmin()}))
	user, root := c.Bearer("ok"), c.Bearer("root")
	for name, body := range map[string]any{"malformed": `{"name":`, "invalid": map[string]any{"name": ""}, "valid": map[string]any{"name": "a"}} {
		if res := user.Post("/items", body); res.Status != http.StatusForbidden || res.Problem().Detail != "admin only" {
			t.Errorf("%s body as a user: %d %s", name, res.Status, res.Body)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("the handler ran %d times for a refused caller", n)
	}
	if res := root.Post("/items", `{"name":`); res.Status != http.StatusBadRequest {
		t.Fatalf("malformed body as root: %d %s", res.Status, res.Body)
	}
	if res := root.Post("/items", map[string]any{"name": "a"}); res.Status != http.StatusCreated {
		t.Fatalf("valid body as root: %d %s", res.Status, res.Body)
	}
	if res := user.Get("/items"); res.Status != http.StatusOK {
		t.Fatalf("read as a user: %d %s", res.Status, res.Body)
	}
	if res := c.Post("/items", map[string]any{"name": "a"}); res.Status != http.StatusUnauthorized {
		t.Fatalf("anonymous write: %d %s", res.Status, res.Body)
	}
}

// The operation's scope runs after the root and the directory scopes, and
// only for its method; a directory scope runs for every method of its URL.
func TestOperationScopeRunsInnermostForItsMethodOnly(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	record := func(tag string) geta.Middleware {
		return geta.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				m, ok := geta.Matched(r.Context())
				mu.Lock()
				seen = append(seen, fmt.Sprintf("%s %s %v %s", tag, r.Method, ok, m.Template))
				mu.Unlock()
				next.ServeHTTP(w, r)
			})
		})
	}
	write := func(context.Context, *empty) error { return nil }
	tbl := geta.Table{
		Root: geta.Scope{record("root")},
		Routes: []geta.Entry{{
			Path: "/items",
			Route: geta.Route{
				Get:    geta.Op(http.StatusOK, okHandler, geta.Doc{}),
				Delete: geta.OpNoBody(http.StatusNoContent, write, geta.Doc{Scope: geta.Scope{record("op1"), record("op2")}}),
			},
			Scopes: []geta.Scope{{record("dir")}},
		}},
	}
	c := getatest.New(t, tbl)
	c.Get("/items")
	c.Delete("/items")
	want := []string{
		"root GET true /items", "dir GET true /items",
		"root DELETE true /items", "dir DELETE true /items", "op1 DELETE true /items", "op2 DELETE true /items",
	}
	if !slices.Equal(seen, want) {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
	}
}

// What a middleware in an operation's scope answers is documented on that
// operation alone, and getatest holds the wire to it; a directory scope's
// answers are documented on every method of its URL.
func TestOperationScopeAnswersAreDocumentedOnItsOperation(t *testing.T) {
	var calls atomic.Int64
	tbl := scopeTable(&calls, geta.Scope{scopeAdmin()})
	shed := geta.Use(noop).Answers(http.StatusTooManyRequests, "Shed")
	tbl.Routes[0].Scopes = []geta.Scope{{shed}}
	c := getatest.New(t, tbl)
	m := doc(t, c.App())
	post := at(t, m, "paths", "/items", "post", "responses").(map[string]any)
	get := at(t, m, "paths", "/items", "get", "responses").(map[string]any)
	if at(t, post, "403", "description") != "Not an administrator" {
		t.Fatalf("POST 403: %v", post["403"])
	}
	if _, has := get["403"]; has {
		t.Fatalf("GET lists the write's 403: %v", get["403"])
	}
	for name, rs := range map[string]map[string]any{"GET": get, "POST": post} {
		if at(t, rs, "429", "description") != "Shed" {
			t.Errorf("%s 429: %v", name, rs["429"])
		}
	}
	a := c.App()
	postMatch := geta.Match{Template: "/items", Method: http.MethodPost}
	getMatch := geta.Match{Template: "/items", Method: http.MethodGet}
	if ok, _ := a.Documented(postMatch, http.StatusForbidden); !ok {
		t.Error("Documented: POST 403 is not listed")
	}
	if ok, _ := a.Documented(getMatch, http.StatusForbidden); ok {
		t.Error("Documented: GET 403 is listed")
	}
	// getatest fails a test whose response is undocumented; this one is.
	if res := c.Bearer("ok").Post("/items", map[string]any{"name": "a"}); res.Status != http.StatusForbidden {
		t.Fatalf("write as a user: %d", res.Status)
	}
}

// The operation's scope is part of the chain New checks for order: a stage
// that descends from the directory's, or from the root's, is refused,
// naming both. A zero middleware there is refused as anywhere else.
func TestOperationScopeOrderIsChecked(t *testing.T) {
	authn := geta.Ordered(geta.OrderAuthenticate, noop)
	authz := geta.Ordered(geta.OrderAuthorize, noop)
	write := func(context.Context, *empty) error { return nil }
	tbl := geta.Table{Routes: []geta.Entry{{
		Path: "/items",
		Route: geta.Route{
			Get:    geta.Op(http.StatusOK, okHandler, geta.Doc{}),
			Delete: geta.OpNoBody(http.StatusNoContent, write, geta.Doc{Scope: geta.Scope{authn}}),
		},
		Scopes: []geta.Scope{{authz}},
	}}}
	rejects(t, tbl, "DELETE /items", "authenticate", "authorize")

	tbl.Routes[0].Scopes = nil
	tbl.Root = geta.Scope{geta.Ordered(geta.OrderObserve, noop), authz}
	rejects(t, tbl, "DELETE /items", "authenticate", "authorize")

	tbl.Root = nil
	tbl.Routes[0].Route.Delete = geta.OpNoBody(http.StatusNoContent, write, geta.Doc{Scope: geta.Scope{authn, authz}})
	accepts(t, tbl)

	tbl.Routes[0].Route.Delete = geta.OpNoBody(http.StatusNoContent, write, geta.Doc{Scope: geta.Scope{{}}})
	rejects(t, tbl, "DELETE /items", "operation scope", "zero geta.Middleware")

	tbl.Routes[0].Route.Delete = geta.OpNoBody(http.StatusNoContent, write, geta.Doc{Scope: geta.Scope{geta.Invalid("bad", fmt.Errorf("broken"))}})
	rejects(t, tbl, "DELETE /items", "operation scope", "broken")
}

// A gate in an operation's scope secures that operation alone: New counts
// it when it resolves the requirement, the document lists the scheme and
// its 401 there only, and the gate checks the operation that is served,
// whatever a root middleware did to the method.
func TestSecureInAnOperationScope(t *testing.T) {
	override := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if m := r.Header.Get("X-Method"); m != "" {
				r.Method = m
			}
			next.ServeHTTP(w, r)
		})
	})
	gate := geta.Secure(scopePolicy())
	tbl := geta.Table{
		Root: geta.Scope{override},
		Routes: []geta.Entry{
			{Path: "/items", Route: geta.Route{
				Get:  geta.Op(http.StatusOK, okHandler, geta.Doc{}),
				Post: geta.Op(http.StatusCreated, whoami, geta.Doc{Scope: geta.Scope{gate}}),
			}},
			{Path: "/keyed", Route: geta.Route{
				Post: geta.Op(http.StatusCreated, whoami, geta.Doc{Security: []geta.Scheme{apiKey}, Scope: geta.Scope{gate}}),
			}},
		},
	}
	c := getatest.New(t, tbl)
	if res := c.Get("/items"); res.Status != http.StatusOK {
		t.Fatalf("open read: %d %s", res.Status, res.Body)
	}
	res := c.Post("/items", nil)
	if res.Status != http.StatusUnauthorized || res.Header.Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("anonymous write: %d %q %s", res.Status, res.Header.Get("WWW-Authenticate"), res.Body)
	}
	if res := c.Bearer("ok").Post("/items", nil); res.Status != http.StatusCreated || res.Text() != `{"id":"ada"}` {
		t.Fatalf("write: %d %s", res.Status, res.Body)
	}
	// A GET the root scope turns into a POST is served as the POST, and the
	// POST's gate checks it.
	if res := c.With("X-Method", http.MethodPost).Get("/items"); res.Status != http.StatusUnauthorized {
		t.Fatalf("a GET made a POST at the root: %d %s", res.Status, res.Body)
	}
	if res := c.With("X-API-Key", "k").Post("/keyed", nil); res.Status != http.StatusCreated {
		t.Fatalf("declared scheme: %d %s", res.Status, res.Body)
	}
	if res := c.Bearer("ok").Post("/keyed", nil); res.Status != http.StatusUnauthorized {
		t.Fatalf("an undeclared scheme: %d %s", res.Status, res.Body)
	}

	m := doc(t, c.App())
	if got := compact(t, at(t, m, "paths", "/items", "post", "security")); got != `[{"bearer":[]}]` {
		t.Fatalf("POST security: %s", got)
	}
	if got := compact(t, at(t, m, "paths", "/items", "get", "security")); got != `[]` {
		t.Fatalf("GET security: %s", got)
	}
	if got := compact(t, at(t, m, "paths", "/keyed", "post", "security")); got != `[{"apiKey":[]}]` {
		t.Fatalf("keyed security: %s", got)
	}
	if _, has := at(t, m, "paths", "/items", "get", "responses").(map[string]any)["401"]; has {
		t.Fatal("the open read lists the write's 401")
	}
	if at(t, m, "paths", "/items", "post", "responses", "401", "description") != "Unauthenticated" {
		t.Fatal("the write does not list its 401")
	}
	if got := compact(t, at(t, m, "components", "securitySchemes")); got != `{"apiKey":{"in":"header","name":"X-API-Key","type":"apiKey"},"bearer":{"scheme":"bearer","type":"http"}}` {
		t.Fatalf("schemes: %s", got)
	}
}

// An operation that requires a scheme has a gate when its own scope holds
// one, and has none when only a sibling operation's scope does.
func TestOperationScopeGateCountsForItsOperationOnly(t *testing.T) {
	gate := geta.Secure(scopePolicy())
	declared := []geta.Scheme{geta.Bearer}
	tbl := one("/items", geta.Route{
		Get:  geta.Op(http.StatusOK, whoami, geta.Doc{Security: declared}),
		Post: geta.Op(http.StatusCreated, whoami, geta.Doc{Security: declared, Scope: geta.Scope{gate}}),
	})
	rejects(t, tbl, "GET /items", "no geta.Secure gate")
	tbl.Routes[0].Route.Get = geta.Op(http.StatusOK, whoami, geta.Doc{Security: declared, Scope: geta.Scope{gate}})
	accepts(t, tbl)
}
