// Package failures holds the failure rows the register routes share. A
// failure table is a slice, so sharing is a variable and slices.Concat.
package failures

import (
	"net/http"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/examples/register/store"
)

// Single rows. Rows that share a status with another cause carry a problem
// type, so a client tells them apart by type rather than by the detail's
// words: two 409s, and a 403 beside the administrator check's.
var (
	UserNotFound = geta.On(store.ErrNotFound, http.StatusNotFound, "user not found")
	UserExists   = geta.On(store.ErrConflict, http.StatusConflict, "a user with this id exists").Type("/problems/user-exists")
	// RoleChange: a create or a replace that would set a role. Roles change
	// only through the admin route, which guards the last admin.
	RoleChange = geta.On(store.ErrRoleChange, http.StatusForbidden, "a role is set only through /admin/users/{id}/role").Type("/problems/role-change")
	// LastAdmin: a demotion or a deletion that would leave no admin.
	LastAdmin = geta.On(store.ErrLastAdmin, http.StatusConflict, "the last admin cannot be demoted or deleted").Type("/problems/last-admin")
)

// Store is what any route that reaches the database can meet: the database
// out of reach, or a transaction it asks to run again.
var Store = []geta.Failure{
	geta.On(store.ErrUnavailable, http.StatusServiceUnavailable, "the store is unavailable"),
	geta.On(store.ErrTransient, http.StatusServiceUnavailable, "the store asks to retry"),
}
