package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var errInjected = errors.New("injected fault")

// spyFS wraps the real filesystem ops, recording every durability call in order
// and optionally injecting an fsync fault. It keeps writes real (temp dir) while
// letting a test observe the segment-durability protocol ordering and drive the
// fail-closed path — the two ACs a plain reopen cannot verify.
type spyFS struct {
	mu       sync.Mutex
	events   []string
	failSync bool
	failDir  bool
}

func (s *spyFS) ops() fsOps {
	real := realFS()
	return fsOps{
		syncFile: func(f *os.File) error {
			s.mu.Lock()
			tag := "syncFile:active"
			if strings.Contains(filepath.Base(f.Name()), ".tmp-") {
				tag = "syncFile:tmp"
			}
			s.events = append(s.events, tag)
			fail := s.failSync
			s.mu.Unlock()
			if fail {
				return errInjected
			}
			return real.syncFile(f)
		},
		syncDir: func(dir string) error {
			s.mu.Lock()
			s.events = append(s.events, "syncDir")
			fail := s.failDir
			s.mu.Unlock()
			if fail {
				return errInjected
			}
			return real.syncDir(dir)
		},
		rename: func(oldp, newp string) error {
			s.mu.Lock()
			s.events = append(s.events, "rename")
			s.mu.Unlock()
			return real.rename(oldp, newp)
		},
	}
}

func (s *spyFS) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

func (s *spyFS) reset() {
	s.mu.Lock()
	s.events = nil
	s.mu.Unlock()
}

func newTestWriter(t *testing.T, dir string, fs fsOps, opts ...Option) *Writer {
	t.Helper()
	cfg := config{maxSegmentSize: defaultMaxSegmentSize}
	for _, o := range opts {
		o(&cfg)
	}
	w, err := openWriter(dir, fs, cfg)
	if err != nil {
		t.Fatalf("openWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

// assertProtocolOrder checks the ADR-0006 point 4 protocol: every atomic rename
// is immediately preceded by a temp content-fsync and immediately followed by a
// parent-directory fsync. Content-fsync alone is not durable (POSIX trap).
func assertProtocolOrder(t *testing.T, events []string) {
	t.Helper()
	sawRename := false
	for i, e := range events {
		if e != "rename" {
			continue
		}
		sawRename = true
		if i == 0 || events[i-1] != "syncFile:tmp" {
			t.Errorf("rename at %d not preceded by content fsync: %v", i, events)
		}
		if i+1 >= len(events) || events[i+1] != "syncDir" {
			t.Errorf("rename at %d not followed by dir fsync: %v", i, events)
		}
	}
	if !sawRename {
		t.Errorf("no atomic rename observed; events: %v", events)
	}
}

// TestSegmentCreationFollowsDurabilityProtocol is the ADR-0006 point 4 guard:
// the very first segment must be created content-fsync → atomic rename → parent
// dir fsync, so a crash before the directory entry is durable never loses the
// segment whole (the POSIX directory-entry trap).
func TestSegmentCreationFollowsDurabilityProtocol(t *testing.T) {
	spy := &spyFS{}
	dir := t.TempDir()
	_ = newTestWriter(t, dir, spy.ops())
	assertProtocolOrder(t, spy.snapshot())
}

// TestRotationFollowsDurabilityProtocol guards that segment ROTATION (not just
// initial creation) also runs the full protocol, and that the rotated-in segment
// entry is made durable via a parent-dir fsync.
func TestRotationFollowsDurabilityProtocol(t *testing.T) {
	spy := &spyFS{}
	dir := t.TempDir()
	w := newTestWriter(t, dir, spy.ops(), WithMaxSegmentSize(256))
	ctx := context.Background()

	spy.reset()
	// Emit enough records to force at least one rotation.
	for i := 0; i < 40; i++ {
		if _, err := w.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: fmt.Sprintf("m-%d", i)}); err != nil {
			t.Fatalf("EmitError %d: %v", i, err)
		}
	}
	assertProtocolOrder(t, spy.snapshot())

	segs, _ := filepath.Glob(filepath.Join(dir, "segment-*.log"))
	if len(segs) < 2 {
		t.Fatalf("expected rotation into >=2 segments, got %d", len(segs))
	}
}

// TestEmitErrorFailClosedOnDurableWriteFailure is the most important safety AC:
// when the durable write cannot complete (fsync fails), EmitError returns a
// FailClosedError, returns no Ack, and leaves nothing committed (ADR-0006 point
// 3/6). The failure then LATCHES sticky — clearing the injected fault does not
// let the writer silently self-heal; every later Emit still fails closed, and
// recovery is a process restart via New that re-derives the sequence from the
// durable record count (never advanced by the failed emit). This is untestable
// without an injectable fsync; a healthy temp dir never fails fsync on its own.
func TestEmitErrorFailClosedOnDurableWriteFailure(t *testing.T) {
	spy := &spyFS{}
	dir := t.TempDir()
	w := newTestWriter(t, dir, spy.ops())
	ctx := context.Background()

	// One good record first, so we can prove the failed one didn't advance seq.
	if _, err := w.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: "ok"}); err != nil {
		t.Fatalf("baseline EmitError: %v", err)
	}

	spy.mu.Lock()
	spy.failSync = true
	spy.mu.Unlock()

	ack, err := w.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: "doomed"})
	if err == nil {
		t.Fatal("EmitError returned nil error on fsync failure; must fail-closed")
	}
	if !IsFailClosed(err) {
		t.Errorf("error is not a fail-closed signal: %v", err)
	}
	if ack != (Ack{}) {
		t.Errorf("Ack must be zero on failure, got %+v", ack)
	}

	// Clear the injected fault: the durability latch must be sticky, so the next
	// emit still fails closed rather than self-healing (and must not panic on the
	// now-consistent-but-latched writer).
	spy.mu.Lock()
	spy.failSync = false
	spy.mu.Unlock()

	if _, err := w.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: "after"}); !IsFailClosed(err) {
		t.Fatalf("emit after a latched fsync failure = %v; latch must be sticky (no self-heal)", err)
	}

	// Exactly one record was ever committed; neither the doomed nor the post-latch
	// emit persisted.
	recs, err := readAll(dir)
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("committed records = %d, want 1 (doomed/after must not persist): %+v", len(recs), recs)
	}

	// Recovery is a process restart: reopen and confirm the sequence resumes at 1
	// — the failed emits never advanced the durable append position.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	w2 := newTestWriter(t, dir, realFS())
	ack2, err := w2.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: "after-restart"})
	if err != nil {
		t.Fatalf("EmitError after restart: %v", err)
	}
	if ack2.Sequence != 1 {
		t.Errorf("resumed sequence = %d, want 1 (failed emit must not advance seq)", ack2.Sequence)
	}
}

