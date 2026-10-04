package geta_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/koji-1009/geta"
)

// Middleware that rewrites the request or replaces its context, and what
// dispatch, the gates, and the handler see of it.

// bearerGate is a gate whose default is bearer, admitting "Bearer good".
func bearerGate() geta.Middleware {
	return geta.Secure(geta.Policy{
		Default: []geta.Scheme{geta.Bearer},
		Verifiers: map[string]geta.Verifier{"bearer": func(r *http.Request) (context.Context, error) {
			if r.Header.Get("Authorization") == "Bearer good" {
				return nil, nil
			}
			return nil, geta.ErrUnauthenticated
		}},
	})
}

// methodOverride turns a POST into the method its X-HTTP-Method-Override names.
var methodOverride = geta.Use(func(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m := r.Header.Get("X-HTTP-Method-Override"); m != "" && r.Method == http.MethodPost {
			r = r.Clone(r.Context())
			r.Method = m
		}
		next.ServeHTTP(w, r)
	})
})

// pathMove rewrites the path to the one X-Move names.
var pathMove = geta.Use(func(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if to := r.Header.Get("X-Move"); to != "" {
			r = r.Clone(r.Context())
			r.URL.Path = to
		}
		next.ServeHTTP(w, r)
	})
})

// A root middleware before the gate that turns a public POST into a DELETE
// must not pass the POST's requirement off as the DELETE's, and the DELETE
// must read its own match.
func TestRewrittenMethodReachesTheGate(t *testing.T) {
	override := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if m := r.Header.Get("X-HTTP-Method-Override"); m != "" && r.Method == http.MethodPost {
				r = r.Clone(r.Context())
				r.Method = m
			}
			next.ServeHTTP(w, r)
		})
	})
	gate := geta.Secure(geta.Policy{
		Default: []geta.Scheme{geta.Bearer},
		Verifiers: map[string]geta.Verifier{"bearer": func(r *http.Request) (context.Context, error) {
			if r.Header.Get("Authorization") == "Bearer good" {
				return nil, nil
			}
			return nil, geta.ErrUnauthenticated
		}},
	})
	var deleted atomic.Bool
	var seen atomic.Value
	app, err := geta.New(geta.Table{
		Root: geta.Scope{override, gate},
		Routes: []geta.Entry{{Path: "/items", Route: geta.Route{
			Post: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{}}),
			Delete: geta.OpNoBody(http.StatusNoContent, func(ctx context.Context, _ *empty) error {
				m, _ := geta.Matched(ctx)
				seen.Store(m.Method + " " + m.Template)
				deleted.Store(true)
				return nil
			}, geta.Doc{}),
		}}},
	}, geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/items", nil)
	req.Header.Set("X-HTTP-Method-Override", "DELETE")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || deleted.Load() {
		t.Fatalf("POST overridden to DELETE without credentials: %d, delete ran=%v; want 401", rec.Code, deleted.Load())
	}
	req = httptest.NewRequest(http.MethodPost, "/items", nil)
	req.Header.Set("X-HTTP-Method-Override", "DELETE")
	req.Header.Set("Authorization", "Bearer good")
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || seen.Load() != "DELETE /items" {
		t.Fatalf("with credentials: %d, Matched in DELETE = %v; want 204, DELETE /items", rec.Code, seen.Load())
	}
}

// A root middleware that rewrites the path: the handler, and the root
// middleware outside the rewrite once dispatch is done, read the operation
// that was served.
func TestRewrittenPathMovesTheMatch(t *testing.T) {
	var after atomic.Value
	observe := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			m, _ := geta.Matched(r.Context())
			after.Store(m.Template)
		})
	})
	rewrite := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/old" {
				r = r.Clone(r.Context())
				r.URL.Path = "/new"
			}
			next.ServeHTTP(w, r)
		})
	})
	var inside atomic.Value
	h := func(ctx context.Context, _ *empty) (*ok, error) {
		m, _ := geta.Matched(ctx)
		inside.Store(m.Template)
		return &ok{true}, nil
	}
	app, err := geta.New(geta.Table{
		Root: geta.Scope{observe, rewrite},
		Routes: []geta.Entry{
			{Path: "/old", Route: get(okHandler)},
			{Path: "/new", Route: get(h)},
		},
	}, geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/old", nil))
	if rec.Code != 200 || inside.Load() != "/new" || after.Load() != "/new" {
		t.Fatalf("%d, inside %v, after %v; want 200, /new, /new", rec.Code, inside.Load(), after.Load())
	}
}

