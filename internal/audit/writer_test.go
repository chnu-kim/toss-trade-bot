package audit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var errInjected = errors.New("injected fault")

// spyFS wraps the real filesystem ops, recording every durability call in order
// and optionally injecting an fsync fault. It keeps writes real (temp dir) while
// letting a test observe the segment-durability protocol ordering and drive the
// fail-closed path — the two ACs a plain reopen cannot verify.
type spyFS struct {
	mu       sync.Mutex
	events   []string
	dirArgs  []string // parallel to the "syncDir" events, the dir argument of each call
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
			s.dirArgs = append(s.dirArgs, dir)
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

// dirArgsSnapshot returns the dir argument of each syncDir call, in order.
func (s *spyFS) dirArgsSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.dirArgs...)
}

func (s *spyFS) reset() {
	s.mu.Lock()
	s.events = nil
	s.dirArgs = nil
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

// TestBootDirectoryCreationFsyncsParentDirectory is the issue #23 boot-durability
// AC: when openWriter creates the audit directory for the first time, ADR-0006
// point 4's create-then-parent-fsync discipline must extend to that first
// MkdirAll too — otherwise a crash before the new directory's entry is durable
// could lose the directory (and everything Ack'd inside it) whole. The very
// first durability call must be a syncDir on dir's PARENT (not dir itself —
// that fsync belongs to the first segment's own create protocol, which follows
// it), and the segment-creation protocol must still hold afterward.
func TestBootDirectoryCreationFsyncsParentDirectory(t *testing.T) {
	spy := &spyFS{}
	parent := t.TempDir()
	dir := filepath.Join(parent, "audit")
	_ = newTestWriter(t, dir, spy.ops())

	events := spy.snapshot()
	if len(events) == 0 || events[0] != "syncDir" {
		t.Fatalf("first durability event must be the boot parent-dir fsync, got %v", events)
	}
	dirArgs := spy.dirArgsSnapshot()
	if len(dirArgs) == 0 || dirArgs[0] != parent {
		t.Fatalf("boot fsync must target dir's parent %q, got %v", parent, dirArgs)
	}

	// The segment-creation protocol (content fsync -> rename -> dir fsync) still
	// holds after the boot fsync.
	assertProtocolOrder(t, events)
	// The segment protocol's own dir fsync targets dir itself, not the parent.
	if len(dirArgs) < 2 || dirArgs[1] != dir {
		t.Fatalf("segment-creation dir fsync must target dir %q, got %v", dir, dirArgs)
	}
}

// TestBootExistingDirectorySkipsParentFsync is the "no-op if already existing"
// half of the boot-fsync AC: when dir already exists (e.g. t.TempDir(), or a
// prior run), openWriter must not perform an extra parent-dir fsync — only the
// first segment's own create protocol fsyncs a directory.
func TestBootExistingDirectorySkipsParentFsync(t *testing.T) {
	spy := &spyFS{}
	dir := t.TempDir() // already exists
	_ = newTestWriter(t, dir, spy.ops())

	events := spy.snapshot()
	wantSyncDirCalls := 1 // only the segment-creation protocol's dir fsync
	got := 0
	for _, e := range events {
		if e == "syncDir" {
			got++
		}
	}
	if got != wantSyncDirCalls {
		t.Fatalf("syncDir calls = %d, want %d (no boot fsync for a pre-existing dir): %v", got, wantSyncDirCalls, events)
	}
	dirArgs := spy.dirArgsSnapshot()
	if len(dirArgs) != 1 || dirArgs[0] != dir {
		t.Fatalf("the one syncDir call must target dir itself (segment protocol), got %v", dirArgs)
	}
}

// TestBootDirectoryCreationMultiLevelFsyncsImmediateParent covers MkdirAll
// creating multiple missing ancestors at once. The issue scopes the fix to "at
// least the audit directory's own parent" — this pins that floor: the boot
// fsync targets dir's direct parent, even when that parent (and further
// ancestors) were themselves freshly created by the same MkdirAll call.
func TestBootDirectoryCreationMultiLevelFsyncsImmediateParent(t *testing.T) {
	spy := &spyFS{}
	root := t.TempDir()
	immediateParent := filepath.Join(root, "a", "b")
	dir := filepath.Join(immediateParent, "audit")
	_ = newTestWriter(t, dir, spy.ops())

	dirArgs := spy.dirArgsSnapshot()
	if len(dirArgs) == 0 || dirArgs[0] != immediateParent {
		t.Fatalf("boot fsync must target dir's immediate parent %q, got %v", immediateParent, dirArgs)
	}
}

// TestBootParentFsyncFailureFailsClosed guards the failure path: if the boot
// parent-dir fsync itself fails (disk trouble), openWriter must fail closed
// rather than silently proceeding with a directory whose entry may not survive
// a crash. The fault is scoped to ONLY the parent-dir syncDir call (not the
// segment-creation protocol's own dir fsync of dir itself), so this isolates a
// failure specifically in the boot fsync from the already-covered segment-
// creation fsync failure path.
func TestBootParentFsyncFailureFailsClosed(t *testing.T) {
	real := realFS()
	parent := t.TempDir()
	dir := filepath.Join(parent, "audit")
	fs := fsOps{
		syncFile: real.syncFile,
		rename:   real.rename,
		syncDir: func(d string) error {
			if d == parent {
				return errInjected
			}
			return real.syncDir(d)
		},
	}

	_, err := openWriter(dir, fs, config{maxSegmentSize: defaultMaxSegmentSize})
	if err == nil {
		t.Fatal("openWriter with a failing boot parent-dir fsync returned nil error; must fail-closed")
	}
	if !IsFailClosed(err) {
		t.Errorf("error is not a fail-closed signal: %v", err)
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
