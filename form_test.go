package geta_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaclient"
	"github.com/koji-1009/geta/getatest"
)

// Content of a media type the operation does not take is a 415 (RFC 9110
// section 15.5.16) naming the one it does in Accept, and on a PATCH in
// Accept-Patch (RFC 5789 section 2.2); content with no Content-Type is
// application/octet-stream (RFC 9110 section 8.3), which no operation takes.
func TestUnsupportedMediaTypeIs415(t *testing.T) {
	h := func(ctx context.Context, in *bodyIn) (*echoed, error) { return &echoed{Got: in.Body.Name}, nil }
	c := getatest.New(t, one("/x", geta.Route{
		Post:  geta.Op(http.StatusOK, h, geta.Doc{}),
		Patch: geta.Op(http.StatusOK, h, geta.Doc{}),
	}))
	unsupported := func(res *getatest.Response, detail, accept, acceptPatch string) {
		t.Helper()
		// A 415 for a media type carries no Accept-Encoding (RFC 9110 section
		// 12.5.3).
		if res.Status != 415 || res.Problem().Detail != detail || res.Header.Get("Accept") != accept ||
			res.Header.Get("Accept-Patch") != acceptPatch || res.Header.Values("Accept-Encoding") != nil {
			t.Fatalf("%d %v %s", res.Status, res.Header, res.Body)
		}
	}
	body := `{"name":"a","price":1}`
	unsupported(c.With("Content-Type", "text/plain").Post("/x", body),
		`Content-Type "text/plain" is not application/json`, "application/json", "")
	unsupported(c.With("Content-Type", "text/plain").Patch("/x", body),
		`Content-Type "text/plain" is not application/json`, "application/json", "application/json")
	unsupported(c.With("Content-Type", "application/x-www-form-urlencoded").Post("/x", "name=a&price=1"),
		`Content-Type "application/x-www-form-urlencoded" is not application/json`, "application/json", "")
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, c.URL()+"/x", strings.NewReader(body))
	unsupported(c.Send(req), "missing Content-Type", "application/json", "")
	// No content is no body, whatever the Content-Type says.
	same(t, violations(t, c.With("Content-Type", "text/plain").Post("/x", "")), "body $: missing required request body")

	// A form takes its own media type and no other.
	f := getatest.New(t, one("/f", geta.Route{Post: geta.Op(http.StatusOK, echoForm, geta.Doc{})}))
	unsupported(f.Post("/f", body), `Content-Type "application/json" is not application/x-www-form-urlencoded`,
		"application/x-www-form-urlencoded", "")
	m := getatest.New(t, one("/m", geta.Route{Post: geta.Op(http.StatusOK, echoUpload, geta.Doc{})}))
	unsupported(m.Form(http.MethodPost, "/m", url.Values{"title": {"a"}}),
		`Content-Type "application/x-www-form-urlencoded" is not multipart/form-data`, "multipart/form-data", "")

	// The document lists the 415 and its headers wherever a body is read.
	d := doc(t, c.App())
	if g := compact(t, at(t, d, "paths", "/x", "patch", "responses", "415", "headers")); g !=
		`{"Accept":{"description":"The media type the operation takes, sent with geta's own 415 for content of another media type (RFC 9110 section 15.5.16)","schema":{"enum":["application/json"],"type":"string"}},`+
			`"Accept-Encoding":{"description":"identity: geta decodes no content coding; sent with geta's own 415 for content in one, and only with that 415 (RFC 9110 section 12.5.3)","schema":{"enum":["identity"],"type":"string"}},`+
			`"Accept-Patch":{"description":"The media type the operation takes, sent with geta's own 415 for content of another media type (RFC 5789 section 2.2)","schema":{"enum":["application/json"],"type":"string"}}}` {
		t.Fatal(g)
	}
	if g := compact(t, at(t, d, "paths", "/x", "post", "responses", "415", "headers")); strings.Contains(g, "Accept-Patch") {
		t.Fatal(g)
	}
}

