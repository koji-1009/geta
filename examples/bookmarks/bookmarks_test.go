package main

import (
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/bookmarks/app"
	"github.com/koji-1009/geta/examples/bookmarks/metrics"
	"github.com/koji-1009/geta/examples/bookmarks/routes"
	"github.com/koji-1009/geta/examples/bookmarks/routes/bookmarks"
	"github.com/koji-1009/geta/examples/bookmarks/routes/me"
	mroute "github.com/koji-1009/geta/examples/bookmarks/routes/metrics"
	"github.com/koji-1009/geta/examples/bookmarks/store"
	"github.com/koji-1009/geta/getatest"
)

const page = "https://app.example.com"

func serve(t *testing.T) (*app.Env, *getatest.Client) {
	t.Helper()
	env := app.OpenQuiet(page)
	return env, getatest.New(t, routes.Table(env), routes.Options(env)...)
}

// login logs user in and returns a client that sends the session cookie,
// as the page's browser does.
func login(t *testing.T, c *getatest.Client, user string) *getatest.Client {
	t.Helper()
	res := c.Post("/login", map[string]any{"user": user, "password": user + "-pass"})
	if res.Status != http.StatusOK {
		t.Fatalf("login %s: %d %s", user, res.Status, res.Body)
	}
	cookies := (&http.Response{Header: res.Header}).Cookies()
	if len(cookies) != 1 || cookies[0].Name != "sid" {
		t.Fatalf("login set %v", cookies)
	}
	return c.With("Cookie", "sid="+cookies[0].Value)
}

func expect(t *testing.T, res *getatest.Response, status int) *getatest.Response {
	t.Helper()
	if res.Status != status {
		t.Fatalf("%d, want %d: %s", res.Status, status, res.Body)
	}
	return res
}

// The document is committed; a change to it is a reviewed diff. It is
// OpenAPI 3.2, the version with a place for QUERY.
func TestDocument(t *testing.T) {
	_, c := serve(t)
	getatest.Golden(t, c.App(), "openapi.json")
}

// A login sets the session cookie; the gate admits it; a logout removes it
// and ends the session. GET /me reads the lang cookie as a parameter, with
// its default when the cookie is not sent.
func TestSessionAndCookieParameter(t *testing.T) {
	_, c := serve(t)
	res := expect(t, c.Post("/login", map[string]any{"user": "ada", "password": "ada-pass"}), http.StatusOK)
	set := res.Header.Get("Set-Cookie")
	for _, attr := range []string{"sid=", "Path=/", "HttpOnly", "Secure", "SameSite=Lax", "Max-Age=28800"} {
		if !strings.Contains(set, attr) {
			t.Errorf("Set-Cookie %q lacks %s", set, attr)
		}
	}
	expect(t, c.Post("/login", map[string]any{"user": "ada", "password": "wrong"}), http.StatusUnauthorized)
	// A form, as an HTML form on another site would post, is refused.
	expect(t, c.Form(http.MethodPost, "/login", map[string][]string{"user": {"ada"}, "password": {"ada-pass"}}), http.StatusUnsupportedMediaType)

	expect(t, c.Get("/me"), http.StatusUnauthorized)
	ada := login(t, c, "ada")
	if got := expect(t, ada.Get("/me"), http.StatusOK).JSON[me.Me](); got.User != "ada" || got.Greeting != "Hello, ada" {
		t.Fatalf("%+v", got)
	}
	sid := strings.TrimPrefix(strings.SplitN(set, ";", 2)[0], "sid=")
	ja := c.With("Cookie", "sid="+sid+"; lang=ja")
	if got := expect(t, ja.Get("/me"), http.StatusOK).JSON[me.Me](); got.Greeting != "こんにちは、ada" {
		t.Fatalf("%+v", got)
	}
	res = expect(t, c.With("Cookie", "sid="+sid+"; lang=fr").Get("/me"), http.StatusBadRequest)
	if p := res.Problem(); len(p.Errors) != 1 || p.Errors[0].In != "cookie" || p.Errors[0].Path != "lang" {
		t.Fatalf("%+v", p)
	}

	res = expect(t, ada.Post("/logout", nil), http.StatusNoContent)
	if set := res.Header.Get("Set-Cookie"); !strings.HasPrefix(set, "sid=;") || !strings.Contains(set, "Max-Age=0") {
		t.Fatalf("logout set %q", set)
	}
	expect(t, ada.Get("/me"), http.StatusUnauthorized)
}

