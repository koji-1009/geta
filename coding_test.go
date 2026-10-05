package geta_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

func gzipped(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// refusedCoding asserts geta's 415 for content in a content coding: it names
// identity in Accept-Encoding (RFC 9110 section 12.5.3) and carries neither
// Accept nor Accept-Patch, which are about media types.
func refusedCoding(t *testing.T, res *getatest.Response, detail string) {
	t.Helper()
	if res.Status != http.StatusUnsupportedMediaType || res.Header.Get("Accept-Encoding") != "identity" ||
		res.Header.Get("Accept") != "" || res.Header.Get("Accept-Patch") != "" || res.Problem().Detail != detail {
		t.Fatalf("%d %v %s", res.Status, res.Header, res.Body)
	}
}

// Content in a content coding geta does not decode is a 415 with
// Accept-Encoding: identity (RFC 9110 sections 15.5.16 and 12.5.3), not a 400
// for the coded bytes read as JSON or as a form; the handler never runs.
func TestContentCodingIs415(t *testing.T) {
	ran := false
	h := func(ctx context.Context, in *bodyIn) (*echoed, error) {
		ran = true
		return &echoed{Got: in.Body.Name}, nil
	}
	c := getatest.New(t, one("/x", geta.Route{
		Post:  geta.Op(http.StatusOK, h, geta.Doc{}),
		Patch: geta.Op(http.StatusOK, h, geta.Doc{}),
	}))
	body := `{"name":"a","price":1}`
	refusedCoding(t, c.With("Content-Encoding", "gzip").Post("/x", gzipped(t, body)), `Content-Encoding "gzip" is not supported`)
	refusedCoding(t, c.With("Content-Encoding", "gzip").Patch("/x", gzipped(t, body)), `Content-Encoding "gzip" is not supported`)
	// Every coding listed counts, on one line or several; identity is none.
	refusedCoding(t, c.With("Content-Encoding", "identity, br").Post("/x", body), `Content-Encoding "br" is not supported`)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, c.URL()+"/x", bytes.NewReader(gzipped(t, body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Add("Content-Encoding", "gzip")
	req.Header.Add("Content-Encoding", "zstd")
	refusedCoding(t, c.Send(req), `Content-Encoding "gzip, zstd" is not supported`)
	// An element is trimmed of spaces and tabs alone: identity beside a
	// no-break space is no identity.
	nbsp := "identity\xc2\xa0"
	refusedCoding(t, c.With("Content-Encoding", nbsp).Post("/x", body), fmt.Sprintf("Content-Encoding %q is not supported", nbsp))
	// The coding is refused before the media type is looked at.
	refusedCoding(t, c.With("Content-Encoding", "gzip").With("Content-Type", "text/plain").Post("/x", gzipped(t, body)), `Content-Encoding "gzip" is not supported`)
	if ran {
		t.Fatal("the handler ran on coded content")
	}
	// identity, which RFC 9110 section 8.4.1 reserves for no coding, is taken.
	if g := got(t, c.With("Content-Encoding", "identity").Post("/x", body)); g != "a" {
		t.Fatal(g)
	}
	// No content is no body, whatever Content-Encoding says.
	same(t, violations(t, c.With("Content-Encoding", "gzip").Post("/x", "")), "body $: missing required request body")

	// Form and multipart bodies are refused the same way.
	f := getatest.New(t, one("/f", geta.Route{Post: geta.Op(http.StatusOK, echoForm, geta.Doc{})}))
	refusedCoding(t, f.With("Content-Encoding", "gzip").With("Content-Type", "application/x-www-form-urlencoded").Post("/f?id=1", gzipped(t, "name=a&agree=true")),
		`Content-Encoding "gzip" is not supported`)
	m := getatest.New(t, one("/m", geta.Route{Post: geta.Op(http.StatusOK, echoUpload, geta.Doc{})}))
	refusedCoding(t, m.With("Content-Encoding", "deflate").Multipart(http.MethodPost, "/m", url.Values{"title": {"a"}}, getatest.FilePart{Field: "avatar", Filename: "a.png", Content: []byte("png")}),
		`Content-Encoding "deflate" is not supported`)

	// The document says so on every operation that reads a body.
	d := doc(t, c.App())
	for _, method := range []string{"post", "patch"} {
		if g := compact(t, at(t, d, "paths", "/x", method, "responses", "415", "headers", "Accept-Encoding", "schema")); g != `{"enum":["identity"],"type":"string"}` {
			t.Fatalf("%s: %s", method, g)
		}
	}
	if g := compact(t, at(t, doc(t, m.App()), "paths", "/m", "post", "responses", "415", "headers", "Accept-Encoding", "schema")); g != `{"enum":["identity"],"type":"string"}` {
		t.Fatal(g)
	}
}

// A 415's detail quotes the request's Content-Type or Content-Encoding cut
// to 128 bytes, as a violation quotes a value: a rune is not split, a byte of
// invalid UTF-8 counts as one, and "…" marks the cut. A header is as long as
// MaxHeaderBytes allows, so a whole one made the problem as long.
func TestRefusedHeaderValuesAreClipped(t *testing.T) {
	h := func(ctx context.Context, in *bodyIn) (*echoed, error) { return &echoed{Got: in.Body.Name}, nil }
	c := getatest.New(t, one("/x", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}))
	body := `{"name":"a","price":1}`
	odd := "application/x" + strings.Repeat("é", 100) // the 128th byte begins an é
	for ct, quoted := range map[string]string{
		strings.Repeat("<", 64<<10): strings.Repeat("<", 128) + "…",
		odd:                         odd[:127] + "…",
		strings.Repeat("\x80", 200): strings.Repeat("\x80", 128) + "…",
		strings.Repeat("t", 128):    strings.Repeat("t", 128),
	} {
		res := c.With("Content-Type", ct).Post("/x", body)
		if want := fmt.Sprintf("Content-Type %q is not application/json", quoted); res.Status != http.StatusUnsupportedMediaType ||
			res.Problem().Detail != want || len(res.Body) > 1<<10 {
			t.Fatalf("%d bytes of Content-Type: %d, %d bytes: %q, want %q", len(ct), res.Status, len(res.Body), res.Problem().Detail, want)
		}
	}
	codings := strings.Repeat("x-a, ", 16<<10)
	res := c.With("Content-Encoding", codings).Post("/x", body)
	refusedCoding(t, res, fmt.Sprintf("Content-Encoding %q is not supported", codings[:128]+"…"))
	if len(res.Body) > 1<<10 {
		t.Fatalf("%d bytes", len(res.Body))
	}
}

// Content geta does not take is a 415 whatever its size: its coding and media
// type are judged before the body is read to MaxBodyBytes. Content it takes
// past MaxBodyBytes is a 413.
func TestRefusedContentIs415PastTheBodyLimit(t *testing.T) {
	lim := geta.DefaultLimits
	lim.MaxBodyBytes = 64
	h := func(ctx context.Context, in *bodyIn) (*echoed, error) { return &echoed{Got: in.Body.Name}, nil }
	c := getatest.New(t, one("/x", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}), geta.WithLimits(lim))
	f := getatest.New(t, one("/f", geta.Route{Post: geta.Op(http.StatusOK, echoForm, geta.Doc{})}), geta.WithLimits(lim))
	big := `{"name":"` + strings.Repeat("a", 200) + `","price":1}`
	bigForm := "name=" + strings.Repeat("a", 200) + "&agree=true"
	const coding = `Content-Encoding "gzip" is not supported`
	refusedCoding(t, c.With("Content-Encoding", "gzip").Post("/x", big), coding)
	refusedCoding(t, f.With("Content-Encoding", "gzip").With("Content-Type", "application/x-www-form-urlencoded").Post("/f?id=1", bigForm), coding)
	for name, res := range map[string]*getatest.Response{
		"json as text/plain": c.With("Content-Type", "text/plain").Post("/x", big),
		"form as JSON":       f.Post("/f?id=1", bigForm),
	} {
		if res.Status != http.StatusUnsupportedMediaType {
			t.Errorf("%s: %d %s", name, res.Status, res.Body)
		}
	}
	for name, res := range map[string]*getatest.Response{
		"json": c.Post("/x", big),
		"form": f.With("Content-Type", "application/x-www-form-urlencoded").Post("/f?id=1", bigForm),
	} {
		if res.Status != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: %d %s", name, res.Status, res.Body)
		}
	}
}