type applicant struct {
	Name  string    `form:"name" schema:"minLength=1"`
	Age   *int      `form:"age" schema:"minimum=0"`
	Tags  *[]string `form:"tag" schema:"maxItems=2"`
	Agree bool      `form:"agree"`
}

type formIn struct {
	ID   string    `query:"id"`
	Body applicant `body:"form"`
}

func echoForm(ctx context.Context, in *formIn) (*echoed, error) {
	return &echoed{Got: fmt.Sprintf("%s %+v", in.ID, show(in.Body))}, nil
}

func show(s applicant) string {
	age, tags := "nil", "nil"
	if s.Age != nil {
		age = fmt.Sprint(*s.Age)
	}
	if s.Tags != nil {
		tags = fmt.Sprint(*s.Tags)
	}
	return fmt.Sprintf("name=%s age=%s tags=%s agree=%t", s.Name, age, tags, s.Agree)
}

func TestFormBinding(t *testing.T) {
	c := getatest.New(t, one("/f", geta.Route{Post: geta.Op(http.StatusOK, echoForm, geta.Doc{})}))
	post := func(body string) *getatest.Response {
		return c.With("Content-Type", "application/x-www-form-urlencoded").Post("/f?id=1", body)
	}
	if g := got(t, post("name=a+b&age=3&tag=x&tag=y&agree=true")); g != "1 name=a b age=3 tags=[x y] agree=true" {
		t.Fatal(g)
	}
	if g := got(t, c.Form(http.MethodPost, "/f?id=1", url.Values{"name": {"é"}, "agree": {"false"}})); g != "1 name=é age=nil tags=nil agree=false" {
		t.Fatal(g)
	}
	// A charset parameter is the form's still.
	got(t, c.With("Content-Type", "application/x-www-form-urlencoded; charset=utf-8").Post("/f?id=1", "name=a&agree=true"))
	same(t, violations(t, c.With("Content-Type", "application/x-www-form-urlencoded").Post("/f", "agree=true")),
		"query id: missing required parameter", "body $.name: missing required field")
	same(t, violations(t, post("name=&age=-1&agree=yes")),
		"body $.name: string length 0 is shorter than minLength 1",
		"body $.age: -1 is less than minimum 0",
		"body $.agree: expected boolean, got string")
	same(t, violations(t, post("name=a&name=b&agree=true")), "body $.name: given 2 times, but takes one value")
	same(t, violations(t, post("name=a&agree=true&tag=1&tag=2&tag=3")), "body $.tag: array length 3 exceeds maxItems 2")
	same(t, violations(t, post("name=a&agree=true&b=1&a=2")), "body $.a: unknown field", "body $.b: unknown field")
	same(t, violations(t, post("name=a;agree=true")), `body $: the form body is malformed: invalid semicolon separator in query`)
	same(t, violations(t, post("")), "body $: missing required request body")
}

type optionalFormIn struct {
	Body *applicant `body:"form"`
}

func TestOptionalFormBody(t *testing.T) {
	h := func(ctx context.Context, in *optionalFormIn) (*echoed, error) {
		if in.Body == nil {
			return &echoed{Got: "none"}, nil
		}
		return &echoed{Got: show(*in.Body)}, nil
	}
	c := getatest.New(t, one("/f", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}))
	if g := got(t, c.Form(http.MethodPost, "/f", nil)); g != "none" {
		t.Fatal(g)
	}
	if g := got(t, c.Form(http.MethodPost, "/f", url.Values{"name": {"a"}, "agree": {"true"}})); g != "name=a age=nil tags=nil agree=true" {
		t.Fatal(g)
	}
	same(t, violations(t, c.Form(http.MethodPost, "/f", url.Values{"name": {"a"}, "agree": {"1"}})), "body $.agree: expected boolean, got string")
}

type upload struct {
	Title  string       `form:"title"`
	Avatar geta.File    `form:"avatar"`
	Extra  *[]geta.File `form:"extra" schema:"maxItems=2"`
	Note   *geta.File   `form:"note"`
}

type uploadIn struct {
	Body upload `body:"multipart"`
}