// TestCommitLatchesFailClosedOnFaultedRotation covers the commit-path rotation
// durability failure the existing fsync test could not reach: when createSegment
// fails mid-rotation (here the parent-dir fsync of step iii), the active segment
// has already been closed to nil, so without a latch the writer would carry a
// stale activeSize and a later rotation-triggering record would silently retry
// createSegment and self-heal. The fix latches the failure sticky: (1) the
// faulted emit fails closed; (2) after the fault is cleared, a further emit STILL
// fails closed (no self-heal) and does not panic on the nil active segment.
func TestCommitLatchesFailClosedOnFaultedRotation(t *testing.T) {
	spy := &spyFS{}
	dir := t.TempDir()
	// 64 bytes is smaller than a single error frame, so after the first record the
	// active size already exceeds the threshold and the NEXT emit must rotate.
	w := newTestWriter(t, dir, spy.ops(), WithMaxSegmentSize(64))
	ctx := context.Background()

	// First record lands in the initial segment without rotating.
	if _, err := w.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: "first"}); err != nil {
		t.Fatalf("baseline EmitError: %v", err)
	}
	// Self-diagnosing precondition: the next emit must be the one that rotates. If
	// rotation had already happened, syncDir would fire early and the test would
	// fault the wrong call.
	if segs, _ := filepath.Glob(filepath.Join(dir, "segment-*.log")); len(segs) != 1 {
		t.Fatalf("precondition: want 1 segment before the faulted rotation, got %d", len(segs))
	}

	// Arm a parent-dir fsync fault: the next emit rotates and createSegment's step
	// (iii) fails → a commit-path durability failure.
	spy.mu.Lock()
	spy.failDir = true
	spy.mu.Unlock()

	ack, err := w.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: "rotate-fault"})
	if err == nil {
		t.Fatal("emit that triggers a faulted rotation returned nil; must fail-closed")
	}
	if !IsFailClosed(err) {
		t.Errorf("faulted rotation is not a fail-closed signal: %v", err)
	}
	if errors.Is(err, ErrClosed) {
		t.Errorf("durability latch must be distinguishable from a voluntary Close: %v", err)
	}
	if ack != (Ack{}) {
		t.Errorf("Ack must be zero on a faulted rotation, got %+v", ack)
	}

	// Clear the fault; the latch must stay sticky (no self-heal) and must not panic
	// dereferencing the nil active segment left by the failed rotation.
	spy.mu.Lock()
	spy.failDir = false
	spy.mu.Unlock()

	if _, err := w.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: "after"}); !IsFailClosed(err) {
		t.Fatalf("emit after a latched rotation failure = %v; latch must be sticky", err)
	}
}

