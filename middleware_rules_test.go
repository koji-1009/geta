package geta_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// trail records, per middleware, the template geta.Matched reports as the
// request passes it.
type trail struct {
	mu   sync.Mutex
	seen []string
}

func (tr *trail) mark(tag string) geta.Middleware {
	return geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m, ok := geta.Matched(r.Context())
			tr.mu.Lock()
			if ok {
				tr.seen = append(tr.seen, tag+"="+m.Template)
			} else {
				tr.seen = append(tr.seen, tag+"=none")
			}
			tr.mu.Unlock()
			next.ServeHTTP(w, r)
		})
	})
}

func (tr *trail) take() string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	s := strings.Join(tr.seen, ",")
	tr.seen = nil
	return s
}

// A zero or misbuilt middleware is refused in the root scope and in a
// Doc.BeforeGate, as anywhere.
func TestRejectsZeroAndInvalidMiddlewareEverywhere(t *testing.T) {
	rejects(t, withRoot(one("/x", get(okHandler)), geta.Middleware{}), "root scope: middleware 0 is the zero geta.Middleware")
	rejects(t, one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{BeforeGate: geta.Scope{geta.Middleware{}}})}),
		"GET /x: Doc.BeforeGate: middleware 0 is the zero geta.Middleware")
	rejects(t, one("/x", geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{BeforeGate: geta.Scope{geta.Invalid("mine", errors.New("bad argument"))}})}),
		"GET /x: Doc.BeforeGate: middleware 0 (mine): bad argument")
	rejects(t, withRoot(one("/x", get(okHandler)), geta.Invalid("mine", errors.New("bad argument"))), "root scope: middleware 0 (mine): bad argument")
}

// A request redirected to its clean path carries the match its clean path
// reaches, and the access log records that route with 308; the target's
// Doc.BeforeGate does not run for the redirect.
func TestARedirectCarriesTheMatchOfItsCleanPath(t *testing.T) {
	var tr trail
	log, buf := logger()
	tbl := limitedTable(geta.Scope{geta.AccessLog(log), tr.mark("root"), gate()})
	a := accepts(t, tbl)
	for range 2 {
		if r := do(t, a, "POST", "/./login"); r.Code != http.StatusPermanentRedirect || r.Header().Get("Location") != "/login" {
			t.Fatal(r.Code, r.Header())
		}
	}
	if got := tr.take(); got != "root=/login,root=/login" {
		t.Fatal(got)
	}
	if !strings.Contains(buf.String(), "method=POST route=/login status=308") {
		t.Fatal(buf.String())
	}
	// The limit of one a minute is untouched by the redirects.
	if r := do(t, a, "POST", "/login"); r.Code != 200 {
		t.Fatal(r.Code)
	}
	if r := do(t, a, "POST", "/login"); r.Code != http.StatusTooManyRequests {
		t.Fatal(r.Code)
	}
}

// Between a root rewrite and the next gate (or dispatch, with none), Matched
// still reports the match the request arrived with; from the next gate on it
// reports the operation the rewritten request reaches.
func TestMatchedFollowsARewriteFromTheNextGate(t *testing.T) {
	routes := []geta.Entry{{Path: "/a", Route: get(okHandler)}, {Path: "/b", Route: get(okHandler)}}
	var tr trail
	a := accepts(t, geta.Table{Root: geta.Scope{pathMove, tr.mark("before"), gate(), tr.mark("after")}, Routes: routes})
	if r := do(t, a, "GET", "/a", "X-Move", "/b", "Authorization", "Bearer ok"); r.Code != 200 {
		t.Fatal(r.Code)
	}
	if got := tr.take(); got != "before=/a,after=/b" {
		t.Fatal(got)
	}
	// With no gate, the match follows at dispatch: the root scope still reads
	// the arrival's, the directory scope the operation's.
	a = accepts(t, geta.Table{Root: geta.Scope{pathMove, tr.mark("root")}, Routes: []geta.Entry{
		{Path: "/a", Route: get(okHandler)},
		{Path: "/b", Route: get(okHandler), Scopes: []geta.Scope{{tr.mark("dir")}}},
	}})
	if r := do(t, a, "GET", "/a", "X-Move", "/b"); r.Code != 200 {
		t.Fatal(r.Code)
	}
	if got := tr.take(); got != "root=/a,dir=/b" {
		t.Fatal(got)
	}
}

