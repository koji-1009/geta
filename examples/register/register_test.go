package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"testing"

	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/model"
	"github.com/koji-1009/geta/examples/register/registertest"
	"github.com/koji-1009/geta/examples/register/routes"
	user "github.com/koji-1009/geta/examples/register/routes/users/id_"
	"github.com/koji-1009/geta/examples/register/store"
	"github.com/koji-1009/geta/getatest"
)

// anonymous is a client with no credentials.
func anonymous(t *testing.T) *getatest.Client {
	env := app.Open()
	env.Log = slog.New(slog.DiscardHandler)
	return getatest.New(t, routes.Table(env), routes.Options(env)...)
}

// client is an administrator's client.
func client(t *testing.T) *getatest.Client { return anonymous(t).Bearer("t-admin") }

func TestSecureByDefault(t *testing.T) {
	c := anonymous(t)
	if res := c.Get("/health"); res.Status != http.StatusOK {
		t.Fatalf("health: %d", res.Status)
	}
	for _, p := range []string{"/users", "/users/1", "/whoami", "/admin/ping", "/nope"} {
		if res := c.Get(p); res.Status != http.StatusUnauthorized {
			t.Errorf("%s: %d", p, res.Status)
		}
	}
	user := c.Bearer("t-user")
	if res := user.Get("/whoami"); res.Text() != `{"id":"bo","admin":false}` {
		t.Fatalf("whoami: %s", res.Body)
	}
	if res := user.Get("/admin/ping"); res.Status != http.StatusForbidden {
		t.Fatalf("admin/ping as user: %d", res.Status)
	}
	if res := c.Bearer("t-admin").Get("/admin/ping"); res.Text() != `{"pong":true}` {
		t.Fatalf("admin/ping as admin: %s", res.Body)
	}
}

// The document is committed; a change to it is a reviewed diff.
func TestOpenAPIGolden(t *testing.T) {
	getatest.Golden(t, client(t).App(), "openapi.json")
}

func TestCRUD(t *testing.T) {
	c := client(t)
	ada := map[string]any{"id": "1", "name": "Ada", "role": "member", "tags": []string{"x", "y"}}

	res := c.Post("/users", ada)
	if res.Status != http.StatusCreated || res.Header.Get("Location") != "/users/1" {
		t.Fatalf("create: %d %q %s", res.Status, res.Header.Get("Location"), res.Body)
	}
	if res := c.Post("/users", ada); res.Status != http.StatusConflict {
		t.Fatalf("duplicate create: %d %s", res.Status, res.Body)
	}
	if res := c.Put("/admin/users/1/role", map[string]any{"role": "admin"}); res.Status != http.StatusOK {
		t.Fatalf("promote: %d %s", res.Status, res.Body)
	}

	res = c.Get("/users/1")
	if res.Status != http.StatusOK {
		t.Fatalf("get: %d %s", res.Status, res.Body)
	}
	u := res.JSON[model.User]()
	if u.Name != "Ada" || u.CreatedAt == nil || u.Active == nil || !*u.Active {
		t.Fatalf("get: %+v", u)
	}

	list := c.Get("/users?role=admin&limit=10").JSON[model.UserList]()
	if list.Total != 1 || len(list.Items) != 1 {
		t.Fatalf("list: %+v", list)
	}
	if got := c.Get("/users/by-role/member").JSON[model.UserList](); got.Total != 0 || got.Items == nil {
		t.Fatalf("by-role: %+v", got)
	}

	if res := c.Get("/users/1/tags/1"); res.Text() != `{"tag":"y"}` {
		t.Fatalf("tag: %d %s", res.Status, res.Body)
	}
	if res := c.Get("/users/1/tags/2"); res.Status != http.StatusNotFound || res.Problem().Detail != "tag index out of range" {
		t.Fatalf("tag out of range: %d %s", res.Status, res.Body)
	}

	res = c.Put("/users/1", map[string]any{"id": "2", "name": "Ada", "role": "admin", "tags": []string{}})
	if res.Status != http.StatusBadRequest || res.Problem().Detail != "body id does not match path id" {
		t.Fatalf("put mismatch: %d %s", res.Status, res.Body)
	}
	res = c.Put("/users/1", map[string]any{"id": "1", "name": "Ada B", "role": "admin", "tags": []string{}})
	if res.Status != http.StatusOK || res.JSON[model.User]().Name != "Ada B" {
		t.Fatalf("put: %d %s", res.Status, res.Body)
	}
	if res := c.Put("/admin/users/1/role", map[string]any{"role": "member"}); res.Status != http.StatusConflict {
		t.Fatalf("demote the only admin: %d %s", res.Status, res.Body)
	}
	if res := c.Delete("/users/1"); res.Status != http.StatusConflict || res.Problem().Detail != "the last admin cannot be demoted or deleted" {
		t.Fatalf("delete the only admin: %d %s", res.Status, res.Body)
	}
	if res := c.Post("/users", map[string]any{"id": "2", "name": "Bo", "role": "member", "tags": []string{}}); res.Status != http.StatusCreated {
		t.Fatalf("create a second: %d %s", res.Status, res.Body)
	}
	if res := c.Put("/admin/users/2/role", map[string]any{"role": "admin"}); res.Status != http.StatusOK {
		t.Fatalf("promote a second: %d %s", res.Status, res.Body)
	}

	if res := c.Delete("/users/1"); res.Status != http.StatusNoContent || len(res.Body) != 0 {
		t.Fatalf("delete: %d %s", res.Status, res.Body)
	}
	if res := c.Delete("/users/1"); res.Status != http.StatusNotFound {
		t.Fatalf("second delete: %d %s", res.Status, res.Body)
	}
}

