package idempotency

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestDoRunsOncePerKey(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		k := New[int](time.Hour)
		runs := 0
		f := func() (int, error) { runs++; return runs, nil }

		if v, replayed, err := k.Do("a", "fp1", f); v != 1 || replayed || err != nil {
			t.Fatalf("first: %d %v %v", v, replayed, err)
		}
		if v, replayed, err := k.Do("a", "fp1", f); v != 1 || !replayed || err != nil || runs != 1 {
			t.Fatalf("a retry: %d %v %v, %d runs", v, replayed, err, runs)
		}
		if _, _, err := k.Do("a", "fp2", f); !errors.Is(err, ErrKeyReused) {
			t.Fatalf("another request under the key: %v", err)
		}
		if v, _, _ := k.Do("b", "fp1", f); v != 2 {
			t.Fatalf("another key: %d", v)
		}

		// A failure keeps nothing: the retry runs.
		boom := errors.New("boom")
		if _, _, err := k.Do("c", "fp", func() (int, error) { return 0, boom }); err != boom {
			t.Fatal(err)
		}
		if v, replayed, err := k.Do("c", "fp", f); v != 3 || replayed || err != nil {
			t.Fatalf("a retry after a failure: %d %v %v", v, replayed, err)
		}
		// Nor does a panic, which a Recover outside turns into a 500.
		func() {
			defer func() { recover() }()
			k.Do("p", "fp", func() (int, error) { panic("boom") })
		}()
		if _, _, err := k.Do("p", "fp", func() (int, error) { return 0, nil }); err != nil {
			t.Fatalf("a retry after a panic: %v", err)
		}

		// While the first request runs, a second with its key is told so.
		started, release := make(chan struct{}), make(chan struct{})
		go k.Do("d", "fp", func() (int, error) { close(started); <-release; return 9, nil })
		<-started
		if _, _, err := k.Do("d", "fp", f); !errors.Is(err, ErrInFlight) {
			t.Fatalf("a request while the first runs: %v", err)
		}
		close(release)
		synctest.Wait()
		if v, replayed, _ := k.Do("d", "fp", f); v != 9 || !replayed {
			t.Fatalf("after the first finished: %d %v", v, replayed)
		}

		// A result is kept for the TTL, then the key is free again.
		time.Sleep(time.Hour)
		if v, replayed, _ := k.Do("a", "fp2", f); v != 4 || replayed {
			t.Fatalf("after the TTL: %d %v", v, replayed)
		}
	})
}
