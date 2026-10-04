package main

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// A page on another origin does what a browser does: a preflight, then the
// request. It fetches a user, reads its ETag, and replaces the user with
// If-Match and its bearer token; CORS allows and exposes those headers
// because the operations declare them.
func TestCrossOriginConditionalWrite(t *testing.T) {
	c := client(t)
	if res := c.Post("/users", map[string]any{"id": "1", "name": "Ada", "role": "member", "tags": []string{}}); res.Status != http.StatusCreated {
		t.Fatalf("create: %d %s", res.Status, res.Body)
	}
	const origin = "https://page.example"
	send := func(method, path, body string, header ...string) *http.Response {
		t.Helper()
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(t.Context(), method, c.URL()+path, r)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", origin)
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		res, err := c.HTTP().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return res
	}
	names := func(v string) []string {
		var out []string
		for n := range strings.SplitSeq(v, ",") {
			out = append(out, strings.ToLower(strings.TrimSpace(n)))
		}
		return out
	}
	allows := func(method string, headers ...string) {
		t.Helper()
		res := send(http.MethodOptions, "/users/1", "", "Access-Control-Request-Method", method,
			"Access-Control-Request-Headers", strings.Join(headers, ","))
		if res.StatusCode != http.StatusNoContent || res.Header.Get("Access-Control-Allow-Origin") != "*" ||
			!slices.Contains(names(res.Header.Get("Access-Control-Allow-Methods")), strings.ToLower(method)) {
			t.Fatalf("preflight %s: %d %v", method, res.StatusCode, res.Header)
		}
		for _, h := range headers {
			if !slices.Contains(names(res.Header.Get("Access-Control-Allow-Headers")), h) {
				t.Fatalf("preflight %s does not allow %s: %v", method, h, res.Header)
			}
		}
	}

	allows(http.MethodGet, "authorization", "if-none-match")
	res := send(http.MethodGet, "/users/1", "", "Authorization", "Bearer t-admin")
	tag := res.Header.Get("ETag")
	if res.StatusCode != http.StatusOK || tag == "" || !slices.Contains(names(res.Header.Get("Access-Control-Expose-Headers")), "etag") {
		t.Fatalf("get: %d %v", res.StatusCode, res.Header)
	}

	allows(http.MethodPut, "authorization", "content-type", "if-match")
	// The preflight allows the methods /users/1 serves, as its Allow lists
	// them less HEAD and OPTIONS: never one that would get 405.
	var served []string
	for _, m := range names(send(http.MethodOptions, "/users/1", "", "Authorization", "Bearer t-admin").Header.Get("Allow")) {
		if m != "head" && m != "options" {
			served = append(served, m)
		}
	}
	pre := names(send(http.MethodOptions, "/users/1", "", "Access-Control-Request-Method", http.MethodPut).Header.Get("Access-Control-Allow-Methods"))
	if !slices.Equal(pre, served) || slices.Contains(pre, "patch") || slices.Contains(pre, "post") {
		t.Fatalf("preflight PUT /users/1 allows %q; /users/1 serves %q", pre, served)
	}
	put := func() *http.Response {
		return send(http.MethodPut, "/users/1", `{"id":"1","name":"Ada B","role":"member","tags":[]}`,
			"Authorization", "Bearer t-admin", "Content-Type", "application/json", "If-Match", tag)
	}
	if res := put(); res.StatusCode != http.StatusOK || res.Header.Get("Access-Control-Allow-Origin") != "*" ||
		!slices.Contains(names(res.Header.Get("Access-Control-Expose-Headers")), "etag") {
		t.Fatalf("put: %d %v", res.StatusCode, res.Header)
	}
	if res := put(); res.StatusCode != http.StatusPreconditionFailed || res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("put with a stale tag: %d %v", res.StatusCode, res.Header)
	}
}