// Two root gates with a rewrite between them read different operations: a
// request reaching an operation the root gates have a scheme to check for is
// a logged 500 naming "other operations"; one reaching a public operation is
// served.
func TestTwoRootGatesAroundARewrite(t *testing.T) {
	var logs syncBuffer
	a, err := geta.New(geta.Table{Root: geta.Scope{gate(), pathMove, gate()}, Routes: []geta.Entry{
		{Path: "/a", Route: get(okHandler)},
		{Path: "/b", Route: get(okHandler)},
		{Path: "/pub", Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{}})}},
	}}, geta.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path, move string
		want       int
	}{
		{"/a", "/b", 500},
		{"/pub", "/a", 500},
		{"/a", "/pub", 200},
		{"/a", "/a", 200},
	} {
		if r := do(t, a, "GET", c.path, "X-Move", c.move, "Authorization", "Bearer ok"); r.Code != c.want {
			t.Errorf("%s moved to %s: %d; want %d", c.path, c.move, r.Code, c.want)
		}
	}
	if !strings.Contains(logs.String(), "other operations") {
		t.Fatal(logs.String())
	}
}

// When the operation's first gate is in its Doc.Scope, its Doc.BeforeGate
// runs just before that gate, after the directory scopes.
func TestBeforeGateBesideAnOperationScopeGate(t *testing.T) {
	var tr trail
	a := accepts(t, geta.Table{Routes: []geta.Entry{{Path: "/x", Scopes: []geta.Scope{{tr.mark("dir")}},
		Route: geta.Route{Get: geta.Op(http.StatusOK, okHandler, geta.Doc{
			Scope:      geta.Scope{tr.mark("s1"), gate(), tr.mark("s2")},
			BeforeGate: geta.Scope{tr.mark("before")},
		})}}}})
	if r := do(t, a, "GET", "/x", "Authorization", "Bearer ok"); r.Code != 200 {
		t.Fatal(r.Code)
	}
	if got := tr.take(); got != "dir=/x,s1=/x,before=/x,s2=/x" {
		t.Fatal(got)
	}
	if r := do(t, a, "GET", "/x"); r.Code != 401 {
		t.Fatal(r.Code)
	}
	if got := tr.take(); got != "dir=/x,s1=/x,before=/x" {
		t.Fatal(got)
	}
}

// A root rewrite before the gate: the Doc.BeforeGate that runs is that of
// the operation the rewritten request reaches.
func TestBeforeGateFollowsARewriteBeforeTheGate(t *testing.T) {
	tbl := limitedTable(geta.Scope{pathMove, gate()})
	tbl.Routes = append(tbl.Routes, geta.Entry{Path: "/elsewhere", Route: geta.Route{Post: geta.Op(http.StatusOK, okHandler, geta.Doc{Security: []geta.Scheme{}})}})
	a := accepts(t, tbl)
	for range 3 {
		if r := do(t, a, "POST", "/elsewhere"); r.Code != 200 {
			t.Fatal("not moved:", r.Code)
		}
	}
	if r := do(t, a, "POST", "/elsewhere", "X-Move", "/login"); r.Code != 200 {
		t.Fatal(r.Code)
	}
	if r := do(t, a, "POST", "/elsewhere", "X-Move", "/login"); r.Code != http.StatusTooManyRequests {
		t.Fatal(r.Code)
	}
}

