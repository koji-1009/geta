package geta_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

type csvIn struct {
	Body []byte `body:"text/csv" doc:"rows to import"`
}

type maybeCSVIn struct {
	Body *[]byte `body:"text/csv"`
}

type blobIn struct {
	ID   string    `path:"id"`
	Body io.Reader `body:"application/octet-stream"`
}

type counted struct {
	Bytes int `json:"bytes"`
}

type csvExport struct {
	Disposition string `header:"Content-Disposition"`
	Body        []byte `body:"text/csv; charset=utf-8"`
}

type pdfOut struct {
	Body io.Reader `body:"application/pdf"`
}

// closer is a reader that records being closed.
type closer struct {
	io.Reader
	closed *bool
}

func (c closer) Close() error {
	*c.closed = true
	return nil
}

// brokenReader reads n bytes, then fails.
type brokenReader struct{ n int }

func (f *brokenReader) Read(b []byte) (int, error) {
	if f.n == 0 {
		return 0, errors.New("the disk went away")
	}
	k := min(len(b), f.n)
	for i := range k {
		b[i] = 'x'
	}
	f.n -= k
	return k, nil
}

func rawTable(closed *bool, pdf func() io.Reader) geta.Table {
	small := func(l geta.Limits) geta.Limits { l.MaxBodyBytes = 16; return l }
	return geta.Table{Routes: []geta.Entry{
		{Path: "/import", Route: geta.Route{
			Post: geta.Op(http.StatusOK, func(_ context.Context, in *csvIn) (*counted, error) {
				return &counted{len(in.Body)}, nil
			}, geta.Doc{}),
			Put: geta.Op(http.StatusOK, func(_ context.Context, in *maybeCSVIn) (*counted, error) {
				if in.Body == nil {
					return &counted{-1}, nil
				}
				return &counted{len(*in.Body)}, nil
			}, geta.Doc{}),
		}},
		{Path: "/blobs/{id}", Route: geta.Route{Put: geta.Op(http.StatusOK, func(_ context.Context, in *blobIn) (*counted, error) {
			b, err := io.ReadAll(in.Body)
			if err != nil {
				return nil, err
			}
			return &counted{len(b)}, nil
		}, geta.Doc{Limits: small})}},
		{Path: "/export", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *empty) (*csvExport, error) {
			return &csvExport{Disposition: `attachment; filename="users.csv"`, Body: []byte("id,name\n1,ann\n")}, nil
		}, geta.Doc{})}},
		{Path: "/report", Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *empty) (*pdfOut, error) {
			return &pdfOut{Body: closer{pdf(), closed}}, nil
		}, geta.Doc{})}},
	}}
}

// An input's raw body is the bytes of the media type its tag names: read
// whole into a []byte (nil *[]byte when absent and optional) or handed over
// as an io.Reader, bounded by the body limit (413, for a reader when the
// handler reads past it), and a 415 naming the media type for another
// Content-Type or a content coding, as any body is.
func TestRawRequestBodies(t *testing.T) {
	var closed bool
	c := getatest.New(t, rawTable(&closed, func() io.Reader { return strings.NewReader("%PDF") }))
	if res := c.Content(http.MethodPost, "/import", "text/csv", []byte("a,b\n1,2\n")); res.Status != 200 || res.JSON[counted]().Bytes != 8 {
		t.Fatal(res.Status, res.Text())
	}
	// Parameters of the media type are the request's own.
	if res := c.Content(http.MethodPost, "/import", "text/csv; charset=utf-8", []byte("a")); res.Status != 200 {
		t.Fatal(res.Status, res.Text())
	}
	res := c.Content(http.MethodPost, "/import", "application/json", []byte(`{"a":1}`))
	if res.Status != http.StatusUnsupportedMediaType || res.Header.Get("Accept") != "text/csv" {
		t.Fatal(res.Status, res.Header, res.Text())
	}
	req, _ := http.NewRequest(http.MethodPost, c.URL()+"/import", strings.NewReader("a"))
	req.Header.Set("Content-Type", "text/csv")
	req.Header.Set("Content-Encoding", "gzip")
	if res := c.Send(req); res.Status != http.StatusUnsupportedMediaType || res.Header.Get("Accept-Encoding") != "identity" {
		t.Fatal(res.Status, res.Header)
	}
	if res := c.Content(http.MethodPost, "/import", "text/csv", nil); res.Status != 400 {
		t.Fatal(res.Status, res.Text())
	}
	if res := c.Content(http.MethodPost, "/import", "text/csv", bytes.Repeat([]byte("a"), 1<<20+1)); res.Status != http.StatusRequestEntityTooLarge {
		t.Fatal(res.Status, res.Text())
	}
	if res := c.Content(http.MethodPut, "/import", "text/csv", nil); res.Status != 200 || res.JSON[counted]().Bytes != -1 {
		t.Fatal(res.Status, res.Text())
	}
	if res := c.Content(http.MethodPut, "/blobs/a", "application/octet-stream", make([]byte, 16)); res.Status != 200 || res.JSON[counted]().Bytes != 16 {
		t.Fatal(res.Status, res.Text())
	}
	if res := c.Content(http.MethodPut, "/blobs/a", "application/octet-stream", make([]byte, 17)); res.Status != http.StatusRequestEntityTooLarge {
		t.Fatal(res.Status, res.Text())
	}
}

