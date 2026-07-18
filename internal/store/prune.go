package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"
)

// Retention / prune (issue #14, ADR-0005 point 6 + ADR-0006 point 4).
//
// This file holds the ONLY deletion path in the store, so it is written to lean
// on preservation everywhere the answer is not certain. The failure it must never
// produce is deleting an intent the reconciler still needs: an in-flight order
// that disappears from the journal cannot be rebuilt on restart (ADR-0003), and a
// bot that cannot rebuild it may submit it a second time — money moves twice, and
// no later correction undoes that. The failure it produces when it is too shy is
// only that the database stays larger for another cycle.
//
// The point of pruning at all is itself a safety argument, not tidiness: an
// unbounded journal fills the disk, and a full disk means submit-attempted cannot
// be appended, which manufactures exactly the ambiguous submits ADR-0002 exists to
// prevent. So the store is kept bounded to "in-flight orders + a few state rows"
// — but bounded strictly through the fully-audited gate, never by age alone.
//
// What is deleted: an intent row plus its markers and its audit-ack ledger rows,
// all in one transaction, and only when the intent is
//
//	terminal          — resolved_at IS NOT NULL,
//	prune-gated       — fully_audited_at IS NOT NULL (#20 sets it; this file only
//	                    reads it, ADR-0006 point 4), and
//	past its window   — BOTH timestamps at or before the caller's cutoff.
//
// What is never deleted, by construction rather than by clause: the global halt,
// the clean-shutdown sentinel and the persistent counters. No statement here names
// those tables. They are reconstruction-resistant state (ADR-0004 point 7 /
// ADR-0012), so losing them would turn a restart into a safety-guard bypass; the
// strongest available guarantee is that this code cannot address them at all, and
// TestPruneLeavesHaltCountersAndLifecycleAlone pins it.
//
// Where a disk-full escalation attaches: a failing prune is NOT itself the
// fail-closed condition. Its failure direction is "nothing was deleted", which is
// safe. The condition ADR-0005 point 6 routes to the kill switch is a failing
// durable APPEND (the submit path's marker write, the audit sink's record write) —
// those callers already escalate through killswitch on their own durable path
// (ADR-0006 point 6, reconciler.escalateAuditFailClosed). store is a leaf and must
// not import killswitch, so prune deliberately trips nothing itself: it surfaces
// its failure as a returned error plus a logged pass, and the wiring owner (#36)
// decides what a persistently failing prune means. Adding a trip here would both
// break the leaf rule and give a housekeeping loop the power to stop trading.

// ErrPruneBatchNotPositive is returned when a prune pass is asked for a batch of
// zero or fewer intents. That is not a harmless no-op: prune is what keeps the
// journal bounded, so a silently non-deleting prune ends in a full disk and the
// ambiguous-submit hazard above. A misconfiguration is refused loudly instead.
var ErrPruneBatchNotPositive = errors.New("store: prune batch limit must be > 0")

// ErrPruneRaced is returned when a row selected as eligible no longer satisfies the
// eligibility predicate at the moment of its own DELETE. It should be unreachable —
// the selection and the delete run inside one transaction on the single write
// connection, so nothing can interleave — which is exactly why it is treated as a
// corruption signal rather than ignored: the whole batch is rolled back (restoring
// any markers already deleted in this pass) and the anomaly is surfaced.
var ErrPruneRaced = errors.New("store: intent stopped being prune-eligible mid-transaction")

