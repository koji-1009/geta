package geta_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// download sends a stored file as the media type it was stored as, one of
// those its body tag lists.
type download struct {
	ContentType string    `header:"Content-Type"`
	ETag        string    `header:"ETag"`
	Disposition string    `header:"Content-Disposition"`
	Body        io.Reader `body:"image/png|application/pdf"`
}

type storedIn struct {
	ID string `path:"id"`
}

// stored is the file each id names: its media type as uploaded, and its
// bytes.
var storedFiles = map[string][2]string{
	"png":   {"image/png", "\x89PNG"},
	"pdf":   {"application/pdf", "%PDF-1.7"},
	"gif":   {"image/gif", "GIF89a"},  // a type the operation does not list
	"empty": {"", "?"},                // no type chosen
	"case":  {"Image/PNG", "\x89PNG"}, // not as the tag writes it
}

func downloadTable(closed *bool) geta.Table {
	return one("/files/{id}", geta.Route{Get: geta.Op(http.StatusOK, func(_ context.Context, in *storedIn) (*download, error) {
		f := storedFiles[in.ID]
		return &download{ContentType: f[0], ETag: `"` + in.ID + `"`, Disposition: "attachment",
			Body: closer{strings.NewReader(f[1]), closed}}, nil
	}, geta.Doc{})})
}

// An envelope's raw body may list several media types; the handler chooses
// the one each response sends through its Content-Type header field, which
// geta writes as the response's Content-Type, beside nosniff and the
// envelope's other headers. A choice the tag does not list, the empty one or
// one in another case included, is a defect: a 500, with none of the
// envelope's headers, the reader closed, and never a 304 in its place.
// Accept is not read (RFC 9110 §12.4.1): the stored type is sent whatever
// it names, with no 406 and no Vary.
func TestARawBodyChoosesAmongItsListedMediaTypes(t *testing.T) {
	var closed bool
	c := getatest.New(t, downloadTable(&closed))
	for id, want := range map[string]string{"png": "image/png", "pdf": "application/pdf"} {
		closed = false
		res := c.Get("/files/" + id)
		if res.Status != 200 || res.Header.Get("Content-Type") != want || res.Text() != storedFiles[id][1] ||
			res.Header.Get("X-Content-Type-Options") != "nosniff" || res.Header.Get("Content-Disposition") != "attachment" ||
			res.Header.Get("ETag") != `"`+id+`"` || !closed {
			t.Errorf("%s: %d %v %q closed %v", id, res.Status, res.Header, res.Text(), closed)
		}
	}
	for _, id := range []string{"gif", "empty", "case"} {
		closed = false
		res := c.Get("/files/" + id)
		if res.Status != 500 || res.Header.Get("Content-Type") != geta.ProblemContentType ||
			res.Header.Get("Content-Disposition") != "" || res.Header.Get("ETag") != "" || !closed {
			t.Errorf("%s: %d %v closed %v", id, res.Status, res.Header, closed)
		}
		// The defect is not hidden behind a 304 the precondition would answer.
		if res := c.With("If-None-Match", `"`+id+`"`).Get("/files/" + id); res.Status != 500 {
			t.Errorf("%s with a matching If-None-Match: %d", id, res.Status)
		}
	}
	res := c.With("Accept", "application/pdf").Get("/files/png")
	if res.Status != 200 || res.Header.Get("Content-Type") != "image/png" || res.Header.Get("Vary") != "" {
		t.Errorf("Accept: %d %v", res.Status, res.Header)
	}
	if res := c.With("If-None-Match", `"png"`).Get("/files/png"); res.Status != 304 || res.Header.Get("Content-Type") != "" {
		t.Errorf("304: %d %v", res.Status, res.Header)
	}
	if res := c.Do(http.MethodHead, "/files/pdf", nil); res.Status != 200 || res.Header.Get("Content-Type") != "application/pdf" {
		t.Errorf("HEAD: %d %v", res.Status, res.Header)
	}
}

