package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/koji-1009/geta/examples/booking/live"
)

// A door panel opens the room's live feed with gorilla/websocket's client,
// and receives each booking of the room as a text message; the server side
// is coder/websocket, handed the request by geta.Upgrade's Accept. The
// handshake runs behind the gate.
func TestPanelFeedOverWebSocket(t *testing.T) {
	h := newHarness(t)
	desk := h.as("frontdesk")
	loc := h.room(desk, "Kaede")
	wsURL := "ws" + strings.TrimPrefix(h.anon.URL(), "http") + loc + "/live"
	// getatest serves on httptest's in-memory network at example.com, a
	// real host: the dialer dials through the client, never the internet.
	dialer := websocket.Dialer{NetDialContext: h.anon.DialContext}

	// No token: the gate refuses before anything switches.
	_, res, err := dialer.DialContext(t.Context(), wsURL, nil)
	if err == nil || res == nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous dial: %v %v", res, err)
	}
	// A plain GET lacks the handshake's headers, which are required
	// parameters: a 400 naming them. One that carries them but does not ask
	// to switch is a 426 naming the protocol.
	up := h.as("viewer").Upgrade(loc+"/live", "")
	if up.Switched || up.Response.Status != http.StatusBadRequest || !strings.Contains(string(up.Response.Body), "Sec-WebSocket-Key") {
		t.Fatalf("plain GET: %d %s", up.Response.Status, up.Response.Body)
	}
	plain := h.as("viewer").With("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==").With("Sec-WebSocket-Version", "13")
	if r := plain.Get(loc + "/live"); r.Status != http.StatusUpgradeRequired || r.Header.Get("Upgrade") != "websocket" {
		t.Fatalf("a GET that does not ask to switch: %d %v", r.Status, r.Header)
	}

	conn, res, err := dialer.DialContext(t.Context(), wsURL, http.Header{"Authorization": {"Bearer " + h.token("viewer")}})
	if err != nil {
		t.Fatalf("dial: %v %v", res, err)
	}
	defer conn.Close()
	if res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatal(res.StatusCode)
	}
	// The subscription is taken once the connection has switched; a ping
	// round trip shows the server is reading.
	pong := make(chan struct{}, 1)
	conn.SetPongHandler(func(string) error { pong <- struct{}{}; return nil })
	msgs := make(chan []byte, 8)
	go func() {
		// Reading runs the pong handler; messages go to msgs.
		for {
			_, b, err := conn.ReadMessage()
			if err != nil {
				close(msgs)
				return
			}
			msgs <- b
		}
	}()
	if err := conn.WriteControl(websocket.PingMessage, []byte("hi"), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pong:
	case <-time.After(5 * time.Second):
		t.Fatal("no pong")
	}

	status(t, h.as("member").Post(loc+"/bookings", map[string]any{"title": "Retro", "date": "2026-10-05", "start": "15:00:00+09:00",
		"length": "PT45M", "organizer": "ada@example.com"}), http.StatusCreated)
	select {
	case b := <-msgs:
		var u live.Update
		if err := json.Unmarshal(b, &u); err != nil || u.Kind != "booked" || u.Title != "Retro" || u.Start != "15:00:00+09:00" {
			t.Fatalf("%s %v", b, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no update")
	}
	if err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-msgs:
		if ok {
			t.Fatal("a message after close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not close")
	}
}