// An envelope's raw body goes out as the media type its tag names, with
// nosniff and the envelope's headers: a []byte with its Content-Length, an
// io.Reader that ends within MaxResponseBuffer too, a longer one streamed
// without, and the reader closed. A reader that fails before anything is
// sent is a clean 500.
func TestRawResponseBodies(t *testing.T) {
	var closed bool
	pdf := func() io.Reader { return strings.NewReader("%PDF-1.7") }
	c := getatest.New(t, rawTable(&closed, func() io.Reader { return pdf() }))
	res := c.Get("/export")
	if res.Status != 200 || res.Header.Get("Content-Type") != "text/csv; charset=utf-8" || res.Text() != "id,name\n1,ann\n" ||
		res.Header.Get("Content-Disposition") != `attachment; filename="users.csv"` || res.Header.Get("X-Content-Type-Options") != "nosniff" ||
		res.Header.Get("Content-Length") != "14" {
		t.Fatal(res.Status, res.Header, res.Text())
	}
	res = c.Get("/report")
	if res.Status != 200 || res.Header.Get("Content-Type") != "application/pdf" || res.Text() != "%PDF-1.7" || res.Header.Get("Content-Length") != "8" || !closed {
		t.Fatal(res.Status, res.Header, res.Text(), closed)
	}
	closed = false
	pdf = func() io.Reader { return &brokenReader{n: 200 << 10} }
	req, _ := http.NewRequest(http.MethodGet, c.URL()+"/report", nil)
	r, err := c.HTTP().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || r.Header.Get("Content-Length") != "" || err == nil || len(got) < 64<<10 || !closed {
		t.Fatal(r.StatusCode, r.Header, len(got), err, closed)
	}
	closed = false
	pdf = func() io.Reader { return &brokenReader{n: 10} }
	if res := c.Get("/report"); res.Status != 500 || !closed {
		t.Fatal(res.Status, res.Text(), closed)
	}
	pdf = func() io.Reader { return strings.NewReader(strings.Repeat("y", 100<<10)) }
	if res := c.Get("/report"); res.Status != 200 || len(res.Body) != 100<<10 || res.Header.Get("Content-Length") != "" {
		t.Fatal(res.Status, res.Header, len(res.Body))
	}
}

type statusedPDF struct {
	Status int       `status:"200|201"`
	Note   string    `header:"X-Note"`
	Body   io.Reader `body:"application/pdf"`
}

// A raw body's reader that is an io.Closer is closed on every path, also
// when the response is refused before a byte of it is read: a status the
// output does not declare, a header no header carries.
func TestARawReaderIsClosedOnEveryPath(t *testing.T) {
	var closed bool
	var status int
	var note string
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{{Path: "/report", Route: geta.Route{
		Get: geta.Op(http.StatusOK, func(context.Context, *empty) (*statusedPDF, error) {
			return &statusedPDF{Status: status, Note: note, Body: closer{strings.NewReader("%PDF"), &closed}}, nil
		}, geta.Doc{}),
	}}}})
	for _, tc := range []struct {
		status int
		note   string
		want   int
	}{{201, "ok", 201}, {299, "ok", 500}, {200, "a\nb", 500}} {
		closed, status, note = false, tc.status, tc.note
		if res := c.Get("/report"); res.Status != tc.want || !closed {
			t.Errorf("status %d, note %q: %d, closed %v", tc.status, tc.note, res.Status, closed)
		}
	}
}

