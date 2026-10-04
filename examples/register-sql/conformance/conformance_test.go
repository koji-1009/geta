package conformance_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/koji-1009/geta/examples/register-sql/conformance"
	"github.com/koji-1009/geta/examples/register/store"
)

// The suite passes a conformant adapter and fails each non-conformant one,
// naming what it got wrong. The engine is a fake driver whose DSN chooses
// how it misbehaves; a real engine's adapter conforms, so only a fake makes
// the suite's failures happen.

var (
	errDup     = errors.New("fake: duplicate key")
	errUnreach = errors.New("fake: connection refused")
	errCreate  = errors.New("fake: permission denied")
	errBegin   = errors.New("fake: no transactions")
)

// fakeDriver keeps one table's keys per DSN. Its DSN is the misbehaviour:
// "accept-duplicates", "create-fails", "begin-fails", or anything else for
// none.
type fakeDriver struct {
	mu   sync.Mutex
	keys map[string]map[string]bool
}

var fake = &fakeDriver{keys: map[string]map[string]bool{}}

func init() { sql.Register("conformance-fake", fake) }

func (d *fakeDriver) Open(dsn string) (driver.Conn, error) { return &fakeConn{d: d, dsn: dsn}, nil }

type fakeConn struct {
	d   *fakeDriver
	dsn string
}

func (c *fakeConn) Prepare(q string) (driver.Stmt, error) { return fakeStmt{c, q}, nil }
func (c *fakeConn) Close() error                          { return nil }

func (c *fakeConn) Begin() (driver.Tx, error) {
	if c.dsn == "begin-fails" {
		return nil, errBegin
	}
	return fakeTx{}, nil
}

func (c *fakeConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	switch {
	case strings.HasPrefix(q, "create table"):
		if c.dsn == "create-fails" {
			return nil, errCreate
		}
		c.d.keys[c.dsn] = map[string]bool{}
	case strings.HasPrefix(q, "insert"):
		if c.d.keys[c.dsn]["a"] && c.dsn != "accept-duplicates" {
			return nil, errDup
		}
		c.d.keys[c.dsn]["a"] = true
	}
	return driver.RowsAffected(1), nil
}

type fakeStmt struct {
	c *fakeConn
	q string
}

func (s fakeStmt) Close() error  { return nil }
func (s fakeStmt) NumInput() int { return 0 }
func (s fakeStmt) Exec([]driver.Value) (driver.Result, error) {
	return s.c.ExecContext(context.Background(), s.q, nil)
}
func (s fakeStmt) Query([]driver.Value) (driver.Rows, error) {
	return nil, errors.New("fake: no queries")
}

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

func open(dsn string) func(t *testing.T) *sql.DB {
	return func(t *testing.T) *sql.DB {
		db, err := sql.Open("conformance-fake", dsn)
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
}

// conformant classifies the fake's errors as the floor asks, keeping them.
func conformant(err error) error {
	switch {
	case errors.Is(err, errDup):
		return fmt.Errorf("%w: %w", store.ErrConflict, err)
	case errors.Is(err, errUnreach):
		return fmt.Errorf("%w: %w", store.ErrUnavailable, err)
	}
	return err
}

func unreachable(*testing.T) error { return errUnreach }

// adapters are the non-conformant adapters by name, each with what the
// suite must say of it.
var adapters = map[string]struct {
	dsn         string
	unreachable func(*testing.T) error
	classify    func(error) error
	want        []string
}{
	"accepts duplicates": {"accept-duplicates", unreachable, conformant,
		[]string{"a duplicate key was accepted", "got <nil>"}},
	"classifies nothing": {"plain", unreachable, func(err error) error { return err },
		[]string{"got fake: duplicate key", "got fake: connection refused"}},
	"loses the driver's error": {"plain", unreachable, func(err error) error {
		if errors.Is(err, errDup) {
			return store.ErrConflict
		}
		return store.ErrUnavailable
	}, []string{"the driver's error is lost: store: conflict", "the driver's error is lost: store: database unavailable"}},
	"cannot begin": {"begin-fails", unreachable, conformant, []string{"fake: no transactions"}},
	"reaches the unreachable": {"plain", func(*testing.T) error { return nil }, conformant,
		[]string{"the unreachable database was reached"}},
	"cannot create the table": {"create-fails", unreachable, conformant,
		[]string{"create table geta_conformance (id text primary key): fake: permission denied"}},
}

func TestAConformantAdapterPasses(t *testing.T) {
	conformance.Run(t, open("conformant"), unreachable, conformant)
}

// TestAdapter runs the suite on the adapter CONFORMANCE_ADAPTER names, in the
// process TestANonConformantAdapterFails starts; it is skipped otherwise.
func TestAdapter(t *testing.T) {
	a, ok := adapters[os.Getenv("CONFORMANCE_ADAPTER")]
	if !ok {
		t.Skip("run by TestANonConformantAdapterFails")
	}
	conformance.Run(t, open(a.dsn), a.unreachable, a.classify)
}

// Each non-conformant adapter fails the suite, which names each fault. The
// suite fails its *testing.T, so it runs in a test process of its own.
func TestANonConformantAdapterFails(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a test process")
	}
	for name, a := range adapters {
		cmd := exec.Command(os.Args[0], "-test.run=^TestAdapter$", "-test.v", "-test.count=1")
		cmd.Env = append(os.Environ(), "CONFORMANCE_ADAPTER="+name)
		out, err := cmd.CombinedOutput()
		if _, failed := errors.AsType[*exec.ExitError](err); !failed {
			t.Errorf("%s: the suite passed (%v):\n%s", name, err, out)
			continue
		}
		for _, want := range a.want {
			if !strings.Contains(string(out), want) {
				t.Errorf("%s: the suite does not say %q:\n%s", name, want, out)
			}
		}
	}
}
