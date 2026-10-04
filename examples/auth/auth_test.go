package main

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/auth/app"
	"github.com/koji-1009/geta/examples/auth/auth"
	"github.com/koji-1009/geta/examples/auth/routes"
	"github.com/koji-1009/geta/examples/auth/routes/login"
	"github.com/koji-1009/geta/examples/auth/routes/me/events"
	"github.com/koji-1009/geta/getatest"
)

func client(t *testing.T) *getatest.Client {
	env := app.Open()
	env.Log = slog.New(slog.DiscardHandler)
	return getatest.New(t, routes.Table(env), routes.Options(env)...)
}

// logIn posts the login form, as a browser does.
func logIn(c *getatest.Client, username, password string) *getatest.Response {
	return c.Form("POST", "/login", url.Values{"username": {username}, "password": {password}})
}

func status(t *testing.T, res *getatest.Response, want int) {
	t.Helper()
	if res.Status != want {
		t.Fatalf("status %d, want %d: %s", res.Status, want, res.Body)
	}
}

func TestBearerAndRoles(t *testing.T) {
	c := client(t)
	status(t, c.Get("/public"), 200)
	res := c.Get("/admin/whoami")
	status(t, res, 401)
	if res.Header.Get("WWW-Authenticate") != "Bearer" {
		t.Fatal(res.Header)
	}
	res = c.Bearer("member-token").Get("/admin/whoami")
	status(t, res, 403)
	if res.Problem().Detail != `requires the "admin" role` {
		t.Fatal(res.Problem())
	}
	res = c.Bearer("admin-token").Get("/admin/whoami")
	status(t, res, 200)
	if res.Text() != `{"role":"admin"}` {
		t.Fatal(res.Text())
	}
	// Secure by default: an unknown URL is 401 to a stranger.
	status(t, c.Get("/nope"), 401)
	status(t, c.Bearer("admin-token").Get("/nope"), 404)
}

func TestCookieSession(t *testing.T) {
	c := client(t)
	status(t, logIn(c, "admin", "wrong"), 401)
	// The login is a form: JSON is a 415 naming the media type taken, and a
	// field the form does not name is a 400.
	res := c.Post("/login", map[string]string{"username": "admin", "password": "admin-pass"})
	status(t, res, 415)
	if res.Header.Get("Accept") != "application/x-www-form-urlencoded" {
		t.Fatal(res.Header)
	}
	status(t, c.Form("POST", "/login", url.Values{"username": {"admin"}, "password": {"admin-pass"}, "remember": {"on"}}), 400)
	res = logIn(c, "admin", "admin-pass")
	status(t, res, 200)
	setCookie := res.Header.Get("Set-Cookie")
	sid, _, _ := strings.Cut(strings.TrimPrefix(setCookie, "sid="), ";")
	if !strings.Contains(setCookie, "HttpOnly") || !strings.Contains(setCookie, "SameSite=Lax") || len(sid) != 32 {
		t.Fatal(setCookie)
	}
	session := c.With("Cookie", "sid="+sid)

	status(t, c.Get("/me"), 401)
	res = session.Get("/me")
	status(t, res, 200)
	if res.Text() != `{"role":"admin"}` {
		t.Fatal(res.Text())
	}
	// A cookie session does not open the bearer-only /admin.
	status(t, session.Get("/admin/whoami"), 401)

	res = session.Post("/logout", nil)
	status(t, res, 200)
	if !strings.HasPrefix(res.Header.Get("Set-Cookie"), "sid=; Path=/; Max-Age=0") {
		t.Fatal(res.Header.Get("Set-Cookie"))
	}
	status(t, session.Get("/me"), 401)
}

// The password is a geta.Password: a log line or an error that prints the
// form prints [redacted] in its place.
func TestThePasswordIsNotPrinted(t *testing.T) {
	in := login.PostIn{Body: login.Credentials{Username: "admin", Password: "admin-pass"}}
	var buf strings.Builder
	slog.New(slog.NewTextHandler(&buf, nil)).Info("login", "in", in, "password", in.Body.Password)
	for _, s := range []string{fmt.Sprintf("%v %+v %#v", in, in, in), buf.String()} {
		if strings.Contains(s, "admin-pass") || !strings.Contains(s, "[redacted]") {
			t.Fatal(s)
		}
	}
}

// Revoking a session closes its live feed from the server's side; a feed
// whose session stays valid stays open.
func TestLogoutClosesTheSessionFeed(t *testing.T) {
	c := client(t)
	if s := c.Stream("/me/events"); s.Response.Status != 401 {
		t.Fatalf("anonymous feed: %d", s.Response.Status)
	}
	session := func() *getatest.Client {
		res := logIn(c, "member", "member-pass")
		sid, _, _ := strings.Cut(strings.TrimPrefix(res.Header.Get("Set-Cookie"), "sid="), ";")
		return c.With("Cookie", "sid="+sid)
	}
	revoked, kept := session(), session()
	feed := revoked.Stream("/me/events")
	other := kept.Stream("/me/events")
	status(t, revoked.Post("/logout", nil), 200)
	e, ok := feed.Next()
	if !ok || e.Name != "revoked" || e.Data != `{"kind":"revoked"}` {
		t.Fatal(e, ok)
	}
	if _, ok := feed.Next(); ok {
		t.Fatal("the feed stayed open after revocation")
	}
	status(t, kept.Get("/me"), 200)
	other.Close()
}