// TestFaultedRotationLatchPreservesPriorDurableRecords is the integrity half:
// latching on a faulted rotation must not corrupt records committed before it.
// After several rotations' worth of durable records, a faulted rotation latches
// the writer; on restart the pre-failure records are all recovered with a dense
// contiguous sequence, and the resumed sequence continues from the durable count.
func TestFaultedRotationLatchPreservesPriorDurableRecords(t *testing.T) {
	spy := &spyFS{}
	dir := t.TempDir()
	w := newTestWriter(t, dir, spy.ops(), WithMaxSegmentSize(64))
	ctx := context.Background()

	const good = 3
	for i := 0; i < good; i++ {
		if _, err := w.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: fmt.Sprintf("g-%d", i)}); err != nil {
			t.Fatalf("EmitError %d: %v", i, err)
		}
	}

	// Fault the next rotation → latch.
	spy.mu.Lock()
	spy.failDir = true
	spy.mu.Unlock()
	if _, err := w.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: "doomed"}); !IsFailClosed(err) {
		t.Fatalf("expected fail-closed on the faulted rotation, got %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Restart with a healthy fs: the pre-failure records survive intact.
	w2 := newTestWriter(t, dir, realFS(), WithMaxSegmentSize(64))
	recs, err := readAll(dir)
	if err != nil {
		t.Fatalf("readAll after restart: %v", err)
	}
	if len(recs) != good {
		t.Fatalf("recovered records = %d, want %d (latch must not corrupt prior durable data): %+v", len(recs), good, recs)
	}
	for i, r := range recs {
		if r.Seq != int64(i) {
			t.Errorf("record[%d].Seq = %d, want %d (sequence must stay dense across the latch)", i, r.Seq, i)
		}
	}
	ack, err := w2.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: "resumed"})
	if err != nil {
		t.Fatalf("EmitError after restart: %v", err)
	}
	if ack.Sequence != int64(good) {
		t.Errorf("resumed sequence = %d, want %d (must derive from the durable count, not the failed emit)", ack.Sequence, good)
	}
}

// TestSequenceMonotonicAcrossRotation is the ADR-0006 AC that rotation does not
// break the derivation of the durable append sequence: sequences stay a dense
// monotonic 0..N-1 even as records cross segment boundaries.
func TestSequenceMonotonicAcrossRotation(t *testing.T) {
	dir := t.TempDir()
	w := newTestWriter(t, dir, realFS(), WithMaxSegmentSize(200))
	ctx := context.Background()

	const n = 50
	for i := 0; i < n; i++ {
		ack, err := w.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: fmt.Sprintf("m-%d", i)})
		if err != nil {
			t.Fatalf("EmitError %d: %v", i, err)
		}
		if ack.Sequence != int64(i) {
			t.Fatalf("Ack.Sequence = %d, want %d (monotonic across rotation)", ack.Sequence, i)
		}
	}
	segs, _ := filepath.Glob(filepath.Join(dir, "segment-*.log"))
	if len(segs) < 2 {
		t.Fatalf("test did not exercise rotation: %d segments", len(segs))
	}

	// Error keys embed the sequence, so all n keys must be distinct.
	recs, err := readAll(dir)
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}
	if len(recs) != n {
		t.Fatalf("committed records = %d, want %d", len(recs), n)
	}
	seen := map[string]bool{}
	for i, r := range recs {
		if r.Seq != int64(i) {
			t.Errorf("record[%d].Seq = %d, want %d", i, r.Seq, i)
		}
		if seen[r.IdempotencyKey] {
			t.Errorf("duplicate error key across rotation: %q", r.IdempotencyKey)
		}
		seen[r.IdempotencyKey] = true
	}
}

// TestTornTailDiscardedOnRecovery is the ADR-0006 torn-tail AC: an uncommitted
// (partially written) trailing record is discarded on restart, committed priors
// survive, and the sequence resumes consistently.
func TestTornTailDiscardedOnRecovery(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	w := newTestWriter(t, dir, realFS())
	for i := 0; i < 3; i++ {
		if _, err := w.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: fmt.Sprintf("m-%d", i)}); err != nil {
			t.Fatalf("EmitError %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Simulate a crash mid-append: append a torn (garbage) partial frame to the
	// active segment tail.
	segs, _ := filepath.Glob(filepath.Join(dir, "segment-*.log"))
	if len(segs) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segs))
	}
	f, err := os.OpenFile(segs[len(segs)-1], os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open for corruption: %v", err)
	}
	// A plausible-looking but incomplete frame: a length header claiming more
	// bytes than follow.
	if _, err := f.Write([]byte{0x00, 0x00, 0x10, 0x00, 0xde, 0xad, 0xbe, 0xef, 0x01, 0x02}); err != nil {
		t.Fatalf("write torn tail: %v", err)
	}
	f.Close()

	// Reopen: recovery must discard the torn tail and keep the 3 committed records.
	w2 := newTestWriter(t, dir, realFS())
	recs, err := readAll(dir)
	if err != nil {
		t.Fatalf("readAll after recovery: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("records after torn-tail recovery = %d, want 3: %+v", len(recs), recs)
	}
	// Next emitted record resumes at seq 3, contiguous with the survivors.
	ack, err := w2.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: "resumed"})
	if err != nil {
		t.Fatalf("EmitError after recovery: %v", err)
	}
	if ack.Sequence != 3 {
		t.Errorf("resumed sequence = %d, want 3", ack.Sequence)
	}
}