// pruneEligibleWhere is THE prune predicate. It is defined once and used twice —
// to select candidates and again to guard each row's own DELETE — so the guard can
// never drift away from the selection.
//
// Every conjunct is load-bearing on its own:
//
//   - resolved_at IS NOT NULL — terminal only. This is what keeps an in-flight
//     intent (prepared, submit-attempted awaiting settlement, unresolved-ambiguous)
//     out of reach of deletion no matter how old it is (ADR-0005 point 6, ADR-0003).
//   - fully_audited_at IS NOT NULL — the prune gate. Unset means at least one
//     lifecycle audit record has not been durably acked, and while that is true the
//     journal is that record's only durable outbox (ADR-0006 point 4). Unset ⇒
//     preserve; that is the fail-safe direction, and it is why a crash tail is safe
//     even though it leaves rows behind forever until a reconciler re-emits them.
//   - resolved_at <= cutoff — the retention window applied to the resolution. In
//     every state this store can produce the NEXT conjunct already implies this one
//     (finalize refuses to run before resolution, so fully_audited_at >=
//     resolved_at), which makes it look redundant. It stops being redundant exactly
//     when the wall clock steps BACKWARDS between the two writes — an NTP correction
//     between ResolveIntent and FinalizeFullyAudited suffices — leaving an old flag
//     timestamp beside a resolution that is still well inside the window. Keeping it
//     means the window holds under a clock this package does not control.
//   - fully_audited_at <= cutoff — the same window applied to the moment the gate
//     opened. An intent can be resolved long ago and only be finalized now (a
//     restart reconciler re-emitting a crash tail does precisely that). Without this
//     conjunct such a row would become deletable the instant it was flagged, while
//     the reconciler that flagged it may still be working through the same intent.
//
// Together the two window conjuncts measure from the LATER of the two events, which
// is the conservative reading of "the retention window elapsed". The comparison is
// inclusive (<=): a cutoff of "now minus the window" means the window has elapsed at
// exactly that instant.
//
// Each conjunct is mutation-checked: removing any one of them turns a specific test
// in prune_test.go red, so none of them is decorative.
const pruneEligibleWhere = `resolved_at IS NOT NULL
	AND fully_audited_at IS NOT NULL
	AND resolved_at <= ?
	AND fully_audited_at <= ?`

// PruneStats reports what one prune pass removed. The three counts are reported
// separately because they are the observable evidence that a pass deleted a
// COMPLETE intent — an unattended operator reading the log can see that markers and
// acks went with their intent rather than being left dangling.
type PruneStats struct {
	Intents   int64
	Markers   int64
	AuditAcks int64
}

// PruneTerminalIntents deletes up to limit prune-eligible intents — terminal, fully
// audited, and with both durable timestamps at or before before — together with
// their markers and audit-ack rows. It returns what it deleted.
//
// before is the retention cutoff, passed in rather than derived from a clock here
// so the durable layer stays free of policy: Pruner owns "now minus the retention
// window" and tests drive the boundary exactly.
//
// The whole pass is ONE transaction on the dedicated write connection, which gives
// three properties this path depends on. Candidate selection and deletion see one
// snapshot, so no concurrent writer can change an intent's eligibility between the
// two (ADR-0005 single-writer serialization). A failure anywhere rolls the entire
// pass back, which is what makes it safe to delete an intent's children before the
// intent itself — the ordering foreign keys require. And because the write
// connection is shared with every other writer, prune queues with them instead of
// racing them; the batch limit is what keeps that queueing bounded.
func (d *DB) PruneTerminalIntents(ctx context.Context, before time.Time, limit int) (PruneStats, error) {
	if limit <= 0 {
		return PruneStats{}, fmt.Errorf("%w (got %d)", ErrPruneBatchNotPositive, limit)
	}
	var stats PruneStats
	err := d.withWriteTx(ctx, func(q querier) error {
		var err error
		stats, err = pruneTerminalIntents(ctx, q, before, limit)
		return err
	})
	if err != nil {
		return PruneStats{}, err
	}
	return stats, nil
}