func describe(f geta.File) string {
	rc, err := f.Open()
	if err != nil {
		return err.Error()
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	return fmt.Sprintf("%s(%s,%d)=%s", f.Filename(), f.ContentType(), f.Size(), b)
}

func echoUpload(ctx context.Context, in *uploadIn) (*echoed, error) {
	s := in.Body.Title + " " + describe(in.Body.Avatar)
	if in.Body.Extra != nil {
		for _, f := range *in.Body.Extra {
			s += " +" + describe(f)
		}
	}
	if in.Body.Note != nil {
		s += " note:" + describe(*in.Body.Note)
	}
	return &echoed{Got: s}, nil
}

func TestMultipartBinding(t *testing.T) {
	c := getatest.New(t, one("/m", geta.Route{Post: geta.Op(http.StatusOK, echoUpload, geta.Doc{})}))
	avatar := getatest.FilePart{Field: "avatar", Filename: "me.png", ContentType: "image/png", Content: []byte("PNG")}
	title := url.Values{"title": {"hi"}}
	if g := got(t, c.Multipart(http.MethodPost, "/m", title, avatar)); g != "hi me.png(image/png,3)=PNG" {
		t.Fatal(g)
	}
	g := got(t, c.Multipart(http.MethodPost, "/m", title, avatar,
		getatest.FilePart{Field: "extra", Filename: "a.txt", Content: []byte("A")},
		getatest.FilePart{Field: "extra", Filename: "dir/b.txt", ContentType: "text/plain", Content: []byte("BB")},
		getatest.FilePart{Field: "note", Filename: "n", Content: nil}))
	if g != "hi me.png(image/png,3)=PNG +a.txt(application/octet-stream,1)=A +b.txt(text/plain,2)=BB note:n(application/octet-stream,0)=" {
		t.Fatal(g)
	}
	// The field decides how a part is read: a file sent with no filename is
	// a file still, and a value sent with one is a value.
	g = got(t, c.Multipart(http.MethodPost, "/m", url.Values{"avatar": {"raw"}},
		getatest.FilePart{Field: "title", Filename: "t.txt", Content: []byte("from a file")}))
	if g != "from a file (,3)=raw" {
		t.Fatal(g)
	}
	same(t, violations(t, c.Multipart(http.MethodPost, "/m", nil)),
		"body $.title: missing required field", "body $.avatar: missing required field")
	same(t, violations(t, c.Multipart(http.MethodPost, "/m", url.Values{"title": {"a", "b"}, "other": {"x"}}, avatar, avatar,
		getatest.FilePart{Field: "extra"}, getatest.FilePart{Field: "extra"}, getatest.FilePart{Field: "extra"},
		getatest.FilePart{Field: "zzz"})),
		"body $.title: given 2 times, but takes one value",
		"body $.avatar: given 2 times, but takes one value",
		"body $.extra: array length 3 exceeds maxItems 2",
		"body $.other: unknown field",
		"body $.zzz: unknown field")
	same(t, violations(t, c.With("Content-Type", "multipart/form-data").Post("/m", "x")),
		"body $: the multipart/form-data Content-Type has no boundary parameter")
	same(t, violations(t, c.With("Content-Type", "multipart/form-data; boundary=b").Post("/m", "not multipart")),
		"body $: the multipart body is malformed: multipart: NextPart: EOF")
	nameless := "--b\r\nContent-Disposition: attachment\r\n\r\nx\r\n--b--\r\n"
	same(t, violations(t, c.With("Content-Type", "multipart/form-data; boundary=b").Post("/m", nameless)),
		"body $: a part has no form-data name",
		"body $.title: missing required field", "body $.avatar: missing required field")
	same(t, violations(t, c.With("Content-Type", "multipart/form-data; boundary=b").Post("/m", "")), "body $: missing required request body")
}

// A multipart body ends with its close delimiter (RFC 2046 section 5.1.1).
// One that ends after a delimiter line, or within the header lines of a
// part, is cut short, and the parts after the cut are lost: it is a 400,
// never a body whose last parts are dropped. mime/multipart's NextPart
// answers both with the io.EOF it answers at the close delimiter, which
// geta took as the end of the body: a note cut in its header was dropped and
// the handler ran without it.
func TestAMultipartBodyCutShortIsRefused(t *testing.T) {
	c := getatest.New(t, one("/m", geta.Route{Post: geta.Op(http.StatusOK, echoUpload, geta.Doc{})}))
	post := func(body string) *getatest.Response {
		return c.With("Content-Type", "multipart/form-data; boundary=b").Post("/m", body)
	}
	parts := "--b\r\nContent-Disposition: form-data; name=\"title\"\r\n\r\nhi\r\n" +
		"--b\r\nContent-Disposition: form-data; name=\"avatar\"; filename=\"a\"\r\n\r\nA\r\n"
	const cut = "body $: the multipart body is malformed: multipart: the body ends before its close delimiter"
	for _, body := range []string{
		parts + "--b\r\nContent-Disposition: form-data; name=\"note\"; filename=\"n\"\r\n",
		parts + "--b\r\nContent-Disposition: form-da",
		parts + "--b\r\n",
		parts + "--b \t\r\n",
		"--b\r\n",
		// A close delimiter that is not one: no line of its own, or another
		// newline than the body's.
		strings.Replace(parts, "\r\nhi\r\n", "\r\nhi --b--\r\n", 1) + "--b\r\n",
		strings.Replace(parts, "\r\nhi\r\n", "\r\nhi\n--b--\r\n", 1) + "--b\r\n",
	} {
		same(t, violations(t, post(body)), cut)
	}
	// The close delimiter, at the end, with transport padding, before an
	// epilogue, or in a body whose lines end in LF alone, which
	// mime/multipart reads.
	for _, body := range []string{
		parts + "--b--",
		parts + "--b-- \t\r\n",
		parts + "--b--\r\nan epilogue\r\n--b\r\n",
		strings.ReplaceAll(parts, "\r\n", "\n") + "--b--\n",
		"a preamble\r\n--b--x\r\n" + parts + "--b--\r\n",
	} {
		if g := got(t, post(body)); g != "hi a(,1)=A" {
			t.Fatalf("%q: %s", body, g)
		}
	}
	// A body of no part but the close delimiter has none: the fields are
	// missing, not the body cut.
	same(t, violations(t, post("--b--\r\n")), "body $.title: missing required field", "body $.avatar: missing required field")
}

// A file past Limits.MaxMultipartMemory is held in a temporary file, which
// is removed once the handler returns, or at once when the request is
// refused.
func TestMultipartTemporaryFilesAreRemoved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	limits := geta.DefaultLimits
	limits.MaxMultipartMemory = 4
	held := -1
	h := func(ctx context.Context, in *uploadIn) (*echoed, error) {
		entries, _ := os.ReadDir(dir)
		held = len(entries)
		return echoUpload(ctx, in)
	}
	app, err := geta.New(one("/m", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}), geta.WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	c := getatest.Serve(t, app)
	empty := func() {
		t.Helper()
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Fatalf("left behind: %v", entries)
		}
	}
	// "abc" fits in memory, "defgh" and "ijklmnop" do not.
	g := got(t, c.Multipart(http.MethodPost, "/m", url.Values{"title": {"t"}},
		getatest.FilePart{Field: "avatar", Filename: "a", Content: []byte("abc")},
		getatest.FilePart{Field: "extra", Filename: "b", Content: []byte("defgh")},
		getatest.FilePart{Field: "extra", Filename: "c", Content: []byte("ijklmnop")}))
	if g != "t a(application/octet-stream,3)=abc +b(application/octet-stream,5)=defgh +c(application/octet-stream,8)=ijklmnop" || held != 2 {
		t.Fatalf("%s; %d files on disk", g, held)
	}
	empty()
	// Refused after the files were stored.
	violations(t, c.Multipart(http.MethodPost, "/m", nil,
		getatest.FilePart{Field: "avatar", Filename: "a", Content: []byte("0123456789")}))
	empty()
}

