package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"
)

// Store is the consumer-facing seam. order/killswitch/reconciler depend on this
// interface and fake it for unit tests; store's own durability, crash, and
// atomicity tests run against the real engine (ADR-0005 point 2).
//
// The single-argument write methods are convenience wrappers, each committing
// as one durable event. When a single logical event must change several areas
// atomically (e.g. record a journal marker AND trip halt), use Atomically and
// call the Tx methods inside it (ADR-0005 point 3).
type Store interface {
	// Atomically runs fn inside one write transaction on the dedicated write
	// connection. It commits iff fn returns nil, otherwise rolls back — all
	// writes in fn live or die together.
	Atomically(ctx context.Context, fn func(tx Tx) error) error

	AppendIntent(ctx context.Context, in Intent) error
	AppendMarker(ctx context.Context, intentID string, kind MarkerKind, orderID string) error
	ResolveIntent(ctx context.Context, intentID, resolution string) error
	LoadUnresolvedIntents(ctx context.Context) ([]Intent, error)

	MarkHaltPending(ctx context.Context, reason string) error
	TripHalt(ctx context.Context, reason string) error
	ClearHalt(ctx context.Context) error
	Halt(ctx context.Context) (HaltState, error)

	SetLifecycle(ctx context.Context, s LifecycleState) error
	Lifecycle(ctx context.Context) (LifecycleState, error)

	SetCounter(ctx context.Context, c Counter) error
	Counter(ctx context.Context, name string) (Counter, error)

	Close() error
}

// DB is the concrete store backed by an embedded SQLite database. It keeps two
// handles on the same file: writeDB is a single dedicated write connection
// (MaxOpenConns=1, _txlock=immediate) so concurrent writers serialize through
// it without a spurious SQLITE_BUSY; readDB serves concurrent readers, which
// WAL permits alongside the one writer.
type DB struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

var _ Store = (*DB)(nil)

// Open opens (creating if absent) the store at path and applies pending
// migrations. Durability is fixed at synchronous=FULL + WAL and must not be
// relaxed (ADR-0005 point 4).
func Open(path string) (*DB, error) {
	writeDB, err := openConn(path, true)
	if err != nil {
		return nil, err
	}
	// Serialize all writes onto one connection: this is the "dedicated write
	// connection" that guarantees single-writer semantics.
	writeDB.SetMaxOpenConns(1)

	if err := migrate(context.Background(), writeDB); err != nil {
		_ = writeDB.Close()
		return nil, err
	}

	readDB, err := openConn(path, false)
	if err != nil {
		_ = writeDB.Close()
		return nil, err
	}

	return &DB{writeDB: writeDB, readDB: readDB}, nil
}

// openConn opens one *sql.DB on path with the fixed durability pragmas. The
// write handle additionally takes the write lock immediately on BeginTx
// (_txlock=immediate) so a read-then-write transaction never has to upgrade a
// lock mid-flight and hit SQLITE_BUSY.
func openConn(path string, write bool) (*sql.DB, error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Add("_pragma", "foreign_keys(ON)")
	if write {
		q.Set("_txlock", "immediate")
	}
	dsn := "file:" + path + "?" + q.Encode()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping %s: %w", path, err)
	}
	return db, nil
}

// Close closes both handles. It is safe to call once.
func (d *DB) Close() error {
	rerr := d.readDB.Close()
	werr := d.writeDB.Close()
	if werr != nil {
		return werr
	}
	return rerr
}

// schemaVersion reports the applied migration version (PRAGMA user_version).
func (d *DB) schemaVersion(ctx context.Context) (int, error) {
	var v int
	if err := d.readDB.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("store: read schema version: %w", err)
	}
	return v, nil
}

