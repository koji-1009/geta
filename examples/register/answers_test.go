package main

import (
	"bytes"
	"net/http"
	"net/url"
	"testing"

	"github.com/koji-1009/geta/getatest"
)

// What geta answers by itself, with no line of the application: OPTIONS on
// a served path, HEAD beside GET, 405 with Allow, 404 behind the gate, an
// unclean path redirected, a body of another media type or in a content
// coding refused with 415, and one past the body limit with 413.
func TestWhatGetaAnswers(t *testing.T) {
	c := client(t)
	if res := c.Post("/users", map[string]any{"id": "1", "name": "Ada", "role": "member", "tags": []string{}}); res.Status != http.StatusCreated {
		t.Fatal(res.Status, res.Text())
	}
	expect := func(res *getatest.Response, status int, header, value string) {
		t.Helper()
		if res.Status != status || header != "" && res.Header.Get(header) != value {
			t.Fatalf("%d %v, want %d with %s: %s", res.Status, res.Header, status, header, value)
		}
	}
	expect(c.Do(http.MethodOptions, "/users/1", nil), http.StatusNoContent, "Allow", "DELETE, GET, HEAD, OPTIONS, PUT")
	// /users/by-role is no template, but /users/{id} matches it.
	expect(c.Do(http.MethodOptions, "/users/by-role", nil), http.StatusNoContent, "Allow", "DELETE, GET, HEAD, OPTIONS, PUT")
	expect(c.Do(http.MethodOptions, "/nowhere", nil), http.StatusNotFound, "", "")
	expect(anonymous(t).Do(http.MethodOptions, "/health", nil), http.StatusUnauthorized, "", "")
	if res := c.Do(http.MethodHead, "/users/1", nil); res.Status != http.StatusOK || len(res.Body) != 0 || res.Header.Get("ETag") == "" {
		t.Fatalf("HEAD: %d %v %q", res.Status, res.Header, res.Body)
	}
	expect(c.Patch("/users/1", map[string]any{}), http.StatusMethodNotAllowed, "Allow", "DELETE, GET, HEAD, OPTIONS, PUT")
	expect(c.Get("/nowhere"), http.StatusNotFound, "", "")
	expect(anonymous(t).Get("/nowhere"), http.StatusUnauthorized, "WWW-Authenticate", "Bearer")
	expect(c.Get("/users/./1"), http.StatusTemporaryRedirect, "Location", "/users/1")
	expect(c.Get("/users//1"), http.StatusNotFound, "", "")

	expect(c.With("Content-Type", "text/plain").Post("/users", "id=2"), http.StatusUnsupportedMediaType, "Accept", "application/json")
	expect(c.With("Content-Encoding", "gzip").Post("/users", "{}"), http.StatusUnsupportedMediaType, "Accept-Encoding", "identity")
	expect(c.Put("/users/1/avatar", "PNG"), http.StatusUnsupportedMediaType, "Accept", "multipart/form-data")
	big := getatest.FilePart{Field: "image", Filename: "big.png", ContentType: "image/png", Content: bytes.Repeat([]byte{0}, 1<<20+1)}
	expect(c.Multipart(http.MethodPut, "/users/1/avatar", url.Values{}, big), http.StatusRequestEntityTooLarge, "", "")
}

// Two 409s and two 403s share a status; their problem types tell them apart.
func TestProblemTypesTellRowsApart(t *testing.T) {
	c := client(t)
	ada := map[string]any{"id": "1", "name": "Ada", "role": "member", "tags": []string{}}
	c.Post("/users", ada)
	for name, tc := range map[string]struct {
		res  *getatest.Response
		typ  string
		code int
	}{
		"a taken id":                {c.Post("/users", ada), "/problems/user-exists", http.StatusConflict},
		"an import with a taken id": {c.Post("/users/import", map[string]any{"users": []any{ada}}), "/problems/user-exists", http.StatusConflict},
		"a role set on create":      {c.Post("/users", map[string]any{"id": "2", "name": "Bo", "role": "admin", "tags": []string{}}), "/problems/role-change", http.StatusForbidden},
		"not an administrator":      {anonymous(t).Bearer("t-user").Post("/users", ada), "about:blank", http.StatusForbidden},
	} {
		if p := tc.res.Problem(); tc.res.Status != tc.code || p.Type != tc.typ {
			t.Errorf("%s: %d %q, want %d %q", name, tc.res.Status, p.Type, tc.code, tc.typ)
		}
	}
	c.Put("/admin/users/1/role", map[string]any{"role": "admin"})
	if res := c.Delete("/users/1"); res.Status != http.StatusConflict || res.Problem().Type != "/problems/last-admin" {
		t.Fatalf("delete the last admin: %d %s", res.Status, res.Body)
	}
}