// A root middleware after the gate that turns a public POST into a protected
// DELETE must not reach the DELETE: the gate checked the POST's requirement,
// which is nothing. Dispatch answers 500 as a defect and runs no handler,
// with or without credentials.
func TestRewriteAfterTheGateIsRefused(t *testing.T) {
	var logs bytes.Buffer
	var deleted atomic.Bool
	app, err := geta.New(geta.Table{
		Root: geta.Scope{bearerGate(), methodOverride},
		Routes: []geta.Entry{{Path: "/items", Route: geta.Route{
			Post: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{}}),
			Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error {
				deleted.Store(true)
				return nil
			}, geta.Doc{}),
		}}},
	}, geta.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	if err != nil {
		t.Fatal(err)
	}
	for _, auth := range []string{"", "Bearer good"} {
		rec := do(t, app, http.MethodPost, "/items", "X-HTTP-Method-Override", "DELETE", "Authorization", auth)
		if rec.Code != http.StatusInternalServerError || deleted.Load() {
			t.Fatalf("auth %q: %d, delete ran=%v; want 500 and no delete", auth, rec.Code, deleted.Load())
		}
	}
	if !strings.Contains(logs.String(), "rewrote the request after a Secure gate") ||
		!strings.Contains(logs.String(), "POST /items") || !strings.Contains(logs.String(), "DELETE /items") {
		t.Fatalf("log does not name the rewrite: %s", logs.String())
	}
	// Without a rewrite, nothing changes.
	if rec := do(t, app, http.MethodPost, "/items"); rec.Code != http.StatusOK {
		t.Fatalf("plain POST: %d", rec.Code)
	}
	if rec := do(t, app, http.MethodDelete, "/items"); rec.Code != http.StatusUnauthorized || deleted.Load() {
		t.Fatalf("DELETE without credentials: %d", rec.Code)
	}
	if rec := do(t, app, http.MethodDelete, "/items", "Authorization", "Bearer good"); rec.Code != http.StatusNoContent || !deleted.Load() {
		t.Fatalf("DELETE with credentials: %d", rec.Code)
	}
}

// A path rewrite after the gate: to a protected operation from a public one
// or from no operation is refused; to a public operation, or to no operation,
// is served as usual.
func TestRewrittenPathAfterTheGate(t *testing.T) {
	var ran atomic.Bool
	secret := func(context.Context, *empty) (*ok, error) {
		ran.Store(true)
		return &ok{true}, nil
	}
	app, err := geta.New(geta.Table{
		Root: geta.Scope{bearerGate(), pathMove},
		Routes: []geta.Entry{
			{Path: "/secret", Route: get(secret)},
			{Path: "/open", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{}})}},
		},
	}, geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path, move, auth string
		want             int
	}{
		{"/open", "/secret", "", http.StatusInternalServerError},
		{"/open", "/secret", "Bearer good", http.StatusInternalServerError},
		{"/nowhere", "/secret", "Bearer good", http.StatusInternalServerError},
		{"/secret", "/open", "Bearer good", http.StatusOK},
		{"/secret", "/nowhere", "Bearer good", http.StatusNotFound},
		{"/secret", "/secret", "Bearer good", http.StatusOK}, // the same operation
	}
	for _, c := range cases {
		ran.Store(false)
		rec := do(t, app, http.MethodGet, c.path, "X-Move", c.move, "Authorization", c.auth)
		if rec.Code != c.want {
			t.Errorf("%s moved to %s, auth %q: %d; want %d", c.path, c.move, c.auth, rec.Code, c.want)
		}
		if rec.Code == http.StatusInternalServerError && ran.Load() {
			t.Errorf("%s moved to %s: the protected handler ran", c.path, c.move)
		}
	}
}

// A rewrite before the gate is the gate's to check, as before.
func TestRewriteBeforeTheGateStillChecks(t *testing.T) {
	app, err := geta.New(geta.Table{
		Root: geta.Scope{methodOverride, bearerGate()},
		Routes: []geta.Entry{{Path: "/items", Route: geta.Route{
			Post:   geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{}}),
			Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error { return nil }, geta.Doc{}),
		}}},
	}, geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	if rec := do(t, app, http.MethodPost, "/items", "X-HTTP-Method-Override", "DELETE"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without credentials: %d; want 401", rec.Code)
	}
	if rec := do(t, app, http.MethodPost, "/items", "X-HTTP-Method-Override", "DELETE", "Authorization", "Bearer good"); rec.Code != http.StatusNoContent {
		t.Fatalf("with credentials: %d; want 204", rec.Code)
	}
}