// TestFillIdempotencyMergeAndCorrection is the ADR-0006 fill AC end-to-end on the
// real writer: re-polling the same snapshot yields the same idempotency key (a
// consumer merges them), while a same-quantity correction lands as a new record.
func TestFillIdempotencyMergeAndCorrection(t *testing.T) {
	dir := t.TempDir()
	w := newTestWriter(t, dir, realFS())
	ctx := context.Background()

	snap := FillSnapshot{FilledQuantity: "10", AverageFilledPrice: "100", FilledAmount: "1000", Commission: "1.0", Tax: "0.5", SettlementDate: "2026-07-08"}

	a1, err := w.EmitFill(ctx, FillEvent{OrderID: "o1", Snapshot: snap})
	if err != nil {
		t.Fatalf("EmitFill 1: %v", err)
	}
	a2, err := w.EmitFill(ctx, FillEvent{OrderID: "o1", Snapshot: snap}) // identical re-poll
	if err != nil {
		t.Fatalf("EmitFill 2: %v", err)
	}
	if a1.IdempotencyKey != a2.IdempotencyKey {
		t.Errorf("identical re-poll produced different keys: %q vs %q", a1.IdempotencyKey, a2.IdempotencyKey)
	}
	if a1.Sequence == a2.Sequence {
		t.Errorf("append-only sink must give each emit its own sequence: both %d", a1.Sequence)
	}

	// Same cumulative quantity, corrected commission → new record (new key).
	corrected := snap
	corrected.Commission = "1.2"
	a3, err := w.EmitFill(ctx, FillEvent{OrderID: "o1", Snapshot: corrected})
	if err != nil {
		t.Fatalf("EmitFill 3: %v", err)
	}
	if a3.IdempotencyKey == a1.IdempotencyKey {
		t.Errorf("same-quantity fee correction was deduped away (key unchanged): %q", a3.IdempotencyKey)
	}

	recs, err := readAll(dir)
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("records = %d, want 3 (append-only, no write-time dedup)", len(recs))
	}
	byKey := map[string]int{}
	for _, r := range recs {
		byKey[r.IdempotencyKey]++
	}
	if byKey[a1.IdempotencyKey] != 2 {
		t.Errorf("identical snapshot should share one key across 2 records, got %d", byKey[a1.IdempotencyKey])
	}
	if byKey[a3.IdempotencyKey] != 1 {
		t.Errorf("corrected snapshot should be its own record, got %d", byKey[a3.IdempotencyKey])
	}
}

// TestLeftoverTempIgnoredOnRecovery guards the crash-between-fsync-and-rename
// window: a leftover .tmp-* file must not be mistaken for a segment, and the
// writer must still open and function.
func TestLeftoverTempIgnoredOnRecovery(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	w := newTestWriter(t, dir, realFS())
	if _, err := w.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: "m0"}); err != nil {
		t.Fatalf("EmitError: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Simulate a crash that left a half-created temp segment behind.
	leftover := filepath.Join(dir, ".tmp-segment-99999999999999999999.log")
	if err := os.WriteFile(leftover, []byte("AUDIT\x00\x01garbage"), 0o644); err != nil {
		t.Fatalf("write leftover: %v", err)
	}

	w2 := newTestWriter(t, dir, realFS())
	// Leftover must be gone (or at least not counted as a segment).
	segs, _ := filepath.Glob(filepath.Join(dir, "segment-*.log"))
	if len(segs) != 1 {
		t.Errorf("segment count = %d, want 1 (leftover temp must not count)", len(segs))
	}
	recs, err := readAll(dir)
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1", len(recs))
	}
	if _, err := w2.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: "m1"}); err != nil {
		t.Fatalf("EmitError after leftover recovery: %v", err)
	}
}

