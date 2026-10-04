package geta_test

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getatest"
)

// An answer that leaves the request's body unread — a middleware's (a rate
// limiter's 429, a gate's 401, any geta.Use middleware's), or a 404 — is
// followed on HTTP/1.x by net/http reading away the rest of the body, before
// it writes the response header and again once the handler has returned, with
// no deadline of its own. Behind a geta.Timeout that discarding is bounded by
// the request's window counted from the answer: a body whose rest arrives in
// time leaves the connection to serve the next request, one that stalls ends
// it once the window has passed. With no Timeout nothing changes.
//
// It runs in a synctest bubble over getatest's in-memory connections, which
// take read deadlines: the times are fake, so the bounds are exact.

const drWindow = 100 * time.Millisecond

// drRefuse answers 429 to a request that carries X-Refuse, before it reads
// any of its body, as a rate limiter does.
var drRefuse = geta.Use(func(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Refuse") != "" {
			geta.WriteProblem(w, http.StatusTooManyRequests, "")
			return
		}
		next.ServeHTTP(w, r)
	})
}).Answers(http.StatusTooManyRequests, "Too many requests")

// drGate refuses a request without credentials with a 401.
var drGate = geta.Secure(geta.Policy{Default: []geta.Scheme{geta.Bearer}, Verifiers: map[string]geta.Verifier{"bearer": bearerVerifier}})

// drOpen dials c and writes head (the request line and headers, without the
// empty line) and the start of a body of 40 bytes.
func drOpen(t *testing.T, c *getatest.Client, head string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := c.DialContext(t.Context(), "tcp", "example.com:80")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := io.WriteString(conn, head+"\r\nContent-Type: application/json\r\nContent-Length: 40\r\n\r\nab"); err != nil {
		t.Fatal(err)
	}
	return conn, bufio.NewReader(conn)
}

func drRead(t *testing.T, br *bufio.Reader) *http.Response {
	t.Helper()
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res
}

var drCases = []struct {
	name string
	root geta.Scope
	head string
	want int
}{
	// The limiter runs before the Timeout: the window is the operation's all
	// the same.
	{"a middleware before the Timeout", geta.Scope{drRefuse, geta.Timeout(drWindow)}, "POST /json HTTP/1.1\r\nHost: x\r\nX-Refuse: 1", http.StatusTooManyRequests},
	{"a gate", geta.Scope{geta.Timeout(drWindow), drGate}, "POST /json HTTP/1.1\r\nHost: x", http.StatusUnauthorized},
	// A request that reaches no operation runs the root scope alone, its
	// Timeout among it.
	{"no operation", geta.Scope{geta.Timeout(drWindow)}, "POST /nowhere HTTP/1.1\r\nHost: x", http.StatusNotFound},
}

func drTable(root geta.Scope) geta.Table {
	return withRoot(dlTable(), root...)
}

// A body that stalls after an answer that left it unread holds the connection
// for the window alone: the response comes once net/http's reading of the rest
// has ended, with Connection: close, and the server closes the connection.
func TestAStalledBodyAfterAnAnswerEndsItsConnectionAfterTheWindow(t *testing.T) {
	for _, tc := range drCases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := getatest.Serve(t, accepts(t, drTable(tc.root)))
				start := time.Now()
				_, br := drOpen(t, c, tc.head)
				res := drRead(t, br)
				if res.StatusCode != tc.want || !res.Close {
					t.Fatalf("%d close=%v, want %d and the connection closed", res.StatusCode, res.Close, tc.want)
				}
				if el := time.Since(start); el != drWindow {
					t.Fatalf("answered after %v, want the %v window", el, drWindow)
				}
				if n, err := br.Read(make([]byte, 1)); n != 0 || err != io.EOF {
					t.Fatalf("after the response: %d bytes, %v; want the connection closed", n, err)
				}
			})
		})
	}
}

// A body whose rest arrives within the window is read away and the connection
// serves the next request, which no deadline of the last one's cuts short.
func TestABodyInTheWindowAfterAnAnswerKeepsItsConnection(t *testing.T) {
	for _, tc := range drCases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := getatest.Serve(t, accepts(t, drTable(tc.root)))
				start := time.Now()
				conn, br := drOpen(t, c, tc.head)
				time.Sleep(drWindow / 2)
				if _, err := io.WriteString(conn, strings.Repeat("x", 38)); err != nil {
					t.Fatal(err)
				}
				res := drRead(t, br)
				if res.StatusCode != tc.want || res.Close {
					t.Fatalf("%d close=%v, want %d and the connection kept", res.StatusCode, res.Close, tc.want)
				}
				if el := time.Since(start); el != drWindow/2 {
					t.Fatalf("answered after %v, want once the rest arrived (%v)", el, drWindow/2)
				}
				// Idle past the last request's window, then send the next.
				time.Sleep(2 * drWindow)
				if _, err := io.WriteString(conn, tc.head+"\r\n\r\n"); err != nil {
					t.Fatal(err)
				}
				if res := drRead(t, br); res.StatusCode != tc.want {
					t.Fatalf("the next request on the connection: %d", res.StatusCode)
				}
			})
		})
	}
}

// A body read to its end before the answer takes no read deadline after it:
// net/http's own read of the connection, which watches for the client going
// away once the body has ended, is not cut, so a response that goes on past
// the window keeps its context, and the connection serves the next request.
func TestABodyReadWholeTakesNoDeadlineAfterTheAnswer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var ended error
		answer := geta.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusTooManyRequests)
				http.NewResponseController(w).Flush()
				time.Sleep(3 * drWindow)
				ended = r.Context().Err()
				io.WriteString(w, "done")
			})
		}).Answers(http.StatusTooManyRequests, "Too many requests")
		c := getatest.Serve(t, accepts(t, drTable(geta.Scope{answer, geta.Timeout(drWindow)})))
		conn, br := drOpen(t, c, "POST /json HTTP/1.1\r\nHost: x")
		if _, err := io.WriteString(conn, strings.Repeat("x", 38)); err != nil {
			t.Fatal(err)
		}
		res := drRead(t, br)
		if res.StatusCode != http.StatusTooManyRequests || res.Close || ended != nil {
			t.Fatalf("%d close=%v, the context ended with %v", res.StatusCode, res.Close, ended)
		}
		if _, err := io.WriteString(conn, "GET /get HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		if res := drRead(t, br); res.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("the next request on the connection: %d", res.StatusCode)
		}
	})
}

// With no Timeout geta sets no read deadline: net/http waits for the rest of
// the body as it would (the server sets no ReadTimeout either), so after an
// hour the client has still no answer. It closes its connection then.
func TestAStalledBodyAfterAnAnswerWithoutATimeoutIsNetHTTPs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := getatest.Serve(t, accepts(t, drTable(geta.Scope{drRefuse})))
		conn, br := drOpen(t, c, "POST /json HTTP/1.1\r\nHost: x\r\nX-Refuse: 1")
		conn.SetReadDeadline(time.Now().Add(time.Hour))
		if _, err := http.ReadResponse(br, nil); !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("got %v, want no response within the hour", err)
		}
	})
}