// A POST with an Idempotency-Key is answered once: a retry with the key and
// the same body gets the first answer, the bookmark saved once; another body
// under the key is a 422. Without a key, each POST saves.
func TestIdempotencyKey(t *testing.T) {
	_, c := serve(t)
	ada, bo := login(t, c, "ada"), login(t, c, "bo")
	go1 := map[string]any{"url": "https://go.dev/", "title": "Go", "tags": []string{"go"}}
	key := `"0b8e2c4a-7f1d-4c55-9e3a-1d2f3a4b5c6d"`

	first := expect(t, ada.With("Idempotency-Key", key).Post("/bookmarks", go1), http.StatusCreated)
	retry := expect(t, ada.With("Idempotency-Key", key).Post("/bookmarks", go1), http.StatusCreated)
	if first.Header.Get("Location") == "" || retry.Header.Get("Location") != first.Header.Get("Location") || retry.Text() != first.Text() {
		t.Fatalf("the retry was answered differently:\n%s %s\n%s %s", first.Header, first.Body, retry.Header, retry.Body)
	}
	if n := len(expect(t, ada.Get("/bookmarks"), http.StatusOK).JSON[bookmarks.BookmarkList]().Items); n != 1 {
		t.Fatalf("%d bookmarks after a retry", n)
	}
	res := expect(t, ada.With("Idempotency-Key", key).Post("/bookmarks", map[string]any{"url": "https://pkg.go.dev/", "title": "Pkg"}),
		http.StatusUnprocessableEntity)
	if p := res.Problem(); p.Type != "/problems/idempotency-key-reused" {
		t.Fatalf("%+v", p)
	}
	// The key is ada's: bo's request under the same key is his own.
	expect(t, bo.With("Idempotency-Key", key).Post("/bookmarks", go1), http.StatusCreated)
	// A key that is not a quoted string is the header's 400.
	res = expect(t, ada.With("Idempotency-Key", "unquoted-key-123").Post("/bookmarks", go1), http.StatusBadRequest)
	if p := res.Problem(); len(p.Errors) != 1 || p.Errors[0].In != "header" {
		t.Fatalf("%+v", p)
	}
	// Without a key, every POST saves.
	expect(t, ada.Post("/bookmarks", go1), http.StatusCreated)
	expect(t, ada.Post("/bookmarks", go1), http.StatusCreated)
	if n := len(ada.Get("/bookmarks").JSON[bookmarks.BookmarkList]().Items); n != 3 {
		t.Fatalf("%d bookmarks", n)
	}
	b := ada.Get(first.Header.Get("Location")).JSON[store.Bookmark]()
	if b.URL != "https://go.dev/" || !slices.Equal(b.Tags, []string{"go"}) {
		t.Fatalf("%+v", b)
	}
	expect(t, bo.Get(first.Header.Get("Location")), http.StatusNotFound)
}