// TestConcurrentEmitsAllDurableUniqueSeq drives the sink from many goroutines
// under -race: every emit must be durably recorded and every sequence unique and
// dense (single-writer serialization, no torn interleave).
func TestConcurrentEmitsAllDurableUniqueSeq(t *testing.T) {
	dir := t.TempDir()
	w := newTestWriter(t, dir, realFS(), WithMaxSegmentSize(512))
	ctx := context.Background()

	const goroutines = 8
	const each = 25
	var wg sync.WaitGroup
	var mu sync.Mutex
	seqs := map[int64]bool{}
	errs := make(chan error, goroutines*each)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				ack, err := w.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: fmt.Sprintf("g%d-%d", g, i)})
				if err != nil {
					errs <- err
					return
				}
				mu.Lock()
				if seqs[ack.Sequence] {
					errs <- fmt.Errorf("duplicate sequence %d", ack.Sequence)
				}
				seqs[ack.Sequence] = true
				mu.Unlock()
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent emit: %v", err)
	}

	total := goroutines * each
	if len(seqs) != total {
		t.Fatalf("unique sequences = %d, want %d", len(seqs), total)
	}
	for i := 0; i < total; i++ {
		if !seqs[int64(i)] {
			t.Errorf("missing sequence %d (not dense)", i)
		}
	}
	recs, err := readAll(dir)
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}
	if len(recs) != total {
		t.Fatalf("durable records = %d, want %d", len(recs), total)
	}
}

// TestRestartResumesSequence guards restart safety: after a clean close and
// reopen, the sequence resumes from the committed count, never resetting.
func TestRestartResumesSequence(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	w := newTestWriter(t, dir, realFS())
	for i := 0; i < 5; i++ {
		if _, err := w.EmitOrderLifecycle(ctx, OrderLifecycleEvent{IntentID: fmt.Sprintf("i%d", i), Marker: "prepared"}); err != nil {
			t.Fatalf("EmitOrderLifecycle %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	w2 := newTestWriter(t, dir, realFS())
	ack, err := w2.EmitOrderLifecycle(ctx, OrderLifecycleEvent{IntentID: "i5", Marker: "prepared"})
	if err != nil {
		t.Fatalf("EmitOrderLifecycle after reopen: %v", err)
	}
	if ack.Sequence != 5 {
		t.Errorf("resumed sequence = %d, want 5 (must not reset on restart)", ack.Sequence)
	}
}

// TestRecoveryDerivesSequenceFromDurableCountAcrossSegments realizes the ADR-0006
// claim that the sequence "derives from the committed durable append position, not
// a separate counter". After rotation into several segments and a restart,
// openWriter must recompute the sequence by summing committed records across ALL
// segments (the total += count recovery path) — so the resumed sequence equals the
// durable record count, not a reset and not a surviving in-memory counter. Every
// other reopen test uses a single 8 MiB segment; only this one walks the sum.
func TestRecoveryDerivesSequenceFromDurableCountAcrossSegments(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	const n = 20
	w := newTestWriter(t, dir, realFS(), WithMaxSegmentSize(200))
	for i := 0; i < n; i++ {
		if _, err := w.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: fmt.Sprintf("m-%d", i)}); err != nil {
			t.Fatalf("EmitError %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	segs, _ := filepath.Glob(filepath.Join(dir, "segment-*.log"))
	if len(segs) < 2 {
		t.Fatalf("test needs multiple segments to exercise the recovery sum, got %d", len(segs))
	}
	recs, err := readAll(dir)
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}
	if len(recs) != n {
		t.Fatalf("durable records = %d, want %d", len(recs), n)
	}

	// Reopen: recovery must walk every segment and sum committed records.
	w2 := newTestWriter(t, dir, realFS(), WithMaxSegmentSize(200))
	ack, err := w2.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: "resumed"})
	if err != nil {
		t.Fatalf("EmitError after reopen: %v", err)
	}
	if ack.Sequence != int64(n) {
		t.Errorf("resumed sequence = %d, want %d (must derive from durable count across all segments)", ack.Sequence, n)
	}
	// The resumed error key embeds seq n; it must not collide with any prior.
	recs2, err := readAll(dir)
	if err != nil {
		t.Fatalf("readAll after resume: %v", err)
	}
	if len(recs2) != n+1 {
		t.Fatalf("records after resume = %d, want %d", len(recs2), n+1)
	}
	seen := map[string]bool{}
	for _, r := range recs2 {
		if seen[r.IdempotencyKey] {
			t.Errorf("duplicate error key after multi-segment recovery: %q", r.IdempotencyKey)
		}
		seen[r.IdempotencyKey] = true
	}
}

// TestTornTailAfterRotationRecovery is the realistic crash-mid-append-after-
// rotation case: sealed segments precede a torn tail on the active segment.
// Recovery must sum the sealed priors AND discard the torn tail together, leaving
// exactly the committed records and a contiguous resumed sequence.
func TestTornTailAfterRotationRecovery(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	const n = 15
	w := newTestWriter(t, dir, realFS(), WithMaxSegmentSize(200))
	for i := 0; i < n; i++ {
		if _, err := w.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: fmt.Sprintf("m-%d", i)}); err != nil {
			t.Fatalf("EmitError %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	segs, _ := filepath.Glob(filepath.Join(dir, "segment-*.log")) // sorted
	if len(segs) < 2 {
		t.Fatalf("test needs multiple segments, got %d", len(segs))
	}

	// Corrupt only the LAST (active) segment's tail with a torn partial frame.
	last := segs[len(segs)-1]
	f, err := os.OpenFile(last, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open last for corruption: %v", err)
	}
	if _, err := f.Write([]byte{0x00, 0x00, 0x20, 0x00, 0xba, 0xad}); err != nil {
		t.Fatalf("write torn tail: %v", err)
	}
	f.Close()

	w2 := newTestWriter(t, dir, realFS(), WithMaxSegmentSize(200))
	recs, err := readAll(dir)
	if err != nil {
		t.Fatalf("readAll after recovery: %v", err)
	}
	if len(recs) != n {
		t.Fatalf("records after torn-tail-after-rotation recovery = %d, want %d (sealed priors + surviving tail)", len(recs), n)
	}
	ack, err := w2.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: "resumed"})
	if err != nil {
		t.Fatalf("EmitError after recovery: %v", err)
	}
	if ack.Sequence != int64(n) {
		t.Errorf("resumed sequence = %d, want %d", ack.Sequence, n)
	}
}