// Atomically runs fn in one write transaction on the dedicated write
// connection. Because writeDB has a single connection, concurrent Atomically
// calls queue rather than race, so there is no spurious SQLITE_BUSY.
//
// The deferred Rollback (same pattern as applyMigration in schema.go) is the
// panic safety net: if fn panics, Go still runs deferred calls while the panic
// propagates, so the transaction is rolled back and the sole write connection
// (SetMaxOpenConns(1)) is returned to the pool before the panic reaches the
// caller. Without it, a panicking fn leaks the write *sql.Tx forever, and every
// later write (TripHalt, AppendIntent, ...) blocks on BeginTx indefinitely —
// see issue #24. Rollback after a successful Commit is a documented no-op
// (sql.ErrTxDone), so this is safe on the happy path too.
func (d *DB) Atomically(ctx context.Context, fn func(tx Tx) error) error {
	sqlTx, err := d.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}
	defer sqlTx.Rollback() //nolint:errcheck // no-op after a successful Commit; panic safety net otherwise
	if err := fn(&txn{q: sqlTx}); err != nil {
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// --- single-event write wrappers: each commits as one durable event ---

// AppendIntent durably appends a new intent at the prepared marker (ADR-0002).
func (d *DB) AppendIntent(ctx context.Context, in Intent) error {
	return d.Atomically(ctx, func(tx Tx) error { return tx.AppendIntent(ctx, in) })
}

// AppendMarker durably appends one journal transition as its own commit;
// transitions are never batched (ADR-0005 point 3).
func (d *DB) AppendMarker(ctx context.Context, intentID string, kind MarkerKind, orderID string) error {
	return d.Atomically(ctx, func(tx Tx) error { return tx.AppendMarker(ctx, intentID, kind, orderID) })
}

// ResolveIntent terminally closes an intent so it leaves the unresolved set.
func (d *DB) ResolveIntent(ctx context.Context, intentID, resolution string) error {
	return d.Atomically(ctx, func(tx Tx) error { return tx.ResolveIntent(ctx, intentID, resolution) })
}

// MarkHaltPending durably opens a pending global halt (ADR-0012 Decision 1(c)).
// killswitch owns when to use it versus a direct TripHalt.
func (d *DB) MarkHaltPending(ctx context.Context, reason string) error {
	return d.Atomically(ctx, func(tx Tx) error { return tx.MarkHaltPending(ctx, reason) })
}

// TripHalt persists the completed global halt (ADR-0004/0012). killswitch owns
// the decision.
func (d *DB) TripHalt(ctx context.Context, reason string) error {
	return d.Atomically(ctx, func(tx Tx) error { return tx.TripHalt(ctx, reason) })
}

// ClearHalt clears the global halt (manual reset per ADR-0004 point 6).
func (d *DB) ClearHalt(ctx context.Context) error {
	return d.Atomically(ctx, func(tx Tx) error { return tx.ClearHalt(ctx) })
}

// SetLifecycle durably sets the clean-shutdown sentinel (ADR-0012 Decision 1(c)).
// cmd/bot (#36) owns the eligibility rules for when each value may be written.
func (d *DB) SetLifecycle(ctx context.Context, s LifecycleState) error {
	return d.Atomically(ctx, func(tx Tx) error { return tx.SetLifecycle(ctx, s) })
}

// SetCounter upserts a persistent counter.
func (d *DB) SetCounter(ctx context.Context, c Counter) error {
	return d.Atomically(ctx, func(tx Tx) error { return tx.SetCounter(ctx, c) })
}

// --- reads: served by the read handle (concurrent under WAL) ---

// LoadUnresolvedIntents returns every intent still open (ResolvedAt == nil),
// each with its markers loaded, ordered by creation. This is the restart-scan
// the reconciler depends on (ADR-0003); it must never lose an unresolved intent.
//
// The load runs in one read transaction so the intent list and every
// per-intent marker query share a single SQLite snapshot. Without it, a
// concurrent writer resolving an intent or appending markers between queries
// could yield a mixed state (an intent already resolved, or markers from a
// later snapshot) the reconciler would then act on.
func (d *DB) LoadUnresolvedIntents(ctx context.Context) ([]Intent, error) {
	tx, err := d.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("store: begin read tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // read-only; Rollback just releases the snapshot
	return loadUnresolvedIntents(ctx, tx)
}

// Halt reads the persisted global halt state.
func (d *DB) Halt(ctx context.Context) (HaltState, error) {
	return readHalt(ctx, d.readDB)
}

// Lifecycle reads the persisted clean-shutdown sentinel.
func (d *DB) Lifecycle(ctx context.Context) (LifecycleState, error) {
	return readLifecycle(ctx, d.readDB)
}

// Counter reads a persistent counter. A never-written counter reads back as a
// zero-value Counter with that name (Value 0), not an error.
func (d *DB) Counter(ctx context.Context, name string) (Counter, error) {
	return readCounter(ctx, d.readDB, name)
}
