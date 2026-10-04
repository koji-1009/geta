package geta_test

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

type dialOK struct {
	OK bool `json:"ok"`
}

// DialContext reaches the app over the in-memory network whatever address
// it is given, so a third-party client handed it never leaves the process:
// example.com is a real host, and 192.0.2.1 (TEST-NET-1) answers no one.
func TestGetatestDialContextReachesTheApp(t *testing.T) {
	h := func(context.Context, *struct{}) (*dialOK, error) { return &dialOK{true}, nil }
	c := getatest.New(t, geta.Table{Routes: []geta.Entry{{Path: "/x", Route: geta.Route{Get: geta.Op(http.StatusOK, h, geta.Doc{})}}}})
	for _, addr := range []string{"example.com:80", "192.0.2.1:9"} {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		conn, err := c.DialContext(ctx, "tcp", addr)
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		io.WriteString(conn, "GET /x HTTP/1.1\r\nHost: "+addr+"\r\nConnection: close\r\n\r\n")
		res, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
		body, _ := io.ReadAll(res.Body)
		conn.Close()
		if res.StatusCode != http.StatusOK || string(body) != `{"ok":true}` {
			t.Fatalf("%s: %d %s", addr, res.StatusCode, body)
		}
	}
}
