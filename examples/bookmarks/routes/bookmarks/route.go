package bookmarks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/bookmarks/app"
	"github.com/koji-1009/geta/examples/bookmarks/auth"
	"github.com/koji-1009/geta/examples/bookmarks/idempotency"
	"github.com/koji-1009/geta/examples/bookmarks/store"
)

// Route is /bookmarks, the caller's own.
//
//   - GET lists them, newest first.
//   - POST saves one. The server gives it its id, so a client that retries
//     a POST whose answer it never got would save it twice; with an
//     Idempotency-Key header, the retry is answered with the first
//     request's result instead.
//   - QUERY searches them. A search is safe, like GET, but its terms are a
//     JSON body rather than a query string: QUERY (geta.MethodQuery) is
//     the method for that, documented as the path's query operation in
//     OpenAPI 3.2.
func Route(env *app.Env) geta.Route {
	h := Handler{Bookmarks: env.Bookmarks, Created: env.Created}
	return geta.Route{
		Get: geta.Op(http.StatusOK, h.List, geta.Doc{Summary: "List bookmarks, newest first"}),
		Post: geta.Op(http.StatusCreated, h.Create, geta.Doc{
			Summary: "Save a bookmark",
			Failures: []geta.Failure{
				geta.On(idempotency.ErrKeyReused, http.StatusUnprocessableEntity, "the Idempotency-Key was used with another request").
					Type("/problems/idempotency-key-reused"),
				geta.On(idempotency.ErrInFlight, http.StatusConflict, "a request with this Idempotency-Key is still being served; retry").
					Type("/problems/idempotency-key-in-flight"),
			},
		}),
		Query: geta.Op(http.StatusOK, h.Search, geta.Doc{Summary: "Search bookmarks"}),
	}
}

type Store interface {
	Add(ctx context.Context, owner string, b store.Bookmark) (store.Bookmark, error)
	List(ctx context.Context, owner string, s store.Search) ([]store.Bookmark, error)
}

// Keys remembers the answers to keyed requests.
type Keys interface {
	Do(key, fingerprint string, f func() (store.Bookmark, error)) (store.Bookmark, bool, error)
}

type Handler struct {
	Bookmarks Store
	Created   Keys
}

// BookmarkList is bookmarks, newest first.
type BookmarkList struct {
	Items []store.Bookmark `json:"items"`
}

func (h Handler) List(ctx context.Context, _ *struct{}) (*BookmarkList, error) {
	bs, err := h.Bookmarks.List(ctx, auth.Caller.Must(ctx).User, store.Search{})
	if err != nil {
		return nil, err
	}
	return &BookmarkList{Items: bs}, nil
}

// NewBookmark is a bookmark as a client saves it.
type NewBookmark struct {
	URL   string    `json:"url" schema:"minLength=1,maxLength=2000,pattern=^https?://"`
	Title string    `json:"title" schema:"minLength=1,maxLength=200"`
	Tags  *[]string `json:"tags,omitzero" schema:"maxItems=10,uniqueItems=true"`
}

// CreateIn reads a header parameter beside the body. The key is optional (a
// pointer); when sent, it is a Structured Field string, quoted, as
// draft-ietf-httpapi-idempotency-key-header writes it:
//
//	Idempotency-Key: "8e03978e-40d5-43e8-bc93-6894a57f9324"
//
// Its pattern is enforced and documented, and CORS lets the page send it,
// since the operation declares it.
type CreateIn struct {
	IdempotencyKey *string     `header:"Idempotency-Key" doc:"A value unique to this save, such as a UUID, quoted; a retry with the same key and body is answered as the first request was" schema:"pattern=^\"[A-Za-z0-9._:-]{8,128}\"$"`
	Body           NewBookmark `body:"json"`
}

// Created is the saved bookmark and where it lives.
type Created struct {
	Location string         `header:"Location"`
	Bookmark store.Bookmark `body:"json"`
}

func (h Handler) Create(ctx context.Context, in *CreateIn) (*Created, error) {
	owner := auth.Caller.Must(ctx).User
	save := func() (store.Bookmark, error) {
		b := store.Bookmark{URL: in.Body.URL, Title: in.Body.Title, Tags: []string{}}
		if in.Body.Tags != nil {
			b.Tags = *in.Body.Tags
		}
		return h.Bookmarks.Add(ctx, owner, b)
	}
	var b store.Bookmark
	var err error
	if in.IdempotencyKey == nil {
		b, err = save()
	} else {
		// A key is the caller's: two users may pick the same one. The
		// fingerprint is the body as bound, so a retry must send the same
		// bookmark; another one under the key is a 422.
		b, _, err = h.Created.Do(owner+"\x00"+*in.IdempotencyKey, fingerprint(in.Body), save)
	}
	if err != nil {
		return nil, err
	}
	return &Created{Location: "/bookmarks/" + b.ID.String(), Bookmark: b}, nil
}

func fingerprint(b NewBookmark) string {
	j, err := json.Marshal(b, json.Deterministic(true))
	if err != nil {
		panic(err) // a NewBookmark always has a JSON form
	}
	sum := sha256.Sum256(j)
	return hex.EncodeToString(sum[:])
}

// Terms are a search: every word of text in the title or the URL, and every
// tag. Limit has a default, so a search may leave it out.
type Terms struct {
	Text  *string   `json:"text,omitzero" doc:"Words each bookmark's title or URL holds, in any case" schema:"maxLength=200,examples=go generics"`
	Tags  *[]string `json:"tags,omitzero" doc:"Tags each bookmark has" schema:"maxItems=10"`
	Limit int       `json:"limit" doc:"At most this many bookmarks" schema:"minimum=1,maximum=100,default=20"`
}

type SearchIn struct {
	Body Terms `body:"json"`
}

func (h Handler) Search(ctx context.Context, in *SearchIn) (*BookmarkList, error) {
	s := store.Search{Limit: in.Body.Limit}
	if in.Body.Text != nil {
		s.Text = *in.Body.Text
	}
	if in.Body.Tags != nil {
		s.Tags = *in.Body.Tags
	}
	bs, err := h.Bookmarks.List(ctx, auth.Caller.Must(ctx).User, s)
	if err != nil {
		return nil, err
	}
	return &BookmarkList{Items: bs}, nil
}