// The document lists a raw body as its media type with no schema, as
// OpenAPI 3.1 describes raw binary content, and the 415 names it in Accept.
func TestRawBodiesAreDocumented(t *testing.T) {
	var closed bool
	m := doc(t, accepts(t, rawTable(&closed, nil)))
	if got := compact(t, at(t, m, "paths", "/import", "post", "requestBody")); got != `{"content":{"text/csv":{}},"description":"rows to import","required":true}` {
		t.Error(got)
	}
	if got := at(t, m, "paths", "/import", "put", "requestBody", "required"); got != false {
		t.Error(got)
	}
	if got := compact(t, at(t, m, "paths", "/import", "post", "responses", "415", "headers", "Accept", "schema")); got != `{"enum":["text/csv"],"type":"string"}` {
		t.Error(got)
	}
	if got := at(t, m, "paths", "/blobs/{id}", "put", "responses", "413", "description").(string); !strings.Contains(got, "16 bytes") {
		t.Error(got)
	}
	if got := compact(t, at(t, m, "paths", "/export", "get", "responses", "200", "content")); got != `{"text/csv; charset=utf-8":{}}` {
		t.Error(got)
	}
	if got := at(t, m, "paths", "/export", "get", "responses", "200", "headers", "Content-Disposition", "required"); got != true {
		t.Error(got)
	}
	if got := compact(t, at(t, m, "paths", "/report", "get", "responses", "200", "content")); got != `{"application/pdf":{}}` {
		t.Error(got)
	}
}

// What the document could not state of a raw body is refused, by geta.New
// and getavet alike: a media range, JSON, a form, an event stream as an
// output, a type that cannot hold the bytes, a schema tag, a media type not
// written as FormatMediaType writes it.
func TestRawBodyMistakesAreRefused(t *testing.T) {
	type jsonIn struct {
		Body []byte `body:"application/json"`
	}
	type rangeIn struct {
		Body []byte `body:"image/*"`
	}
	type formIn struct {
		Body []byte `body:"application/x-www-form-urlencoded"`
	}
	type stringIn struct {
		Body string `body:"text/plain"`
	}
	type schemaIn struct {
		Body []byte `body:"text/plain" schema:"maxLength=3"`
	}
	type caseIn struct {
		Body []byte `body:"Text/Plain"`
	}
	type streamOut struct {
		Body []byte `body:"text/event-stream"`
	}
	type pointerOut struct {
		Body *[]byte `body:"text/plain"`
	}
	type stringOut struct {
		Body string `body:"text/plain"`
	}
	rejects(t, posting[jsonIn](), `body tag "application/json" is JSON; use body:"json"`)
	rejects(t, posting[rangeIn](), `body tag "image/*" is a media range`)
	rejects(t, posting[formIn](), `is a form; use body:"form"`)
	rejects(t, posting[stringIn](), `a body:"text/plain" field has type string, not []byte, *[]byte, or io.Reader`)
	rejects(t, posting[schemaIn](), `a body:"text/plain" field takes no schema tag`)
	rejects(t, posting[caseIn](), `is not canonical; write "text/plain"`)
	rejects(t, one("/x", get(func(context.Context, *empty) (*streamOut, error) { return nil, nil })), `is an event stream; return a *geta.Stream`)
	rejects(t, one("/x", get(func(context.Context, *empty) (*pointerOut, error) { return nil, nil })), `the body field is a pointer`)
	rejects(t, one("/x", get(func(context.Context, *empty) (*stringOut, error) { return nil, nil })), `a body:"text/plain" field has type string, not []byte or io.Reader`)
	rejects(t, one("/x", geta.Route{Delete: geta.Op(http.StatusNoContent, func(context.Context, *empty) (*pdfOut, error) { return nil, nil }, geta.Doc{})}),
		"success status 204 takes no body")
}

// posting is /x answering POST with an input of type In.
func posting[In any]() geta.Table {
	return one("/x", geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *In) (*ok, error) { return &ok{true}, nil }, geta.Doc{})})
}
