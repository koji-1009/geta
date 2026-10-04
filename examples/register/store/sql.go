package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/koji-1009/geta/examples/register/model"
)

// Engine is the database the SQL store keeps users in, as an adapter in
// examples/register-sql supplies it. The store imports no driver: main opens
// the *sql.DB, and the adapter's classifier turns every driver error the
// store returns into one of the database errors of dberrors.go.
//
// The store's SQL numbers its placeholders ($1, $2, ...), which both example
// drivers accept: pgx, and modernc.org/sqlite, which binds $1 to the first
// argument. A driver that takes only ? would need queries of its own.
type Engine struct {
	DB *sql.DB
	// Classify is the adapter's classifier.
	Classify func(error) error
	// BoolType and TimeType are the column types a boolean and a timestamp
	// are held in: boolean and timestamptz where the engine has them,
	// integer (0 or 1) and text (RFC 3339) where it does not.
	BoolType, TimeType string
}

// SQL keeps users in a database/sql engine.
type SQL struct {
	db       *sql.DB
	classify func(error) error
	now      func() time.Time
}

// NewSQL returns a store over e, creating its table if needed.
func NewSQL(ctx context.Context, e Engine) (*SQL, error) {
	s := &SQL{db: e.DB, classify: e.Classify, now: time.Now}
	_, err := s.db.ExecContext(ctx, `create table if not exists users (
	id text primary key,
	name text not null,
	age integer,
	role text not null,
	tags text not null,
	active `+e.BoolType+` not null,
	balance text,
	created_at `+e.TimeType+` not null
)`)
	if err != nil {
		return nil, s.fail(err)
	}
	return s, nil
}

const columns = "id, name, age, role, tags, active, balance, created_at"

// fail classifies a driver error; nil and the store's own errors pass
// through the classifier unchanged.
func (s *SQL) fail(err error) error {
	if err == nil {
		return nil
	}
	return s.classify(err)
}

// tx runs f in a transaction: a nil return commits, an error or a panic
// rolls back. Its error is classified, so a uniqueness violation inside is
// ErrConflict.
func (s *SQL) tx(ctx context.Context, f func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return s.fail(err)
	}
	defer tx.Rollback() // a no-op once committed
	if err := f(tx); err != nil {
		return s.fail(err)
	}
	return s.fail(tx.Commit())
}

// scanUser reads the columns of one row. A boolean scans into a bool from a
// native boolean or from 0/1, and a timestamp into a string from text or
// from a typed column (database/sql renders it as RFC 3339).
func scanUser(row interface{ Scan(...any) error }) (model.User, error) {
	var u model.User
	var age sql.Null[int64]
	var role, tags, created string
	var active bool
	var balance sql.Null[string]
	if err := row.Scan(&u.ID, &u.Name, &age, &role, &tags, &active, &balance, &created); err != nil {
		return u, err
	}
	if age.Valid {
		n := int(age.V)
		u.Age = &n
	}
	u.Role = model.Role(role)
	if err := json.Unmarshal([]byte(tags), &u.Tags); err != nil {
		return u, err
	}
	u.Active = &active
	if balance.Valid {
		u.Balance = &balance.V
	}
	at, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return u, err
	}
	at = at.UTC()
	u.CreatedAt = &at
	return u, nil
}

// List returns one page of users ordered by id, and the size of the match.
func (s *SQL) List(ctx context.Context, f Filter) ([]model.User, int, error) {
	where, args := "", []any{}
	if f.Role != nil {
		where, args = " where role = $1", append(args, string(*f.Role))
	}
	var total int
	if err := s.db.QueryRowContext(ctx, "select count(*) from users"+where, args...).Scan(&total); err != nil {
		return nil, 0, s.fail(err)
	}
	rows, err := s.db.QueryContext(ctx, "select "+columns+" from users"+where+" order by id limit "+
		strconv.Itoa(f.Limit)+" offset "+strconv.Itoa(f.Offset), args...)
	if err != nil {
		return nil, 0, s.fail(err)
	}
	defer rows.Close()
	out := []model.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, s.fail(err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, s.fail(err)
	}
	return out, total, nil
}

// Find returns the user with id.
func (s *SQL) Find(ctx context.Context, id string) (*model.User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, "select "+columns+" from users where id = $1", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, s.fail(err)
	}
	return &u, nil
}

const insertUser = "insert into users (" + columns + ") values ($1, $2, $3, $4, $5, $6, $7, $8)"

// Create stores a new user. A taken id is the engine's uniqueness
// violation, which the adapter's classifier makes ErrConflict.
func (s *SQL) Create(ctx context.Context, u model.User) error {
	_, err := s.db.ExecContext(ctx, insertUser, insertArgs(u, s.now())...)
	return s.fail(err)
}