// pruneTerminalIntents runs one pass inside an existing transaction.
func pruneTerminalIntents(ctx context.Context, q querier, before time.Time, limit int) (PruneStats, error) {
	cutoff := before.UnixNano()

	ids, err := selectPruneCandidates(ctx, q, cutoff, limit)
	if err != nil {
		return PruneStats{}, err
	}

	var stats PruneStats
	for _, id := range ids {
		// Children first: markers and audit_acks both hold a foreign key to intents,
		// and neither declares ON DELETE CASCADE, so the parent cannot go first. The
		// risk this ordering creates — children deleted for an intent that then turns
		// out not to be deletable — is closed by the guarded parent DELETE below plus
		// the enclosing transaction: if the guard refuses, the whole pass rolls back
		// and the markers come back. A partially-deleted marker set would not only
		// destroy journal evidence, it would leave a state the V4 cross-row
		// pre-check (#29) refuses to migrate.
		acks, err := execCount(ctx, q, `DELETE FROM audit_acks WHERE intent_id = ?`, id)
		if err != nil {
			return PruneStats{}, fmt.Errorf("store: prune audit acks for %q: %w", id, err)
		}
		markers, err := execCount(ctx, q, `DELETE FROM markers WHERE intent_id = ?`, id)
		if err != nil {
			return PruneStats{}, fmt.Errorf("store: prune markers for %q: %w", id, err)
		}

		// The predicate is re-asserted on the row's own DELETE. Inside this
		// transaction it cannot fail, which is the point: if it ever does, something
		// is wrong at a level this code cannot reason about, so the pass aborts and
		// rolls back rather than deleting on an assumption.
		n, err := execCount(ctx, q, `DELETE FROM intents WHERE intent_id = ? AND `+pruneEligibleWhere,
			id, cutoff, cutoff)
		if err != nil {
			return PruneStats{}, fmt.Errorf("store: prune intent %q: %w", id, err)
		}
		if n != 1 {
			return PruneStats{}, fmt.Errorf("%w: %q (rows affected = %d); pass rolled back", ErrPruneRaced, id, n)
		}

		stats.Intents++
		stats.Markers += markers
		stats.AuditAcks += acks
	}
	return stats, nil
}

