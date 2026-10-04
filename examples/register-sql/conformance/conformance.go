// Package conformance is the conformance suite for an adapter's classifier,
// the function that wraps a driver error in one of the register store's
// database errors, run against a live engine.
//
// The suite's SQL has no placeholders, so it runs on any engine.
package conformance

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/koji-1009/geta/examples/register/store"
)

// Run runs the suite. open returns a fresh connection to an engine where the
// test may create and drop tables. unreachable returns the unclassified
// driver error for a database it cannot reach, such as a ping of a server
// that does not listen. classify is the adapter's classifier.
//
// Run does not check the lock case, since the lock wait is an engine
// setting (SQLite's busy_timeout, PostgreSQL's lock_timeout); an adapter
// tests that an unobtainable lock is store.ErrUnavailable itself.
func Run(t *testing.T, open func(t *testing.T) *sql.DB, unreachable func(t *testing.T) error, classify func(error) error) {
	ctx := context.Background()
	const insert = "insert into geta_conformance (id) values ('a')"
	setup := func(t *testing.T) *sql.DB {
		t.Helper()
		db := open(t)
		mustExec(t, db, "drop table if exists geta_conformance")
		mustExec(t, db, "create table geta_conformance (id text primary key)")
		t.Cleanup(func() {
			db.ExecContext(ctx, "drop table if exists geta_conformance")
			db.Close()
		})
		mustExec(t, db, insert)
		return db
	}

	t.Run("a uniqueness violation is ErrConflict, and the driver's error stays reachable", func(t *testing.T) {
		db := setup(t)
		_, driver := db.ExecContext(ctx, insert)
		if driver == nil {
			t.Fatal("a duplicate key was accepted")
		}
		err := classify(driver)
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("got %v", err)
		}
		if !errors.Is(err, driver) {
			t.Fatalf("the driver's error is lost: %v", err)
		}
	})

	t.Run("a uniqueness violation inside a transaction is ErrConflict", func(t *testing.T) {
		db := setup(t)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		_, err = tx.ExecContext(ctx, insert)
		if !errors.Is(classify(err), store.ErrConflict) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("an unreachable database is ErrUnavailable, and the driver's error stays reachable", func(t *testing.T) {
		driver := unreachable(t)
		if driver == nil {
			t.Fatal("the unreachable database was reached")
		}
		err := classify(driver)
		if !errors.Is(err, store.ErrUnavailable) {
			t.Fatalf("got %v", err)
		}
		if !errors.Is(err, driver) {
			t.Fatalf("the driver's error is lost: %v", err)
		}
	})
}

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