// geta's OPTIONS runs no operation's Doc.BeforeGate: it is not limited, and
// spends nothing of the operation's limit.
func TestOptionsRunsNoBeforeGate(t *testing.T) {
	a := accepts(t, limitedTable(geta.Scope{gate()}))
	for range 3 {
		if r := do(t, a, "OPTIONS", "/login", "Authorization", "Bearer ok"); r.Code != 204 {
			t.Fatal(r.Code)
		}
	}
	if r := do(t, a, "POST", "/login"); r.Code != 200 {
		t.Fatal(r.Code)
	}
}

// ConcurrencyLimit's 503 is documented on every operation behind it.
func TestConcurrencyLimitIsDocumented(t *testing.T) {
	m := doc(t, accepts(t, withRoot(one("/x", get(okHandler)), geta.ConcurrencyLimit(4))))
	for _, method := range []string{"get", "options"} {
		if got := at(t, m, "paths", "/x", method, "responses", "503", "description"); got != "Server at capacity" {
			t.Errorf("%s: %v", method, got)
		}
	}
}

// A deadline already earlier on the incoming context (an outer Timeout) is
// kept: an inner, longer Timeout does not extend it.
func TestTimeoutKeepsAnEarlierDeadline(t *testing.T) {
	left := make(chan time.Duration, 1)
	h := func(ctx context.Context, _ *empty) (*ok, error) {
		d, _ := ctx.Deadline()
		left <- time.Until(d)
		return &ok{true}, nil
	}
	a := accepts(t, withRoot(one("/x", get(h)), geta.Timeout(time.Second), geta.Timeout(time.Hour)))
	if r := do(t, a, "GET", "/x"); r.Code != 200 {
		t.Fatal(r.Code)
	}
	if d := <-left; d > time.Second || d <= 0 {
		t.Fatal(d)
	}
}

// A switched response is logged with upgrade=true.
func TestAccessLogMarksAnUpgrade(t *testing.T) {
	log, buf := logger()
	tbl := upgradeTable()
	tbl.Root = geta.Scope{geta.AccessLog(log), gate()}
	u := getatest.New(t, tbl).Bearer("ok").Upgrade("/ws", "echo")
	if !u.Switched {
		t.Fatal(u.Response.Status)
	}
	io.WriteString(u.Conn, "hi\n")
	if line, _ := bufio.NewReader(u.Conn).ReadString('\n'); line != "echo: hi\n" {
		t.Fatalf("%q", line)
	}
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(buf.String(), "upgrade=true"); {
		if time.Now().After(deadline) {
			t.Fatal(buf.String())
		}
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(buf.String(), "route=/ws status=101") || strings.Contains(buf.String(), "streaming=true") {
		t.Fatal(buf.String())
	}
}

// A panic after the response has started cannot change its status: Recover
// logs it and aborts the connection, so the client reads a broken response,
// never a complete one. The response is chunked, so only the abort tells the
// client it is broken: the same response without the panic reads complete.
func TestRecoverAfterTheCommitAborts(t *testing.T) {
	log, buf := logger()
	for _, panics := range []bool{false, true} {
		late := geta.Use(func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				io.WriteString(w, "partial")
				http.NewResponseController(w).Flush()
				if panics {
					panic("late")
				}
			})
		})
		c := getatest.New(t, withRoot(one("/x", get(okHandler)), geta.Recover(log), late))
		res, err := c.HTTP().Get(c.URL() + "/x")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || string(body) != "partial" || len(res.TransferEncoding) == 0 || res.TransferEncoding[0] != "chunked" ||
			panics != errors.Is(err, io.ErrUnexpectedEOF) || !panics && err != nil {
			t.Fatalf("panics %v: %d %q %v %v", panics, res.StatusCode, body, res.TransferEncoding, err)
		}
	}
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(buf.String(), "panic=late"); {
		if time.Now().After(deadline) {
			t.Fatal(buf.String())
		}
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(buf.String(), "geta: panic") || !strings.Contains(buf.String(), "stack=") {
		t.Fatal(buf.String())
	}
}
