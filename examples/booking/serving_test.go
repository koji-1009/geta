package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/koji-1009/geta/examples/booking/app"
	"github.com/koji-1009/geta/examples/booking/issuer"
)

// The root scope codes a large body in zstd or gzip, whichever the client
// prefers; each coded form has a tag of its own, read back as the identity
// form's, so a client holding the zstd form gets its 304.
func TestCompressedListsAndTheirTags(t *testing.T) {
	h := newHarness(t)
	desk := h.as("frontdesk")
	for i := range 12 {
		h.room(desk, fmt.Sprintf("Room %02d", i))
	}
	get := func(accept, inm string) *http.Response {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, h.anon.URL()+"/rooms", nil)
		req.Header.Set("Authorization", "Bearer "+h.token("viewer"))
		req.Header.Set("Accept-Encoding", accept)
		if inm != "" {
			req.Header.Set("If-None-Match", inm)
		}
		res, err := h.anon.HTTP().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	plain := get("identity", "")
	body, _ := io.ReadAll(plain.Body)
	if plain.Header.Get("Content-Encoding") != "" || len(body) < 1024 || plain.Header.Get("ETag") == "" {
		t.Fatalf("%v %d", plain.Header, len(body))
	}

	z := get("gzip;q=0.5, zstd", "")
	if z.Header.Get("Content-Encoding") != "zstd" || z.Header.Get("ETag") != strings.TrimSuffix(plain.Header.Get("ETag"), `"`)+`-zstd"` ||
		!strings.Contains(z.Header.Get("Vary"), "Accept-Encoding") {
		t.Fatal(z.Header)
	}
	dec, err := zstd.NewReader(z.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	if got, err := io.ReadAll(dec); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("the zstd body decodes to another: %v", err)
	}

	g := get("gzip, zstd;q=0.5", "")
	gr, err := gzip.NewReader(g.Body)
	if err != nil || g.Header.Get("Content-Encoding") != "gzip" || g.Header.Get("ETag") != strings.TrimSuffix(plain.Header.Get("ETag"), `"`)+`-gzip"` {
		t.Fatal(g.Header, err)
	}
	if got, _ := io.ReadAll(gr); !bytes.Equal(got, body) {
		t.Fatal("the gzip body decodes to another")
	}

	if res := get("zstd", z.Header.Get("ETag")); res.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match with the zstd tag: %d", res.StatusCode)
	}
}

// What geta answers itself: OPTIONS on a served path, 405 with Allow, 404,
// 415, and 413 past the app's own limit; each a problem, the gate first.
func TestWhatGetaAnswers(t *testing.T) {
	h := newHarness(t)
	desk := h.as("frontdesk")
	loc := h.room(desk, "Ume")

	res := status(t, desk.Do(http.MethodOptions, loc+"/bookings", nil), http.StatusNoContent)
	if res.Header.Get("Allow") != "GET, HEAD, OPTIONS, POST" {
		t.Fatal(res.Header)
	}
	// OPTIONS runs the root scope alone, whose gate checks its default: a
	// stranger gets a 401 even where a GET would be public.
	status(t, h.anon.Do(http.MethodOptions, "/health", nil), http.StatusUnauthorized)
	status(t, h.anon.Get("/health"), http.StatusOK)

	res = status(t, desk.Delete(loc), http.StatusMethodNotAllowed)
	if res.Header.Get("Allow") != "GET, HEAD, OPTIONS" || res.Problem().Status != http.StatusMethodNotAllowed {
		t.Fatal(res.Header, res.Text())
	}
	status(t, desk.Get("/nowhere"), http.StatusNotFound)
	status(t, h.anon.Get("/nowhere"), http.StatusUnauthorized) // secure by default

	res = status(t, desk.With("Content-Type", "text/plain").Post("/rooms", "name=x"), http.StatusUnsupportedMediaType)
	if res.Header.Get("Accept") != "application/json" {
		t.Fatal(res.Header)
	}
	res = status(t, desk.With("Content-Encoding", "gzip").Post("/rooms", "{}"), http.StatusUnsupportedMediaType)
	if res.Header.Get("Accept-Encoding") != "identity" {
		t.Fatal(res.Header)
	}

	// routes.Options lowers MaxBodyBytes to 64 KiB.
	big := `{"name":"` + strings.Repeat("x", 70<<10) + `"}`
	status(t, desk.Post("/rooms", big), http.StatusRequestEntityTooLarge)
}