func TestOversizedFormIs413(t *testing.T) {
	limits := geta.DefaultLimits
	limits.MaxBodyBytes = 64
	limits.MaxMultipartMemory = 0
	app, err := geta.New(geta.Table{Routes: []geta.Entry{
		{Path: "/m", Route: geta.Route{Post: geta.Op(http.StatusOK, echoUpload, geta.Doc{})}},
		{Path: "/f", Route: geta.Route{Post: geta.Op(http.StatusOK, echoForm, geta.Doc{})}},
	}}, geta.WithLimits(limits), geta.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatal(err)
	}
	c := getatest.Serve(t, app)
	tooLarge := func(res *getatest.Response) {
		t.Helper()
		if res.Status != 413 || res.Problem().Detail != "the request body exceeds 64 bytes" {
			t.Fatalf("%d %s", res.Status, res.Body)
		}
	}
	tooLarge(c.Multipart(http.MethodPost, "/m", url.Values{"title": {"t"}},
		getatest.FilePart{Field: "avatar", Filename: "a", Content: bytes.Repeat([]byte("x"), 100)}))
	tooLarge(c.Form(http.MethodPost, "/f?id=1", url.Values{"name": {strings.Repeat("x", 100)}}))

	// A body of no declared length that MaxBodyBytes cuts within a part's
	// header lines is a 413, not a malformed header.
	body := "--B\r\nContent-Disposition: form-data; name=\"title\"\r\n\r\nt\r\n--B\r\nContent-Type: text/plain\r\n" +
		"Content-Disposition: form-data; name=\"avatar\"; filename=\"a\"\r\n\r\nx\r\n--B--\r\n"
	req := httptest.NewRequest(http.MethodPost, "/m", strings.NewReader(body))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "multipart/form-data; boundary=B")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "the request body exceeds 64 bytes") {
		t.Fatalf("cut in a part's header: %d %s", rec.Code, rec.Body)
	}

	// Nor is one that mime/multipart finds malformed before the cut: content
	// that begins with the delimiter reads as a delimiter line, and the next
	// delimiter line as a header without its colon.
	head := "--B\r\nContent-Disposition: form-data; name=\"title\"\r\n\r\n--B\r\n--B\r\n"
	if len(head) > 64 {
		t.Fatal(len(head))
	}
	req = httptest.NewRequest(http.MethodPost, "/m", strings.NewReader(head+strings.Repeat("x", 200)))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "multipart/form-data; boundary=B")
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "the request body exceeds 64 bytes") {
		t.Fatalf("malformed before the cut: %d %s", rec.Code, rec.Body)
	}

	// The answer does not depend on how the body is split into reads: a body
	// of no declared length past MaxBodyBytes is a 413 wherever the excess
	// lies. send sends body of no declared length through chunked.
	send := func(body string, sizes []int, end error) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/m", &chunked{s: body, sizes: sizes, end: end})
		req.ContentLength = -1
		req.Header.Set("Content-Type", "multipart/form-data; boundary=B")
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec
	}
	tooLargeRec := func(what string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "the request body exceeds 64 bytes") {
			t.Errorf("%s: %d %s", what, rec.Code, rec.Body)
		}
	}
	// The close delimiter within the limit, the epilogue past it.
	closed := "--B\r\nContent-Disposition: form-data; name=\"title\"\r\n\r\nt\r\n--B--\r\n"
	if len(closed) > 64 {
		t.Fatal(len(closed))
	}
	tooLargeRec("epilogue past the limit, one read", send(closed+strings.Repeat("x", 100), nil, nil))
	tooLargeRec("epilogue past the limit, split reads", send(closed+strings.Repeat("x", 100), []int{len(closed)}, nil))
	// Malformed before the limit, cut after it.
	tooLargeRec("malformed before the cut, split reads", send(head+strings.Repeat("x", 200), []int{len(head)}, nil))

	// A client that disconnects sent a body that could not be read, not a
	// malformed one.
	rec = send("--B\r\nContent-Disposition: form-data; name=\"title\"\r\n\r\nt", nil, io.ErrUnexpectedEOF)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "the request body could not be read") {
		t.Errorf("disconnected: %d %s", rec.Code, rec.Body)
	}

	// A file geta cannot store is a 500 even in a body past the limit.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "absent"))
	file := "--B\r\nContent-Disposition:form-data;name=avatar;filename=a\r\n\r\n"
	if len(file)+1 > 64 {
		t.Fatal(len(file))
	}
	if rec := send(file+strings.Repeat("x", 100), nil, nil); rec.Code != http.StatusInternalServerError {
		t.Errorf("a file not stored: %d %s", rec.Code, rec.Body)
	}
}

