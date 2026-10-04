package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/app"
	"github.com/koji-1009/geta/examples/booking/auth"
	"github.com/koji-1009/geta/examples/booking/issuer"
	"github.com/koji-1009/geta/examples/booking/routes"
	"github.com/koji-1009/geta/examples/booking/telemetry"
	"github.com/koji-1009/geta/getatest"
)

// harness is the service as main assembles it, beside a demo issuer on a
// real listener: the keys are discovered and fetched over HTTP by keyfunc,
// and every token is the issuer's.
type harness struct {
	t   *testing.T
	iss *issuer.Issuer
	// issuerURL is the issuer's base URL.
	issuerURL string
	env       *app.Env
	tel       *telemetry.Telemetry
	// anon is a client with no token.
	anon *getatest.Client
}

type option func(*app.Env, *issuer.Issuer)

// offline starts the service while the issuer's key set cannot be fetched.
func offline(_ *app.Env, iss *issuer.Issuer) { iss.SetOffline(true) }

func newHarness(t *testing.T, opts ...option) *harness {
	t.Helper()
	iss, err := issuer.New(auth.Audience, map[string]issuer.Client{
		"frontdesk": {Secret: "frontdesk-secret", Scopes: []string{auth.ScopeWriteRooms, auth.ScopeWriteBookings, auth.ScopeManageBookings}},
		"member":    {Secret: "member-secret", Scopes: []string{auth.ScopeWriteBookings}},
		"guest":     {Secret: "guest-secret", Scopes: []string{auth.ScopeWriteBookings}},
		"viewer":    {Secret: "viewer-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(iss.Handler())
	t.Cleanup(srv.Close)
	iss.URL = srv.URL
	pre := &app.Env{}
	for _, o := range opts {
		o(pre, iss)
	}
	keys, err := auth.Discover(t.Context(), srv.Client(), iss.URL)
	if err != nil {
		t.Fatal(err)
	}
	env := app.OpenQuiet(iss.URL, keys)
	if pre.RatePerMinute != 0 {
		env.RatePerMinute = pre.RatePerMinute
	}
	tel := telemetry.New()
	env.Observe = geta.Scope{tel.Middleware()}
	c := getatest.New(t, routes.Table(env), routes.Options(env)...)
	return &harness{t: t, iss: iss, issuerURL: srv.URL, env: env, tel: tel, anon: c}
}

// token asks the issuer's token endpoint for a token, as a client would.
func (h *harness) token(client string) string {
	h.t.Helper()
	res, err := http.PostForm(h.issuerURL+"/token", url.Values{
		"grant_type": {"client_credentials"}, "client_id": {client}, "client_secret": {client + "-secret"},
	})
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil || res.StatusCode != http.StatusOK || body.TokenType != "Bearer" {
		h.t.Fatalf("token for %s: %d %v", client, res.StatusCode, err)
	}
	return body.AccessToken
}

// as is a client holding client's token.
func (h *harness) as(client string) *getatest.Client { return h.anon.Bearer(h.token(client)) }

func status(t *testing.T, res *getatest.Response, want int) *getatest.Response {
	t.Helper()
	if res.Status != want {
		t.Fatalf("status %d, want %d: %s", res.Status, want, res.Body)
	}
	return res
}

func problemType(t *testing.T, res *getatest.Response, status int, typ string) {
	t.Helper()
	if res.Status != status {
		t.Fatalf("status %d, want %d: %s", res.Status, status, res.Body)
	}
	if p := res.Problem(); p.Type != typ {
		t.Fatalf("problem type %q, want %q: %s", p.Type, typ, res.Body)
	}
}

// room creates a room open 08:00 to 18:00 at +09:00, at 1200.00 an hour.
func (h *harness) room(c *getatest.Client, name string) string {
	h.t.Helper()
	res := status(h.t, c.Post("/rooms", map[string]any{
		"name": name, "capacity": 8, "opens": "08:00:00+09:00", "closes": "18:00:00+09:00",
		"hourlyRate": "1200.00", "panel": "192.0.2.10", "panel6": "2001:db8::10",
	}), http.StatusCreated)
	loc := res.Header.Get("Location")
	if !strings.HasPrefix(loc, "/rooms/") {
		h.t.Fatal(loc)
	}
	return loc
}