// QUERY /bookmarks searches with a JSON body; limit has a default.
func TestQuerySearch(t *testing.T) {
	_, c := serve(t)
	ada := login(t, c, "ada")
	for _, b := range []map[string]any{
		{"url": "https://go.dev/blog/intro-generics", "title": "An Introduction To Generics", "tags": []string{"go", "blog"}},
		{"url": "https://go.dev/doc/effective_go", "title": "Effective Go", "tags": []string{"go", "doc"}},
		{"url": "https://www.rfc-editor.org/rfc/rfc9110", "title": "HTTP Semantics", "tags": []string{"http", "rfc"}},
	} {
		expect(t, ada.Post("/bookmarks", b), http.StatusCreated)
	}
	titles := func(terms map[string]any) []string {
		t.Helper()
		var out []string
		for _, b := range expect(t, ada.Query("/bookmarks", terms), http.StatusOK).JSON[bookmarks.BookmarkList]().Items {
			out = append(out, b.Title)
		}
		return out
	}
	for name, c := range map[string]struct {
		terms map[string]any
		want  []string
	}{
		"nothing":          {map[string]any{}, []string{"HTTP Semantics", "Effective Go", "An Introduction To Generics"}},
		"a word":           {map[string]any{"text": "go"}, []string{"Effective Go", "An Introduction To Generics"}},
		"two words":        {map[string]any{"text": "GO generics"}, []string{"An Introduction To Generics"}},
		"a tag":            {map[string]any{"tags": []string{"rfc"}}, []string{"HTTP Semantics"}},
		"a word and a tag": {map[string]any{"text": "go", "tags": []string{"doc"}}, []string{"Effective Go"}},
		"a limit":          {map[string]any{"limit": 1}, []string{"HTTP Semantics"}},
		"nothing matches":  {map[string]any{"text": "rust"}, nil},
	} {
		if got := titles(c.terms); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
	expect(t, ada.Query("/bookmarks", map[string]any{"limit": 0}), http.StatusBadRequest)
	expect(t, ada.Query("/bookmarks", map[string]any{"sort": "title"}), http.StatusBadRequest)
}

// The page's origin may send the session cookie and read the answers; any
// other origin gets no CORS headers, and its browser's writes are refused
// before they reach a handler.
func TestCORSWithCredentials(t *testing.T) {
	_, c := serve(t)
	ada := login(t, c, "ada")

	preflight := func(origin, method, headers string) *getatest.Response {
		req, _ := http.NewRequest(http.MethodOptions, c.URL()+"/bookmarks", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", method)
		req.Header.Set("Access-Control-Request-Headers", headers)
		return c.Send(req)
	}
	res := expect(t, preflight(page, http.MethodPost, "content-type, idempotency-key"), http.StatusNoContent)
	h := res.Header
	if h.Get("Access-Control-Allow-Origin") != page || h.Get("Access-Control-Allow-Credentials") != "true" ||
		h.Get("Access-Control-Max-Age") != "600" || !slices.Contains(h.Values("Vary"), "Origin") {
		t.Fatalf("preflight from the page: %v", h)
	}
	for _, want := range []string{"Idempotency-Key", "Content-Type"} {
		if !strings.Contains(h.Get("Access-Control-Allow-Headers"), want) {
			t.Errorf("Access-Control-Allow-Headers %q lacks %s", h.Get("Access-Control-Allow-Headers"), want)
		}
	}
	for _, m := range []string{"GET", "POST", "QUERY"} {
		if !strings.Contains(h.Get("Access-Control-Allow-Methods"), m) {
			t.Errorf("Access-Control-Allow-Methods %q lacks %s", h.Get("Access-Control-Allow-Methods"), m)
		}
	}
	res = expect(t, preflight("https://evil.example", http.MethodPost, "content-type"), http.StatusNoContent)
	if res.Header.Get("Access-Control-Allow-Origin") != "" || res.Header.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("preflight from another origin: %v", res.Header)
	}

	// The page's own write, as its browser sends it: the cookie, Origin, and
	// Sec-Fetch-Site. The answer carries what lets the page read it,
	// Location among the headers it may read.
	fromPage := ada.With("Origin", page).With("Sec-Fetch-Site", "same-site")
	res = expect(t, fromPage.Post("/bookmarks", map[string]any{"url": "https://go.dev/", "title": "Go"}), http.StatusCreated)
	if res.Header.Get("Access-Control-Allow-Origin") != page || res.Header.Get("Access-Control-Allow-Credentials") != "true" ||
		!strings.Contains(res.Header.Get("Access-Control-Expose-Headers"), "Location") {
		t.Fatalf("a write from the page: %v", res.Header)
	}

	// A page on another origin can make the browser send the cookie with a
	// write; cross-origin protection refuses it, and the store is unchanged.
	fromElsewhere := ada.With("Origin", "https://evil.example").With("Sec-Fetch-Site", "cross-site")
	res = expect(t, fromElsewhere.Post("/bookmarks", map[string]any{"url": "https://evil.example/", "title": "Evil"}), http.StatusForbidden)
	if res.Problem().Detail != "cross-origin request refused" || res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("a write from another origin: %v %s", res.Header, res.Body)
	}
	// A read from there is served (GET changes nothing), but without CORS
	// headers its browser keeps the answer from it.
	res = expect(t, fromElsewhere.Get("/bookmarks"), http.StatusOK)
	if res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("a read from another origin: %v", res.Header)
	}
	if n := len(ada.Get("/bookmarks").JSON[bookmarks.BookmarkList]().Items); n != 1 {
		t.Fatalf("%d bookmarks", n)
	}
	// A program, which sends neither Origin nor Sec-Fetch-Site, writes.
	expect(t, ada.Post("/bookmarks", map[string]any{"url": "https://pkg.go.dev/", "title": "Pkg"}), http.StatusCreated)
}

