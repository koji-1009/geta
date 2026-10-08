package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net"
	neturl "net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/koji-1009/geta/examples/register-sql/conformance"
	"github.com/koji-1009/geta/examples/register/model"
	"github.com/koji-1009/geta/examples/register/registertest"
	"github.com/koji-1009/geta/examples/register/store"
)

// The tests run against a real PostgreSQL: GETA_POSTGRES_URL when set,
// otherwise an embedded server started once for the package. Either way the
// run creates a database of its own on that server and drops it at the end:
// the tests drop and create tables, so two runs sharing one database at once
// would break each other.
var (
	once     sync.Once
	url      string // this run's own database
	startErr error
	server   *embeddedpostgres.EmbeddedPostgres
	tmpDir   string
	baseURL  string // the server's database, from which url's is created and dropped
	ownDB    string // url's database name, once created
)

func TestMain(m *testing.M) {
	code := m.Run()
	if ownDB != "" {
		if err := dropOwnDatabase(); err != nil {
			fmt.Fprintf(os.Stderr, "dropping database %s: %v\n", ownDB, err)
			if code == 0 {
				code = 1
			}
		}
	}
	if server != nil {
		server.Stop()
	}
	if tmpDir != "" {
		os.RemoveAll(tmpDir)
	}
	os.Exit(code)
}

func dbURL(t *testing.T) string {
	t.Helper()
	once.Do(func() {
		if u := os.Getenv("GETA_POSTGRES_URL"); u != "" {
			url, startErr = createOwnDatabase(u)
			return
		}
		// Each run has its own directory and port, so two runs at once
		// share neither.
		dir, err := os.MkdirTemp("", "geta-pg")
		if err != nil {
			startErr = err
			return
		}
		tmpDir = dir
		port, err := freePort()
		if err != nil {
			startErr = err
			return
		}
		cfg := embeddedpostgres.DefaultConfig().
			Port(port).
			RuntimePath(filepath.Join(dir, "run")).
			DataPath(filepath.Join(dir, "data")).
			StartTimeout(2 * time.Minute)
		if cache := os.Getenv("GETA_POSTGRES_CACHE"); cache != "" {
			cfg = cfg.CachePath(cache)
		}
		server = embeddedpostgres.NewDatabase(cfg)
		if startErr = server.Start(); startErr != nil {
			server = nil
			return
		}
		url, startErr = createOwnDatabase(fmt.Sprintf("postgres://postgres:postgres@localhost:%d/postgres?sslmode=disable", port))
	})
	if startErr != nil {
		t.Fatalf("starting PostgreSQL: %v", startErr)
	}
	return url
}

// createOwnDatabase creates a database no other run uses on the server base
// connects to, and returns base with that database in place of its own.
func createOwnDatabase(base string) (string, error) {
	u, err := neturl.Parse(base)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "", errors.New("GETA_POSTGRES_URL is not a postgres:// URL")
	}
	var b [8]byte
	rand.Read(b[:])
	name := fmt.Sprintf("geta_test_%x", b)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := Open(ctx, base)
	if err != nil {
		return "", err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "create database "+name); err != nil {
		return "", fmt.Errorf("creating this run's database: %w", err)
	}
	baseURL, ownDB = base, name
	u.Path = "/" + name
	u.RawPath = ""
	return u.String(), nil
}

// dropOwnDatabase drops the database createOwnDatabase made, closing any
// connection a test left open to it.
func dropOwnDatabase() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := Open(ctx, baseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, "drop database if exists "+ownDB+" with (force)")
	return err
}

// freePort asks the kernel for a port no one is listening on. Another
// process could take it before the server binds it; that is a failed start,
// not two runs sharing one server.
func freePort() (uint32, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return uint32(l.Addr().(*net.TCPAddr).Port), nil
}

func open(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), dbURL(t))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestConformance(t *testing.T) {
	conformance.Run(t, open, func(t *testing.T) error {
		// Port 1 has no server listening.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		db, err := sql.Open("pgx", "postgres://postgres:postgres@127.0.0.1:1/postgres?sslmode=disable&connect_timeout=2")
		if err != nil {
			return err
		}
		defer db.Close()
		return db.PingContext(ctx)
	}, Classify)
}

// The register example's routes, unchanged, over PostgreSQL.
func TestRegisterExample(t *testing.T) {
	db := open(t)
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), "drop table if exists users"); err != nil {
		t.Fatal(err)
	}
	users, err := store.NewSQL(context.Background(), Users(db))
	if err != nil {
		t.Fatal(err)
	}
	registertest.Run(t, users)
}

