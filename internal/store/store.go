package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"
)

// ErrUnsafeDBPath is returned by Open when the database path contains a character
// that would corrupt the SQLite URI DSN. openConn builds the DSN as
// "file:" + path + "?" + query, so a '#' in the path is read as a URI fragment
// boundary — SQLite then silently opens a *truncated, different* file (verified:
// ".../data#prod/store.db" opens ".../data") — a '?' misparses the pragma query
// (durability pragmas can be lost), and a raw '%' is misread as a percent-escape
// introducer. Because "reopen the SAME journal" is load-bearing for restart
// recovery (ADR-0003/0005), a path that could point the engine at a different
// file must fail closed at Open, not silently degrade.
var ErrUnsafeDBPath = errors.New("store: database path contains characters unsafe for the SQLite URI DSN (#, ?, or %)")

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

	// Audit-ack ↔ prune-gate wiring (ADR-0006 point 4). RecordAuditAck records one
	// lifecycle record's durable ack; FinalizeFullyAudited sets the per-intent
	// prune-gate flag once ALL are acked; FullyAudited reads it;
	// UnackedLifecycleRecords reconstructs the still-un-acked records a restart
	// reconciler must re-emit. See the Tx methods for the full contract.
	RecordAuditAck(ctx context.Context, intentID, recordKey string) error
	FinalizeFullyAudited(ctx context.Context, intentID string) (bool, error)
	FullyAudited(ctx context.Context, intentID string) (time.Time, bool, error)
	UnackedLifecycleRecords(ctx context.Context, intentID string) ([]LifecycleRecord, error)
	LoadNotFullyAuditedIntents(ctx context.Context) ([]Intent, error)

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
	// Fail closed BEFORE touching the filesystem if the path would corrupt the URI
	// DSN (silent wrong-file / lost pragmas — L-8), so no stray file is pre-created
	// for a path we are about to reject.
	if err := validateDBPath(path); err != nil {
		return nil, err
	}
	// Pre-create the main file owner-only so the .db and its WAL/SHM sidecars are
	// never group/other-readable (M-2). This runs before openConn because SQLite
	// keeps an existing file's mode.
	if err := secureDBFile(path); err != nil {
		return nil, err
	}

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

// validateDBPath rejects paths whose characters break the "file:PATH?query" URI
// DSN grammar (see ErrUnsafeDBPath). Rejection (rather than escaping) is chosen
// deliberately: SQLite URI percent-decoding rules are subtle and a mis-escape
// would reintroduce the very silent-wrong-file hazard this guards against, so a
// loud fail-closed refusal at startup is the robust choice. os.Stat-after-open
// verification is not sufficient on its own here because secureDBFile pre-creates
// the intended path, so the intended path always exists even when SQLite opened a
// different (truncated) file.
func validateDBPath(path string) error {
	if i := strings.IndexAny(path, "#?%"); i >= 0 {
		return fmt.Errorf("%w: %q (offending %q at index %d)", ErrUnsafeDBPath, path, string(path[i]), i)
	}
	return nil
}

// secureDBFile ensures the main database file exists with owner-only permissions
// (0o600) BEFORE SQLite opens it, so neither the .db nor its WAL/SHM sidecars are
// readable by other accounts on a shared host — the DB (and its uncheckpointed
// journal pages in the -wal) holds order intents, client_order_ids, and halt
// reasons, i.e. the account's whole trading activity (M-2). A freshly created file
// is made 0o600 (0o600 has no group/other bits, so any umask only tightens it
// further).
//
// It then tightens the main file AND any pre-existing -wal/-shm sidecars that are
// group/other-accessible. SQLite only sets a sidecar's mode when it CREATES it
// (inheriting the main file's mode), so sidecars an older pre-hardening binary left
// at 0o644 (e.g. a crash-left WAL) would otherwise stay world-readable after an
// upgrade even though the main file is now 0o600. The sidecars are never created
// here — an empty -wal/-shm invented before open would corrupt SQLite's WAL
// recovery — so an absent sidecar is skipped (SQLite creates the real one
// 0o600-inherited). This is a fail-safe repair (an unattended upgrade keeps booting
// rather than refusing to start); if a file cannot be tightened Open fails closed
// rather than proceeding with world-readable trading data.
func secureDBFile(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("store: pre-create %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("store: pre-create %s: %w", path, err)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue // absent sidecar: SQLite will create it 0o600-inherited
			}
			return fmt.Errorf("store: stat %s: %w", p, err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			if err := os.Chmod(p, 0o600); err != nil {
				return fmt.Errorf("store: tighten permissions on %s (mode %#o is group/other-accessible): %w", p, perm, err)
			}
		}
	}
	return nil
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

// RecordAuditAck durably records one lifecycle record's ack as its own commit
// (ADR-0006 point 4: each lifecycle marker audit fsync → store transaction ack).
func (d *DB) RecordAuditAck(ctx context.Context, intentID, recordKey string) error {
	return d.Atomically(ctx, func(tx Tx) error { return tx.RecordAuditAck(ctx, intentID, recordKey) })
}

// FinalizeFullyAudited sets the prune-gate flag iff every lifecycle record is
// durably acked (and the intent is resolved), committing as one durable event.
func (d *DB) FinalizeFullyAudited(ctx context.Context, intentID string) (bool, error) {
	var done bool
	err := d.Atomically(ctx, func(tx Tx) error {
		var e error
		done, e = tx.FinalizeFullyAudited(ctx, intentID)
		return e
	})
	return done, err
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

// FullyAudited reads the per-intent prune-gate flag.
func (d *DB) FullyAudited(ctx context.Context, intentID string) (time.Time, bool, error) {
	return readFullyAudited(ctx, d.readDB, intentID)
}

// UnackedLifecycleRecords reconstructs the still-un-acked lifecycle records for an
// intent. The load reads the intent (with markers) and its ack set, so it runs in
// one read transaction to pin a single SQLite snapshot — otherwise a concurrent ack
// commit between the two queries could drop a record that is actually still
// un-acked from the result.
func (d *DB) UnackedLifecycleRecords(ctx context.Context, intentID string) ([]LifecycleRecord, error) {
	tx, err := d.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("store: begin read tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // read-only; Rollback just releases the snapshot
	return unackedLifecycleRecords(ctx, tx, intentID)
}

// LoadNotFullyAuditedIntents returns every intent whose fully-audited flag is unset,
// each with its markers — the restart recovery-candidate scan (ADR-0006 point 4).
// Like LoadUnresolvedIntents it runs in one read transaction so the intent list and
// every per-intent marker query share one SQLite snapshot (no mixed-state skew).
func (d *DB) LoadNotFullyAuditedIntents(ctx context.Context) ([]Intent, error) {
	tx, err := d.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("store: begin read tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // read-only; Rollback just releases the snapshot
	return loadNotFullyAuditedIntents(ctx, tx)
}