// The document lists every media type the body tag lists as a content key
// of the success status, each with no schema, and states no Content-Type
// header: OpenAPI ignores a response header of that name, and the content
// keys say what it may be.
func TestListedMediaTypesAreDocumented(t *testing.T) {
	m := doc(t, accepts(t, downloadTable(new(bool))))
	if got := compact(t, at(t, m, "paths", "/files/{id}", "get", "responses", "200", "content")); got != `{"application/pdf":{},"image/png":{}}` {
		t.Error(got)
	}
	headers := at(t, m, "paths", "/files/{id}", "get", "responses", "200", "headers").(map[string]any)
	for name := range headers {
		if strings.EqualFold(name, "Content-Type") {
			t.Errorf("Content-Type is documented as a header: %v", headers)
		}
	}
	if _, ok := headers["Content-Disposition"]; !ok {
		t.Errorf("Content-Disposition is not documented: %v", headers)
	}
	// With several success statuses, each lists them.
	type statused struct {
		Status int    `status:"200|201"`
		Type   string `header:"Content-Type"`
		Body   []byte `body:"text/csv|text/plain; charset=utf-8"`
	}
	m = doc(t, accepts(t, one("/x", get(func(context.Context, *empty) (*statused, error) { return nil, nil }))))
	for _, s := range []string{"200", "201"} {
		if got := compact(t, at(t, m, "paths", "/x", "get", "responses", s, "content")); got != `{"text/csv":{},"text/plain; charset=utf-8":{}}` {
			t.Errorf("%s: %s", s, got)
		}
	}
}

// What the document could not state of a choice of media types is refused,
// by geta.New and getavet alike: a body listing several without a
// Content-Type field to choose, a Content-Type field beside a body that
// lists one or none (geta writes that Content-Type itself), a Content-Type
// field that is not a string or takes a schema or doc tag, a listed type
// twice, and a listed type a raw body cannot take.
func TestMediaTypeChoiceMistakesAreRefused(t *testing.T) {
	type noChoice struct {
		Body []byte `body:"image/png|application/pdf"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*noChoice, error) { return nil, nil })),
		`noChoice.Body: the body lists several media types, but the envelope has no string field tagged header:"Content-Type"`)
	type oneType struct {
		Type string `header:"Content-Type"`
		Body []byte `body:"image/png"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*oneType, error) { return nil, nil })),
		`oneType.Type: header "Content-Type" is reserved for the body geta writes; it chooses among the media types a raw body field lists`)
	type jsonBody struct {
		Type string `header:"Content-Type"`
		Body ok     `body:"json"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*jsonBody, error) { return nil, nil })),
		`jsonBody.Type: header "Content-Type" is reserved`)
	type pointer struct {
		Type *string `header:"Content-Type"`
		Body []byte  `body:"image/png|application/pdf"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*pointer, error) { return nil, nil })),
		"pointer.Type: a Content-Type header field is a string, not *string")
	type withDoc struct {
		Type string `header:"Content-Type" doc:"the type"`
		Body []byte `body:"image/png|application/pdf"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*withDoc, error) { return nil, nil })),
		"withDoc.Type: a Content-Type header field takes no schema or doc tag")
	type twice struct {
		Type string `header:"Content-Type"`
		Body []byte `body:"image/png|image/png"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*twice, error) { return nil, nil })),
		`twice.Body: body tag "image/png|image/png" names "image/png" twice`)
	type listedJSON struct {
		Type string `header:"Content-Type"`
		Body []byte `body:"image/png|application/problem+json"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*listedJSON, error) { return nil, nil })),
		`listedJSON.Body: body tag "application/problem+json" is JSON`)
	type emptyMember struct {
		Type string `header:"Content-Type"`
		Body []byte `body:"image/png|"`
	}
	rejects(t, one("/x", get(func(context.Context, *empty) (*emptyMember, error) { return nil, nil })),
		`emptyMember.Body: body tag "" is not a media type`)
	// A list is no input's: a request body takes one media type.
	type listIn struct {
		Body []byte `body:"image/png|application/pdf"`
	}
	rejects(t, posting[listIn](), "listIn.Body")
	// A problem's envelope writes JSON, and takes no Content-Type field.
	type conflictBody struct {
		Code string `json:"code"`
	}
	type problemEnv struct {
		Type string       `header:"Content-Type"`
		Body conflictBody `body:"json"`
	}
	rejects(t, one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Failures: []geta.Failure{
		geta.OnAsProblem(http.StatusConflict, "conflict", func(err error) problemEnv { return problemEnv{} }),
	}})}), `header "Content-Type" is reserved`)
}
