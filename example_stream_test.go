package geta_test

import (
	"bufio"
	"context"
	"fmt"
	"iter"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	"github.com/koji-1009/geta"
)

// Tick is one event of the stream. EventName and EventID set each event's
// event: and id: lines; its JSON is the data.
type Tick struct {
	N int `json:"n"`
}

func (t Tick) EventName() string { return "tick" }
func (t Tick) EventID() string   { return strconv.Itoa(t.N) }

type TicksIn struct {
	Count int `query:"count" schema:"minimum=1,maximum=10,default=3"`
}

// ticks answers a stream. The source is an iter.Seq: it yields until it is
// done, or until yield reports that the client went away; a source that
// waits must also watch ctx, which ends when the stream does.
func ticks(ctx context.Context, in *TicksIn) (*geta.Stream[Tick], error) {
	events := func(yield func(Tick) bool) {
		for n := 1; n <= in.Count; n++ {
			if ctx.Err() != nil || !yield(Tick{N: n}) {
				return
			}
		}
	}
	return &geta.Stream[Tick]{Events: iter.Seq[Tick](events)}, nil
}

// A *geta.Stream output answers 200 with text/event-stream, one event per
// value; the operation's middleware, the gate included, has run before it
// opens. In a real feed, set KeepAlive so proxies keep the connection open,
// and MaxIdle or MaxLifetime to bound it.
func ExampleStream() {
	app, err := geta.New(geta.Table{Routes: []geta.Entry{{
		Path:  "/ticks",
		Route: geta.Route{Get: geta.Op(http.StatusOK, ticks, geta.Doc{Summary: "Count, as events"})},
	}}})
	if err != nil {
		panic(err)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ticks?count=2", nil))
	fmt.Println(rec.Code, rec.Header().Get("Content-Type"))
	fmt.Print(rec.Body.String())
	// Output:
	// 200 text/event-stream; charset=utf-8
	// event: tick
	// id: 1
	// data: {"n":1}
	//
	// event: tick
	// id: 2
	// data: {"n":2}
}

// Serve is the Upgrade mode for a protocol no library speaks: geta checks
// that the request asks to switch to Protocol (426 if not), writes the 101
// with Header, and hands over the connection, which it closes when Serve
// returns. Here the protocol is a line echo. For WebSocket, use Accept and a
// library instead (examples/booking's /rooms/{room}/live).
func ExampleUpgrade() {
	echo := func(ctx context.Context, _ *struct{}) (*geta.Upgrade, error) {
		return &geta.Upgrade{
			Protocol: "line-echo",
			Serve: func(conn net.Conn, rw *bufio.ReadWriter) {
				for {
					line, err := rw.ReadString('\n')
					if err != nil || line == "bye\n" {
						return
					}
					rw.WriteString(strings.ToUpper(line))
					rw.Flush()
				}
			},
		}, nil
	}
	app, err := geta.New(geta.Table{Routes: []geta.Entry{{
		Path:  "/echo",
		Route: geta.Route{Get: geta.Op(http.StatusSwitchingProtocols, echo, geta.Doc{Summary: "Echo lines in capitals"})},
	}}})
	if err != nil {
		panic(err)
	}
	srv := httptest.NewServer(app)
	defer srv.Close()

	// A plain request is refused before the handler runs.
	res, err := http.Get(srv.URL + "/echo")
	if err != nil {
		panic(err)
	}
	res.Body.Close()
	fmt.Println(res.StatusCode)

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET /echo HTTP/1.1\r\nHost: example\r\nConnection: Upgrade\r\nUpgrade: line-echo\r\n\r\n")
	r := bufio.NewReader(conn)
	res, err = http.ReadResponse(r, nil)
	if err != nil {
		panic(err)
	}
	fmt.Println(res.StatusCode, res.Header.Get("Upgrade"))
	fmt.Fprint(conn, "hello\nagain\nbye\n")
	for range 2 {
		line, _ := r.ReadString('\n')
		fmt.Print(line)
	}
	// Output:
	// 426
	// 101 line-echo
	// HELLO
	// AGAIN
}
