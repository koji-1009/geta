package geta_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// Rows that share a status can be told apart by their problem type.
func TestFailureTypesTellRowsApart(t *testing.T) {
	errGone := errors.New("gone")
	errHidden := errors.New("hidden")
	which := errGone
	h := func(context.Context, *empty) (*ok, error) { return nil, which }
	r := geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{Failures: []geta.Failure{
		geta.On(errGone, 404, "deleted").Type("https://errors.example/gone"),
		geta.On(errHidden, 404, "not visible").Type("/problems/hidden"),
	}})}
	c := getatest.New(t, one("/x", r))
	if p := c.Get("/x").Problem(); p.Type != "https://errors.example/gone" || p.Status != 404 {
		t.Fatal(p)
	}
	which = errHidden
	if p := c.Get("/x").Problem(); p.Type != "/problems/hidden" {
		t.Fatal(p)
	}
	desc := at(t, doc(t, c.App()), "paths", "/x", "get", "responses", "404", "description")
	if desc != "deleted (type https://errors.example/gone); not visible (type /problems/hidden)" {
		t.Fatal(desc)
	}
}

func TestUntypedRowsAreAboutBlank(t *testing.T) {
	c := getatest.New(t, failing(errNotFound, geta.On(errNotFound, 404, "")))
	if p := c.Get("/x").Problem(); p.Type != "about:blank" {
		t.Fatal(p.Type)
	}
}

func TestRejectsAnInvalidProblemType(t *testing.T) {
	for _, bad := range []string{"", "http://[::1", "a b"} {
		rejects(t, failing(errNotFound, geta.On(errNotFound, 404, "").Type(bad)), "is not a URI reference")
	}
}

// A 500 carries an occurrence id, and the log line for the defect carries
// the same one; nothing of the error reaches the client.
func TestDefectsCarryAnInstanceTheLogCarriesToo(t *testing.T) {
	log, buf := logger()
	boom := func(context.Context, *empty) (*ok, error) { panic("kaboom") }
	tbl := geta.Table{
		Root: geta.Scope{geta.Recover(log), geta.Secure(geta.Policy{Verifiers: map[string]geta.Verifier{"bearer": bearerVerifier}})},
		Routes: []geta.Entry{
			{Path: "/unmatched", Route: get(func(context.Context, *empty) (*ok, error) { return nil, fmt.Errorf("db: %w", errNotFound) })},
			{Path: "/panic", Route: get(boom)},
			{Path: "/verifier", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{geta.Bearer}})}},
		},
	}
	a, err := geta.New(tbl, geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	c := getatest.Serve(t, a)
	seen := map[string]bool{}
	for _, req := range []struct{ path, auth string }{{"/unmatched", ""}, {"/panic", ""}, {"/verifier", "Bearer bug"}, {"/unmatched", ""}} {
		cl := c
		if req.auth != "" {
			cl = c.With("Authorization", req.auth)
		}
		res := cl.Get(req.path)
		p := res.Problem()
		if res.Status != 500 || !strings.HasPrefix(p.Instance, "urn:uuid:") || len(p.Instance) != len("urn:uuid:")+36 {
			t.Fatalf("%s: %d %+v", req.path, res.Status, p)
		}
		if strings.Contains(res.Text(), "db:") || strings.Contains(res.Text(), "kaboom") || strings.Contains(res.Text(), "verifier bug") {
			t.Fatalf("%s: the error leaked: %s", req.path, res.Body)
		}
		if !strings.Contains(buf.String(), "instance="+p.Instance) {
			t.Fatalf("%s: the log lacks %s:\n%s", req.path, p.Instance, buf.String())
		}
		if seen[p.Instance] {
			t.Fatal("an instance was reused")
		}
		seen[p.Instance] = true
	}
	// A failure row is not a defect: no instance.
	if p := getatest.New(t, failing(errNotFound, geta.On(errNotFound, 404, ""))).Get("/x").Problem(); p.Instance != "" {
		t.Fatal(p.Instance)
	}
}

// A failure row and geta's own answer on the same status are two causes, and
// the document states both, the author's first: a handler's 400 does not hide
// that a binding failure answers 400 too.
func TestRowAndGetaOnOneStatusAreBothDocumented(t *testing.T) {
	var calls atomic.Int64
	c := getatest.New(t, scopeTable(&calls, nil,
		geta.On(errNotFound, http.StatusBadRequest, "the name is taken"),
		geta.On(errNotFound, http.StatusRequestEntityTooLarge, "too many items"),
		geta.On(errNotFound, http.StatusInternalServerError, "the store is down"),
		geta.On(errNotFound, http.StatusGatewayTimeout, "the store is slow"),
	))
	m := doc(t, c.App())
	for status, want := range map[string]string{
		"400": "the name is taken; The request does not match its contract",
		"413": "too many items; The request body is larger than 1048576 bytes (Limits.MaxBodyBytes)",
		"500": "the store is down; A defect in the server, such as an error no failure row matches, output that cannot be encoded, or a panic",
		"504": "the store is slow; The deadline passed before a response",
	} {
		if got := at(t, m, "paths", "/items", "post", "responses", status, "description"); got != want {
			t.Errorf("%s: %q, want %q", status, got, want)
		}
	}
}

// A failure row and a middleware that answer the same status are two
// causes, and the document states both, the author's first.
func TestRowAndMiddlewareOnOneStatusAreBothDocumented(t *testing.T) {
	var calls atomic.Int64
	row := geta.On(errNotFound, http.StatusForbidden, "a role is set elsewhere")
	c := getatest.New(t, scopeTable(&calls, geta.Scope{scopeAdmin()}, row))
	m := doc(t, c.App())
	if got := at(t, m, "paths", "/items", "post", "responses", "403", "description"); got != "a role is set elsewhere; Not an administrator" {
		t.Fatalf("403: %q", got)
	}
	root := c.Bearer("root")
	if res := root.Post("/items", map[string]any{"name": "fail"}); res.Status != http.StatusForbidden || res.Problem().Detail != "a role is set elsewhere" {
		t.Fatalf("the row: %d %s", res.Status, res.Body)
	}
}
