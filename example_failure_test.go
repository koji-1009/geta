package geta_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"

	"github.com/koji-1009/geta"
)

// The errors a store returns. It knows nothing of HTTP: the failure table
// of each operation decides what they become.
var (
	errOrderNotFound = errors.New("order not found")
	errOrderShipped  = errors.New("order already shipped")
)

// StockError is an error with data of its own; a row matches it by type.
type StockError struct{ Item string }

func (e *StockError) Error() string { return "out of stock: " + e.Item }

type CancelIn struct {
	ID string `path:"id" schema:"maxLength=16"`
}

// cancelOrder stands in for a handler that returns whatever the layer below
// returned, wrapped or not.
func cancelOrder(ctx context.Context, in *CancelIn) error {
	switch in.ID {
	case "1":
		return nil
	case "2":
		return fmt.Errorf("cancel %s: %w", in.ID, errOrderShipped)
	case "3":
		return &StockError{Item: "lamp"}
	case "4":
		return errors.New("disk on fire") // matches no row
	}
	return errOrderNotFound
}

// Failures is an operation's failure table: each row maps an error to a
// status and the detail the client reads, checked top to bottom. On matches
// with errors.Is, OnAs by type; Type gives a row a problem type of its own,
// which tells apart rows that share a status. The rows are the operation's
// documented failures, and an error no row matches is a 500 that carries
// nothing of the error but is logged with it.
func ExampleOn() {
	failures := []geta.Failure{
		geta.On(errOrderNotFound, http.StatusNotFound, "no such order"),
		geta.On(errOrderShipped, http.StatusConflict, "the order has shipped").Type("/problems/shipped"),
		geta.OnAs[*StockError](http.StatusConflict, "an item is out of stock").Type("/problems/out-of-stock"),
	}
	app, err := geta.New(geta.Table{Routes: []geta.Entry{{
		Path: "/orders/{id}",
		Route: geta.Route{Delete: geta.OpNoBody(http.StatusNoContent, cancelOrder, geta.Doc{
			Summary:  "Cancel an order",
			Failures: failures,
		})},
	}}}, geta.WithLogger(slog.New(slog.DiscardHandler))) // the 500's log line, with the error, goes here
	if err != nil {
		panic(err)
	}
	for _, id := range []string{"1", "2", "3", "4", "9"} {
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/orders/"+id, nil))
		var p geta.Problem
		json.Unmarshal(rec.Body.Bytes(), &p)
		fmt.Printf("%s: %d %s %q\n", id, rec.Code, p.Type, p.Detail)
	}
	// Output:
	// 1: 204  ""
	// 2: 409 /problems/shipped "the order has shipped"
	// 3: 409 /problems/out-of-stock "an item is out of stock"
	// 4: 500 about:blank ""
	// 9: 404 about:blank "no such order"
}