// TestEmitAfterClose fails closed rather than silently dropping.
func TestEmitAfterClose(t *testing.T) {
	dir := t.TempDir()
	w := newTestWriter(t, dir, realFS())
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := w.EmitError(context.Background(), ErrorEvent{Operation: "op", ErrorClass: "class"}); err == nil {
		t.Error("EmitError on closed sink returned nil error")
	}
}

// --- issue #25: write-time oversize rejection + file permissions ---

// mustMarshalRecordSize marshals rec exactly as commit() does and returns the
// resulting byte length — the same quantity commit()'s write-time bound check
// and readFrame's recovery-time bound check both compare against.
func mustMarshalRecordSize(t *testing.T, rec record) int {
	t.Helper()
	b, err := json.Marshal(&rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return len(b)
}

// TestOversizeRecordBoundaryWriteTimeRejection is the issue #25 boundary AC: at
// the maxRecordSize edge, a record that fits is accepted and durably
// recoverable, while one byte over is rejected at write time (no ack, nothing
// committed) without poisoning the writer — a normal-size record right after
// still commits, and survives restart alongside the boundary record.
func TestOversizeRecordBoundaryWriteTimeRejection(t *testing.T) {
	dir := t.TempDir()
	w := newTestWriter(t, dir, realFS())
	ctx := context.Background()
	fixedTime := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)

	// buildMessage computes, by direct construction + measurement (not
	// trial-and-error), the Message length that makes the exact record commit()
	// will marshal land at precisely `target` bytes. It self-verifies the
	// computed length actually hits the target, so a future drift in the record
	// struct's shape fails loudly here instead of silently mis-measuring the
	// boundary.
	buildMessage := func(seq int64, target int) string {
		t.Helper()
		key := errorKey("", "", "op", "class", seq)
		// Anchor with a 1-byte message (not 0: Message has `omitempty`, so an
		// empty string would drop the whole "message" key and misrepresent the
		// overhead of the field actually being present).
		anchor := record{Kind: KindError, OccurredAt: fixedTime, Operation: "op", ErrorClass: "class", Message: "a", Seq: seq, IdempotencyKey: key}
		size1 := mustMarshalRecordSize(t, anchor)
		msgLen := 1 + (target - size1)
		if msgLen < 1 {
			t.Fatalf("target %d is smaller than the record's fixed overhead (%d bytes with a 1-byte message)", target, size1)
		}
		msg := strings.Repeat("a", msgLen)
		got := mustMarshalRecordSize(t, record{Kind: KindError, OccurredAt: fixedTime, Operation: "op", ErrorClass: "class", Message: msg, Seq: seq, IdempotencyKey: key})
		if got != target {
			t.Fatalf("computed message length %d yields marshaled size %d, want %d", msgLen, got, target)
		}
		return msg
	}

	// Exactly at maxRecordSize: must be accepted and durably committed.
	atMax := buildMessage(w.seq, maxRecordSize)
	ack, err := w.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: atMax, OccurredAt: fixedTime})
	if err != nil {
		t.Fatalf("EmitError at maxRecordSize boundary: %v", err)
	}
	if ack == (Ack{}) {
		t.Fatal("expected a non-zero Ack for a record exactly at maxRecordSize")
	}

	// One byte over maxRecordSize: must be rejected at write time.
	over := buildMessage(w.seq, maxRecordSize+1)
	ack2, err := w.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: over, OccurredAt: fixedTime})
	if err == nil {
		t.Fatal("EmitError one byte over maxRecordSize returned nil error; must reject at write time")
	}
	if !IsRecordTooLarge(err) {
		t.Errorf("oversize rejection is not IsRecordTooLarge: %v", err)
	}
	if IsFailClosed(err) {
		t.Errorf("oversize rejection must NOT satisfy IsFailClosed (must not read as a killswitch-trip signal): %v", err)
	}
	if ack2 != (Ack{}) {
		t.Errorf("Ack must be zero on oversize rejection, got %+v", ack2)
	}

	// The writer must not be poisoned: a normal-size record right after the
	// rejection still commits.
	ack3, err := w.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: "normal-after-rejection"})
	if err != nil {
		t.Fatalf("EmitError after oversize rejection: %v", err)
	}
	if ack3 == (Ack{}) {
		t.Fatal("expected a non-zero Ack for a normal record following an oversize rejection")
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Self-diagnosing precondition: the boundary record's frame alone already
	// exceeds the default segment threshold, so the post-rejection record must
	// have rotated into a second segment — sealing the first (the boundary
	// record) rather than leaving it active. If this ever stops holding, the
	// restart below would only exercise the active-segment path, not the
	// sealed-segment boot-brick path the AC cares about.
	if segs, _ := filepath.Glob(filepath.Join(dir, "segment-*.log")); len(segs) != 2 {
		t.Fatalf("precondition: want 2 segments (boundary record sealed, post-rejection record active), got %d", len(segs))
	}

	// Restart THROUGH openWriter, not just readAll: this is what actually
	// exercises H-2's two destruction paths — a sealed segment's
	// sealed-segment-corrupt fail-closed boot-brick, and an active segment's
	// truncateActiveTail silently discarding an ack'd record. The boundary
	// (maxRecordSize) record landed alone in the first (now sealed, since the
	// post-rejection record rotated into a second segment) segment, so a
	// successful reopen here is itself the boot-brick-absence proof the AC
	// requires — readAll alone never touches openWriter and would not catch a
	// regression here.
	w2 := newTestWriter(t, dir, realFS())

	// The rejected oversize record must not have consumed a sequence: the
	// resumed sequence is 2 (seq 0 = boundary, seq 1 = post-rejection normal).
	resumedAck, err := w2.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: "resumed"})
	if err != nil {
		t.Fatalf("EmitError after restart: %v", err)
	}
	if resumedAck.Sequence != 2 {
		t.Errorf("resumed sequence = %d, want 2 (rejected record must not have consumed a sequence)", resumedAck.Sequence)
	}

	// Exactly the two accepted records plus the post-restart one survive — the
	// boundary record intact (not truncated) and the post-rejection normal
	// record. The rejected oversize record was never committed and must not
	// appear.
	recs, err := readAll(dir)
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("recovered records = %d, want 3 (boundary + post-rejection normal + post-restart): seqs=%v", len(recs), seqsOf(recs))
	}
	if len(recs[0].Message) != len(atMax) {
		t.Errorf("recovered boundary record message length = %d, want %d (must not be truncated)", len(recs[0].Message), len(atMax))
	}
	if recs[1].Message != "normal-after-rejection" {
		t.Errorf("recovered second record message = %q, want %q", recs[1].Message, "normal-after-rejection")
	}
	if recs[2].Message != "resumed" {
		t.Errorf("recovered third record message = %q, want %q", recs[2].Message, "resumed")
	}
}

