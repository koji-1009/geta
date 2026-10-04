package getatest

import (
	"context"
	"io"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/koji-1009/geta"
)

type dlNote struct {
	Text string `json:"text"`
}

type dlIn struct {
	Body dlNote `body:"json"`
}

// The client serves the app over connections that take read deadlines, so
// geta's 408 for a body that has not arrived by the Timeout deadline is
// what a test gets when it sends, with Send, a request whose body stalls:
// here a pipe the test writes part of the body to and never closes. In a
// synctest bubble the deadline passes without waiting for it. A body that
// arrives whole is answered as before.
func TestSendAStalledBodyGetsThe408(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		echo := func(_ context.Context, in *dlIn) (*dlNote, error) { return &in.Body, nil }
		c := New(t, geta.Table{Root: geta.Scope{geta.Timeout(time.Second)}, Routes: []geta.Entry{
			{Path: "/notes", Route: geta.Route{Post: geta.Op(http.StatusOK, echo, geta.Doc{})}},
		}})
		body, w := io.Pipe()
		t.Cleanup(func() { w.Close() })
		go w.Write([]byte(`{"te`))
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, c.URL()+"/notes", body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		start := time.Now()
		p := c.Send(req).Problem()
		if p.Status != http.StatusRequestTimeout || time.Since(start) != time.Second {
			t.Fatalf("%+v after %v", p, time.Since(start))
		}
		if res := c.Post("/notes", `{"text":"a"}`); res.Status != http.StatusOK {
			t.Fatalf("a body in time: %d %s", res.Status, res.Text())
		}
	})
}
