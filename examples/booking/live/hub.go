// Package live fans a room's booking changes out to the room's door panels.
// One process shares memory, so a hub is a map of channels.
package live

import (
	"sync"
	"uuid"
)

// Update is one message on a room's live feed.
type Update struct {
	Kind    string    `json:"kind"` // booked, changed, cancelled
	Booking uuid.UUID `json:"booking"`
	Title   string    `json:"title"`
	Date    string    `json:"date"`
	Start   string    `json:"start"`
}

// Hub delivers each update to every current subscriber of its room. A
// subscriber that cannot keep up loses updates rather than holding up the
// writer.
type Hub struct {
	mu   sync.Mutex
	subs map[uuid.UUID]map[chan Update]struct{}
}

// NewHub returns an empty hub.
func NewHub() *Hub { return &Hub{subs: map[uuid.UUID]map[chan Update]struct{}{}} }

// Publish delivers u to the room's subscribers.
func (h *Hub) Publish(room uuid.UUID, u Update) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[room] {
		select {
		case ch <- u:
		default:
		}
	}
}

// Subscribe subscribes to room until leave is called.
func (h *Hub) Subscribe(room uuid.UUID) (updates <-chan Update, leave func()) {
	ch := make(chan Update, 16)
	h.mu.Lock()
	if h.subs[room] == nil {
		h.subs[room] = map[chan Update]struct{}{}
	}
	h.subs[room][ch] = struct{}{}
	h.mu.Unlock()
	return ch, sync.OnceFunc(func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.subs[room], ch)
		if len(h.subs[room]) == 0 {
			delete(h.subs, room)
		}
	})
}