func seqsOf(recs []record) []int64 {
	out := make([]int64, len(recs))
	for i, r := range recs {
		out[i] = r.Seq
	}
	return out
}

// TestSegmentAndDirPermissionsAreOwnerOnly is the issue #25 permissions AC: a
// freshly created audit directory and segment file are exactly 0700/0600, even
// under a maximally permissive process umask — proving the writer enforces the
// mode explicitly (via Chmod) rather than relying on the umask-masked
// MkdirAll/OpenFile request, which would NOT be independent of umask.
func TestSegmentAndDirPermissionsAreOwnerOnly(t *testing.T) {
	// A not-yet-existing leaf directory under an existing parent, so New()
	// takes the fresh-creation path (t.TempDir() itself already exists at
	// 0700, which would take the pre-existing warn-only path instead of
	// exercising the Chmod). The parent is resolved BEFORE the umask is
	// tightened below, so only the leaf mkdir is subject to it — MkdirAll's
	// own intermediate-parent creation under a maximally hostile umask is a
	// separate (out-of-scope) concern from this issue's dirPerm/segmentPerm
	// bound.
	dir := filepath.Join(t.TempDir(), "audit")

	old := syscall.Umask(0o777) // strip every bit MkdirAll/OpenFile would request
	t.Cleanup(func() { syscall.Umask(old) })

	w := newTestWriter(t, dir, realFS())

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("audit dir mode = %o, want 0700 (regardless of umask)", got)
	}

	if _, err := w.EmitError(context.Background(), ErrorEvent{Operation: "op", ErrorClass: "class", Message: "m"}); err != nil {
		t.Fatalf("EmitError: %v", err)
	}

	segs, err := filepath.Glob(filepath.Join(dir, "segment-*.log"))
	if err != nil || len(segs) != 1 {
		t.Fatalf("glob segments: %v, %v", segs, err)
	}
	segInfo, err := os.Stat(segs[0])
	if err != nil {
		t.Fatalf("stat segment: %v", err)
	}
	if got := segInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("segment mode = %o, want 0600 (regardless of umask)", got)
	}
}