// A role changes only through /admin/users/{id}/role: even an administrator
// can neither create nor import an admin, nor change a role with PUT
// /users/{id}, so the role route's last-admin guard cannot be skipped.
func TestRolesChangeOnlyThroughTheAdminRoute(t *testing.T) {
	anon := anonymous(t)
	admin, user := anon.Bearer("t-admin"), anon.Bearer("t-user")
	const detail = "a role is set only through /admin/users/{id}/role"
	if res := admin.Post("/users", map[string]any{"id": "x", "name": "X", "role": "admin", "tags": []string{}}); res.Status != http.StatusForbidden || res.Problem().Detail != detail {
		t.Fatalf("create an admin: %d %s", res.Status, res.Body)
	}
	batch := map[string]any{"users": []map[string]any{
		{"id": "y", "name": "Y", "role": "member", "tags": []string{}},
		{"id": "x", "name": "X", "role": "admin", "tags": []string{}},
	}}
	if res := admin.Post("/users/import", batch); res.Status != http.StatusForbidden || res.Problem().Detail != detail {
		t.Fatalf("import an admin: %d %s", res.Status, res.Body)
	}
	for _, id := range []string{"x", "y"} {
		if res := user.Get("/users/" + id); res.Status != http.StatusNotFound {
			t.Fatalf("a refused import stored %s: %d %s", id, res.Status, res.Body)
		}
	}
	if res := user.Get("/users/x"); res.Status != http.StatusNotFound {
		t.Fatalf("a refused create stored the user: %d %s", res.Status, res.Body)
	}
	if res := admin.Post("/users", map[string]any{"id": "x", "name": "X", "role": "member", "tags": []string{}}); res.Status != http.StatusCreated {
		t.Fatalf("create: %d %s", res.Status, res.Body)
	}
	if res := admin.Put("/users/x", map[string]any{"id": "x", "name": "X", "role": "admin", "tags": []string{}}); res.Status != http.StatusForbidden || res.Problem().Detail != detail {
		t.Fatalf("promote through PUT: %d %s", res.Status, res.Body)
	}
	if res := admin.Put("/admin/users/x/role", map[string]any{"role": "admin"}); res.Status != http.StatusOK {
		t.Fatalf("promote: %d %s", res.Status, res.Body)
	}
	if res := admin.Put("/users/x", map[string]any{"id": "x", "name": "X", "role": "member", "tags": []string{}}); res.Status != http.StatusForbidden || res.Problem().Detail != detail {
		t.Fatalf("demote through PUT: %d %s", res.Status, res.Body)
	}
	if u := user.Get("/users/x").JSON[model.User](); u.Role != model.RoleAdmin {
		t.Fatalf("role: %s", u.Role)
	}
}

