// Package idempotency makes a non-idempotent request safe to retry: a client
// sends an Idempotency-Key with a POST, and a retry with the same key gets
// the result of the first request rather than a second write
// (draft-ietf-httpapi-idempotency-key-header).
package idempotency

import (
	"errors"
	"sync"
	"time"
)

// Errors a keyed request can meet. The draft answers each with a status: 422
// for a key reused with another request, 409 for a key whose first request
// is still being served.
var (
	ErrKeyReused = errors.New("idempotency: the key was used with another request")
	ErrInFlight  = errors.New("idempotency: a request with the key is still being served")
)

// Keys remembers the result of each keyed request for TTL, safe for
// concurrent use. T is what a replay answers.
type Keys[T any] struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]*entry[T]
	// sweepAt is when Do next removes the expired results.
	sweepAt time.Time
}

type entry[T any] struct {
	fingerprint string
	done        bool
	result      T
	expires     time.Time
}

// New returns Keys that remember a result for ttl; the draft suggests a day.
func New[T any](ttl time.Duration) *Keys[T] {
	return &Keys[T]{ttl: ttl, now: time.Now, entries: map[string]*entry[T]{}}
}

// Do runs f once per key. The first request with key runs f, and its
// result, if f succeeds, is kept; a later request with key and the same
// fingerprint (a digest of what it asks for) gets that result, with
// replayed true, and f does not run. A request with key and another
// fingerprint is ErrKeyReused; one that arrives while the first is running
// is ErrInFlight. When f fails, nothing is kept, so the client may retry
// with the same key.
//
// key must name the caller as well as the client's key: two callers may
// choose the same key.
func (k *Keys[T]) Do(key, fingerprint string, f func() (T, error)) (result T, replayed bool, err error) {
	k.mu.Lock()
	now := k.now()
	if !now.Before(k.sweepAt) {
		for name, e := range k.entries {
			if e.done && !now.Before(e.expires) {
				delete(k.entries, name)
			}
		}
		k.sweepAt = now.Add(time.Minute)
	}
	if e, ok := k.entries[key]; ok && (!e.done || now.Before(e.expires)) {
		defer k.mu.Unlock()
		switch {
		case e.fingerprint != fingerprint:
			return result, false, ErrKeyReused
		case !e.done:
			return result, false, ErrInFlight
		}
		return e.result, true, nil
	}
	e := &entry[T]{fingerprint: fingerprint}
	k.entries[key] = e
	k.mu.Unlock()

	// A panic in f leaves nothing kept, as an error does; otherwise the key
	// would answer ErrInFlight until the process ends.
	kept := false
	defer func() {
		if !kept {
			k.mu.Lock()
			delete(k.entries, key)
			k.mu.Unlock()
		}
	}()
	result, err = f()
	if err != nil {
		return result, false, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	e.done, e.result, e.expires = true, result, k.now().Add(k.ttl)
	kept = true
	return result, false, nil
}
