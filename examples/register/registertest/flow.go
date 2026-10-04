// Package registertest is the register example's HTTP flow, runnable over
// any store: the in-memory one, or the SQL one on any example engine.
package registertest

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/model"
	"github.com/koji-1009/geta/examples/register/routes"
	"github.com/koji-1009/geta/examples/register/store"
	"github.com/koji-1009/geta/getatest"
)

// Run drives create, conflict, fetch, list, replace, conditional replace,
// and delete through the real routes over users, as an administrator, and
// checks that a caller who is not one can write nothing.
func Run(t *testing.T, users store.Users) {
	env := app.Open()
	env.Users = users
	env.Log = slog.New(slog.DiscardHandler)
	anon := getatest.New(t, routes.Table(env))
	c := anon.Bearer("t-admin")
	ada := map[string]any{"id": "rt-1", "name": "Ada", "age": 36, "role": "member", "tags": []string{"x", "y"},
		"balance": "12.10", "active": false}

	expect := func(res *getatest.Response, status int, what string) {
		t.Helper()
		if res.Status != status {
			t.Fatalf("%s: %d, want %d: %s", what, res.Status, status, res.Body)
		}
	}
	// A role is set only through the admin route: a new user is a member.
	expect(c.Post("/users", map[string]any{"id": "rt-1", "name": "Ada", "role": "admin", "tags": []string{}}), http.StatusForbidden, "create an admin")
	expect(c.Post("/users", ada), http.StatusCreated, "create")
	res := c.Post("/users", ada)
	expect(res, http.StatusConflict, "a taken id")
	if res.Problem().Detail != "a user with this id exists" {
		t.Fatal(res.Problem())
	}
	expect(c.Put("/admin/users/rt-1/role", map[string]any{"role": "admin"}), http.StatusOK, "promote through the admin route")
	u := c.Get("/users/rt-1").JSON[model.User]()
	if u.Name != "Ada" || *u.Age != 36 || *u.Balance != "12.10" || *u.Active || len(u.Tags) != 2 || u.CreatedAt == nil {
		t.Fatalf("fetch: %+v", u)
	}
	expect(c.Post("/users", map[string]any{"id": "rt-2", "name": "Bo", "role": "member", "tags": []string{}}), http.StatusCreated, "second create")
	list := c.Get("/users?role=member").JSON[model.UserList]()
	if list.Total != 1 || list.Items[0].ID != "rt-2" || !*list.Items[0].Active {
		t.Fatalf("list: %+v", list)
	}
	page := c.Get("/users?limit=1&offset=1").JSON[model.UserList]()
	if page.Total != 2 || len(page.Items) != 1 || page.Items[0].ID != "rt-2" {
		t.Fatalf("page: %+v", page)
	}
	res = c.Put("/users/rt-1", map[string]any{"id": "rt-1", "name": "Ada B", "role": "admin", "tags": []string{}})
	expect(res, http.StatusOK, "replace")
	if v := res.JSON[model.User](); v.Name != "Ada B" || v.Age != nil || v.Role != model.RoleAdmin || !v.CreatedAt.Equal(*u.CreatedAt) {
		t.Fatalf("replace: %+v", v)
	}
	// A replace keeps the role: demoting the only admin through PUT would
	// skip the admin route's last-admin guard.
	res = c.Put("/users/rt-1", map[string]any{"id": "rt-1", "name": "Ada C", "role": "member", "tags": []string{}})
	expect(res, http.StatusForbidden, "replace with another role")
	if res.Problem().Detail != "a role is set only through /admin/users/{id}/role" {
		t.Fatal(res.Problem())
	}
	if v := c.Get("/users/rt-1").JSON[model.User](); v.Name != "Ada B" || v.Role != model.RoleAdmin {
		t.Fatalf("a refused replace changed the user: %+v", v)
	}
	expect(c.Put("/admin/users/rt-1/role", map[string]any{"role": "member"}), http.StatusConflict, "demote the only admin through the admin route")
	expect(c.Put("/users/none", map[string]any{"id": "none", "name": "X", "role": "member", "tags": []string{}}), http.StatusNotFound, "replace a missing user")
	// The last admin cannot be deleted either.
	res = c.Delete("/users/rt-1")
	expect(res, http.StatusConflict, "delete the only admin")
	if res.Problem().Detail != "the last admin cannot be demoted or deleted" {
		t.Fatal(res.Problem())
	}
	expect(c.Get("/users/rt-1"), http.StatusOK, "a refused delete kept the user")

	lostUpdates(t, anon, expect)
	expect(c.Put("/admin/users/rt-2/role", map[string]any{"role": "admin"}), http.StatusOK, "promote a second admin")
	expect(c.Delete("/users/rt-1"), http.StatusNoContent, "delete one of two admins")
	expect(c.Delete("/users/rt-1"), http.StatusNotFound, "delete again")
	expect(c.Get("/users/rt-1"), http.StatusNotFound, "fetch deleted")

	// Import is all or none: a batch with one taken id creates nothing.
	batch := func(ids ...string) map[string]any {
		var us []map[string]any
		for _, id := range ids {
			us = append(us, map[string]any{"id": id, "name": id, "role": "member", "tags": []string{}})
		}
		return map[string]any{"users": us}
	}
	expect(c.Post("/users/import", batch("im-1", "rt-2")), http.StatusConflict, "import with a taken id")
	expect(c.Get("/users/im-1"), http.StatusNotFound, "a refused batch created nothing")
	expect(c.Post("/users/import", batch("im-1", "im-1")), http.StatusConflict, "import with one id twice")
	// A new user is a member: one admin in the batch refuses all of it.
	withAdmin := batch("im-1", "im-2")
	withAdmin["users"].([]map[string]any)[1]["role"] = "admin"
	res = c.Post("/users/import", withAdmin)
	expect(res, http.StatusForbidden, "import an admin")
	if res.Problem().Detail != "a role is set only through /admin/users/{id}/role" {
		t.Fatal(res.Problem())
	}
	expect(c.Get("/users/im-1"), http.StatusNotFound, "a batch refused for a role created nothing")
	res = c.Post("/users/import", batch("im-1", "im-2"))
	expect(res, http.StatusCreated, "import")
	if res.Text() != `{"created":2}` {
		t.Fatalf("import: %s", res.Body)
	}

	// Roles: the last admin cannot be demoted or deleted.
	role := func(id, r string) *getatest.Response {
		return c.Put("/admin/users/"+id+"/role", map[string]any{"role": r})
	}
	expect(role("im-1", "admin"), http.StatusOK, "promote")
	expect(role("nope", "admin"), http.StatusNotFound, "promote a missing user")
	expect(role("rt-2", "member"), http.StatusOK, "demote one of two")
	expect(role("im-1", "member"), http.StatusConflict, "demote the last admin")
	expect(c.Delete("/users/im-1"), http.StatusConflict, "delete the last admin")
	expect(role("im-2", "admin"), http.StatusOK, "promote a second")
	expect(role("im-1", "member"), http.StatusOK, "demote one of two")

	// Writes are an administrator's: a bearer user who is not one is refused
	// each, and the store is as it was. Reads stay open to that user.
	user := anon.Bearer("t-user")
	if res := user.Get("/whoami"); res.Text() != `{"id":"bo","admin":false}` {
		t.Fatalf("whoami: %s", res.Body)
	}
	before := c.Get("/users").Text()
	refused := func(res *getatest.Response, what string) {
		t.Helper()
		expect(res, http.StatusForbidden, what)
		if res.Problem().Detail != "admin only" {
			t.Fatalf("%s: %+v", what, res.Problem())
		}
	}
	refused(user.Post("/users", map[string]any{"id": "nu-1", "name": "N", "role": "member", "tags": []string{}}), "create as a user")
	refused(user.Put("/users/im-2", map[string]any{"id": "im-2", "name": "Renamed", "role": "admin", "tags": []string{}}), "replace an admin as a user")
	refused(user.Delete("/users/im-2"), "delete an admin as a user")
	refused(user.Delete("/users/rt-2"), "delete a member as a user")
	refused(user.Post("/users/import", batch("nu-2")), "import as a user")
	if after := c.Get("/users").Text(); after != before {
		t.Fatalf("a refused write changed the store:\n%s\n%s", before, after)
	}
	expect(user.Get("/users/im-2"), http.StatusOK, "fetch as a user")

	if s := user.Get("/stats/users").Text(); s != `{"total":3,"admins":1}` {
		t.Fatalf("stats: %s", s)
	}
}

