package main

import (
	"net/http"
	"testing"

	"github.com/koji-1009/geta/examples/register/teams"
	"github.com/koji-1009/geta/getatest"
)

func TestTeams(t *testing.T) {
	c := client(t)
	expect := func(res *getatest.Response, status int, detail string) {
		t.Helper()
		if res.Status != status {
			t.Fatalf("%d, want %d: %s", res.Status, status, res.Body)
		}
		if detail != "" && res.Problem().Detail != detail {
			t.Fatalf("detail %q, want %q", res.Problem().Detail, detail)
		}
	}
	c.Post("/users", map[string]any{"id": "u1", "name": "U", "role": "member", "tags": []string{}})

	res := c.Post("/teams", map[string]any{"id": "core", "name": "Core"})
	expect(res, http.StatusCreated, "")
	if res.Header.Get("Location") != "/teams/core" {
		t.Fatal(res.Header)
	}
	expect(c.Post("/teams", map[string]any{"id": "core", "name": "Again"}), http.StatusConflict, "a team with this id exists")
	expect(c.Get("/teams/none"), http.StatusNotFound, "team not found")

	expect(c.Put("/teams/core/members/u1", nil), http.StatusNoContent, "")
	expect(c.Put("/teams/core/members/u1", nil), http.StatusConflict, "the user is already a member")
	expect(c.Put("/teams/core/members/ghost", nil), http.StatusNotFound, "user not found")
	expect(c.Put("/teams/none/members/u1", nil), http.StatusNotFound, "team not found")
	if got := c.Get("/teams/core").JSON[teams.Team](); len(got.Members) != 1 || got.Members[0] != "u1" {
		t.Fatalf("%+v", got)
	}

	expect(c.Delete("/teams/core"), http.StatusConflict, "the team still has members")
	expect(c.Delete("/teams/core/members/u1"), http.StatusNoContent, "")
	expect(c.Delete("/teams/core/members/u1"), http.StatusNotFound, "the user is not a member")
	expect(c.Delete("/teams/core"), http.StatusNoContent, "")
	if got := c.Get("/teams").Text(); got != `{"items":[]}` {
		t.Fatal(got)
	}
}

// PUT /teams/{team} creates or replaces, and the client says which it means
// with a precondition: If-None-Match: * creates only, If-Match replaces only
// the version it names. Without one, PUT does whichever the store needs.
func TestTeamPutCreatesOrReplaces(t *testing.T) {
	c := client(t)
	expect := func(res *getatest.Response, status int, what string) *getatest.Response {
		t.Helper()
		if res.Status != status {
			t.Fatalf("%s: %d, want %d: %s", what, res.Status, status, res.Body)
		}
		return res
	}
	createOnly := c.With("If-None-Match", "*")
	res := expect(createOnly.Put("/teams/ops", map[string]any{"name": "Ops", "description": "on call"}), http.StatusOK, "create with If-None-Match: *")
	tag := res.Header.Get("ETag")
	if got := res.JSON[teams.Team](); got.ID != "ops" || got.Name != "Ops" || *got.Description != "on call" || len(got.Members) != 0 || tag == "" {
		t.Fatalf("created %+v, tag %q", got, tag)
	}
	if res := expect(createOnly.Put("/teams/ops", map[string]any{"name": "Again"}), http.StatusPreconditionFailed, "a second create"); res.Problem().Detail == "" {
		t.Fatal("a 412 without a detail")
	}
	expect(c.With("If-Match", tag).Put("/teams/none", map[string]any{"name": "X"}), http.StatusPreconditionFailed, "a replace of a team that does not exist")
	expect(c.Get("/teams/none"), http.StatusNotFound, "a refused replace created nothing")

	// A replace keeps the members; a description left out is none.
	c.Post("/users", map[string]any{"id": "u1", "name": "U", "role": "member", "tags": []string{}})
	expect(c.Put("/teams/ops/members/u1", nil), http.StatusNoContent, "add a member")
	tag = c.Get("/teams/ops").Header.Get("ETag")
	res = expect(c.With("If-Match", tag).Put("/teams/ops", map[string]any{"name": "Operations"}), http.StatusOK, "replace the version held")
	if got := res.JSON[teams.Team](); got.Name != "Operations" || got.Description != nil || len(got.Members) != 1 {
		t.Fatalf("replaced %+v", got)
	}
	expect(c.With("If-Match", tag).Put("/teams/ops", map[string]any{"name": "Stale"}), http.StatusPreconditionFailed, "replace a version since changed")
	expect(c.Put("/teams/ops", map[string]any{"name": "Ops"}), http.StatusOK, "an unconditional replace")
	expect(c.Put("/teams/dev", map[string]any{"name": "Dev"}), http.StatusOK, "an unconditional create")
	expect(c.Put("/teams/Not-Valid", map[string]any{"name": "X"}), http.StatusBadRequest, "an id the team's pattern refuses")
	if got := c.Get("/teams").JSON[struct {
		Items []teams.Team `json:"items"`
	}](); len(got.Items) != 2 {
		t.Fatalf("teams: %+v", got)
	}
}

