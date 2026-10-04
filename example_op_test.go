package geta_test

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/koji-1009/geta"
)

// GreetIn is what GET /hello/{name} reads: a path parameter, and a query
// parameter that has a default, so a request may leave it out.
type GreetIn struct {
	Name  string `path:"name" schema:"minLength=1,maxLength=40"`
	Shout bool   `query:"shout" doc:"Answer in capitals" schema:"default=false"`
}

// Greeting is what it answers.
type Greeting struct {
	Text string `json:"text"`
}

func greet(ctx context.Context, in *GreetIn) (*Greeting, error) {
	text := "hello, " + in.Name
	if in.Shout {
		text = strings.ToUpper(text)
	}
	return &Greeting{Text: text}, nil
}

// greetTable is a one-route table; an application's comes from geta sync
// (routes.Table), built from its routes directory.
func greetTable() geta.Table {
	return geta.Table{Routes: []geta.Entry{{
		Path:  "/hello/{name}",
		Route: geta.Route{Get: geta.Op(http.StatusOK, greet, geta.Doc{Summary: "Greet someone"})},
	}}}
}

// An operation is a handler, its success status, and its documentation. The
// handler's input is bound and checked before it runs: a value the input's
// type refuses is a 400 listing each violation, and the handler never sees
// it.
func ExampleOp() {
	app, err := geta.New(greetTable())
	if err != nil {
		panic(err)
	}
	for _, target := range []string{"/hello/ada", "/hello/ada?shout=true", "/hello/ada?shout=loudly"} {
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		fmt.Println(rec.Code, rec.Header().Get("Content-Type"), strings.TrimSpace(rec.Body.String()))
	}
	// Output:
	// 200 application/json {"text":"hello, ada"}
	// 200 application/json {"text":"HELLO, ADA"}
	// 400 application/problem+json {"type":"about:blank","title":"Bad Request","status":400,"detail":"the request does not match its contract","errors":[{"in":"query","path":"shout","message":"expected boolean, got string"}]}
}

// New assembles the table once, checking every route against its URL, its
// types, and its documentation, and reports every mistake at once. The App
// it returns is an http.Handler, and its OpenAPI document is read off the
// same types.
func ExampleNew() {
	app, err := geta.New(greetTable(), geta.WithInfo(geta.Info{Title: "greeter", Version: "1.0.0"}))
	if err != nil {
		panic(err)
	}
	fmt.Println(app.Operations())

	// A path tag the URL lacks is an assembly error, not a request's.
	type wrongIn struct {
		ID string `path:"id"`
	}
	_, err = geta.New(geta.Table{Routes: []geta.Entry{{
		Path: "/users/{name}",
		Route: geta.Route{Get: geta.Op(http.StatusOK, func(context.Context, *wrongIn) (*Greeting, error) {
			return &Greeting{}, nil
		}, geta.Doc{})},
	}}})
	fmt.Println(err != nil)
	// Output:
	// [GET /hello/{name} OPTIONS /hello/{name}]
	// true
}

// Run serves until the process is told to stop (SIGINT or SIGTERM) or its
// context ends, then drains: open event streams end and in-flight requests
// get geta.ShutdownGrace. This is the whole of a main.
func ExampleRun() {
	app, err := geta.New(greetTable())
	if err != nil {
		log.Fatal(err)
	}
	if err := geta.Run(context.Background(), &http.Server{Addr: ":8080", Handler: app}); err != nil {
		log.Fatal(err)
	}
}

// Serve is Run on a listener the caller holds: here a port the system picks,
// served until the context is cancelled. Serve returns nil after a clean
// shutdown.
func ExampleServe() {
	app, err := geta.New(greetTable())
	if err != nil {
		panic(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- geta.Serve(ctx, &http.Server{Handler: app}, l) }()

	res, err := http.Get("http://" + l.Addr().String() + "/hello/bo")
	if err != nil {
		panic(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	fmt.Println(res.Status, strings.TrimSpace(string(body)))

	stop()
	fmt.Println(<-served)
	// Output:
	// 200 OK {"text":"hello, bo"}
	// <nil>
}
