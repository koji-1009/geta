# register-sql

How an application writes its own database adapter. geta ships none. This is a separate module (`go.mod`) because it imports drivers.

| Pattern | Where |
| --- | --- |
| An adapter for SQLite (modernc.org/sqlite): `Open` and a classifier wrapping driver errors in the register store's database errors (`store.ErrConflict`, `store.ErrUnavailable`, `store.ErrTransient`, in `examples/register/store/dberrors.go`) | `sqlite/sqlite.go` |
| The same for PostgreSQL (pgx) | `postgres/postgres.go` |
| A conformance suite for a classifier, `conformance.Run`, and each classifier checked by it | `conformance/`, `sqlite/sqlite_test.go`, `postgres/postgres_test.go` |
| examples/register's routes, unchanged, over the SQL store (`examples/register/store/sql.go`) | `cmd/register/main.go`, `registertest.Run` in each `_test.go` |
| The last-admin guard under concurrent writes on PostgreSQL | `postgres/postgres_test.go` |

## Run

```
cd examples/register-sql
go run ./cmd/register -addr 127.0.0.1:8080 -db register.db
```

It serves examples/register's API and document over SQLite. The tokens are the same: `t-admin` and `t-user`.

## Test

```
go -C examples/register-sql test ./...
GETA_POSTGRES_URL=postgres://user:pass@localhost:5432/postgres go -C examples/register-sql test ./postgres
```

Without `GETA_POSTGRES_URL`, the PostgreSQL tests start an embedded server. Either way, each run creates its own database and drops it.
