// Package store keeps each user's bookmarks in memory.
package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"uuid"
)

// ErrNotFound is a bookmark that does not exist, or is another user's.
var ErrNotFound = errors.New("store: bookmark not found")

// Bookmark is a saved link.
type Bookmark struct {
	ID        uuid.UUID `json:"id"`
	URL       string    `json:"url" schema:"minLength=1,maxLength=2000,pattern=^https?://"`
	Title     string    `json:"title" schema:"minLength=1,maxLength=200"`
	Tags      []string  `json:"tags" schema:"maxItems=10,uniqueItems=true"`
	CreatedAt time.Time `json:"createdAt"`
}

// Search narrows a listing: every word of Text in the title or the URL,
// every one of Tags, at most Limit bookmarks (0 is no limit).
type Search struct {
	Text  string
	Tags  []string
	Limit int
}

// Memory is the store, safe for concurrent use.
type Memory struct {
	mu    sync.Mutex
	now   func() time.Time
	owned map[string][]Bookmark // by owner, oldest first
}

// NewMemory returns an empty store.
func NewMemory() *Memory { return &Memory{now: time.Now, owned: map[string][]Bookmark{}} }

// Add stores a bookmark of owner's under a new id.
func (m *Memory) Add(ctx context.Context, owner string, b Bookmark) (Bookmark, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b.ID, b.CreatedAt = uuid.NewV7(), m.now().UTC().Truncate(time.Second)
	b.Tags = slices.Clone(b.Tags)
	m.owned[owner] = append(m.owned[owner], b)
	return b, nil
}

// Find returns one of owner's bookmarks.
func (m *Memory) Find(ctx context.Context, owner string, id uuid.UUID) (Bookmark, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.owned[owner] {
		if b.ID == id {
			b.Tags = slices.Clone(b.Tags)
			return b, nil
		}
	}
	return Bookmark{}, ErrNotFound
}

// List returns owner's bookmarks that s matches, newest first.
func (m *Memory) List(ctx context.Context, owner string, s Search) ([]Bookmark, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	words := strings.Fields(strings.ToLower(s.Text))
	out := []Bookmark{}
	all := m.owned[owner]
	for i := len(all) - 1; i >= 0; i-- {
		b := all[i]
		text := strings.ToLower(b.Title + " " + b.URL)
		if !allOf(words, func(w string) bool { return strings.Contains(text, w) }) ||
			!allOf(s.Tags, func(t string) bool { return slices.Contains(b.Tags, t) }) {
			continue
		}
		b.Tags = slices.Clone(b.Tags)
		out = append(out, b)
		if s.Limit > 0 && len(out) == s.Limit {
			break
		}
	}
	return out, nil
}

func allOf(xs []string, ok func(string) bool) bool {
	for _, x := range xs {
		if !ok(x) {
			return false
		}
	}
	return true
}
