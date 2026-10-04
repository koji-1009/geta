package getaclient_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaclient"
)

// The handler's own input and output types; a client imports them from the
// application's route package.
type NoteIn struct {
	ID   int    `path:"id" schema:"minimum=1"`
	Lang string `query:"lang" schema:"enum=en|ja,default=en"`
}

type Note struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
}

var errNoNote = errors.New("no such note")

func getNote(ctx context.Context, in *NoteIn) (*Note, error) {
	if in.ID != 1 {
		return nil, errNoNote
	}
	text := map[string]string{"en": "hello", "ja": "こんにちは"}[in.Lang]
	return &Note{ID: in.ID, Text: text}, nil
}

// Call sends the input by its tags (path, query, header, cookie, body) to
// the operation on the template, and reads the answer back into the output
// type. A failure is an *Error carrying the problem the server sent.
func ExampleCall() {
	app, err := geta.New(geta.Table{Routes: []geta.Entry{{
		Path: "/notes/{id}",
		Route: geta.Route{Get: geta.Op(http.StatusOK, getNote, geta.Doc{
			Failures: []geta.Failure{geta.On(errNoNote, http.StatusNotFound, "no such note")},
		})},
	}}})
	if err != nil {
		panic(err)
	}
	srv := httptest.NewServer(app)
	defer srv.Close()
	c := &getaclient.Client{Base: srv.URL}
	ctx := context.Background()

	n, err := getaclient.Call[NoteIn, Note](ctx, c, http.MethodGet, "/notes/{id}", &NoteIn{ID: 1, Lang: "ja"})
	fmt.Println(n, err)

	_, err = getaclient.Call[NoteIn, Note](ctx, c, http.MethodGet, "/notes/{id}", &NoteIn{ID: 2, Lang: "en"})
	if e, ok := errors.AsType[*getaclient.Error](err); ok {
		fmt.Println(e.Status, e.Problem.Detail)
	}
	// Output:
	// &{1 こんにちは} <nil>
	// 404 no such note
}
