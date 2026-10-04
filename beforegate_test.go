package geta_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

func gate() geta.Middleware {
	return geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer}, Verifiers: map[string]geta.Verifier{"bearer": bearerVerifier}})
}

// limit is an application's rate limiter, as geta sees one: at OrderShed,
// it admits the first n requests and answers every later one 429 with a
// Retry-After, both declared, so the document and CORS carry them.
func limit(n int) geta.Middleware {
	var mu sync.Mutex
	seen := 0
	return geta.Ordered(geta.OrderShed, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen++
			admitted := seen <= n
			mu.Unlock()
			if !admitted {
				w.Header().Set("Retry-After", "60")
				geta.WriteProblem(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}).Answers(http.StatusTooManyRequests, "Rate limit exceeded").
		Header(http.StatusTooManyRequests, "Retry-After", "The whole seconds until the rate limit admits a request", geta.HeaderOf[int]("minimum=1"))
}

// once admits one request.
func once() geta.Scope {
	return geta.Scope{limit(1)}
}

func limitedTable(root geta.Scope) geta.Table {
	return geta.Table{Root: root, Routes: []geta.Entry{
		{Path: "/login", Route: geta.Route{Post: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{}, BeforeGate: once()})}},
		{Path: "/secret", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{BeforeGate: once()})}},
		{Path: "/me", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{})}},
	}}
}

// An operation's Doc.BeforeGate runs before the root scope's gate, for that
// operation alone: a login is limited, a protected operation is limited
// before its credentials are checked (a stranger's second request is a
// 429, not a 401), and every other operation is neither.
func TestBeforeGateRunsBeforeAuthentication(t *testing.T) {
	c := getatest.New(t, limitedTable(geta.Scope{geta.Recover(quietLogger()), gate()}))
	if res := c.Post("/login", nil); res.Status != 200 {
		t.Fatal(res.Status, res.Text())
	}
	if res := c.Post("/login", nil); res.Status != http.StatusTooManyRequests || res.Header.Get("Retry-After") == "" {
		t.Fatal(res.Status, res.Header)
	}
	if res := c.Get("/secret"); res.Status != http.StatusUnauthorized {
		t.Fatal(res.Status)
	}
	if res := c.Get("/secret"); res.Status != http.StatusTooManyRequests {
		t.Fatal(res.Status)
	}
	for range 3 {
		if res := c.Bearer("ok").Get("/me"); res.Status != 200 {
			t.Fatal(res.Status)
		}
	}
}

// What a Doc.BeforeGate answers is documented on its operation alone.
func TestBeforeGateAnswersAreDocumentedWhereTheyApply(t *testing.T) {
	m := doc(t, accepts(t, limitedTable(geta.Scope{gate()})))
	for _, op := range []string{"/login post", "/secret get", "/me get"} {
		path, method, _ := strings.Cut(op, " ")
		_, has := at(t, m, "paths", path, method, "responses").(map[string]any)["429"]
		if has != (path != "/me") {
			t.Errorf("%s lists 429: %v", op, has)
		}
	}
	if _, has := at(t, m, "paths", "/login", "options", "responses").(map[string]any)["429"]; has {
		t.Error("OPTIONS /login lists 429")
	}
}

// A Doc.BeforeGate joins the chain where its gate is: in a directory's
// scope when the gate is there, after the root scope when there is none;
// its order is checked against the scope it joins, and a gate in it is
// refused.
func TestBeforeGateIsPlacedAndChecked(t *testing.T) {
	tbl := geta.Table{Routes: []geta.Entry{
		{Path: "/secret", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{BeforeGate: once()})}, Scopes: []geta.Scope{{gate()}}},
		{Path: "/open", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{BeforeGate: once()})}},
	}}
	c := getatest.New(t, tbl)
	if res := c.Get("/secret"); res.Status != http.StatusUnauthorized {
		t.Fatal(res.Status)
	}
	if res := c.Get("/secret"); res.Status != http.StatusTooManyRequests {
		t.Fatal(res.Status)
	}
	if res := c.Get("/open"); res.Status != 200 {
		t.Fatal(res.Status)
	}
	if res := c.Get("/open"); res.Status != http.StatusTooManyRequests {
		t.Fatal(res.Status)
	}
	rejects(t, limitedTable(geta.Scope{geta.Timeout(time.Second), gate()}), "GET /secret: middleware shed(4000) runs after deadline(5000)")
	rejects(t, one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{BeforeGate: geta.Scope{gate()}})}), "Doc.BeforeGate: middleware 0 is a geta.Secure gate")
}

// A root middleware after the gate that moves a request to an operation
// whose BeforeGate did not run for it is a defect, as a move past the gate
// is: a 500, and nothing is served.
func TestBeforeGateCannotBeSkippedByARewrite(t *testing.T) {
	served := false
	rewrite := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/elsewhere" {
				r.URL.Path = "/login"
			}
			next.ServeHTTP(w, r)
		})
	})
	login := func(context.Context, *empty) (*ok, error) { served = true; return &ok{true}, nil }
	a, err := geta.New(geta.Table{Root: geta.Scope{gate(), rewrite}, Routes: []geta.Entry{
		{Path: "/login", Route: geta.Route{Get: geta.Op(http.StatusOK, login, geta.Doc{Security: []geta.Scheme{}, BeforeGate: once()})}},
		{Path: "/elsewhere", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{}})}},
	}}, geta.WithLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	if got := do(t, a, http.MethodGet, "/elsewhere").Code; got != 500 || served {
		t.Fatal(got, served)
	}
	if got := do(t, a, http.MethodGet, "/login").Code; got != 200 || !served {
		t.Fatal(got, served)
	}
}
