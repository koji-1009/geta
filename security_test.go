package geta_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
)

type who struct {
	ID string `json:"id"`
}

var caller = geta.NewKey[who]("caller")

func bearerVerifier(r *http.Request) (context.Context, error) {
	switch r.Header.Get("Authorization") {
	case "Bearer ok":
		return caller.With(r.Context(), who{"ada"}), nil
	case "Bearer down":
		return nil, fmt.Errorf("fetch keys: %w", geta.ErrUnavailable)
	case "Bearer bug":
		return nil, errors.New("verifier bug")
	}
	return nil, geta.ErrUnauthenticated
}

var apiKey = geta.APIKeyHeader("apiKey", "X-API-Key")

func apiKeyVerifier(r *http.Request) (context.Context, error) {
	if r.Header.Get("X-API-Key") == "k" {
		return nil, nil
	}
	return nil, geta.ErrUnauthenticated
}

func whoami(ctx context.Context, _ *empty) (*who, error) {
	w, _ := caller.Value(ctx)
	return &w, nil
}

func secured(def []geta.Scheme, routes ...geta.Entry) *geta.App {
	tbl := geta.Table{
		Root: geta.Scope{geta.Secure(geta.Policy{
			Default:   def,
			Verifiers: map[string]geta.Verifier{"bearer": bearerVerifier, "apiKey": apiKeyVerifier},
		})},
		Routes: routes,
	}
	a, err := geta.New(tbl)
	if err != nil {
		panic(err)
	}
	return a
}

func route(path string, sec []geta.Scheme) geta.Entry {
	return geta.Entry{Path: path, Route: geta.Route{Get: geta.Op(http.StatusOK, whoami, geta.Doc{Security: sec})}}
}

func TestPublicByDefaultWhenTheDefaultIsEmpty(t *testing.T) {
	a := secured(nil, route("/x", nil))
	if c := do(t, a, "GET", "/x").Code; c != 200 {
		t.Fatal(c)
	}
}

func TestDeclaredSchemeRejectsThenAdmits(t *testing.T) {
	a := secured(nil, route("/x", []geta.Scheme{geta.Bearer}))
	r := do(t, a, "GET", "/x")
	if r.Code != 401 || r.Header().Get("WWW-Authenticate") != "Bearer" || r.Header().Get("Content-Type") != geta.ProblemContentType {
		t.Fatal(r.Code, r.Header())
	}
	r = do(t, a, "GET", "/x", "Authorization", "Bearer ok")
	if r.Code != 200 || r.Body.String() != `{"id":"ada"}` {
		t.Fatal(r.Code, r.Body)
	}
}

func TestExplicitPublicOverridesTheDefault(t *testing.T) {
	a := secured([]geta.Scheme{geta.Bearer}, route("/x", []geta.Scheme{}), route("/y", nil))
	if c := do(t, a, "GET", "/x").Code; c != 200 {
		t.Fatal(c)
	}
	if c := do(t, a, "GET", "/y").Code; c != 401 {
		t.Fatal("the default did not apply:", c)
	}
}

// With a non-empty default, an unknown URL answers 401 to a stranger: the
// API does not say which of its URLs are real.
func TestTheDefaultCoversUnmatchedURLs(t *testing.T) {
	a := secured([]geta.Scheme{geta.Bearer}, route("/x", nil))
	if c := do(t, a, "GET", "/nope").Code; c != 401 {
		t.Fatal(c)
	}
	if c := do(t, a, "GET", "/nope", "Authorization", "Bearer ok").Code; c != 404 {
		t.Fatal(c)
	}
	if c := do(t, a, "POST", "/x").Code; c != 401 {
		t.Fatal("405 before authentication:", c)
	}
}

func TestSchemesAreAlternatives(t *testing.T) {
	a := secured(nil, route("/x", []geta.Scheme{apiKey, geta.Bearer}))
	for _, h := range [][]string{{"Authorization", "Bearer ok"}, {"X-API-Key", "k"}} {
		if c := do(t, a, "GET", "/x", h...).Code; c != 200 {
			t.Errorf("%v: %d", h, c)
		}
	}
	if c := do(t, a, "GET", "/x", "X-API-Key", "wrong").Code; c != 401 {
		t.Fatal(c)
	}
}

func TestUnavailableIs503AndADefectIs500(t *testing.T) {
	log, buf := logger()
	tbl := geta.Table{
		Root:   geta.Scope{geta.Secure(geta.Policy{Verifiers: map[string]geta.Verifier{"bearer": bearerVerifier}})},
		Routes: []geta.Entry{route("/x", []geta.Scheme{geta.Bearer})},
	}
	a, err := geta.New(tbl, geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	if c := do(t, a, "GET", "/x", "Authorization", "Bearer down").Code; c != 503 {
		t.Fatal(c)
	}
	r := do(t, a, "GET", "/x", "Authorization", "Bearer bug")
	if r.Code != 500 || strings.Contains(r.Body.String(), "verifier bug") || !strings.Contains(buf.String(), "verifier bug") {
		t.Fatal(r.Code, r.Body, buf.String())
	}
}

// A verifier's defect is logged with the operation's template, never the raw
// path.
func TestAVerifierDefectLogsTheTemplateNeverThePath(t *testing.T) {
	log, buf := logger()
	h := func(context.Context, *idIn) (*ok, error) { return &ok{true}, nil }
	tbl := geta.Table{
		Root:   geta.Scope{geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer}, Verifiers: map[string]geta.Verifier{"bearer": bearerVerifier}})},
		Routes: []geta.Entry{{Path: "/items/{id}", Route: get(h)}},
	}
	a, err := geta.New(tbl, geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	if c := do(t, a, "GET", "/items/424242", "Authorization", "Bearer bug").Code; c != 500 {
		t.Fatal(c)
	}
	if l := buf.String(); !strings.Contains(l, "route=/items/{id}") || strings.Contains(l, "path=") || strings.Contains(l, "424242") {
		t.Fatalf("log: %s", l)
	}
}