// selectPruneCandidates returns the ids of at most limit eligible intents, oldest
// resolution first. Draining the result set completely before any DELETE is
// required, not stylistic: the pass runs on the single write connection, where a
// live result set and a write cannot coexist.
func selectPruneCandidates(ctx context.Context, q querier, cutoff int64, limit int) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT intent_id FROM intents WHERE `+pruneEligibleWhere+`
		 ORDER BY resolved_at, intent_id LIMIT ?`,
		cutoff, cutoff, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("store: select prune candidates: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: scan prune candidate: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate prune candidates: %w", err)
	}
	return ids, nil
}

// execCount runs a statement and returns how many rows it affected.
func execCount(ctx context.Context, q querier, query string, args ...any) (int64, error) {
	res, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

// --- the retention loop ----------------------------------------------------

// PruneJournal is the narrow durable seam the retention loop deletes through —
// exactly one method, so the loop cannot reach any other part of the store even by
// accident. *DB satisfies it.
type PruneJournal interface {
	PruneTerminalIntents(ctx context.Context, before time.Time, limit int) (PruneStats, error)
}

var _ PruneJournal = (*DB)(nil)

// PruneConfig wires a Pruner. The three knobs are the retention parameters
// ADR-0005 point 6 left to the implementation, and all of them are validated
// fail-closed: a zero is never read as "use a default" here, because each zero has
// a dangerous reading (delete immediately / spin / delete nothing).
type PruneConfig struct {
	Journal PruneJournal

	// RetentionWindow is how long a terminal, fully-audited intent is kept before it
	// becomes eligible. It is measured from the LATER of its resolution and its
	// fully-audited timestamps (see pruneEligibleWhere), so it is also the grace
	// period protecting an intent a reconciler has only just finalized.
	RetentionWindow time.Duration

	// Interval is the cadence between prune passes.
	Interval time.Duration

	// MaxBatch bounds how many intents one pass may delete, which bounds how long a
	// pass holds the single write connection. A backlog larger than the batch is
	// simply drained over several passes.
	MaxBatch int

	// Now defaults to time.Now. Injected in tests to place the cutoff exactly.
	Now func() time.Time

	// Ticks, when non-nil, replaces the internal ticker so a test can step the
	// cadence deterministically. Production leaves it nil.
	Ticks <-chan time.Time

	// Logger defaults to slog.Default(). Unattended operation makes the log the only
	// post-hoc account of what was deleted, so every pass and every failure logs.
	Logger *slog.Logger
}

// Pruner is the supervised retention loop. It owns the retention POLICY (how old
// is old enough, how often, how much at once) and nothing else; the eligibility
// rules that protect money-safety live in the SQL predicate, where a wrong policy
// value cannot reach them. That split is deliberate: a misconfigured window makes
// the store bigger or smaller, it can never make an unresolved or un-audited intent
// deletable.
type Pruner struct {
	journal   PruneJournal
	retention time.Duration
	interval  time.Duration
	maxBatch  int
	now       func() time.Time
	ticks     <-chan time.Time
	logger    *slog.Logger
}

// NewPruner validates cfg and builds a Pruner. Validation refuses non-positive
// values rather than substituting defaults — the twin of reconciler.New and
// killswitch's Config.validate — because each degenerate value silently disables a
// protection: a zero window deletes intents the moment they are finalized, a zero
// interval spins (and panics in time.NewTicker), and a zero batch never deletes at
// all, which ends in the full disk this loop exists to prevent.
func NewPruner(cfg PruneConfig) (*Pruner, error) {
	if cfg.Journal == nil {
		return nil, fmt.Errorf("store: NewPruner requires a Journal")
	}
	if cfg.RetentionWindow <= 0 {
		return nil, fmt.Errorf("store: NewPruner requires RetentionWindow > 0, got %s", cfg.RetentionWindow)
	}
	if cfg.Interval <= 0 {
		return nil, fmt.Errorf("store: NewPruner requires Interval > 0, got %s", cfg.Interval)
	}
	if cfg.MaxBatch <= 0 {
		return nil, fmt.Errorf("store: NewPruner requires MaxBatch > 0, got %d", cfg.MaxBatch)
	}

	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Pruner{
		journal:   cfg.Journal,
		retention: cfg.RetentionWindow,
		interval:  cfg.Interval,
		maxBatch:  cfg.MaxBatch,
		now:       now,
		ticks:     cfg.Ticks,
		logger:    logger,
	}, nil
}

// PruneOnce runs a single pass with the cutoff derived from the configured window.
func (p *Pruner) PruneOnce(ctx context.Context) (PruneStats, error) {
	return p.journal.PruneTerminalIntents(ctx, p.now().Add(-p.retention), p.maxBatch)
}

// Run is the supervised long-running loop: one pass at start, then one per tick,
// until ctx is cancelled. It returns ctx.Err() on shutdown.
//
// It never stops on its own. A failing pass is housekeeping that did not happen,
// and its consequence — a store that stays larger for another interval — is
// strictly less dangerous than the alternatives a fail-closed loop would invite
// (halting trading over a deletion, or worse, retrying deletion harder). The real
// disk-full hazard is caught where it actually bites, at the durable APPEND paths,
// which escalate through the kill switch on their own (see the file comment).
func (p *Pruner) Run(ctx context.Context) error {
	ticks := p.ticks
	if ticks == nil {
		t := time.NewTicker(p.interval)
		defer t.Stop()
		ticks = t.C
	}

	p.runPass(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-ticks:
			if !ok {
				// The cadence is gone, so no further pass can happen. Returning (rather
				// than spinning on a closed channel) hands the decision to the supervisor
				// that owns this goroutine; nothing unsafe follows from prune stopping.
				p.logger.Error("prune ticker stopped; retention loop exiting")
				return nil
			}
			p.runPass(ctx)
		}
	}
}

// runPass executes one pass inside a recover boundary and accounts the result.
//
// The recover boundary is the unattended-operation requirement: a panic in a
// housekeeping goroutine must not take the process — and the order loop — down with
// it (CLAUDE.md "죽지 않는다"). A panicking pass is rolled back by the enclosing
// transaction's deferred Rollback, so recovering here cannot leave a half-deleted
// intent behind.
func (p *Pruner) runPass(ctx context.Context) {
	stats, err := p.guardedPrune(ctx)
	switch {
	case err != nil && ctx.Err() != nil:
		p.logger.Info("prune pass aborted by shutdown", "error", err)
	case err != nil:
		// Nothing was deleted (the pass is one transaction). Log and keep the cadence.
		p.logger.Error("prune pass failed; nothing was deleted", "error", err)
	case stats.Intents > 0:
		p.logger.Info("pruned terminal intents",
			"intents", stats.Intents, "markers", stats.Markers, "audit_acks", stats.AuditAcks,
			"retention_window", p.retention.String())
	}
}

func (p *Pruner) guardedPrune(ctx context.Context) (stats PruneStats, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("store: recovered panic in prune pass: %v\n%s", rec, debug.Stack())
		}
	}()
	return p.PruneOnce(ctx)
}