// Two demotions of the last two admins at once, under PostgreSQL's default
// READ COMMITTED: either may win, never both.
func TestLastAdminUnderConcurrentDemotions(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	defer db.Close()
	if _, err := db.ExecContext(ctx, "drop table if exists users"); err != nil {
		t.Fatal(err)
	}
	users, err := store.NewSQL(ctx, Users(db))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		if _, err := db.ExecContext(ctx, "delete from users"); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"a1", "a2"} {
			if err := users.Create(ctx, model.User{ID: id, Name: id, Role: model.RoleAdmin, Tags: []string{}}); err != nil {
				t.Fatal(err)
			}
		}
		start := make(chan struct{})
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for j, id := range []string{"a1", "a2"} {
			wg.Go(func() {
				<-start
				_, errs[j] = users.SetRole(ctx, id, model.RoleMember)
			})
		}
		close(start)
		wg.Wait()
		admins, _, err := users.List(ctx, store.Filter{Role: new(model.RoleAdmin), Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(admins) == 0 {
			t.Fatalf("round %d: both demotions succeeded: %v", i, errs)
		}
		for _, err := range errs {
			if err != nil && !errors.Is(err, store.ErrLastAdmin) {
				t.Fatalf("round %d: %v", i, err)
			}
		}
	}
}

// Two deletions, or a deletion and a demotion, of the last two admins at
// once: either may win, never both.
func TestLastAdminUnderConcurrentDeletions(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	defer db.Close()
	if _, err := db.ExecContext(ctx, "drop table if exists users"); err != nil {
		t.Fatal(err)
	}
	users, err := store.NewSQL(ctx, Users(db))
	if err != nil {
		t.Fatal(err)
	}
	remove := func(id string) error { return users.Delete(ctx, id, nil) }
	demote := func(id string) error {
		_, err := users.SetRole(ctx, id, model.RoleMember)
		return err
	}
	for name, ops := range map[string][2]func(string) error{
		"delete and delete": {remove, remove},
		"delete and demote": {remove, demote},
	} {
		t.Run(name, func(t *testing.T) {
			for i := range 50 {
				if _, err := db.ExecContext(ctx, "delete from users"); err != nil {
					t.Fatal(err)
				}
				for _, id := range []string{"a1", "a2"} {
					if err := users.Create(ctx, model.User{ID: id, Name: id, Role: model.RoleAdmin, Tags: []string{}}); err != nil {
						t.Fatal(err)
					}
				}
				start := make(chan struct{})
				errs := make([]error, 2)
				var wg sync.WaitGroup
				for j, id := range []string{"a1", "a2"} {
					wg.Go(func() {
						<-start
						errs[j] = ops[j](id)
					})
				}
				close(start)
				wg.Wait()
				admins, _, err := users.List(ctx, store.Filter{Role: new(model.RoleAdmin), Limit: 10})
				if err != nil {
					t.Fatal(err)
				}
				if len(admins) == 0 {
					t.Fatalf("round %d: both succeeded: %v", i, errs)
				}
				for _, err := range errs {
					if err != nil && !errors.Is(err, store.ErrLastAdmin) {
						t.Fatalf("round %d: %v", i, err)
					}
				}
			}
		})
	}
}

func TestForeignKeyIsAConflict(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	defer db.Close()
	for _, q := range []string{
		"drop table if exists child", "drop table if exists parent",
		"create table parent (id text primary key)",
		"create table child (id text primary key, parent text references parent(id))",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	defer db.ExecContext(ctx, "drop table child; drop table parent")
	_, err := db.ExecContext(ctx, "insert into child (id, parent) values ($1, $2)", "c", "missing")
	if err := Classify(err); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
}

// Two serializable transactions that read what the other writes: one of them
// is told to retry, and that is ErrTransient.
func TestSerializationFailureIsTransient(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	defer db.Close()
	for _, q := range []string{"drop table if exists counters", "create table counters (id int primary key, n int)", "insert into counters values (1, 0), (2, 0)"} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	defer db.ExecContext(ctx, "drop table counters")
	begin := func() *sql.Tx {
		tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}
	a, b := begin(), begin()
	var n int
	a.QueryRowContext(ctx, "select sum(n) from counters").Scan(&n)
	b.QueryRowContext(ctx, "select sum(n) from counters").Scan(&n)
	if _, err := a.ExecContext(ctx, "update counters set n = n + 1 where id = 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ExecContext(ctx, "update counters set n = n + 1 where id = 2"); err != nil {
		t.Fatal(err)
	}
	if err := a.Commit(); err != nil {
		t.Fatal(err)
	}
	err := Classify(b.Commit())
	if !errors.Is(err, store.ErrTransient) {
		t.Fatalf("got %v", err)
	}
}

// A cancelled or expired context is the caller's: it is not classified, even
// though context.DeadlineExceeded satisfies net.Error.
func TestContextErrorsAreNotClassified(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		err := Classify(fmt.Errorf("query: %w", cause))
		if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrUnavailable) || errors.Is(err, store.ErrTransient) || !errors.Is(err, cause) {
			t.Errorf("Classify(%v) = %#v", cause, err)
		}
	}
	db := open(t)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := db.ExecContext(ctx, "select pg_sleep(5)")
	if err = Classify(err); err == nil || errors.Is(err, store.ErrUnavailable) {
		t.Fatalf("a query past its deadline: %v", err)
	}
}

func TestUnreachableIsUnavailable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Open(ctx, "postgres://postgres:postgres@127.0.0.1:1/postgres?sslmode=disable&connect_timeout=2")
	if !errors.Is(err, store.ErrUnavailable) {
		t.Fatal(err)
	}
}
