package getatest

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaclient"
)

// failsApp serves /g; a request with X-Abort is aborted before any byte of
// its response, and one with X-Cut after part of a body its Content-Length
// promised; a request with X-Flush is flushed through http.Flusher first.
func failsApp(t *testing.T) *geta.App {
	t.Helper()
	m := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Header.Get("X-Abort") != "":
				panic(http.ErrAbortHandler)
			case r.Header.Get("X-Cut") != "":
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Length", "100")
				io.WriteString(w, `{"text":`)
				http.NewResponseController(w).Flush()
				panic(http.ErrAbortHandler)
			case r.Header.Get("X-Flush") != "":
				w.(http.Flusher).Flush()
			}
			next.ServeHTTP(w, r)
		})
	})
	app, err := geta.New(geta.Table{Root: geta.Scope{m}, Routes: []geta.Entry{{Path: "/g", Route: geta.Route{Get: geta.Op(http.StatusOK, greet, geta.Doc{})}}}})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

// Every helper fails the test, naming the request, when it cannot build the
// request (a path that is no URL), when the body cannot be encoded, when no
// response arrives, and when the response is cut off.
func TestHelpersFailOnTransportFailures(t *testing.T) {
	app := failsApp(t)
	for name, fn := range map[string]func(c *Client){
		"Do":               func(c *Client) { c.Do(http.MethodGet, "/%zz", nil) },
		"Content":          func(c *Client) { c.Content(http.MethodPost, "/%zz", "text/plain", nil) },
		"Form":             func(c *Client) { c.Form(http.MethodPost, "/%zz", url.Values{}) },
		"Multipart":        func(c *Client) { c.Multipart(http.MethodPost, "/%zz", nil) },
		"Stream":           func(c *Client) { c.Stream("/%zz") },
		"Upgrade":          func(c *Client) { c.Upgrade("/%zz", "chat") },
		"encode":           func(c *Client) { c.Post("/g", make(chan int)) },
		"abort":            func(c *Client) { c.With("X-Abort", "1").Get("/g") },
		"abort a stream":   func(c *Client) { c.With("X-Abort", "1").Stream("/g") },
		"abort an upgrade": func(c *Client) { c.With("X-Abort", "1").Upgrade("/g", "chat") },
		"cut":              func(c *Client) { c.With("X-Cut", "1").Get("/g") },
		"cut a stream":     func(c *Client) { c.With("X-Cut", "1").Stream("/g") },
		"cut an upgrade":   func(c *Client) { c.With("X-Cut", "1").Upgrade("/g", "chat") },
	} {
		f := inGoroutine(t, func(f *failures) { fn(Serve(f, app)) })
		if !f.fatal || len(f.errs) != 1 || !strings.HasPrefix(f.errs[0], "getatest: ") {
			t.Errorf("%s: fatal %v, %q", name, f.fatal, f.errs)
		}
	}
}

// readOnlyBody hands back each response's body as a reader and closer only,
// as a RoundTripper that wraps the body does, keeping the bodies it wrapped
// to close.
type readOnlyBody struct {
	next   http.RoundTripper
	bodies *[]io.Closer
}

func (r readOnlyBody) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := r.next.RoundTrip(req)
	if err == nil {
		*r.bodies = append(*r.bodies, res.Body)
		res.Body = struct{ io.ReadCloser }{res.Body}
	}
	return res, err
}

// Upgrade fails the test when a 101's body is no connection it can write: a
// transport the test set on Client.HTTP wrapped it.
func TestUpgradeFailsOnASwitchedBodyItCannotWrite(t *testing.T) {
	echo := func(context.Context, *struct{}) (*geta.Upgrade, error) {
		return &geta.Upgrade{Protocol: "echo", Serve: func(net.Conn, *bufio.ReadWriter) {}}, nil
	}
	app, err := geta.New(geta.Table{Routes: []geta.Entry{{Path: "/ws", Route: geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, echo, geta.Doc{})}}}})
	if err != nil {
		t.Fatal(err)
	}
	var bodies []io.Closer
	f := inGoroutine(t, func(f *failures) {
		c := Serve(f, app)
		c.HTTP().Transport = readOnlyBody{next: c.HTTP().Transport, bodies: &bodies}
		c.Upgrade("/ws", "echo")
	})
	for _, b := range bodies {
		b.Close()
	}
	if !f.fatal || len(f.errs) != 1 || f.errs[0] != "getatest: 101 response body is not writable" || len(bodies) != 1 {
		t.Fatalf("fatal %v, %q, %d bodies", f.fatal, f.errs, len(bodies))
	}
}

// A request built without a header map is sent with the client's headers.
func TestSendAddsHeadersToARequestWithoutAHeaderMap(t *testing.T) {
	var got string
	m := geta.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Get("X-Trace")
			next.ServeHTTP(w, r)
		})
	})
	c := New(t, geta.Table{Root: geta.Scope{m}, Routes: []geta.Entry{{Path: "/g", Route: geta.Route{Get: geta.Op(http.StatusOK, greet, geta.Doc{})}}}}).With("X-Trace", "t")
	u, _ := url.Parse(c.URL() + "/g")
	if r := c.Send(&http.Request{Method: http.MethodGet, URL: u}); r.Status != 200 || got != "t" {
		t.Fatal(r.Status, got)
	}
}

// A flush through http.Flusher on the writer the app is served on reaches
// the connection, and the response is recorded and checked as any other.
func TestTheRecordingWriterFlushes(t *testing.T) {
	f := inGoroutine(t, func(f *failures) {
		if r := Serve(f, failsApp(t)).With("X-Flush", "1").Get("/g"); r.Status != 200 || r.JSON[greeting]().Text != "hi" {
			f.errs = append(f.errs, "the flushed response was lost")
		}
	})
	if len(f.errs) != 0 {
		t.Fatalf("%q", f.errs)
	}
}

// Typed's client reports a response cut off in its body as the call's
// error.
func TestTypedReportsACutOffBody(t *testing.T) {
	c := Serve(t, failsApp(t)).With("X-Cut", "1")
	if _, err := getaclient.Call[struct{}, greeting](context.Background(), c.Typed(), http.MethodGet, "/g", nil); err == nil {
		t.Fatal("a cut-off body was taken")
	}
}

// With -update, Golden fails the test when the file cannot be written,
// though its directory can be made.
func TestGoldenFailsWhenTheFileCannotBeWritten(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "openapi.json")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	*update = true
	f := golden(t, greetApp(t, "Greet"), dir)
	*update = false
	if len(f.errs) != 1 || !f.fatal {
		t.Fatalf("%q", f.errs)
	}
}