// Writes on user records are an administrator's: a bearer user who is not
// one gets 403 on each, and nothing changes. Reads stay open to any caller.
func TestUserWritesAreAdminOnly(t *testing.T) {
	anon := anonymous(t)
	admin, user := anon.Bearer("t-admin"), anon.Bearer("t-user")
	if res := user.Get("/whoami"); res.Text() != `{"id":"bo","admin":false}` {
		t.Fatalf("whoami: %s", res.Body)
	}
	if res := admin.Post("/users", map[string]any{"id": "a", "name": "A", "role": "member", "tags": []string{"x"}}); res.Status != http.StatusCreated {
		t.Fatalf("seed: %d %s", res.Status, res.Body)
	}
	if res := admin.Put("/admin/users/a/role", map[string]any{"role": "admin"}); res.Status != http.StatusOK {
		t.Fatalf("promote: %d %s", res.Status, res.Body)
	}
	if res := admin.Post("/users", map[string]any{"id": "m", "name": "M", "role": "member", "tags": []string{}}); res.Status != http.StatusCreated {
		t.Fatalf("seed: %d %s", res.Status, res.Body)
	}
	before := admin.Get("/users").Text()

	const detail = "admin only"
	writes := map[string]*getatest.Response{
		"create":                 user.Post("/users", map[string]any{"id": "n", "name": "N", "role": "member", "tags": []string{}}),
		"replace a member":       user.Put("/users/m", map[string]any{"id": "m", "name": "Renamed", "role": "member", "tags": []string{}}),
		"replace an admin":       user.Put("/users/a", map[string]any{"id": "a", "name": "Renamed", "role": "admin", "tags": []string{}}),
		"replace a missing user": user.Put("/users/none", map[string]any{"id": "none", "name": "X", "role": "member", "tags": []string{}}),
		"delete a member":        user.Delete("/users/m"),
		"delete an admin":        user.Delete("/users/a"),
		"delete a missing user":  user.Delete("/users/none"),
		"import":                 user.Post("/users/import", map[string]any{"users": []map[string]any{{"id": "i", "name": "I", "role": "member", "tags": []string{}}}}),
	}
	for name, res := range writes {
		if res.Status != http.StatusForbidden || res.Problem().Detail != detail {
			t.Errorf("%s as a user: %d %s", name, res.Status, res.Body)
		}
	}
	if after := admin.Get("/users").Text(); after != before {
		t.Fatalf("a refused write changed the users:\n%s\n%s", before, after)
	}

	for _, p := range []string{"/users", "/users/a", "/users/by-role/admin", "/users/a/tags/0", "/stats/users", "/whoami"} {
		if res := user.Get(p); res.Status != http.StatusOK {
			t.Errorf("read %s as a user: %d %s", p, res.Status, res.Body)
		}
	}
	if s := user.Stream("/users/events"); s.Response.Status != http.StatusOK {
		t.Errorf("feed as a user: %d", s.Response.Status)
	}
}

// A caller who may not write is refused before the request's contract is
// checked: an invalid body from a user who is not an administrator is a
// 403, not a 400 that would describe the contract.
func TestNotAdminIsRefusedBeforeTheContract(t *testing.T) {
	user := anonymous(t).Bearer("t-user")
	writes := map[string]*getatest.Response{
		"create":  user.Post("/users", map[string]any{"id": "n", "x": 1}),
		"replace": user.Put("/users/m", `{"id":`),
		"delete":  user.Delete("/users/" + strings.Repeat("a", 65)),
		"import":  user.Post("/users/import", map[string]any{"users": []any{}}),
	}
	for name, res := range writes {
		if res.Status != http.StatusForbidden || res.Problem().Detail != "admin only" {
			t.Errorf("%s with an invalid request as a user: %d %s", name, res.Status, res.Body)
		}
	}
}

