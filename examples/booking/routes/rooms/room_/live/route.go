package live

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"uuid"

	"github.com/coder/websocket"
	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/booking/app"
	"github.com/koji-1009/geta/examples/booking/failures"
	hub "github.com/koji-1009/geta/examples/booking/live"
	"github.com/koji-1009/geta/examples/booking/model"
)

// Route is /rooms/{room}/live: a WebSocket a door panel holds open, on
// which the room's booking changes arrive as JSON text messages. The switch
// is a returned value, so the gate runs first: a panel without a token gets
// a 401, never a socket.
func Route(env *app.Env) geta.Route {
	h := Handler{Rooms: env.Store, Live: env.Live}
	return geta.Route{
		Get: geta.Op(http.StatusSwitchingProtocols, h.Get, geta.Doc{
			Summary:     "A room's live booking feed over WebSocket",
			OperationID: "watchRoom",
			Failures:    []geta.Failure{failures.RoomNotFound},
		}),
	}
}

type Rooms interface {
	Room(ctx context.Context, id uuid.UUID) (*model.Room, error)
}

type Feed interface {
	Subscribe(room uuid.UUID) (<-chan hub.Update, func())
}

type Handler struct {
	Rooms Rooms
	Live  Feed
}

// GetIn is the opening handshake's request (RFC 6455 section 4.1). geta
// checks Connection and Upgrade itself; the key and the version are
// declared, so geta answers a malformed one 400 before the library runs, and
// the document lists them.
type GetIn struct {
	Room    uuid.UUID `path:"room"`
	Key     string    `header:"Sec-WebSocket-Key" schema:"pattern=^[A-Za-z0-9+/]{22}==$"`
	Version string    `header:"Sec-WebSocket-Version" schema:"enum=13"`
}

func (h Handler) Get(ctx context.Context, in *GetIn) (*geta.Upgrade, error) {
	if _, err := h.Rooms.Room(ctx, in.Room); err != nil {
		return nil, err
	}
	return &geta.Upgrade{
		Protocol: "websocket",
		// coder/websocket performs the handshake and frames the protocol;
		// geta has run the gate and checked the request asks to switch.
		Accept: func(w http.ResponseWriter, r *http.Request) {
			c, err := websocket.Accept(w, r, nil)
			if err != nil {
				return // Accept has answered the refusal
			}
			defer c.CloseNow()
			// The subscription is taken once the connection has switched,
			// so a request geta answers 426 leaves nothing behind.
			updates, leave := h.Live.Subscribe(in.Room)
			defer leave()
			// A panel sends only control frames: CloseRead answers pings and
			// the closing handshake, and ends ctx when the panel goes.
			ctx := c.CloseRead(r.Context())
			for {
				select {
				case u := <-updates:
					b, err := json.Marshal(u)
					if err != nil || c.Write(ctx, websocket.MessageText, b) != nil {
						return
					}
				case <-ctx.Done():
					return
				}
			}
		},
	}, nil
}
