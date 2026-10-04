package events

import (
	"context"
	"testing"
	"time"
)

func subscribers(h *Hub) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// An event published after Subscribe returns and before the iterator starts
// still arrives: geta starts the iterator only after the response began.
func TestSubscribeBeforeIterating(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	seq := h.Subscribe(ctx)
	h.Publish(UserEvent{Kind: "created", ID: "1"})
	for e := range seq {
		if e.ID != "1" {
			t.Fatal(e)
		}
		break
	}
	if n := subscribers(h); n != 0 {
		t.Fatalf("%d subscribers after the iterator stopped", n)
	}
}

// A subscription whose iterator never runs is removed when ctx ends.
func TestSubscriptionEndsWithContext(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(t.Context())
	h.Subscribe(ctx)
	if n := subscribers(h); n != 1 {
		t.Fatalf("%d subscribers", n)
	}
	cancel()
	// context.AfterFunc runs the removal in its own goroutine.
	deadline := time.Now().Add(5 * time.Second)
	for subscribers(h) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the subscription outlived its context")
		}
		time.Sleep(time.Millisecond)
	}
}