// The document lists 403 for not being an administrator on exactly the
// operations that require one, beside any other reason the operation has
// for a 403.
func TestAdminOnlyIsDocumentedWhereItApplies(t *testing.T) {
	var doc struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Description string `json:"description"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(client(t).App().OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	const notAdmin = "Not an administrator"
	const role = "a role is set only through /admin/users/{id}/role (type /problems/role-change)"
	want := map[string]string{
		"post /users":                         role + "; " + notAdmin,
		"put /users/{id}":                     role + "; " + notAdmin,
		"delete /users/{id}":                  notAdmin,
		"put /users/{id}/avatar":              notAdmin,
		"post /users/{id}/attachments":        notAdmin,
		"post /users/import":                  role + "; " + notAdmin,
		"get /admin/ping":                     notAdmin,
		"put /admin/users/{id}/role":          notAdmin,
		"post /teams":                         notAdmin,
		"put /teams/{team}":                   notAdmin,
		"patch /teams/{team}":                 notAdmin,
		"delete /teams/{team}":                notAdmin,
		"put /teams/{team}/members/{user}":    notAdmin,
		"delete /teams/{team}/members/{user}": notAdmin,
	}
	for path, item := range doc.Paths {
		for method, op := range item {
			key := method + " " + path
			got := op.Responses["403"].Description
			if got != want[key] {
				t.Errorf("%s: 403 %q, want %q", key, got, want[key])
			}
		}
	}
}

// /users/by-role/{role} pages as /users does: 20 by default, at most 100 a
// page, with the total beside the page.
func TestByRolePages(t *testing.T) {
	c := client(t)
	for i := range 25 {
		id := fmt.Sprint("m", i)
		if res := c.Post("/users", map[string]any{"id": id, "name": id, "role": "member", "tags": []string{}}); res.Status != http.StatusCreated {
			t.Fatalf("seed %s: %d %s", id, res.Status, res.Body)
		}
	}
	for q, want := range map[string]int{"": 20, "?limit=5": 5, "?limit=10&offset=20": 5, "?offset=25": 0, "?limit=100": 25} {
		got := c.Get("/users/by-role/member" + q).JSON[model.UserList]()
		if got.Total != 25 || len(got.Items) != want {
			t.Errorf("%q: %d items of %d, want %d of 25", q, len(got.Items), got.Total, want)
		}
	}
	for _, q := range []string{"?limit=101", "?limit=0", "?offset=-1"} {
		if res := c.Get("/users/by-role/member" + q); res.Status != http.StatusBadRequest {
			t.Errorf("%q: %d %s", q, res.Status, res.Body)
		}
	}
}

// A store out of reach is a 503 on every route that reads it.
func TestUnavailableStoreIs503(t *testing.T) {
	env := app.Open()
	env.Log = slog.New(slog.DiscardHandler)
	env.Users = unavailableStore{env.Users}
	c := getatest.New(t, routes.Table(env), routes.Options(env)...).Bearer("t-admin")
	for _, p := range []string{"/users", "/users/1", "/users/by-role/admin", "/users/1/tags/0"} {
		if res := c.Get(p); res.Status != http.StatusServiceUnavailable {
			t.Errorf("%s: %d %s", p, res.Status, res.Body)
		}
	}
	// Adding a team member reads the user store too.
	if res := c.Post("/teams", map[string]any{"id": "core", "name": "Core"}); res.Status != http.StatusCreated {
		t.Fatalf("create a team: %d %s", res.Status, res.Body)
	}
	if res := c.Put("/teams/core/members/1", nil); res.Status != http.StatusServiceUnavailable || res.Problem().Detail != "the store is unavailable" {
		t.Errorf("add a member: %d %s", res.Status, res.Body)
	}
}

type unavailableStore struct{ store.Users }

// errUnavailable is a driver error as an adapter's classifier wraps it.
var errUnavailable = fmt.Errorf("%w: %w", store.ErrUnavailable, errors.New("connection refused"))

func (unavailableStore) List(context.Context, store.Filter) ([]model.User, int, error) {
	return nil, 0, errUnavailable
}

func (unavailableStore) Find(context.Context, string) (*model.User, error) {
	return nil, errUnavailable
}

func TestFlowOnTheMemoryStore(t *testing.T) {
	registertest.Run(t, store.NewMemory())
}

// The routes and the store match database failures by the store's errors
// only: no driver is among their dependencies.
func TestRoutesImportNoDriver(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the go command")
	}
	out, err := exec.Command("go", "list", "-deps", "./routes/...", "./store/...").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, driver := range []string{"modernc.org/sqlite", "github.com/jackc/pgx"} {
		if strings.Contains(string(out), driver) {
			t.Errorf("a route depends on %s", driver)
		}
	}
}

func TestEventsFeed(t *testing.T) {
	c := client(t)
	if s := anonymous(t).Stream("/users/events"); s.Response.Status != http.StatusUnauthorized {
		t.Fatalf("anonymous feed: %d", s.Response.Status)
	}
	feed := c.Stream("/users/events")
	if feed.Response.Status != http.StatusOK {
		t.Fatal(feed.Response.Status)
	}
	// The subscription is in place once the response has started.
	c.Post("/users", map[string]any{"id": "e1", "name": "E", "role": "member", "tags": []string{}})
	c.Post("/users", map[string]any{"id": "e1", "name": "E", "role": "member", "tags": []string{}}) // a conflict announces nothing
	c.Put("/users/e1", map[string]any{"id": "e1", "name": "F", "role": "member", "tags": []string{}})
	c.Delete("/users/e1")
	var got []string
	for range 3 {
		e, ok := feed.Next()
		if !ok {
			t.Fatal("the feed ended")
		}
		got = append(got, e.Name+" "+e.Data)
	}
	want := `created {"kind":"created","id":"e1"}|updated {"kind":"updated","id":"e1"}|deleted {"kind":"deleted","id":"e1"}`
	if strings.Join(got, "|") != want {
		t.Fatal(got)
	}
}

func TestContractViolationsAre400(t *testing.T) {
	c := client(t)
	cases := map[string]*getatest.Response{
		"unknown role in path":   c.Get("/users/by-role/owner"),
		"limit above maximum":    c.Get("/users?limit=1000"),
		"non-integer index":      c.Get("/users/1/tags/abc"),
		"unknown body member":    c.Post("/users", map[string]any{"id": "1", "name": "A", "role": "admin", "tags": []string{}, "x": 1}),
		"missing required":       c.Post("/users", map[string]any{"id": "1", "role": "admin", "tags": []string{}}),
		"duplicate tags":         c.Post("/users", map[string]any{"id": "1", "name": "A", "role": "admin", "tags": []string{"a", "a"}}),
		"malformed json":         c.Post("/users", `{"id":`),
		"wrong case member name": c.Post("/users", map[string]any{"ID": "1", "name": "A", "role": "admin", "tags": []string{}}),
	}
	for name, res := range cases {
		if res.Status != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, res.Status, res.Body)
			continue
		}
		if p := res.Problem(); p.Status != 400 {
			t.Errorf("%s: problem %+v", name, p)
		}
	}
}

// The handler is a method: test it with a fake store and no HTTP at all.
func TestHandlerDirectly(t *testing.T) {
	h := user.Handler{Users: fakeStore{}}
	_, err := h.Get(t.Context(), &user.GetIn{ID: "7"})
	if err != store.ErrNotFound {
		t.Fatalf("got %v", err)
	}
}

type fakeStore struct{}

func (fakeStore) Find(context.Context, string) (*model.User, error) { return nil, store.ErrNotFound }
func (fakeStore) Replace(context.Context, model.User, func(model.User) error) (*model.User, error) {
	return nil, store.ErrNotFound
}
func (fakeStore) Delete(context.Context, string) error { return store.ErrNotFound }