// chunked delivers s in reads of sizes, then the rest at once, then fails
// with end (io.EOF if nil).
type chunked struct {
	s     string
	sizes []int
	end   error
}

func (c *chunked) Read(p []byte) (int, error) {
	if c.s == "" {
		if c.end != nil {
			return 0, c.end
		}
		return 0, io.EOF
	}
	n := len(c.s)
	if len(c.sizes) > 0 {
		n, c.sizes = min(n, c.sizes[0]), c.sizes[1:]
	}
	n = copy(p, c.s[:n])
	c.s = c.s[n:]
	return n, nil
}

// getaclient sends a form or multipart body from the input type, files
// from geta.NewFile.
func TestClientSendsFormsAndFiles(t *testing.T) {
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/m", Route: geta.Route{Post: geta.Op(http.StatusOK, echoUpload, geta.Doc{})}},
		{Path: "/f", Route: geta.Route{Post: geta.Op(http.StatusOK, echoForm, geta.Doc{})}},
	}})
	tc := c.Typed()
	extra := []geta.File{geta.NewFile("x.bin", "", strings.NewReader("X"))}
	out, err := getaclient.Call[uploadIn, echoed](t.Context(), tc, http.MethodPost, "/m", &uploadIn{Body: upload{
		Title:  "名前",
		Avatar: geta.NewFile("avatar.png", "image/png", bytes.NewReader([]byte("PNGDATA"))),
		Extra:  &extra,
	}})
	if err != nil || out.Got != "名前 avatar.png(image/png,7)=PNGDATA +x.bin(application/octet-stream,1)=X" {
		t.Fatal(out, err)
	}
	age := 7
	tags := []string{"a", "b c"}
	fout, err := getaclient.Call[formIn, echoed](t.Context(), tc, http.MethodPost, "/f", &formIn{ID: "9",
		Body: applicant{Name: "n&m=", Age: &age, Tags: &tags, Agree: true}})
	if err != nil || fout.Got != "9 name=n&m= age=7 tags=[a b c] agree=true" {
		t.Fatal(fout, err)
	}
	// A violation comes back as the problem.
	_, err = getaclient.Call[formIn, echoed](t.Context(), tc, http.MethodPost, "/f", &formIn{ID: "9"})
	if err == nil || !strings.Contains(err.Error(), "body $.name: string length 0 is shorter than minLength 1") {
		t.Fatal(err)
	}
}

