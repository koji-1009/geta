package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/model"
	"github.com/koji-1009/geta/examples/register/routes/health"
	"github.com/koji-1009/geta/examples/register/routes/users"
	byrole "github.com/koji-1009/geta/examples/register/routes/users/by-role/role_"
	user "github.com/koji-1009/geta/examples/register/routes/users/id_"
	"github.com/koji-1009/geta/examples/register/routes/users/id_/avatar"
	tag "github.com/koji-1009/geta/examples/register/routes/users/id_/tags/index_"
	"github.com/koji-1009/geta/getaclient"
)

// The round trip of clientcheck/roundtrip.py, from Go with the handlers' own
// types and no generated code. Each call is checked against the app (the
// template names an operation taking and returning these types) and each
// response against the document.
func TestTypedClientRoundTrip(t *testing.T) {
	// One app, three callers: each Bearer adds an Authorization header, so
	// both tokens are set on the anonymous client, not one on top of the other.
	anon := anonymous(t)
	c := anon.Bearer("t-admin").Typed()
	asUser := anon.Bearer("t-user").Typed()
	public := anon.Typed()
	ctx := t.Context()
	failed := func(err error, status int, what string) *getaclient.Error {
		t.Helper()
		e, ok := errors.AsType[*getaclient.Error](err)
		if !ok || e.Status != status || e.Problem == nil {
			t.Fatalf("%s: %v", what, err)
		}
		return e
	}

	h, err := getaclient.Call[struct{}, health.Status](ctx, public, http.MethodGet, "/health", nil)
	if err != nil || h.Status != "ok" {
		t.Fatal("health:", h, err)
	}

	ada := model.User{ID: "rt-1", Name: "Ada", Role: model.RoleMember, Tags: []string{"x", "y"}}
	created, err := getaclient.Call[users.CreateIn, users.Created](ctx, c, http.MethodPost, "/users", &users.CreateIn{Body: ada})
	if err != nil || created.Location != "/users/rt-1" {
		t.Fatal("create answers its Location header:", created, err)
	}
	_, err = getaclient.Call[users.CreateIn, users.Created](ctx, c, http.MethodPost, "/users", &users.CreateIn{Body: ada})
	failed(err, http.StatusConflict, "a duplicate create is a 409 problem")

	other := model.User{ID: "rt-3", Name: "Bo", Role: model.RoleMember, Tags: []string{}}
	_, err = getaclient.Call[users.CreateIn, users.Created](ctx, asUser, http.MethodPost, "/users", &users.CreateIn{Body: other})
	if e := failed(err, http.StatusForbidden, "a create by a caller who is not an administrator is a 403 problem"); e.Problem.Detail != "admin only" {
		t.Fatal(e.Problem)
	}
	err = getaclient.CallNoBody(ctx, asUser, http.MethodDelete, "/users/{id}", &user.DeleteIn{Path: user.Path{ID: "rt-1"}})
	failed(err, http.StatusForbidden, "a delete by a caller who is not an administrator is a 403 problem")

	got, err := getaclient.Call[user.GetIn, user.Tagged](ctx, c, http.MethodGet, "/users/{id}", &user.GetIn{Path: user.Path{ID: "rt-1"}})
	if err != nil || got.ETag != user.ETag(got.User) {
		t.Fatal("fetch answers the user's ETag header:", got, err)
	}
	u := &got.User
	if u.Name != "Ada" || u.Role != model.RoleMember || len(u.Tags) != 2 || u.CreatedAt == nil || u.Active == nil || !*u.Active {
		t.Fatal("fetch decodes into the model, server-set fields included:", u)
	}
	_, err = getaclient.Call[user.GetIn, user.Tagged](ctx, c, http.MethodGet, "/users/{id}",
		&user.GetIn{Conditional: geta.Conditional{IfNoneMatch: new(got.ETag)}, Path: user.Path{ID: "rt-1"}})
	if e, ok := errors.AsType[*getaclient.Error](err); !ok || e.Status != http.StatusNotModified || len(e.Body) != 0 {
		t.Fatal("a fetch of the user the client holds is a 304 with no body:", err)
	}

	limit, role := 10, model.RoleMember
	list, err := getaclient.Call[users.ListIn, model.UserList](ctx, c, http.MethodGet, "/users", &users.ListIn{Limit: &limit, Role: &role})
	if err != nil || list.Total < 1 {
		t.Fatal("list with typed query parameters:", list, err)
	}

	by, err := getaclient.Call[byrole.ListIn, model.UserList](ctx, c, http.MethodGet, "/users/by-role/{role}", &byrole.ListIn{Role: model.RoleMember})
	if err != nil || by == nil {
		t.Fatal("typed path parameter:", err)
	}

	tg, err := getaclient.Call[tag.GetIn, tag.Tag](ctx, c, http.MethodGet, "/users/{id}/tags/{index}", &tag.GetIn{ID: "rt-1", Index: 1})
	if err != nil || tg.Tag != "y" {
		t.Fatal("two path parameters, one an integer:", tg, err)
	}
	_, err = getaclient.Call[tag.GetIn, tag.Tag](ctx, c, http.MethodGet, "/users/{id}/tags/{index}", &tag.GetIn{ID: "rt-1", Index: 9})
	if e := failed(err, http.StatusNotFound, "a failure row reaches the client"); e.Problem.Detail != "tag index out of range" {
		t.Fatal(e.Problem)
	}

	bad := model.User{ID: "rt-2", Name: "Ada", Role: model.RoleMember, Tags: []string{}}
	_, err = getaclient.Call[user.PutIn, user.Tagged](ctx, c, http.MethodPut, "/users/{id}", &user.PutIn{Path: user.Path{ID: "rt-1"}, Body: bad})
	failed(err, http.StatusBadRequest, "a mismatched id is a 400")

	renamed := model.User{ID: "rt-1", Name: "Ada B", Role: model.RoleMember, Tags: []string{}}
	put, err := getaclient.Call[user.PutIn, user.Tagged](ctx, c, http.MethodPut, "/users/{id}",
		&user.PutIn{Conditional: geta.Conditional{IfMatch: new(got.ETag)}, Path: user.Path{ID: "rt-1"}, Body: renamed})
	if err != nil || put.User.Name != "Ada B" || put.ETag == got.ETag || put.ETag != user.ETag(put.User) {
		t.Fatal("a replace of the user fetched round-trips the model and its new tag:", put, err)
	}
	renamed.Name = "Ada C"
	_, err = getaclient.Call[user.PutIn, user.Tagged](ctx, c, http.MethodPut, "/users/{id}",
		&user.PutIn{Conditional: geta.Conditional{IfMatch: new(got.ETag)}, Path: user.Path{ID: "rt-1"}, Body: renamed})
	failed(err, http.StatusPreconditionFailed, "a replace of a user since changed is a 412 problem")

	// A multipart upload: the file from an io.Reader, beside a text field.
	caption := "me"
	av, err := getaclient.Call[avatar.PutIn, avatar.Avatar](ctx, c, http.MethodPut, "/users/{id}/avatar", &avatar.PutIn{ID: "rt-1",
		Body: avatar.Upload{Image: geta.NewFile("me.png", "image/png", strings.NewReader("PNG")), Caption: &caption}})
	sum := sha256.Sum256([]byte("PNG"))
	if err != nil || av.Bytes != 3 || av.Filename != "me.png" || av.ContentType != "image/png" || av.Caption == nil || *av.Caption != "me" ||
		av.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("an avatar upload round-trips its file:", av, err)
	}
	_, err = getaclient.Call[avatar.PutIn, avatar.Avatar](ctx, c, http.MethodPut, "/users/{id}/avatar", &avatar.PutIn{ID: "rt-9",
		Body: avatar.Upload{Image: geta.NewFile("me.png", "image/png", strings.NewReader("PNG"))}})
	failed(err, http.StatusNotFound, "an avatar for no user is a 404")

	tooMany := 1000
	_, err = getaclient.Call[users.ListIn, model.UserList](ctx, c, http.MethodGet, "/users", &users.ListIn{Limit: &tooMany})
	failed(err, http.StatusBadRequest, "a constraint violation is a 400")

	if err := getaclient.CallNoBody(ctx, c, http.MethodDelete, "/users/{id}", &user.DeleteIn{Path: user.Path{ID: "rt-1"}}); err != nil {
		t.Fatal("delete:", err)
	}
	err = getaclient.CallNoBody(ctx, c, http.MethodDelete, "/users/{id}", &user.DeleteIn{Path: user.Path{ID: "rt-1"}})
	failed(err, http.StatusNotFound, "a second delete is a 404")
}
