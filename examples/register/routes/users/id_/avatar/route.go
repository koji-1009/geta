package avatar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"slices"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/auth"
	"github.com/koji-1009/geta/examples/register/failures"
	"github.com/koji-1009/geta/examples/register/model"
)

// Route is /users/{id}/avatar: a multipart upload, as a browser's form or
// `curl -F avatar=@me.png` sends one.
func Route(env *app.Env) geta.Route {
	h := Handler{Users: env.Users}
	return geta.Route{
		Put: geta.Op(http.StatusOK, h.Put, geta.Doc{
			Summary:  "Upload a user's avatar",
			Failures: slices.Concat([]geta.Failure{failures.UserNotFound}, failures.Store),
			Scope:    geta.Scope{auth.RequireAdmin()},
		}),
	}
}

// Store is what this route needs from the store.
type Store interface {
	Find(ctx context.Context, id string) (*model.User, error)
}

type Handler struct{ Users Store }

// Upload is the multipart body: the image, and an optional caption.
type Upload struct {
	Image   geta.File `form:"image"`
	Caption *string   `form:"caption" schema:"maxLength=140"`
}

type PutIn struct {
	ID   string `path:"id" schema:"maxLength=64"`
	Body Upload `body:"multipart"`
}

// Avatar is what the server took in.
type Avatar struct {
	User        string  `json:"user"`
	Filename    string  `json:"filename"`
	ContentType string  `json:"contentType"`
	Bytes       int64   `json:"bytes"`
	SHA256      string  `json:"sha256" schema:"pattern=^[0-9a-f]{64}$"`
	Caption     *string `json:"caption,omitzero" schema:"maxLength=140"`
}

func (h Handler) Put(ctx context.Context, in *PutIn) (*Avatar, error) {
	if _, err := h.Users.Find(ctx, in.ID); err != nil {
		return nil, err
	}
	f, err := in.Body.Image.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sum := sha256.New()
	n, err := io.Copy(sum, f)
	if err != nil {
		return nil, err
	}
	return &Avatar{User: in.ID, Filename: in.Body.Image.Filename(), ContentType: in.Body.Image.ContentType(),
		Bytes: n, SHA256: hex.EncodeToString(sum.Sum(nil)), Caption: in.Body.Caption}, nil
}