// A logout that lands after the gate admitted the cookie and before the
// handler asks for the revocation channel still closes the feed.
func TestLogoutBeforeTheFeedStarts(t *testing.T) {
	s := auth.NewSessions()
	sid, err := s.Login(t.Context(), "member", "member-pass")
	if err != nil {
		t.Fatal(err)
	}
	// The gate admitted sid; then the logout.
	ctx := auth.Caller.With(t.Context(), auth.Principal{Role: "member", Session: sid})
	s.Logout(t.Context(), sid)

	stream, err := events.Handler{Sessions: s}.Get(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan []string)
	go func() {
		var got []string
		for e := range stream.Events {
			got = append(got, e.Kind)
		}
		done <- got
	}()
	select {
	case got := <-done:
		if len(got) != 1 || got[0] != "revoked" {
			t.Fatal(got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the feed of a revoked session stayed open")
	}
}

// clock is a time a test moves by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *clock) advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.t = c.t.Add(d) }

// A session ends at its expiry on the server, whatever the client does with
// the cookie's Max-Age: the gate refuses it, its feed closes, and the store
// drops it, so sessions nobody logs out of do not pile up.
func TestSessionsExpire(t *testing.T) {
	clk := &clock{t: time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)}
	env := app.Open()
	env.Log = slog.New(slog.DiscardHandler)
	env.Sessions = auth.NewSessionsAt(clk.now)
	c := getatest.New(t, routes.Table(env), routes.Options(env)...)
	session := func() *getatest.Client {
		res := logIn(c, "member", "member-pass")
		status(t, res, 200)
		if !strings.Contains(res.Header.Get("Set-Cookie"), "Max-Age=3600") {
			t.Fatal(res.Header.Get("Set-Cookie"))
		}
		sid, _, _ := strings.Cut(strings.TrimPrefix(res.Header.Get("Set-Cookie"), "sid="), ";")
		return c.With("Cookie", "sid="+sid)
	}

	s := session()
	clk.advance(auth.SessionTTL - time.Second)
	status(t, s.Get("/me"), 200)
	feed := s.Stream("/me/events")
	clk.advance(time.Second)
	status(t, s.Get("/me"), 401)
	if e, ok := feed.Next(); !ok || e.Name != "revoked" {
		t.Fatal("the feed of an expired session stayed open", e, ok)
	}
	if _, ok := feed.Next(); ok {
		t.Fatal("the feed stayed open after expiry")
	}

	// Expired sessions are removed even if nobody presents them again.
	for range 5 {
		session()
	}
	if n := env.Sessions.Len(); n != 5 {
		t.Fatalf("%d sessions held, want 5", n)
	}
	clk.advance(auth.SessionTTL)
	kept := session()
	if n := env.Sessions.Len(); n != 1 {
		t.Fatalf("%d sessions held after expiry, want 1", n)
	}
	status(t, kept.Get("/me"), 200)
}

// Login attempts are limited per client address; every other route is not,
// and a refused attempt never reaches the credential check.
func TestLoginIsRateLimited(t *testing.T) {
	env := app.Open()
	env.Log = slog.New(slog.DiscardHandler)
	env.LoginsPerMinute = 3
	c := getatest.New(t, routes.Table(env), routes.Options(env)...)
	for range 3 {
		status(t, logIn(c, "admin", "wrong"), 401)
	}
	res := logIn(c, "admin", "admin-pass")
	status(t, res, 429)
	if res.Header.Get("Retry-After") == "" || res.Header.Get("Set-Cookie") != "" {
		t.Fatal(res.Header)
	}
	for range 10 {
		status(t, c.Get("/public"), 200)
		status(t, c.Bearer("admin-token").Get("/admin/whoami"), 200)
	}
	// The limit is /login's own (Doc.BeforeGate): its 429 is documented
	// there, and on no other operation.
	for _, op := range c.App().Operations() {
		method, template, _ := strings.Cut(op, " ")
		documented, _ := c.App().Documented(geta.Match{Template: template, Method: method, Operation: true}, 429)
		if documented != (op == "POST /login") {
			t.Errorf("%s documents 429: %v", op, documented)
		}
	}
}

func TestDocumentCarriesTheDeclarations(t *testing.T) {
	c := client(t)
	getatest.Golden(t, c.App(), "openapi.json")
	doc := string(c.App().OpenAPI())
	for _, want := range []string{`"cookieAuth": {`, `"in": "cookie"`, `"bearer": {`, `"403": {`} {
		if !strings.Contains(doc, want) {
			t.Errorf("document lacks %s", want)
		}
	}
}

// The same application, documented as OpenAPI 3.2: its event stream event by
// event, its responses with summaries, its session cookie named.
func TestDocument32(t *testing.T) {
	env := app.Open()
	env.Log = slog.New(slog.DiscardHandler)
	c := getatest.New(t, routes.Table(env), append(routes.Options(env), geta.WithOpenAPI(geta.OpenAPI32))...)
	getatest.Golden(t, c.App(), "openapi.3.2.json")
}