// The 500 a refused rewrite answers is in every operation's document.
func TestRefusedRewriteStatusIsDocumented(t *testing.T) {
	app, err := geta.New(geta.Table{
		Root:   geta.Scope{bearerGate(), methodOverride},
		Routes: []geta.Entry{{Path: "/items", Route: geta.Route{Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error { return nil }, geta.Doc{})}}},
	}, geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	if documented, matched := app.Documented(geta.Match{Template: "/items", Method: http.MethodDelete, Operation: true}, http.StatusInternalServerError); !documented || !matched {
		t.Fatalf("500 documented=%v matched=%v", documented, matched)
	}
}

// matchedOut reports what the handler saw as the match.
type matchedOut struct {
	Method   string `json:"method"`
	Template string `json:"template"`
}

func reportMatched(ran *atomic.Bool) func(context.Context, *empty) (*matchedOut, error) {
	return func(ctx context.Context, _ *empty) (*matchedOut, error) {
		ran.Store(true)
		mt, ok := geta.Matched(ctx)
		if !ok {
			return &matchedOut{}, nil
		}
		return &matchedOut{Method: mt.Method, Template: mt.Template}, nil
	}
}

// Once dispatch has chosen the operation, it is fixed: a middleware in the
// operation's own scopes that changes the method does not make the gate after
// it check another operation's requirement, and the handler's match is still
// the operation served.
func TestRewriteInsideTheOperationDoesNotMoveTheGate(t *testing.T) {
	var posted, deleted atomic.Bool
	app, err := geta.New(geta.Table{
		Routes: []geta.Entry{{
			Path: "/items",
			Route: geta.Route{
				Post:   geta.Op(http.StatusOK, reportMatched(&posted), geta.Doc{}),
				Delete: geta.Op(http.StatusOK, reportMatched(&deleted), geta.Doc{Security: []geta.Scheme{}}),
			},
			Scopes: []geta.Scope{{methodOverride, bearerGate()}},
		}},
	}, geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, app, http.MethodPost, "/items", "X-HTTP-Method-Override", "DELETE")
	if rec.Code != http.StatusUnauthorized || posted.Load() || deleted.Load() {
		t.Fatalf("POST overridden to DELETE without credentials: %d %s, post ran=%v, delete ran=%v; want 401 and no handler",
			rec.Code, rec.Body, posted.Load(), deleted.Load())
	}
	rec = do(t, app, http.MethodPost, "/items", "X-HTTP-Method-Override", "DELETE", "Authorization", "Bearer good")
	if rec.Code != http.StatusOK || !posted.Load() || deleted.Load() {
		t.Fatalf("with credentials: %d, post ran=%v, delete ran=%v; want 200 from the POST", rec.Code, posted.Load(), deleted.Load())
	}
	if got := rec.Body.String(); got != `{"method":"POST","template":"/items"}`+"\n" && got != `{"method":"POST","template":"/items"}` {
		t.Fatalf("handler saw %s; want the POST", got)
	}
}

// detachedGate is a gate whose verifier returns a context that does not derive
// from the request's.
func detachedGate() geta.Middleware {
	return geta.Secure(geta.Policy{
		Default: []geta.Scheme{geta.Bearer},
		Verifiers: map[string]geta.Verifier{"bearer": func(r *http.Request) (context.Context, error) {
			if r.Header.Get("Authorization") == "Bearer good" {
				return context.WithValue(context.Background(), detachedKey{}, "principal"), nil
			}
			return nil, geta.ErrUnauthenticated
		}},
	})
}

type detachedKey struct{}

// A verifier's context that does not derive from the request's keeps geta's
// request context: the handler still reads the match and the verifier's
// value.
func TestDetachedVerifierContextKeepsTheMatch(t *testing.T) {
	var principal atomic.Value
	var ran atomic.Bool
	h := func(ctx context.Context, in *empty) (*matchedOut, error) {
		if v, ok := ctx.Value(detachedKey{}).(string); ok {
			principal.Store(v)
		}
		return reportMatched(&ran)(ctx, in)
	}
	app, err := geta.New(geta.Table{
		Root:   geta.Scope{detachedGate()},
		Routes: []geta.Entry{{Path: "/items", Route: geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{})}}},
	}, geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, app, http.MethodGet, "/items", "Authorization", "Bearer good")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != `{"method":"GET","template":"/items"}`+"\n" && got != `{"method":"GET","template":"/items"}` {
		t.Fatalf("handler saw %s; want GET /items", got)
	}
	if principal.Load() != "principal" {
		t.Fatalf("the verifier's value is lost: %v", principal.Load())
	}
}

