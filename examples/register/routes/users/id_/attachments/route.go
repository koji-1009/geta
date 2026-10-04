package attachments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"slices"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/app"
	"github.com/koji-1009/geta/examples/register/auth"
	"github.com/koji-1009/geta/examples/register/failures"
	"github.com/koji-1009/geta/examples/register/model"
)

// Route is /users/{id}/attachments: several files in one multipart upload,
// as `curl -F file=@a.pdf -F file=@b.png -F note=signed` sends them.
func Route(env *app.Env) geta.Route {
	h := Handler{Users: env.Users}
	return geta.Route{
		Post: geta.Op(http.StatusOK, h.Post, geta.Doc{
			Summary: "Upload a user's attachments",
			Failures: slices.Concat([]geta.Failure{
				failures.UserNotFound,
				geta.On(ErrEmptyFile, http.StatusUnprocessableEntity, "an attachment is empty"),
			}, failures.Store),
			Scope: geta.Scope{auth.RequireAdmin()},
		}),
	}
}

// ErrEmptyFile is an attachment with no content.
var ErrEmptyFile = errors.New("an attachment is empty")

// Store is what this route needs from the store.
type Store interface {
	Find(ctx context.Context, id string) (*model.User, error)
}

type Handler struct{ Users Store }

// Upload is the multipart body. Files is every part named "file": a
// []geta.File takes at least one, and maxItems bounds how many. Note is an
// ordinary field beside them. The files are held in memory up to
// Limits.MaxMultipartMemory (1 MiB) and in temporary files past it, which
// geta removes once the handler has returned: read them here, never keep a
// geta.File.
type Upload struct {
	Files []geta.File `form:"file" doc:"The files, one part each" schema:"maxItems=5"`
	Note  *string     `form:"note" doc:"A note on the whole upload" schema:"maxLength=200"`
}

type PostIn struct {
	ID   string `path:"id" schema:"maxLength=64"`
	Body Upload `body:"multipart"`
}

// Attachment is what the server took in of one file.
type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Bytes       int64  `json:"bytes"`
	SHA256      string `json:"sha256" schema:"pattern=^[0-9a-f]{64}$"`
}

// Attachments is every file of an upload, in the order they were sent.
type Attachments struct {
	User  string       `json:"user"`
	Items []Attachment `json:"items"`
	Note  *string      `json:"note,omitzero"`
}

func (h Handler) Post(ctx context.Context, in *PostIn) (*Attachments, error) {
	if _, err := h.Users.Find(ctx, in.ID); err != nil {
		return nil, err
	}
	out := &Attachments{User: in.ID, Note: in.Body.Note}
	for _, f := range in.Body.Files {
		a, err := digest(f)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, a)
	}
	return out, nil
}

func digest(f geta.File) (Attachment, error) {
	if f.Size() == 0 {
		return Attachment{}, ErrEmptyFile
	}
	r, err := f.Open()
	if err != nil {
		return Attachment{}, err
	}
	defer r.Close()
	sum := sha256.New()
	n, err := io.Copy(sum, r)
	if err != nil {
		return Attachment{}, err
	}
	return Attachment{Filename: f.Filename(), ContentType: f.ContentType(), Bytes: n, SHA256: hex.EncodeToString(sum.Sum(nil))}, nil
}
