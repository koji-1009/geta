package teams

import (
	"strings"
	"sync"
	"testing"
)

// A listed team's members are a copy: reading them while members are added
// and removed is no race (run with -race).
func TestListCopiesMembers(t *testing.T) {
	ctx := t.Context()
	m := NewMemory()
	if err := m.Create(ctx, Team{ID: "core", Name: "Core"}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"a", "c", "e"} {
		if err := m.AddMember(ctx, "core", u); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 1000 {
			m.AddMember(ctx, "core", "b")
			m.RemoveMember(ctx, "core", "b")
		}
	})
	wg.Go(func() {
		for range 1000 {
			list, err := m.List(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			_ = strings.Join(list[0].Members, ",")
		}
	})
	wg.Wait()
	list, _ := m.List(ctx)
	list[0].Members[0] = "z"
	if got, _, _ := m.Find(ctx, "core"); got.Members[0] != "a" {
		t.Fatalf("writing to a listed team changed the store: %v", got.Members)
	}
}