// An application that takes a coding decodes it in a root middleware and
// removes Content-Encoding: geta then reads the decoded content, and
// MaxBodyBytes bounds the decoded bytes.
func TestDecodedContentIsTaken(t *testing.T) {
	gunzip := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Content-Encoding") == "gzip" {
				zr, err := gzip.NewReader(r.Body)
				if err != nil {
					geta.WriteProblem(w, http.StatusBadRequest, "the gzip content is malformed")
					return
				}
				r.Body = zr
				r.Header.Del("Content-Encoding")
				r.ContentLength = -1
			}
			next.ServeHTTP(w, r)
		})
	})
	h := func(ctx context.Context, in *bodyIn) (*echoed, error) { return &echoed{Got: in.Body.Name}, nil }
	tbl := withRoot(one("/x", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}), gunzip)
	lim := geta.DefaultLimits
	lim.MaxBodyBytes = 64
	c := getatest.New(t, tbl, geta.WithLimits(lim))
	if g := got(t, c.With("Content-Encoding", "gzip").Post("/x", gzipped(t, `{"name":"a","price":1}`))); g != "a" {
		t.Fatal(g)
	}
	big := `{"name":"` + string(bytes.Repeat([]byte("a"), 200)) + `","price":1}`
	if coded := gzipped(t, big); len(coded) > 64 {
		t.Fatalf("the coded body is %d bytes; the test needs it under the limit", len(coded))
	}
	if res := c.With("Content-Encoding", "gzip").Post("/x", gzipped(t, big)); res.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
}
