package geta_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// A refusal geta writes in place of the handler while the request's body is
// unread does not wait for the body. net/http, before it writes a response
// header on HTTP/1.x, reads away up to 256 KiB of an unread body unless the
// response closes the connection; a client whose body stalls would get no
// answer at all with no Timeout. So such a refusal closes an HTTP/1.1
// connection (Connection: close) and is answered at once; over HTTP/2 the
// stream alone is reset and the connection serves the next request. A body
// read to its end, or one of length zero, leaves the HTTP/1.1 connection open.
//
// It runs in a synctest bubble over httptest's in-memory connections, with no
// Timeout: a response that waited for the body would never come, and the
// bubble would report its goroutines blocked.

type ubCountIn struct {
	N    int       `query:"n"`
	Body io.Reader `body:"text/csv"`
}

func ubApp(t *testing.T) *geta.App {
	small := func(l geta.Limits) geta.Limits { l.MaxBodyBytes = 16; return l }
	okFor := func(context.Context, *dlJSONIn) (*ok, error) { return &ok{true}, nil }
	return accepts(t, geta.Table{Routes: []geta.Entry{
		{Path: "/json", Route: geta.Route{Post: geta.Op(http.StatusOK, okFor, geta.Doc{})}},
		{Path: "/small", Route: geta.Route{Post: geta.Op(http.StatusOK, okFor, geta.Doc{Limits: small})}},
		{Path: "/count", Route: geta.Route{Post: geta.Op(http.StatusOK, func(context.Context, *ubCountIn) (*ok, error) { return &ok{true}, nil }, geta.Doc{})}},
		{Path: "/get", Route: get(okHandler)},
		{Path: "/none", Route: geta.Route{Post: geta.Op(http.StatusOK, okHandler, geta.Doc{})}},
	}})
}

// ubServer serves a in memory, over HTTP/2 (https) when h2, over HTTP/1.1
// otherwise, and returns its client and base URL.
func ubServer(t *testing.T, a http.Handler, h2 bool) (*http.Client, string) {
	srv := httptest.NewTestServer(t, a)
	srv.EnableHTTP2 = h2
	client := srv.Client()
	if h2 {
		return client, "https://example.com"
	}
	return client, "http://example.com"
}

// ubSend sends req and reads its response, reporting whether the request
// reused a connection.
func ubSend(t *testing.T, client *http.Client, req *http.Request) (*http.Response, bool) {
	t.Helper()
	var reused bool
	trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { reused = i.Reused }}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res, reused
}

// ubNext sends a GET after a response and reports whether it reused the
// connection.
func ubNext(t *testing.T, client *http.Client, base string) bool {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+"/get", nil)
	res, reused := ubSend(t, client, req)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the next request: %d", res.StatusCode)
	}
	return reused
}

func TestARefusalDoesNotWaitForAnUnreadBody(t *testing.T) {
	cases := []struct {
		name, path, ct, coding string
		want                   int
	}{
		{"media type", "/json", "text/plain", "", http.StatusUnsupportedMediaType},
		{"coding", "/json", "application/json", "gzip", http.StatusUnsupportedMediaType},
		{"declared length", "/small", "application/json", "", http.StatusRequestEntityTooLarge},
		{"no body read", "/none", "application/json", "", http.StatusUnsupportedMediaType},
		{"no body read on GET", "/get", "application/json", "", http.StatusUnsupportedMediaType},
		{"a parameter, the reader unread", "/count", "text/csv", "", http.StatusBadRequest},
	}
	for _, h2 := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("h2=%v/%s", h2, tc.name), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					client, base := ubServer(t, ubApp(t), h2)
					// Warm the connection, so the next requests can tell
					// whether they reuse it.
					ubNext(t, client, base)
					pr, pw := io.Pipe()
					t.Cleanup(func() { pw.Close() })
					go io.WriteString(pw, "ab") // part of the body, then nothing
					method := http.MethodPost
					if tc.path == "/get" {
						method = http.MethodGet
					}
					req, _ := http.NewRequest(method, base+tc.path, pr)
					req.ContentLength = 40
					req.Header.Set("Content-Type", tc.ct)
					if tc.coding != "" {
						req.Header.Set("Content-Encoding", tc.coding)
					}
					start := time.Now()
					res, _ := ubSend(t, client, req)
					if el := time.Since(start); el != 0 {
						t.Errorf("answered after %v", el)
					}
					wantMajor := map[bool]int{false: 1, true: 2}[h2]
					if res.StatusCode != tc.want || res.ProtoMajor != wantMajor {
						t.Fatalf("%s %d, want HTTP/%d %d", res.Proto, res.StatusCode, wantMajor, tc.want)
					}
					if res.Close == h2 {
						t.Fatalf("%s %d close=%v, want %v", res.Proto, res.StatusCode, res.Close, !h2)
					}
					if reused := ubNext(t, client, base); reused != h2 {
						t.Fatalf("the next request over %s reused its connection %v, want %v", res.Proto, reused, h2)
					}
				})
			})
		}
	}
}

// Once the refusal is sent, the server closes the HTTP/1.1 connection without
// waiting for the client to send the rest of its body or to close it first:
// net/http's closing of the body after the handler, which would read up to
// 256 KiB of it looking for its end, is not left waiting either.
func TestARefusalClosesTheConnectionAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := getatest.Serve(t, ubApp(t))
		conn, err := c.DialContext(t.Context(), "tcp", "example.com:80")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		head := "POST /json HTTP/1.1\r\nHost: x\r\nContent-Type: text/plain\r\nContent-Length: 40\r\n\r\nab"
		if _, err := io.WriteString(conn, head); err != nil {
			t.Fatal(err)
		}
		br := bufio.NewReader(conn)
		res, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusUnsupportedMediaType || !res.Close {
			t.Fatalf("%d close=%v", res.StatusCode, res.Close)
		}
		if n, err := br.Read(make([]byte, 1)); n != 0 || err != io.EOF {
			t.Fatalf("after the response: %d bytes, %v; want the connection closed", n, err)
		}
	})
}

// A refusal of a body read to its end, of one of length zero, and of one of
// unknown length that turns out empty, keeps the HTTP/1.1 connection: there is
// nothing left for net/http to read, so it serves the next request.
func TestARefusalOfAReadBodyKeepsTheConnection(t *testing.T) {
	cases := []struct {
		name, path, ct, body string
		chunked              bool
		want                 int
	}{
		{"a body read whole", "/json", "application/json", `{"text":1}`, false, http.StatusBadRequest},
		{"a body of length zero", "/json", "application/json", "", false, http.StatusBadRequest},
		{"a chunked body that is empty", "/json", "application/json", "", true, http.StatusBadRequest},
		{"a chunked body read whole", "/json", "application/json", `{"text":1}`, true, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client, base := ubServer(t, ubApp(t), false)
				ubNext(t, client, base)
				var body io.Reader = strings.NewReader(tc.body)
				if tc.chunked {
					body = io.MultiReader(body) // no length the client can tell: chunked
				}
				req, _ := http.NewRequest(http.MethodPost, base+tc.path, body)
				if tc.chunked {
					req.ContentLength = -1
				}
				req.Header.Set("Content-Type", tc.ct)
				res, _ := ubSend(t, client, req)
				if res.StatusCode != tc.want || res.Close {
					t.Fatalf("%d close=%v, want %d and the connection kept", res.StatusCode, res.Close, tc.want)
				}
				if !ubNext(t, client, base) {
					t.Fatal("the next request did not reuse the connection")
				}
			})
		})
	}
}
