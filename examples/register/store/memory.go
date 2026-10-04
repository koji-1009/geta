// Package store keeps users. It knows nothing of HTTP: it returns its own
// errors, and each route's failure table decides what they become.
package store

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/koji-1009/geta/examples/register/model"
)

// Errors the store returns, beside the database errors of dberrors.go.
var (
	ErrNotFound  = errors.New("store: not found")
	ErrLastAdmin = errors.New("store: the last admin cannot be demoted or deleted")
	// ErrRoleChange: a write that would set a role other than through
	// SetRole, which guards the last admin.
	ErrRoleChange = errors.New("store: a role is set only by SetRole")
)

// Users is everything the routes need from a store, across all of them.
type Users interface {
	List(ctx context.Context, f Filter) ([]model.User, int, error)
	Find(ctx context.Context, id string) (*model.User, error)
	Create(ctx context.Context, u model.User) error
	CreateMany(ctx context.Context, us []model.User) error
	Replace(ctx context.Context, u model.User, check func(current model.User) error) (*model.User, error)
	SetRole(ctx context.Context, id string, role model.Role) (*model.User, error)
	Delete(ctx context.Context, id string) error
}

// Filter narrows a listing.
type Filter struct {
	Role   *model.Role
	Limit  int
	Offset int
}

// Memory is an in-memory store, safe for concurrent use.
type Memory struct {
	mu    sync.RWMutex
	users map[string]model.User
	now   func() time.Time
}

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{users: map[string]model.User{}, now: time.Now}
}

// List returns one page of users ordered by id, and the size of the match.
func (m *Memory) List(ctx context.Context, f Filter) ([]model.User, int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var all []model.User
	for _, u := range m.users {
		if f.Role == nil || u.Role == *f.Role {
			all = append(all, u)
		}
	}
	slices.SortFunc(all, func(a, b model.User) int { return cmp.Compare(a.ID, b.ID) })
	total := len(all)
	lo := min(f.Offset, total)
	hi := min(lo+f.Limit, total)
	return all[lo:hi], total, nil
}

// Find returns the user with id.
func (m *Memory) Find(ctx context.Context, id string) (*model.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.users[id]
	if !ok {
		return nil, ErrNotFound
	}
	return &u, nil
}

// Create stores a new user, stamping its creation time.
func (m *Memory) Create(ctx context.Context, u model.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.users[u.ID]; ok {
		return ErrConflict
	}
	now := m.now().UTC()
	u.CreatedAt = &now
	if u.Active == nil {
		active := true
		u.Active = &active
	}
	m.users[u.ID] = u
	return nil
}

// CreateMany stores every user or none: one taken id, or one id given
// twice, fails the whole batch with ErrConflict.
func (m *Memory) CreateMany(ctx context.Context, us []model.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	for _, u := range us {
		if _, ok := m.users[u.ID]; ok || seen[u.ID] {
			return fmt.Errorf("user %q: %w", u.ID, ErrConflict)
		}
		seen[u.ID] = true
	}
	now := m.now().UTC()
	for _, u := range us {
		u.CreatedAt = &now
		if u.Active == nil {
			active := true
			u.Active = &active
		}
		m.users[u.ID] = u
	}
	return nil
}

// SetRole changes a user's role. The last admin cannot be demoted.
func (m *Memory) SetRole(ctx context.Context, id string, role model.Role) (*model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return nil, ErrNotFound
	}
	if u.Role == model.RoleAdmin && role != model.RoleAdmin {
		admins := 0
		for _, o := range m.users {
			if o.Role == model.RoleAdmin {
				admins++
			}
		}
		if admins == 1 {
			return nil, ErrLastAdmin
		}
	}
	u.Role = role
	m.users[id] = u
	return &u, nil
}

// Replace overwrites an existing user, keeping its creation time. check, if
// not nil, is given the stored user first, under the store's lock, and an
// error from it is Replace's, with nothing written. The role is not
// Replace's to change: a role other than the stored one is ErrRoleChange.
func (m *Memory) Replace(ctx context.Context, u model.User, check func(current model.User) error) (*model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.users[u.ID]
	if !ok {
		return nil, ErrNotFound
	}
	if check != nil {
		if err := check(old); err != nil {
			return nil, err
		}
	}
	if u.Role != old.Role {
		return nil, ErrRoleChange
	}
	u.CreatedAt = old.CreatedAt
	if u.Active == nil {
		u.Active = old.Active
	}
	m.users[u.ID] = u
	return &u, nil
}

// Delete removes the user with id. The last admin cannot be deleted.
func (m *Memory) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return ErrNotFound
	}
	if u.Role == model.RoleAdmin {
		admins := 0
		for _, o := range m.users {
			if o.Role == model.RoleAdmin {
				admins++
			}
		}
		if admins == 1 {
			return ErrLastAdmin
		}
	}
	delete(m.users, id)
	return nil
}
