package user

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/auth"
	"github.com/koji-1009/geta/examples/register/events"
	"github.com/koji-1009/geta/examples/register/failures"
	"github.com/koji-1009/geta/examples/register/model"
)

// Route is /users/{id}.
func Route(env *app.Env) geta.Route {
	h := Handler{Users: env.Users, Events: env.Events}
	common := slices.Concat([]geta.Failure{failures.UserNotFound}, failures.Store)
	// Anyone may fetch; only an administrator writes.
	admin := geta.Scope{auth.RequireAdmin()}
	return geta.Route{
		Get: geta.Op(http.StatusOK, h.Get, geta.Doc{
			Summary:  "Fetch a user",
			Failures: common,
		}),
		Put: geta.Op(http.StatusOK, h.Put, geta.Doc{
			Summary: "Replace a user",
			Failures: slices.Concat(common, []geta.Failure{
				geta.On(ErrIDMismatch, http.StatusBadRequest, "body id does not match path id"),
				failures.RoleChange,
			}),
			Scope: admin,
		}),
		Delete: geta.OpNoBody(http.StatusNoContent, h.Delete, geta.Doc{
			Summary:  "Delete a user",
			Failures: slices.Concat(common, []geta.Failure{failures.LastAdmin}),
			Scope:    admin,
		}),
	}
}

// ErrIDMismatch: a PUT whose body names another user than its path.
var ErrIDMismatch = errors.New("body id does not match path id")

// Store is what this route needs from the store. Replace calls check with
// the stored user before it writes, in the same transaction, and writes
// nothing when check fails.
type Store interface {
	Find(ctx context.Context, id string) (*model.User, error)
	Replace(ctx context.Context, u model.User, check func(current model.User) error) (*model.User, error)
	Delete(ctx context.Context, id string) error
}

// Publisher announces a change on the live feed.
type Publisher interface{ Publish(events.UserEvent) }

type Handler struct {
	Users  Store
	Events Publisher
}

// Path is shared by every operation on this URL.
type Path struct {
	ID string `path:"id" schema:"maxLength=64"`
}

// A fetch may be conditional, answering 304 to a client that holds the
// current user; a replace may be too, so that a client replaces only the
// user it fetched: If-Match with its ETag answers 412 once another write
// has changed it.
type GetIn struct {
	geta.Conditional
	Path
}

type PutIn struct {
	geta.Conditional
	Path
	Body model.User `body:"json"`
}

type DeleteIn struct{ Path }

// Tagged is a user and its entity tag, which a conditional request names.
type Tagged struct {
	ETag string     `header:"ETag"`
	User model.User `body:"json"`
}

// ETag is the user's entity tag: a digest of its wire form, so any change
// to the record changes it.
func ETag(u model.User) string {
	b, err := json.Marshal(u)
	if err != nil {
		panic(err) // a model.User always has a wire form
	}
	sum := sha256.Sum256(b)
	return `"` + base64.RawURLEncoding.EncodeToString(sum[:18]) + `"`
}

func (h Handler) Get(ctx context.Context, in *GetIn) (*Tagged, error) {
	u, err := h.Users.Find(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	tag := ETag(*u)
	if err := in.Check(tag, time.Time{}); err != nil {
		return nil, err
	}
	return &Tagged{ETag: tag, User: *u}, nil
}

func (h Handler) Put(ctx context.Context, in *PutIn) (*Tagged, error) {
	if in.Body.ID != in.ID {
		return nil, ErrIDMismatch
	}
	// The precondition is checked against the stored user inside the
	// store's write, so no other write lands between the check and this one.
	u, err := h.Users.Replace(ctx, in.Body, func(current model.User) error {
		return in.Check(ETag(current), time.Time{})
	})
	if err != nil {
		return nil, err
	}
	h.Events.Publish(events.UserEvent{Kind: "updated", ID: in.ID})
	return &Tagged{ETag: ETag(*u), User: *u}, nil
}

func (h Handler) Delete(ctx context.Context, in *DeleteIn) error {
	if err := h.Users.Delete(ctx, in.ID); err != nil {
		return err
	}
	h.Events.Publish(events.UserEvent{Kind: "deleted", ID: in.ID})
	return nil
}
