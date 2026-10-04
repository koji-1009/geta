package store

import "errors"

// The database errors, driver-independent, so a failure table can map them
// without a route importing a driver. The memory store returns ErrConflict
// for a taken id, so it and an SQL engine fail the same way and one failure
// row serves both.
//
// The SQL store's engine supplies a classifier (examples/register-sql has
// one for SQLite and one for PostgreSQL) that wraps a driver error in one of
// these, keeping the driver's error reachable:
//
//	fmt.Errorf("%w: %w", store.ErrConflict, err)
//
// and returns any other error unchanged. A classifier maps a uniqueness
// violation to ErrConflict, and an unreachable database or an unobtainable
// lock to ErrUnavailable. An engine that reports serialization failures and
// deadlocks maps them to ErrTransient, and a foreign-key violation to
// ErrConflict. Match them with errors.Is; the driver's error stays reachable
// with errors.As.
var (
	// ErrConflict means the write collides with existing data.
	ErrConflict = errors.New("store: conflict")
	// ErrUnavailable means the database could not be reached, or a lock
	// could not be acquired in time.
	ErrUnavailable = errors.New("store: database unavailable")
	// ErrTransient means the database aborted a transaction that may
	// succeed if retried: a serialization failure or a deadlock.
	ErrTransient = errors.New("store: transient failure")
)