// PATCH /teams/{team} is a merge patch: a member left out is kept, null
// clears the description, a value sets it, and a null name, which a team
// cannot be without, is refused before the handler runs. If-Match makes the
// change conditional on the version the client read.
func TestTeamPatch(t *testing.T) {
	c := client(t)
	expect := func(res *getatest.Response, status int, what string) teams.Team {
		t.Helper()
		if res.Status != status {
			t.Fatalf("%s: %d, want %d: %s", what, res.Status, status, res.Body)
		}
		if status != http.StatusOK {
			return teams.Team{}
		}
		return res.JSON[teams.Team]()
	}
	c.Post("/teams", map[string]any{"id": "core", "name": "Core", "description": "the platform"})

	got := expect(c.Patch("/teams/core", map[string]any{"name": "Core team"}), http.StatusOK, "set the name")
	if got.Name != "Core team" || got.Description == nil || *got.Description != "the platform" {
		t.Fatalf("a description left out was not kept: %+v", got)
	}
	got = expect(c.Patch("/teams/core", map[string]any{"description": "platform and tools"}), http.StatusOK, "set the description")
	if got.Name != "Core team" || *got.Description != "platform and tools" {
		t.Fatalf("%+v", got)
	}
	got = expect(c.Patch("/teams/core", map[string]any{"description": nil}), http.StatusOK, "clear the description")
	if got.Name != "Core team" || got.Description != nil {
		t.Fatalf("null did not clear the description: %+v", got)
	}
	got = expect(c.Patch("/teams/core", map[string]any{}), http.StatusOK, "an empty patch")
	if got.Name != "Core team" || got.Description != nil {
		t.Fatalf("an empty patch changed the team: %+v", got)
	}
	res := c.Patch("/teams/core", map[string]any{"name": nil})
	expect(res, http.StatusBadRequest, "a null name")
	if p := res.Problem(); len(p.Errors) != 1 || p.Errors[0].Path != "$.name" {
		t.Fatalf("a null name: %+v", p)
	}
	expect(c.Patch("/teams/core", map[string]any{"members": []string{"x"}}), http.StatusBadRequest, "a member the patch does not name")
	expect(c.Patch("/teams/none", map[string]any{"name": "X"}), http.StatusNotFound, "patch a missing team")

	// Two clients read the same version; the first change wins, the second
	// is refused until it reads the team again.
	tag := c.Get("/teams/core").Header.Get("ETag")
	first := c.With("If-Match", tag).Patch("/teams/core", map[string]any{"description": "first"})
	expect(first, http.StatusOK, "the first conditional change")
	expect(c.With("If-Match", tag).Patch("/teams/core", map[string]any{"description": "second"}), http.StatusPreconditionFailed, "a change of a stale version")
	expect(c.With("If-None-Match", first.Header.Get("ETag")).Get("/teams/core"), http.StatusNotModified, "a fetch of the version held")
	if got := c.Get("/teams/core").JSON[teams.Team](); *got.Description != "first" {
		t.Fatalf("the stale change was written: %+v", got)
	}
}

// Team writes are an administrator's, as user writes are: a bearer user who
// is not one gets 403 on each, before the contract is checked, and nothing
// changes. Reads stay open to any authenticated caller.
func TestTeamWritesAreAdminOnly(t *testing.T) {
	anon := anonymous(t)
	admin, user := anon.Bearer("t-admin"), anon.Bearer("t-user")
	admin.Post("/users", map[string]any{"id": "u1", "name": "U", "role": "member", "tags": []string{}})
	if res := admin.Post("/teams", map[string]any{"id": "core", "name": "Core"}); res.Status != http.StatusCreated {
		t.Fatalf("seed: %d %s", res.Status, res.Body)
	}
	if res := admin.Put("/teams/core/members/u1", nil); res.Status != http.StatusNoContent {
		t.Fatalf("seed: %d %s", res.Status, res.Body)
	}
	before := admin.Get("/teams").Text()

	writes := map[string]*getatest.Response{
		"create":                user.Post("/teams", map[string]any{"id": "mine", "name": "Mine"}),
		"create invalid":        user.Post("/teams", map[string]any{"id": "NOT VALID"}),
		"delete":                user.Delete("/teams/core"),
		"delete a missing team": user.Delete("/teams/none"),
		"add a member":          user.Put("/teams/core/members/bo", nil),
		"remove a member":       user.Delete("/teams/core/members/u1"),
		"replace":               user.Put("/teams/core", map[string]any{"name": "Mine"}),
		"create by PUT":         user.Put("/teams/mine", map[string]any{"name": "Mine"}),
		"patch":                 user.Patch("/teams/core", map[string]any{"description": "mine"}),
	}
	for name, res := range writes {
		if res.Status != http.StatusForbidden || res.Problem().Detail != "admin only" {
			t.Errorf("%s as a user: %d %s", name, res.Status, res.Body)
		}
	}
	if after := admin.Get("/teams").Text(); after != before {
		t.Fatalf("a refused write changed the teams:\n%s\n%s", before, after)
	}
	for _, p := range []string{"/teams", "/teams/core"} {
		if res := user.Get(p); res.Status != http.StatusOK {
			t.Errorf("read %s as a user: %d %s", p, res.Status, res.Body)
		}
	}
	if res := anon.Get("/teams"); res.Status != http.StatusUnauthorized {
		t.Errorf("read /teams with no token: %d", res.Status)
	}
}