// A client past the rate gets a 429 with an honest Retry-After.
func TestRateLimit(t *testing.T) {
	h := newHarness(t, func(env *app.Env, _ *issuer.Issuer) { env.RatePerMinute = 2 })
	status(t, h.anon.Get("/health"), http.StatusOK)
	status(t, h.anon.Get("/health"), http.StatusOK)
	res := status(t, h.anon.Get("/health"), http.StatusTooManyRequests)
	if res.Header.Get("Retry-After") != "30" {
		t.Fatal(res.Header)
	}
}

// While the issuer's key set cannot be fetched, no token can be judged: the
// gate answers 503, not 401, and a public route still serves.
func TestNoKeysIsUnavailable(t *testing.T) {
	h := newHarness(t, offline)
	res := status(t, h.as("frontdesk").Get("/rooms"), http.StatusServiceUnavailable)
	if res.Problem().Status != http.StatusServiceUnavailable {
		t.Fatal(res.Text())
	}
	status(t, h.anon.Get("/health"), http.StatusOK)
}

// A token signed by another key, and one for another audience, are 401s
// with golang-jwt's reason.
func TestForeignTokens(t *testing.T) {
	h := newHarness(t)
	other := newHarness(t)
	res := status(t, h.anon.Bearer(other.token("frontdesk")).Get("/rooms"), http.StatusUnauthorized)
	if !strings.Contains(res.Header.Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Fatal(res.Header)
	}
	h.iss.Audience = "another-service"
	res = status(t, h.anon.Bearer(h.token("frontdesk")).Get("/rooms"), http.StatusUnauthorized)
	if !strings.Contains(res.Header.Get("WWW-Authenticate"), "audience") {
		t.Fatal(res.Header)
	}
}

// Spans are named by the template served, never the raw path; a request no
// operation serves is named by its method alone; an incoming traceparent is
// continued; the duration metric carries the route.
func TestTracesAndMetrics(t *testing.T) {
	h := newHarness(t)
	ended := h.tel.Watch()
	desk := h.as("frontdesk")
	loc := h.room(desk, "Sakura")
	const trace = "4bf92f3577b34da6a3ce929d0e0e4736"
	status(t, desk.With("traceparent", "00-"+trace+"-00f067aa0ba902b7-01").Get(loc), http.StatusOK)
	status(t, desk.Get("/nowhere"), http.StatusNotFound)

	want := map[string]bool{"POST /rooms": false, "GET /rooms/{room}": false, "GET": false}
	deadline := time.After(5 * time.Second)
	for done := 0; done < len(want); {
		select {
		case s := <-ended:
			if seen, ok := want[s.Name]; ok && !seen {
				want[s.Name] = true
				done++
			}
			if s.Name == "GET /rooms/{room}" && (s.TraceID != trace || s.Route != "/rooms/{room}" || s.Status != 200) {
				t.Fatalf("%+v", s)
			}
			if strings.Contains(s.Name, loc) {
				t.Fatalf("a span named by the raw path: %+v", s)
			}
		case <-deadline:
			t.Fatalf("spans not seen: %v", want)
		}
	}
	routes, err := h.tel.Routes(t.Context())
	if err != nil || routes["/rooms/{room}"] == 0 || routes["/rooms"] == 0 {
		t.Fatal(routes, err)
	}
	b, _ := json.Marshal(routes)
	if strings.Contains(string(b), loc) {
		t.Fatal(routes)
	}
}