// TestExistingPermissiveDirWarnsButIsNotAutoTightened covers task 3's "validate
// + warn" requirement for a directory that predates this New() call: New()
// must not silently chmod an operator-managed existing directory, but must
// surface a warning through the injected logger so the wider-than-owner mode
// is visible in the unattended bot's diagnostics.
func TestExistingPermissiveDirWarnsButIsNotAutoTightened(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod dir permissive: %v", err)
	}

	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	w, err := New(dir, WithLogger(logger))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("pre-existing directory mode changed to %o; New() must not silently tighten it, only warn", got)
	}
	if !strings.Contains(buf.String(), "audit directory") {
		t.Errorf("expected a permission warning logged for the pre-existing permissive directory, got: %s", buf.String())
	}
}

// TestReopenTightensPreExistingSegmentPermissions covers the upgrade gap a
// GitHub-native codex review flagged on PR #31: segments written by a version
// of this package predating the 0600 segmentPerm tightening (issue #25)
// otherwise stay group/world-readable forever after upgrade, because
// recovery's OpenFile calls have no O_CREATE (so their mode argument is
// ignored) and nothing else ever revisits an existing segment's mode. New()
// must actively tighten every pre-existing segment — sealed AND active — to
// segmentPerm on open, not just the containing directory.
func TestReopenTightensPreExistingSegmentPermissions(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Small segment size forces rotation, so recovery on reopen must handle
	// BOTH a sealed segment and the active one.
	w := newTestWriter(t, dir, realFS(), WithMaxSegmentSize(64))
	for i := 0; i < 5; i++ {
		if _, err := w.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: fmt.Sprintf("m-%d", i)}); err != nil {
			t.Fatalf("EmitError %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	segs, err := filepath.Glob(filepath.Join(dir, "segment-*.log"))
	if err != nil || len(segs) < 2 {
		t.Fatalf("glob segments: %v, %v (need >=2 to cover sealed+active)", segs, err)
	}

	// Simulate legacy data: every segment written under the pre-fix 0644 mode.
	for _, s := range segs {
		if err := os.Chmod(s, 0o644); err != nil {
			t.Fatalf("chmod legacy segment %s: %v", s, err)
		}
	}

	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	w2, err := New(dir, WithMaxSegmentSize(64), WithLogger(logger))
	if err != nil {
		t.Fatalf("New (reopen over legacy 0644 segments): %v", err)
	}
	t.Cleanup(func() { _ = w2.Close() })

	for _, s := range segs {
		info, err := os.Stat(s)
		if err != nil {
			t.Fatalf("stat %s: %v", s, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("segment %s mode after reopen = %o, want 0600 (pre-existing segments must be tightened, not left at their legacy mode)", s, got)
		}
	}
	if !strings.Contains(buf.String(), "tightened") {
		t.Errorf("expected a log line about tightening pre-existing segment permissions, got: %s", buf.String())
	}

	// The writer must still be fully functional after the tightening — a
	// further emit commits normally and recovers.
	if _, err := w2.EmitError(ctx, ErrorEvent{Operation: "op", ErrorClass: "class", Message: "after-tighten"}); err != nil {
		t.Fatalf("EmitError after permission tightening: %v", err)
	}
	recs, err := readAll(dir)
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}
	if len(recs) != 6 {
		t.Fatalf("records = %d, want 6 (5 legacy + 1 after tightening)", len(recs))
	}
}
