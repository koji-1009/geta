// Package postgres is an example adapter for PostgreSQL, over pgx's
// database/sql driver: Open, and Classify, which classifies errors by
// SQLSTATE into the register store's database errors, so a handler never
// imports the driver. geta ships no adapter: an application writes its own
// like this one and checks its classifier with a conformance suite, as
// postgres_test.go does with this module's conformance package.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/koji-1009/geta/examples/register/store"
)

// Open connects with a postgres:// URL and checks the connection.
func Open(ctx context.Context, url string) (*sql.DB, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("postgres: %w", Classify(err))
	}
	return db, nil
}

// Classify wraps a driver error that is one of the store's database errors
// in that error, and returns any other error unchanged.
func Classify(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch {
		case pe.Code == "23505", pe.Code == "23503": // unique, foreign key
			return fmt.Errorf("%w: %w", store.ErrConflict, err)
		case pe.Code == "40001", pe.Code == "40P01": // serialization failure, deadlock
			return fmt.Errorf("%w: %w", store.ErrTransient, err)
		case pe.Code == "55P03", pe.Code == "57P01", pe.Code == "57P02", pe.Code == "57P03", strings.HasPrefix(pe.Code, "08"):
			// lock not available, shutdown, cannot connect now, connection exception
			return fmt.Errorf("%w: %w", store.ErrUnavailable, err)
		}
		return err
	}
	// A cancelled or expired context is the caller's, not the database's:
	// context.DeadlineExceeded satisfies net.Error, so it is returned as it
	// is before that check, as the sqlite adapter returns it.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var ne net.Error
	var ce *pgconn.ConnectError
	if errors.As(err, &ce) || errors.As(err, &ne) || errors.Is(err, sql.ErrConnDone) {
		return fmt.Errorf("%w: %w", store.ErrUnavailable, err)
	}
	return err
}

// Users is db as the register example's SQL store takes it: PostgreSQL holds
// a boolean as a boolean and a timestamp as a timestamptz.
func Users(db *sql.DB) store.Engine {
	return store.Engine{DB: db, Classify: Classify, BoolType: "boolean", TimeType: "timestamptz"}
}
