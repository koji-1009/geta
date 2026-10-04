package geta_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

var errNotFound = errors.New("store: not found")

type lookupErr struct{ id string }

func (e *lookupErr) Error() string { return "lookup " + e.id }

func TestNotFoundAndMethodNotAllowedAreProblems(t *testing.T) {
	c := getatest.New(t, one("/x", get(okHandler)))
	res := c.Get("/nope")
	if res.Status != 404 || res.Problem().Title != "Not Found" {
		t.Fatalf("404: %d %s", res.Status, res.Body)
	}
	res = c.Post("/x", nil)
	if res.Status != 405 || res.Header.Get("Allow") != "GET, HEAD, OPTIONS" || res.Problem().Status != 405 {
		t.Fatalf("405: %d %v %s", res.Status, res.Header, res.Body)
	}
	if res := c.Do(http.MethodHead, "/x", nil); res.Status != 200 || len(res.Body) != 0 {
		t.Fatalf("HEAD: %d %s", res.Status, res.Body)
	}
	if res := c.Get("/x/"); res.Status != 404 {
		t.Fatalf("trailing slash: %d", res.Status)
	}
}

func TestRootURL(t *testing.T) {
	c := getatest.New(t, one("/", get(okHandler)))
	if res := c.Get("/"); res.Status != 200 {
		t.Fatalf("/: %d", res.Status)
	}
	if res := c.Get("/other"); res.Status != 404 {
		t.Fatalf("/other: %d", res.Status)
	}
}

// The root scope wraps the dispatch: it sees 404 and 405, and it can read
// what the URL matched. A directory scope wraps only its routes.
func TestScopesAndMatch(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	record := func(tag string) geta.Middleware {
		return geta.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				m, ok := geta.Matched(r.Context())
				mu.Lock()
				seen = append(seen, fmt.Sprintf("%s %s %v %q %q", tag, r.URL.Path, ok, m.Template, m.Doc.Summary))
				mu.Unlock()
				next.ServeHTTP(w, r)
			})
		})
	}
	tbl := geta.Table{
		Root: geta.Scope{record("root")},
		Routes: []geta.Entry{{
			Path:   "/users/{id}",
			Route:  geta.Route{Get: geta.Op(http.StatusOK, idHandler, geta.Doc{Summary: "Fetch"})},
			Scopes: []geta.Scope{{record("users")}, {record("id")}},
		}},
	}
	c := getatest.New(t, tbl)
	c.Get("/users/7")
	c.Get("/nope")
	c.Delete("/users/7")
	want := []string{
		`root /users/7 true "/users/{id}" "Fetch"`,
		`users /users/7 true "/users/{id}" "Fetch"`,
		`id /users/7 true "/users/{id}" "Fetch"`,
		`root /nope false "" ""`,
		`root /users/7 false "" ""`,
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
	}
}

func failing(err error, rows ...geta.Failure) geta.Table {
	h := func(ctx context.Context, _ *empty) (*ok, error) { return nil, err }
	return one("/x", geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{Failures: rows})})
}

func TestWrappedErrorMeetsItsRow(t *testing.T) {
	err := fmt.Errorf("find user 7: %w", errNotFound)
	c := getatest.New(t, failing(err, geta.On(errNotFound, 404, "user not found")))
	res := c.Get("/x")
	p := res.Problem()
	if res.Status != 404 || p.Detail != "user not found" || p.Type != "about:blank" || strings.Contains(res.Text(), "find user") {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
}

func TestEmptyDetailSendsOnlyTheStatusText(t *testing.T) {
	c := getatest.New(t, failing(errNotFound, geta.On(errNotFound, 404, "")))
	res := c.Get("/x")
	if p := res.Problem(); p.Detail != "" || p.Title != "Not Found" {
		t.Fatalf("%s", res.Body)
	}
	if strings.Contains(res.Text(), "store") {
		t.Fatalf("the error text leaked: %s", res.Body)
	}
}

func TestUndeclaredErrorIsA500AndIsLogged(t *testing.T) {
	log, buf := logger()
	app, err := geta.New(failing(fmt.Errorf("query: %w", errNotFound)), geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	res := getatest.Serve(t, app).Get("/x")
	if res.Status != 500 || res.Problem().Detail != "" || strings.Contains(res.Text(), "query") {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
	if !strings.Contains(buf.String(), "query: store: not found") || !strings.Contains(buf.String(), "route=/x") {
		t.Fatalf("log: %s", buf.String())
	}
}

func TestRowsAreCheckedTopToBottom(t *testing.T) {
	err := fmt.Errorf("%w", &lookupErr{"7"})
	c := getatest.New(t, failing(err,
		geta.On(errNotFound, 404, "first"),
		geta.OnAs[*lookupErr](422, "second"),
		geta.OnAs[*lookupErr](409, "third"),
	))
	res := c.Get("/x")
	if res.Status != 422 || res.Problem().Detail != "second" {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
}

func TestDeadlineExceededIs504(t *testing.T) {
	c := getatest.New(t, failing(fmt.Errorf("query: %w", context.DeadlineExceeded)))
	if res := c.Get("/x"); res.Status != 504 {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
}

func TestClientCancellationWritesNothing(t *testing.T) {
	log, buf := logger()
	app, err := geta.New(failing(context.Canceled), geta.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, "GET", "/x", nil))
	if rec.Body.Len() != 0 || len(rec.Header()) != 0 {
		t.Fatalf("wrote %v %q", rec.Header(), rec.Body)
	}
	if !strings.Contains(buf.String(), "client went away") {
		t.Fatalf("log: %s", buf.String())
	}
}

func TestNilOutputIsADefect(t *testing.T) {
	h := func(ctx context.Context, _ *empty) (*ok, error) { return nil, nil }
	c := getatest.New(t, one("/x", get(h)))
	if res := c.Get("/x"); res.Status != 500 {
		t.Fatalf("%d", res.Status)
	}
}

func TestRowsMayBeSharedAsSlices(t *testing.T) {
	common := []geta.Failure{geta.On(errNotFound, 404, "missing")}
	errGone := errors.New("gone")
	h := func(ctx context.Context, _ *empty) (*ok, error) { return nil, errGone }
	r := geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{
		Failures: append(append([]geta.Failure(nil), common...), geta.On(errGone, 410, "")),
	})}
	if res := getatest.New(t, one("/x", r)).Get("/x"); res.Status != 410 {
		t.Fatalf("%d", res.Status)
	}
}
