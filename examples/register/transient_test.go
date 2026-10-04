package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/model"
	"github.com/koji-1009/geta/examples/register/routes"
	"github.com/koji-1009/geta/examples/register/store"
	"github.com/koji-1009/geta/getatest"
)

// A transaction the store asks to run again (store.ErrTransient) meets the
// register's own row, 503, as an unavailable store does; geta maps neither by
// itself, and a database error no row maps is a 500.
func TestTransientStoreIs503(t *testing.T) {
	env := app.Open()
	env.Log = slog.New(slog.DiscardHandler)
	env.Users = failingStore{env.Users, fmt.Errorf("%w: %w", store.ErrTransient, errors.New("serialization failure"))}
	c := getatest.New(t, routes.Table(env), routes.Options(env)...).Bearer("t-admin")
	if res := c.Get("/users"); res.Status != http.StatusServiceUnavailable || res.Problem().Detail != "the store asks to retry" {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
	env = app.Open()
	env.Log = slog.New(slog.DiscardHandler)
	env.Users = failingStore{env.Users, fmt.Errorf("%w: %w", store.ErrConflict, errors.New("unique violation"))}
	c = getatest.New(t, routes.Table(env), routes.Options(env)...).Bearer("t-admin")
	if res := c.Get("/users"); res.Status != http.StatusInternalServerError {
		t.Fatalf("a conflict on a list, which no row maps: %d %s", res.Status, res.Body)
	}
}

type failingStore struct {
	store.Users
	err error
}

func (s failingStore) List(context.Context, store.Filter) ([]model.User, int, error) {
	return nil, 0, s.err
}