func TestFormDocument(t *testing.T) {
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{
		{Path: "/m", Route: geta.Route{Post: geta.Op(http.StatusOK, echoUpload, geta.Doc{})}},
		{Path: "/f", Route: geta.Route{Post: geta.Op(http.StatusOK, echoForm, geta.Doc{})}},
	}})
	d := doc(t, c.App())
	if g := compact(t, at(t, d, "paths", "/f", "post", "requestBody")); g != `{"content":{"application/x-www-form-urlencoded":{"schema":{`+
		`"additionalProperties":false,"properties":{"age":{"format":"int64","minimum":0,"type":"integer"},"agree":{"type":"boolean"},`+
		`"name":{"maxLength":4096,"minLength":1,"type":"string"},"tag":{"items":{"maxLength":4096,"type":"string"},"maxItems":2,"type":"array"}},`+
		`"required":["name","agree"],"type":"object"}}},"required":true}` {
		t.Fatal(g)
	}
	if g := compact(t, at(t, d, "paths", "/m", "post", "requestBody")); g != `{"content":{"multipart/form-data":{"schema":{`+
		`"additionalProperties":false,"properties":{"avatar":{"contentMediaType":"application/octet-stream","type":"string"},`+
		`"extra":{"items":{"contentMediaType":"application/octet-stream","type":"string"},"maxItems":2,"type":"array"},`+
		`"note":{"contentMediaType":"application/octet-stream","type":"string"},"title":{"maxLength":4096,"type":"string"}},`+
		`"required":["title","avatar"],"type":"object"}}},"required":true}` {
		t.Fatal(g)
	}
	for _, p := range []string{"/f", "/m"} {
		for _, status := range []string{"400", "413", "415"} {
			at(t, d, "paths", p, "post", "responses", status)
		}
	}
	// The committed copy passes the OpenAPI 3.1 meta-schema (uvx
	// openapi-spec-validator testdata/forms.openapi.json).
	getatest.Golden(t, c.App(), "testdata/forms.openapi.json")
}