// Authorization is ordinary middleware; it declares its 403 so the document
// lists it.
func TestAuthorizationIsOrdinaryMiddleware(t *testing.T) {
	gate := geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer}, Verifiers: map[string]geta.Verifier{"bearer": bearerVerifier}})
	forbid := geta.Ordered(geta.OrderAuthorize, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if u, ok := caller.Value(r.Context()); !ok || u.ID != "root" {
				geta.WriteProblem(w, http.StatusForbidden, "admin only")
				return
			}
			next.ServeHTTP(w, r)
		})
	}).Answers(http.StatusForbidden, "Not an administrator")
	tbl := geta.Table{Root: geta.Scope{gate}, Routes: []geta.Entry{route("/admin", nil)}}
	tbl.Routes[0].Scopes = []geta.Scope{{forbid}}
	a := accepts(t, tbl)
	if c := do(t, a, "GET", "/admin").Code; c != 401 {
		t.Fatal(c)
	}
	if c := do(t, a, "GET", "/admin", "Authorization", "Bearer ok").Code; c != 403 {
		t.Fatal(c)
	}
	m := doc(t, a)
	if at(t, m, "paths", "/admin", "get", "responses", "403", "description") != "Not an administrator" {
		t.Fatal(m)
	}
}

// The gate keeps what it was given: changing the policy, the Doc, or a
// Matched Doc afterwards changes no decision.
func TestChangesAfterNewChangeNoDecision(t *testing.T) {
	sec := []geta.Scheme{geta.Bearer}
	verifiers := map[string]geta.Verifier{"bearer": bearerVerifier, "apiKey": apiKeyVerifier}
	def := []geta.Scheme{geta.Bearer}
	rewrite := func(ctx context.Context, _ *empty) (*who, error) {
		if m, ok := geta.Matched(ctx); ok {
			m.Doc.Security[0] = apiKey
		}
		return &who{}, nil
	}
	a, err := geta.New(geta.Table{
		Root: geta.Scope{geta.Secure(geta.Policy{Default: def, Verifiers: verifiers})},
		Routes: []geta.Entry{
			{Path: "/x", Route: geta.Route{Get: geta.Op(http.StatusOK, rewrite, geta.Doc{Security: sec})}},
			{Path: "/d", Route: geta.Route{Get: geta.Op(http.StatusOK, whoami, geta.Doc{})}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sec[0] = apiKey
	def[0] = apiKey
	delete(verifiers, "bearer")
	for range 2 {
		if c := do(t, a, "GET", "/x", "Authorization", "Bearer ok").Code; c != 200 {
			t.Fatal(c)
		}
		if c := do(t, a, "GET", "/x", "X-API-Key", "k").Code; c != 401 {
			t.Fatal(c)
		}
		if c := do(t, a, "GET", "/d", "Authorization", "Bearer ok").Code; c != 200 {
			t.Fatal(c)
		}
	}
}

// Secure keeps its schemes' flows: a scope added to the map the caller
// passed, after Secure returned, reaches neither New nor the document.
func TestSecureKeepsItsFlows(t *testing.T) {
	scopes := map[string]string{"read": "Read"}
	s := geta.Scheme{Name: "o", Type: "oauth2", Flows: &geta.OAuthFlows{ClientCredentials: &geta.OAuthFlow{TokenURL: "https://id.example/token", Scopes: scopes}}}
	gate := geta.Secure(geta.Policy{Default: []geta.Scheme{s}, Verifiers: map[string]geta.Verifier{"o": apiKeyVerifier}})
	scopes["write"] = "Write"
	a, err := geta.New(geta.Table{Root: geta.Scope{gate}, Routes: []geta.Entry{route("/x", nil)}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(a.OpenAPI()), `"write"`) {
		t.Fatal("the document lists a scope added after Secure returned")
	}
}

// Two gates must both admit, and with no Doc.Security each checks its own
// default.
func TestTwoGatesEachRequireTheirDefault(t *testing.T) {
	a := geta.APIKeyHeader("a", "X-A")
	c := geta.APIKeyHeader("c", "X-C")
	header := func(name string) geta.Verifier {
		return func(r *http.Request) (context.Context, error) {
			if r.Header.Get(name) == "good" {
				return nil, nil
			}
			return nil, geta.ErrUnauthenticated
		}
	}
	outer := geta.Secure(geta.Policy{Default: []geta.Scheme{a}, Verifiers: map[string]geta.Verifier{"a": header("X-A")}})
	inner := geta.Secure(geta.Policy{Default: []geta.Scheme{c}, Verifiers: map[string]geta.Verifier{"c": header("X-C")}})
	app, err := geta.New(one("/x", get(okHandler), geta.Scope{outer}, geta.Scope{inner}), geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Security []map[string][]string `json:"security"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(app.OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	if sec := doc.Paths["/x"]["get"].Security; len(sec) != 1 || len(sec[0]) != 2 || sec[0]["a"] == nil || sec[0]["c"] == nil {
		t.Fatalf("documented security %v, want one requirement naming a and c", sec)
	}
	for _, tc := range []struct {
		headers []string
		want    int
	}{
		{[]string{"X-A"}, 401},
		{[]string{"X-C"}, 401},
		{[]string{"X-A", "X-C"}, 200},
	} {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		for _, h := range tc.headers {
			req.Header.Set(h, "good")
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%v: %d, want %d", tc.headers, rec.Code, tc.want)
		}
	}
}
