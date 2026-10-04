package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/koji-1009/geta/examples/register-sql/conformance"
	"github.com/koji-1009/geta/examples/register/registertest"
	"github.com/koji-1009/geta/examples/register/store"
)

func TestConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) *sql.DB {
		db, err := Open(filepath.Join(t.TempDir(), "conformance.db"))
		if err != nil {
			t.Fatal(err)
		}
		return db
	}, func(t *testing.T) error {
		// A file in a directory that does not exist cannot be opened.
		db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "no", "such", "dir", "x.db"))
		if err != nil {
			return err
		}
		defer db.Close()
		return db.Ping()
	}, Classify)
}

// The register example's routes, unchanged, over SQLite: a taken id is a
// 409 because the adapter made the engine's error store.ErrConflict.
func TestRegisterExample(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "register.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	users, err := store.NewSQL(context.Background(), Users(db))
	if err != nil {
		t.Fatal(err)
	}
	registertest.Run(t, users)
}

func TestForeignKeyIsAConflict(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "fk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		"create table parent (id text primary key)",
		"create table child (id text primary key, parent text references parent(id))",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.ExecContext(ctx, "insert into child (id, parent) values ($1, $2)", "c", "missing")
	if err := Classify(err); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
}

// A path holding URI syntax opens that exact file, and the pragmas still
// apply to it.
func TestOpensTheExactPath(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"a?b.db", "a%41.db", "a#b.db"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			db, err := Open(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var fk int64
			if err := db.QueryRowContext(ctx, "pragma foreign_keys").Scan(&fk); err != nil {
				t.Fatal(err)
			}
			if fk != 1 {
				t.Errorf("foreign_keys = %d", fk)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			if !slices.Contains(names, name) {
				t.Errorf("files in the directory: %q, want %q", names, name)
			}
		})
	}
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var seq int64
	var schema, file string
	if err := db.QueryRowContext(ctx, "pragma database_list").Scan(&seq, &schema, &file); err != nil {
		t.Fatal(err)
	}
	if file != "" {
		t.Errorf(":memory: is backed by %q", file)
	}
}

func TestUnopenableIsUnavailable(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "no", "such", "dir", "x.db"))
	if !errors.Is(err, store.ErrUnavailable) {
		t.Fatal(err)
	}
}