// lostUpdates has two administrators fetch the admin rt-1 and each replace
// it naming the ETag fetched: the first wins, and the second, whose copy is
// stale, gets 412 and changes nothing until it fetches again. Then writers
// holding one tag race, and exactly one wins.
func lostUpdates(t *testing.T, anon *getatest.Client, expect func(*getatest.Response, int, string)) {
	t.Helper()
	alice, bob := anon.Bearer("t-admin"), anon.Bearer("t-admin")
	replace := func(c *getatest.Client, tag, name string) *getatest.Response {
		if tag != "" {
			c = c.With("If-Match", tag)
		}
		return c.Put("/users/rt-1", map[string]any{"id": "rt-1", "name": name, "role": "admin", "tags": []string{}})
	}
	tag := alice.Get("/users/rt-1").Header.Get("ETag")
	if tag == "" || bob.Get("/users/rt-1").Header.Get("ETag") != tag {
		t.Fatalf("a fetch answers no ETag, or two fetches of one user answer two: %q", tag)
	}
	res := replace(alice, tag, "Ada by Alice")
	expect(res, http.StatusOK, "the first replace of the user fetched")
	next := res.Header.Get("ETag")
	if next == "" || next == tag {
		t.Fatalf("a replace answers the tag %q, after %q", next, tag)
	}
	res = replace(bob, tag, "Ada by Bob")
	expect(res, http.StatusPreconditionFailed, "a replace of a user changed since its fetch")
	if d := res.Problem().Detail; !strings.HasPrefix(d, "If-Match failed") {
		t.Fatal(d)
	}
	if u := alice.Get("/users/rt-1").JSON[model.User](); u.Name != "Ada by Alice" {
		t.Fatalf("the stale replace was written: %+v", u)
	}
	expect(bob.With("If-None-Match", next).Get("/users/rt-1"), http.StatusNotModified, "a fetch of the user the client holds")
	expect(bob.With("If-None-Match", tag).Get("/users/rt-1"), http.StatusOK, "a fetch by a client holding a stale user")
	if got := bob.Get("/users/rt-1").Header.Get("ETag"); got != next {
		t.Fatalf("a refetch answers %q, the replace %q", got, next)
	}
	expect(replace(bob, next, "Ada by Bob"), http.StatusOK, "the retry on the user fetched again")
	// The precondition is a client's to send, and a missing user is a 404
	// whatever it says.
	expect(replace(alice, "", "Ada B"), http.StatusOK, "an unconditional replace")
	expect(alice.With("If-Match", tag).Put("/users/none", map[string]any{"id": "none", "name": "X", "role": "member", "tags": []string{}}),
		http.StatusNotFound, "a conditional replace of a missing user")
	expect(alice.With("If-None-Match", "*").Put("/users/rt-1", map[string]any{"id": "rt-1", "name": "X", "role": "admin", "tags": []string{}}),
		http.StatusPreconditionFailed, "a replace meant to create a user that exists")
	expect(alice.With("If-Match", `"v1", *`).Put("/users/rt-1", map[string]any{"id": "rt-1", "name": "X", "role": "admin", "tags": []string{}}),
		http.StatusBadRequest, "a replace with a malformed If-Match")

	// Writers holding the same tag race: the store checks it inside its
	// write, so exactly one wins and the others get 412.
	tag = alice.Get("/users/rt-1").Header.Get("ETag")
	statuses := make([]int, 8)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Go(func() { statuses[i] = replace(alice, tag, "Ada "+strconv.Itoa(i)).Status })
	}
	wg.Wait()
	won := 0
	for _, s := range statuses {
		switch s {
		case http.StatusOK:
			won++
		case http.StatusPreconditionFailed:
		default:
			t.Fatalf("a racing replace answered %d: %v", s, statuses)
		}
	}
	if won != 1 {
		t.Fatalf("%d of the racing replaces won: %v", won, statuses)
	}
	expect(replace(alice, "", "Ada B"), http.StatusOK, "restore the name")
}
