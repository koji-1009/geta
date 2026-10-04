// Package events fans user changes out to live subscribers. One process
// shares memory, so a hub is a map of channels; nothing crosses processes.
package events

import (
	"context"
	"iter"
	"sync"
)

// UserEvent is one change on the /users/events feed.
type UserEvent struct {
	Kind string `json:"kind" schema:"enum=created|updated|deleted"`
	ID   string `json:"id"`
}

// EventName names the SSE event after the kind of change.
func (e UserEvent) EventName() string { return e.Kind }

// Hub delivers each published event to every current subscriber, at most
// once and with no replay. A subscriber that cannot keep up loses events
// rather than holding up the publisher.
type Hub struct {
	mu   sync.Mutex
	subs map[chan UserEvent]struct{}
}

// NewHub returns an empty hub.
func NewHub() *Hub { return &Hub{subs: map[chan UserEvent]struct{}{}} }

// Publish delivers e to the current subscribers.
func (h *Hub) Publish(e UserEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Subscribe subscribes now and yields events until ctx ends. The
// subscription is in place when Subscribe returns, not when the iterator
// starts: geta starts a stream's iterator only after the response has
// begun, and an event published in between still arrives. It is removed
// when ctx ends or the iterator stops, whichever comes first.
func (h *Hub) Subscribe(ctx context.Context) iter.Seq[UserEvent] {
	ch := make(chan UserEvent, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	leave := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
		})
	}
	stop := context.AfterFunc(ctx, leave)
	return func(yield func(UserEvent) bool) {
		defer func() {
			stop()
			leave()
		}()
		for {
			select {
			case e := <-ch:
				if !yield(e) {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}
}
