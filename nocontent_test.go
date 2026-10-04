package geta_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/koji-1009/geta"
)

type ncIn struct {
	N *int `query:"n" schema:"maximum=9"`
}

// Content sent to an operation that reads no body is a 415, answered alone,
// before the handler runs, whatever the method: the document defines no
// request body there, so it would otherwise be dropped unread behind a
// success. Zero bytes are no content.
func TestContentToAnOperationWithoutABodyIs415(t *testing.T) {
	var ran atomic.Int32
	h := func(context.Context, *ncIn) (*ok, error) { ran.Add(1); return &ok{true}, nil }
	del := func(context.Context, *ncIn) error { ran.Add(1); return nil }
	a := accepts(t, one("/r", geta.Route{
		Get:    geta.Op(http.StatusOK, h, geta.Doc{}),
		Post:   geta.Op(http.StatusCreated, h, geta.Doc{}),
		Delete: geta.OpNoBody(http.StatusNoContent, del, geta.Doc{}),
	}))
	send := func(method, path, ct string, body io.Reader, length int64) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, body)
		req.ContentLength = length
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, req)
		return rec
	}
	refused := func(name string, rec *httptest.ResponseRecorder, head bool) {
		t.Helper()
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%s: %d %s, want 415", name, rec.Code, rec.Body)
			return
		}
		if rec.Header().Get("Accept") != "" || rec.Header().Get("Accept-Encoding") != "" {
			t.Errorf("%s: the 415 names a media type or a coding: %v", name, rec.Header())
		}
		if head {
			return
		}
		var p geta.Problem
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Detail != "the operation takes no request content" || len(p.Errors) != 0 {
			t.Errorf("%s: problem %+v (%v)", name, p, err)
		}
	}
	big := strings.Repeat("x", 2<<20)
	for _, c := range []struct {
		name, method, path, ct, body string
		length                       int64
	}{
		{"JSON to a POST", http.MethodPost, "/r", "application/json", `{"a":1}`, 7},
		{"text to a POST", http.MethodPost, "/r", "text/plain", "hello", 5},
		{"2 MiB with no Content-Type", http.MethodPost, "/r", "", big, int64(len(big))},
		{"JSON to a GET", http.MethodGet, "/r", "application/json", `{"a":1}`, 7},
		{"content to a HEAD", http.MethodHead, "/r", "text/plain", "hello", 5},
		{"content to a DELETE", http.MethodDelete, "/r", "text/plain", "hello", 5},
		{"content to OPTIONS", http.MethodOptions, "/r", "text/plain", "hello", 5},
		{"content of unknown length", http.MethodPost, "/r", "text/plain", "hello", -1},
		{"beside a parameter's violation, alone", http.MethodGet, "/r?n=99", "text/plain", "hello", 5},
	} {
		refused(c.name, send(c.method, c.path, c.ct, strings.NewReader(c.body), c.length), c.method == http.MethodHead)
	}
	if n := ran.Load(); n != 0 {
		t.Fatalf("the handler ran %d times on refused content", n)
	}
	// Zero bytes are no content: a Content-Length of zero, and a body of
	// unknown length that ends at once.
	if rec := send(http.MethodPost, "/r", "application/json", strings.NewReader(""), 0); rec.Code != http.StatusCreated {
		t.Errorf("Content-Length 0: %d %s", rec.Code, rec.Body)
	}
	if rec := send(http.MethodPost, "/r", "application/json", strings.NewReader(""), -1); rec.Code != http.StatusCreated {
		t.Errorf("an empty body of unknown length: %d %s", rec.Code, rec.Body)
	}
	if rec := send(http.MethodGet, "/r", "", nil, 0); rec.Code != http.StatusOK {
		t.Errorf("a GET with no content: %d %s", rec.Code, rec.Body)
	}

	// Over a real connection: a chunked body is judged on its first byte, an
	// empty one served; a declared length is refused before any of it is
	// read, so a client waiting for 100 Continue gets the 415 in its place.
	addr := serve(t, a)
	for _, c := range []struct {
		name, head, body string
		want             int
	}{
		{"an empty chunked body", "POST /r HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked", "0\r\n\r\n", http.StatusCreated},
		{"a chunked body", "POST /r HTTP/1.1\r\nHost: x\r\nContent-Type: text/plain\r\nTransfer-Encoding: chunked", "5\r\nhello\r\n0\r\n\r\n", http.StatusUnsupportedMediaType},
		{"a chunked GET", "GET /r HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked", "1\r\nx\r\n0\r\n\r\n", http.StatusUnsupportedMediaType},
		{"Expect: 100-continue", "POST /r HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 10\r\nExpect: 100-continue", "", http.StatusUnsupportedMediaType},
	} {
		conn, br := rawRequest(t, addr, c.head, c.body)
		if res := readResponse(t, conn, br, 2*time.Second); res.StatusCode != c.want {
			t.Errorf("%s: %d, want %d", c.name, res.StatusCode, c.want)
		}
	}

	// The document says so: every operation that reads no body, the options
	// operation included, lists the 415, with no headers.
	m := doc(t, a)
	for _, method := range []string{"get", "post", "delete", "options"} {
		r := at(t, m, "paths", "/r", method, "responses", "415").(map[string]any)
		if r["description"] != "The request has content, which the operation does not take" || r["headers"] != nil {
			t.Errorf("%s: the 415 is documented as %v", method, r)
		}
	}
}