// A rewrite after a gate whose verifier detaches the context is still refused.
func TestRewriteAfterADetachingGateIsRefused(t *testing.T) {
	var deleted atomic.Bool
	app, err := geta.New(geta.Table{
		Root: geta.Scope{detachedGate(), methodOverride},
		Routes: []geta.Entry{{Path: "/items", Route: geta.Route{
			Post: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{geta.Bearer}}),
			Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error {
				deleted.Store(true)
				return nil
			}, geta.Doc{Security: []geta.Scheme{geta.Bearer}}),
		}}},
	}, geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	// The gate admits the POST; the override after it makes it a DELETE,
	// whose requirement is the same scheme but not the operation checked.
	rec := do(t, app, http.MethodPost, "/items", "X-HTTP-Method-Override", "DELETE", "Authorization", "Bearer good")
	if rec.Code != http.StatusInternalServerError || deleted.Load() {
		t.Fatalf("rewrite after a detaching gate: %d, delete ran=%v; want 500 and no delete", rec.Code, deleted.Load())
	}
}

type detachKey struct{}

// detachContext passes the request on with a context that does not derive from
// the request's, as a middleware that builds its context from scratch does.
var detachContext = geta.Use(func(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(context.Background(), detachKey{}, "detached")))
	})
})

// A root middleware that detaches the context between the gate and dispatch
// takes away what the gate checked: dispatch must not serve the protected
// DELETE a later rewrite reaches, nor anything else, without it.
func TestADetachedContextIsRefused(t *testing.T) {
	var logs bytes.Buffer
	var deleted atomic.Bool
	app, err := geta.New(geta.Table{
		Root: geta.Scope{bearerGate(), detachContext, methodOverride},
		Routes: []geta.Entry{{Path: "/items", Route: geta.Route{
			Post: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{}}),
			Delete: geta.OpNoBody(http.StatusNoContent, func(context.Context, *empty) error {
				deleted.Store(true)
				return nil
			}, geta.Doc{}),
		}}},
	}, geta.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	if err != nil {
		t.Fatal(err)
	}
	for _, auth := range []string{"", "Bearer good"} {
		rec := do(t, app, http.MethodPost, "/items", "X-HTTP-Method-Override", "DELETE", "Authorization", auth)
		if rec.Code != http.StatusInternalServerError || deleted.Load() {
			t.Fatalf("auth %q: %d, delete ran=%v; want 500 and no delete", auth, rec.Code, deleted.Load())
		}
	}
	if rec := do(t, app, http.MethodPost, "/items"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("plain POST: %d; want 500", rec.Code)
	}
	if !strings.Contains(logs.String(), "replaced the request context") {
		t.Fatalf("log does not name the defect: %s", logs.String())
	}
	// The match is gone with the context, so the line carries the route
	// placeholder, and never the raw path.
	if l := logs.String(); !strings.Contains(l, "route="+geta.UnmatchedRoute) || strings.Contains(l, "path=") || strings.Contains(l, "/items") {
		t.Fatalf("log: %s", l)
	}
}

// An operation never runs without its match: the handler of a request whose
// context a root middleware detached is not reached, and the middleware
// outside the detaching one still reads the match.
func TestADetachedContextNeverServesWithoutTheMatch(t *testing.T) {
	var ran, handlerMatched atomic.Bool
	var outer geta.Match
	var outerOK bool
	observe := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			outer, outerOK = geta.Matched(r.Context())
		})
	})
	h := func(ctx context.Context, _ *empty) (*ok, error) {
		ran.Store(true)
		_, matched := geta.Matched(ctx)
		handlerMatched.Store(matched)
		return &ok{true}, nil
	}
	app, err := geta.New(geta.Table{
		Root:   geta.Scope{observe, detachContext},
		Routes: []geta.Entry{{Path: "/x", Route: get(h)}},
	}, geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, app, http.MethodGet, "/x")
	if rec.Code != http.StatusInternalServerError || ran.Load() {
		t.Fatalf("%d, handler ran=%v (matched=%v); want 500 and no handler", rec.Code, ran.Load(), handlerMatched.Load())
	}
	if !outerOK || outer.Template != "/x" || outer.Method != http.MethodGet {
		t.Fatalf("outer middleware: Matched = %+v, %v", outer, outerOK)
	}
}
