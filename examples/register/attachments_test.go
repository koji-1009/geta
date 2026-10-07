package main

import (
	"bytes"
	"net/http"
	"net/url"
	"testing"

	"github.com/koji-1009/geta/examples/register/routes/users/id_/attachments"
	"github.com/koji-1009/geta/getatest"
)

// POST /users/{id}/attachments takes every part named "file" as one
// []geta.File, in the order sent, beside an ordinary field. None is a 400
// (a []geta.File takes at least one), and more than maxItems is a 400.
func TestMultipleFileUpload(t *testing.T) {
	c := client(t)
	c.Post("/users", map[string]any{"id": "u1", "name": "U", "role": "member", "tags": []string{}})
	file := func(name, content string) getatest.FilePart {
		return getatest.FilePart{Field: "file", Filename: name, ContentType: "text/plain", Content: []byte(content)}
	}

	res := c.Multipart(http.MethodPost, "/users/u1/attachments", url.Values{"note": {"signed"}},
		file("a.txt", "alpha"), file("b.txt", "beta"), getatest.FilePart{Field: "file", Filename: "c.bin", Content: bytes.Repeat([]byte{7}, 512<<10)})
	if res.Status != http.StatusOK {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
	got := res.JSON[attachments.Attachments]()
	if got.User != "u1" || got.Note == nil || *got.Note != "signed" || len(got.Items) != 3 {
		t.Fatalf("%+v", got)
	}
	if a := got.Items[0]; a.Filename != "a.txt" || a.ContentType != "text/plain" || a.Bytes != 5 ||
		a.SHA256 != "8ed3f6ad685b959ead7022518e1af76cd816f8e8ec7ccdda1ed4018e8f2223f8" {
		t.Fatalf("%+v", a)
	}
	if a := got.Items[2]; a.Filename != "c.bin" || a.ContentType != "application/octet-stream" || a.Bytes != 512<<10 {
		t.Fatalf("%+v", a)
	}

	for name, files := range map[string][]getatest.FilePart{
		"no file":    nil,
		"six files":  {file("1", "x"), file("2", "x"), file("3", "x"), file("4", "x"), file("5", "x"), file("6", "x")},
		"a misnamed": {{Field: "files", Filename: "a", Content: []byte("x")}},
	} {
		if res := c.Multipart(http.MethodPost, "/users/u1/attachments", nil, files...); res.Status != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, res.Status, res.Body)
		}
	}
	if res := c.Multipart(http.MethodPost, "/users/u1/attachments", nil, file("empty", "")); res.Status != http.StatusUnprocessableEntity {
		t.Errorf("an empty file: %d %s", res.Status, res.Body)
	}
	if res := c.Multipart(http.MethodPost, "/users/none/attachments", nil, file("a", "x")); res.Status != http.StatusNotFound {
		t.Errorf("a missing user: %d %s", res.Status, res.Body)
	}
}
