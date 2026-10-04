// Package sqlite is an example adapter for SQLite, over modernc.org/sqlite
// (pure Go, no cgo): Open, and Classify, which wraps the driver's errors in
// the register store's database errors, so a handler never imports the
// driver. geta ships no adapter: an application writes its own like this one
// and checks its classifier with a conformance suite, as sqlite_test.go does
// with this module's conformance package.
package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/koji-1009/geta/examples/register/store"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Open opens the database file at path (":memory:" for a private in-memory
// database), with foreign keys on and a busy timeout, so a locked database
// waits before it reports ErrUnavailable. SQLite has one writer, so the pool
// holds one connection.
func Open(path string) (*sql.DB, error) {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(wal)")
	db, err := sql.Open("sqlite", "file:"+uriPath.Replace(path)+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: %w", Classify(err))
	}
	return db, nil
}

// uriPath escapes what SQLite's URI syntax would read as other than the
// file name: % starts an escape, ? the parameters, # the fragment.
var uriPath = strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")

// Classify wraps a driver error that is one of the store's database errors
// in that error, and returns any other error unchanged.
func Classify(err error) error {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return err
	}
	switch se.Code() {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY, sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
		return fmt.Errorf("%w: %w", store.ErrConflict, err)
	}
	switch se.Code() & 0xff {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED, sqlite3.SQLITE_CANTOPEN:
		return fmt.Errorf("%w: %w", store.ErrUnavailable, err)
	}
	if strings.Contains(err.Error(), "database is locked") {
		return fmt.Errorf("%w: %w", store.ErrUnavailable, err)
	}
	return err
}

// Users is db as the register example's SQL store takes it. SQLite has no
// boolean or timestamp type: a boolean is an integer 0 or 1, and a timestamp
// is RFC 3339 text.
func Users(db *sql.DB) store.Engine {
	return store.Engine{DB: db, Classify: Classify, BoolType: "integer", TimeType: "text"}
}