func TestRejectsFormMistakes(t *testing.T) {
	type fileInJSON struct {
		Body struct {
			F geta.File `json:"f"`
		} `body:"json"`
	}
	rejects(t, one("/x", get(func(context.Context, *fileInJSON) (*ok, error) { return nil, nil })),
		`.F: type geta.File outside a body:"multipart" form field`)
	type fileInQuery struct {
		F geta.File `query:"f"`
	}
	rejects(t, one("/x", get(func(context.Context, *fileInQuery) (*ok, error) { return nil, nil })),
		`type geta.File outside a body:"multipart" form field`)
	type fileInForm struct {
		Body struct {
			F geta.File `form:"f"`
		} `body:"form"`
	}
	rejects(t, one("/x", get(func(context.Context, *fileInForm) (*ok, error) { return nil, nil })),
		`F: form field "f" has file type geta.File outside a multipart body`)
	type untagged struct {
		Body struct {
			F string
		} `body:"form"`
	}
	rejects(t, one("/x", get(func(context.Context, *untagged) (*ok, error) { return nil, nil })), "F has no form tag")
	type fileSchema struct {
		Body struct {
			F geta.File `form:"f" schema:"maxLength=10"`
		} `body:"multipart"`
	}
	rejects(t, one("/x", get(func(context.Context, *fileSchema) (*ok, error) { return nil, nil })),
		`schema tag "maxLength=10" on a geta.File`)
	type uniqueFiles struct {
		Body struct {
			F []geta.File `form:"f" schema:"uniqueItems=true"`
		} `body:"multipart"`
	}
	rejects(t, one("/x", get(func(context.Context, *uniqueFiles) (*ok, error) { return nil, nil })),
		`form field "f": uniqueItems on files`)
	type notStruct struct {
		Body string `body:"form"`
	}
	rejects(t, one("/x", get(func(context.Context, *notStruct) (*ok, error) { return nil, nil })),
		`Body: a body:"form" field has type string, not a struct`)
	type schemaOnBody struct {
		Body applicant `body:"form" schema:"minLength=1"`
	}
	rejects(t, one("/x", get(func(context.Context, *schemaOnBody) (*ok, error) { return nil, nil })),
		`Body: a body:"form" field takes no schema tag`)
	type twice struct {
		Body struct {
			A string `form:"a"`
			B string `form:"a"`
		} `body:"form"`
	}
	rejects(t, one("/x", get(func(context.Context, *twice) (*ok, error) { return nil, nil })), `B binds form field "a", already bound by A`)
	type mapField struct {
		Body struct {
			M map[string]string `form:"m"`
		} `body:"multipart"`
	}
	rejects(t, one("/x", get(func(context.Context, *mapField) (*ok, error) { return nil, nil })),
		`M: form field "m" has unsupported type map[string]string`)
	type twoBodies struct {
		A applicant `body:"form"`
		B item      `body:"json"`
	}
	rejects(t, one("/x", get(func(context.Context, *twoBodies) (*ok, error) { return nil, nil })), "B: a second body field")
}