// The rate limit keys by the client's address as the trusted proxy reports
// it: clients behind one proxy get a bucket each, and a client that sends
// X-Forwarded-For itself, straight to the server, cannot choose its bucket.
func TestRateLimitBehindAProxy(t *testing.T) {
	env := app.OpenQuiet(page)
	env.RatePerMinute = 2
	peer := remoteAddr(t)
	env.TrustedProxies = []netip.Prefix{netip.PrefixFrom(peer, peer.BitLen())}
	c := getatest.New(t, routes.Table(env), routes.Options(env)...)

	viaProxy := func(client string) *getatest.Client { return c.With("X-Forwarded-For", client) }
	for range 2 {
		expect(t, viaProxy("203.0.113.7").Get("/metrics"), http.StatusOK)
	}
	res := expect(t, viaProxy("203.0.113.7").Get("/metrics"), http.StatusTooManyRequests)
	if res.Header.Get("Retry-After") == "" {
		t.Fatal(res.Header)
	}
	// Another client behind the proxy has its own bucket.
	expect(t, viaProxy("203.0.113.8").Get("/metrics"), http.StatusOK)
	// An entry a client wrote before the proxy's is not believed: the key
	// is still the address the proxy appended.
	expect(t, viaProxy("198.51.100.1, 203.0.113.7").Get("/metrics"), http.StatusTooManyRequests)

	// Not trusting the peer, the server keys by the peer alone, whatever the
	// header says.
	env2 := app.OpenQuiet(page)
	env2.RatePerMinute = 2
	direct := getatest.New(t, routes.Table(env2), routes.Options(env2)...)
	expect(t, direct.With("X-Forwarded-For", "203.0.113.1").Get("/metrics"), http.StatusOK)
	expect(t, direct.With("X-Forwarded-For", "203.0.113.2").Get("/metrics"), http.StatusOK)
	expect(t, direct.With("X-Forwarded-For", "203.0.113.3").Get("/metrics"), http.StatusTooManyRequests)
}

// remoteAddr is the address getatest's in-memory network gives a client,
// which stands in for the reverse proxy here.
func remoteAddr(t *testing.T) netip.Addr {
	t.Helper()
	var addr string
	probe := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			addr = r.RemoteAddr
			next.ServeHTTP(w, r)
		})
	})
	c := getatest.New(t, geta.Table{Root: geta.Scope{probe}})
	c.Get("/")
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		t.Fatalf("the in-memory network's address %q: %v", addr, err)
	}
	return ap.Addr().Unmap()
}

// The root scope's middleware counts by template: two bookmarks are one
// route, and the URLs nothing serves are one label.
func TestMetricsByTemplate(t *testing.T) {
	_, c := serve(t)
	ada := login(t, c, "ada")
	var locs []string
	for _, title := range []string{"A", "B"} {
		locs = append(locs, expect(t, ada.Post("/bookmarks", map[string]any{"url": "https://example.com/" + title, "title": title}),
			http.StatusCreated).Header.Get("Location"))
	}
	for _, loc := range locs {
		expect(t, ada.Get(loc), http.StatusOK)
	}
	expect(t, ada.Get("/bookmarks/not-a-uuid"), http.StatusBadRequest)
	expect(t, ada.Get("/wp-login.php"), http.StatusNotFound)
	expect(t, ada.Get("/.env"), http.StatusNotFound)
	expect(t, ada.Delete("/bookmarks"), http.StatusMethodNotAllowed)

	counts := map[string]int{}
	for _, n := range expect(t, c.Get("/metrics"), http.StatusOK).JSON[mroute.Counts]().Items {
		counts[n.Route+" "+http.StatusText(n.Status)] = n.Count
	}
	for k, want := range map[string]int{
		"POST /login OK":                          1,
		"POST /bookmarks Created":                 2,
		"GET /bookmarks/{bookmark} OK":            2,
		"GET /bookmarks/{bookmark} Bad Request":   1,
		metrics.Unmatched + " Not Found":          2,
		metrics.Unmatched + " Method Not Allowed": 1,
	} {
		if counts[k] != want {
			t.Errorf("%s: %d, want %d (all: %v)", k, counts[k], want, counts)
		}
	}
}