// insertArgs are insertUser's arguments. A bool is written as the driver
// binds it: a boolean on PostgreSQL, 1 or 0 on SQLite.
func insertArgs(u model.User, now time.Time) []any {
	tags, _ := json.Marshal(u.Tags)
	active := true
	if u.Active != nil {
		active = *u.Active
	}
	return []any{u.ID, u.Name, u.Age, string(u.Role), string(tags), active, u.Balance,
		now.UTC().Format(time.RFC3339Nano)}
}

// CreateMany stores every user or none, in one transaction.
func (s *SQL) CreateMany(ctx context.Context, us []model.User) error {
	now := s.now()
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, u := range us {
			if _, err := tx.ExecContext(ctx, insertUser, insertArgs(u, now)...); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetRole changes a user's role in one transaction; the last admin cannot
// be demoted.
//
// Reading the role and counting the admins does not by itself stop two
// demotions of the last two admins from both succeeding: under PostgreSQL's
// READ COMMITTED each counts two. So a demotion first writes every admin row
// — a no-op write, valid on every engine — which takes their row locks on
// PostgreSQL and the write lock on SQLite. A concurrent demotion waits on
// that write, and its own reads, made after it, see the first one's commit.
func (s *SQL) SetRole(ctx context.Context, id string, role model.Role) (*model.User, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if role != model.RoleAdmin {
			if _, err := tx.ExecContext(ctx, "update users set role = role where role = $1", string(model.RoleAdmin)); err != nil {
				return err
			}
		}
		var current string
		if err := tx.QueryRowContext(ctx, "select role from users where id = $1", id).Scan(&current); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if current == string(model.RoleAdmin) && role != model.RoleAdmin {
			var admins int
			if err := tx.QueryRowContext(ctx, "select count(*) from users where role = $1", string(model.RoleAdmin)).Scan(&admins); err != nil {
				return err
			}
			if admins == 1 {
				return ErrLastAdmin
			}
		}
		_, err := tx.ExecContext(ctx, "update users set role = $1 where id = $2", string(role), id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Find(ctx, id)
}

// Replace overwrites an existing user in one transaction. The role is not
// Replace's to change: the update matches only the stored role, and a user
// that exists with another role is ErrRoleChange.
//
// check, if not nil, is given the stored user first, and an error from it
// is Replace's, with nothing written. The row is written first, a no-op
// write as SetRole makes, so that a concurrent Replace waits on its lock
// until this one commits, and then reads what this one wrote: two writers
// that read the same user cannot both pass a check on it.
func (s *SQL) Replace(ctx context.Context, u model.User, check func(current model.User) error) (*model.User, error) {
	tags, _ := json.Marshal(u.Tags)
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if check != nil {
			if _, err := tx.ExecContext(ctx, "update users set role = role where id = $1", u.ID); err != nil {
				return err
			}
			current, err := scanUser(tx.QueryRowContext(ctx, "select "+columns+" from users where id = $1", u.ID))
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			if err := check(current); err != nil {
				return err
			}
		}
		set := "name = $1, age = $2, tags = $3, balance = $4"
		args := []any{u.Name, u.Age, string(tags), u.Balance}
		if u.Active != nil {
			args = append(args, *u.Active)
			set += ", active = $" + strconv.Itoa(len(args))
		}
		args = append(args, u.ID, string(u.Role))
		res, err := tx.ExecContext(ctx, "update users set "+set+
			" where id = $"+strconv.Itoa(len(args)-1)+" and role = $"+strconv.Itoa(len(args)), args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return nil
		}
		var exists int
		err = tx.QueryRowContext(ctx, "select 1 from users where id = $1", u.ID).Scan(&exists)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return err
		}
		return ErrRoleChange
	})
	if err != nil {
		return nil, err
	}
	return s.Find(ctx, u.ID)
}

// Delete removes the user with id in one transaction; the last admin cannot
// be deleted. It first writes every admin row and the user's own, as SetRole
// does, so a concurrent deletion or demotion waits and then counts what this
// one left, and a concurrent promotion of the user lands before its role is
// read.
func (s *SQL) Delete(ctx context.Context, id string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "update users set role = role where role = $1 or id = $2", string(model.RoleAdmin), id); err != nil {
			return err
		}
		var current string
		if err := tx.QueryRowContext(ctx, "select role from users where id = $1", id).Scan(&current); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if current == string(model.RoleAdmin) {
			var admins int
			if err := tx.QueryRowContext(ctx, "select count(*) from users where role = $1", string(model.RoleAdmin)).Scan(&admins); err != nil {
				return err
			}
			if admins == 1 {
				return ErrLastAdmin
			}
		}
		res, err := tx.ExecContext(ctx, "delete from users where id = $1", id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}
