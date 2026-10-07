// Package teams keeps teams of users, in memory.
package teams

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
)

// Errors the team store returns.
var (
	ErrTeamNotFound  = errors.New("teams: team not found")
	ErrTeamExists    = errors.New("teams: team exists")
	ErrTeamNotEmpty  = errors.New("teams: team has members")
	ErrAlreadyMember = errors.New("teams: already a member")
	ErrNotMember     = errors.New("teams: not a member")
)

// Team is a named group of users.
type Team struct {
	ID          string   `json:"id" schema:"minLength=1,maxLength=64,pattern=^[a-z0-9-]+$"`
	Name        string   `json:"name" schema:"minLength=1,maxLength=100"`
	Description *string  `json:"description,omitzero" doc:"What the team is for; absent when it has none" schema:"maxLength=500"`
	Members     []string `json:"members"`
}

// Memory is a team store, safe for concurrent use.
type Memory struct {
	mu    sync.Mutex
	teams map[string]*Team
	// versions holds each team's version, from one counter for the whole
	// store, so a team deleted and created again never repeats a tag a
	// client may still hold.
	versions map[string]int
	seq      int
}

// NewMemory returns an empty store.
func NewMemory() *Memory { return &Memory{teams: map[string]*Team{}, versions: map[string]int{}} }

// touch gives the team id a new version. The caller holds mu.
func (m *Memory) touch(id string) {
	m.seq++
	m.versions[id] = m.seq
}

// tag is the entity tag of the team id's current version. The caller holds
// mu.
func (m *Memory) tag(id string) string { return `"t` + strconv.Itoa(m.versions[id]) + `"` }

// copyOf is a copy of t that shares nothing with the store.
func copyOf(t *Team) *Team {
	c := *t
	c.Members = slices.Clone(t.Members)
	if t.Description != nil {
		d := *t.Description
		c.Description = &d
	}
	return &c
}

// List returns every team, ordered by id.
func (m *Memory) List(ctx context.Context) ([]Team, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Team, 0, len(m.teams))
	for _, t := range m.teams {
		out = append(out, *copyOf(t))
	}
	slices.SortFunc(out, func(a, b Team) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

// Create stores a new team with no members.
func (m *Memory) Create(ctx context.Context, t Team) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.teams[t.ID]; ok {
		return ErrTeamExists
	}
	t.Members = nil
	m.teams[t.ID] = copyOf(&t)
	m.touch(t.ID)
	return nil
}

// Find returns the team with id and its entity tag.
func (m *Memory) Find(ctx context.Context, id string) (*Team, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.teams[id]
	if !ok {
		return nil, "", ErrTeamNotFound
	}
	return copyOf(t), m.tag(id), nil
}

// Put creates the team t.ID if there is none, or replaces its name and
// description if there is; its members are kept, as they change only through
// AddMember and RemoveMember. check is called first, under the store's lock,
// with the stored team and its tag, or nil and "" when there is none; an
// error from it is Put's, with nothing written. created reports which it
// was.
func (m *Memory) Put(ctx context.Context, t Team, check func(current *Team, tag string) error) (team *Team, tag string, created bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.teams[t.ID]
	if !ok {
		if err := check(nil, ""); err != nil {
			return nil, "", false, err
		}
		t.Members = nil
		m.teams[t.ID] = copyOf(&t)
		m.touch(t.ID)
		return copyOf(&t), m.tag(t.ID), true, nil
	}
	if err := check(copyOf(current), m.tag(t.ID)); err != nil {
		return nil, "", false, err
	}
	t.Members = current.Members
	m.teams[t.ID] = copyOf(&t)
	m.touch(t.ID)
	return copyOf(&t), m.tag(t.ID), false, nil
}

// Update changes the team id by apply, which is given the stored team and
// its tag under the store's lock and returns the team to store; an error
// from it is Update's, with nothing written. The id and the members are the
// store's to keep: apply's changes to them are ignored.
func (m *Memory) Update(ctx context.Context, id string, apply func(current Team, tag string) (Team, error)) (*Team, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.teams[id]
	if !ok {
		return nil, "", ErrTeamNotFound
	}
	next, err := apply(*copyOf(current), m.tag(id))
	if err != nil {
		return nil, "", err
	}
	next.ID, next.Members = id, current.Members
	m.teams[id] = copyOf(&next)
	m.touch(id)
	return copyOf(&next), m.tag(id), nil
}

// Delete removes an empty team. check is called first, under the store's
// lock, with the stored team and its tag; an error from it is Delete's, with
// nothing removed.
func (m *Memory) Delete(ctx context.Context, id string, check func(current Team, tag string) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.teams[id]
	if !ok {
		return ErrTeamNotFound
	}
	if err := check(*copyOf(t), m.tag(id)); err != nil {
		return err
	}
	if len(t.Members) > 0 {
		return ErrTeamNotEmpty
	}
	delete(m.teams, id)
	delete(m.versions, id)
	return nil
}

// AddMember adds user to the team.
func (m *Memory) AddMember(ctx context.Context, team, user string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.teams[team]
	if !ok {
		return ErrTeamNotFound
	}
	if slices.Contains(t.Members, user) {
		return ErrAlreadyMember
	}
	t.Members = append(t.Members, user)
	slices.Sort(t.Members)
	m.touch(team)
	return nil
}

// RemoveMember removes user from the team.
func (m *Memory) RemoveMember(ctx context.Context, team, user string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.teams[team]
	if !ok {
		return ErrTeamNotFound
	}
	i := slices.Index(t.Members, user)
	if i < 0 {
		return ErrNotMember
	}
	t.Members = slices.Delete(t.Members, i, i+1)
	m.touch(team)
	return nil
}
