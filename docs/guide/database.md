# Database

## Database

geta ships no database layer, driver, or adapter; the database is `database/sql` plus your driver. A database error is an ordinary error: map it in failure rows like any other; geta maps none, so an unmapped one is a 500. `examples/register` and `examples/register-sql` show one way to copy:

- The store's own errors, `store.ErrConflict`, `store.ErrUnavailable`, `store.ErrTransient` (`examples/register/store/dberrors.go`), mapped in failure rows, so route packages never import a driver.
- An adapter per engine: an `Open` returning `*sql.DB`, and a classifier `func(error) error` wrapping a driver error as `fmt.Errorf("%w: %w", store.ErrConflict, err)` (`errors.Is` matches the kind, `errors.As` reaches the driver error); SQLite (modernc.org/sqlite) and PostgreSQL (pgx) in `examples/register-sql`.
- A conformance suite for a classifier, `conformance.Run(t, open, unreachable, classify)` in `examples/register-sql/conformance`, run by each adapter's tests.
- SQL in the driver's placeholders (geta rewrites nothing), and every driver error the store returns classified, transactions included (`examples/register/store/sql.go`: `fail`, `tx`).

