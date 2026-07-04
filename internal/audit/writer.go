package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// defaultMaxSegmentSize is the byte threshold at which a segment rotates.
	defaultMaxSegmentSize = 8 << 20 // 8 MiB
	segmentPrefix         = "segment-"
	segmentSuffix         = ".log"
	tempPrefix            = ".tmp-"
	segmentPerm           = 0o644
	dirPerm               = 0o755
)

// segmentHeader marks a file as an audit segment and versions its format, so a
// stray or corrupt file is not mistaken for committed records.
var segmentHeader = []byte("AUDIT\x00\x01")

// record is the on-disk audit record (the framed JSON payload). Seq is the
// global durable append position; IdempotencyKey is the per-class merge handle.
type record struct {
	Seq            int64         `json:"seq"`
	Kind           Kind          `json:"kind"`
	IdempotencyKey string        `json:"idempotency_key"`
	OccurredAt     time.Time     `json:"occurred_at"`
	IntentID       string        `json:"intent_id,omitempty"`
	OrderID        string        `json:"order_id,omitempty"`
	Marker         string        `json:"marker,omitempty"`
	Detail         string        `json:"detail,omitempty"`
	Fill           *FillSnapshot `json:"fill,omitempty"`
	Operation      string        `json:"operation,omitempty"`
	ErrorClass     string        `json:"error_class,omitempty"`
	Message        string        `json:"message,omitempty"`
}

// fsOps groups the durability-critical filesystem operations so tests can
// observe protocol ordering and inject faults while the writer still writes real
// files to a real temp dir (ADR-0006 point 2). Production uses realFS.
type fsOps struct {
	syncFile func(*os.File) error
	syncDir  func(dir string) error
	rename   func(oldpath, newpath string) error
}

func realFS() fsOps {
	return fsOps{
		syncFile: func(f *os.File) error { return f.Sync() },
		syncDir:  syncDir,
		rename:   os.Rename,
	}
}

// syncDir fsyncs a directory so a create/rename/unlink of an entry within it is
// durable — the POSIX directory-entry requirement content fsync alone does not
// satisfy (ADR-0006 point 4).
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	serr := d.Sync()
	cerr := d.Close()
	if serr != nil {
		return serr
	}
	return cerr
}

type config struct {
	maxSegmentSize int64
	logger         *slog.Logger
}

// Option configures a Writer.
type Option func(*config)

// WithMaxSegmentSize sets the byte threshold at which a segment rotates. A record
// always fits in a fresh segment even if it exceeds the threshold. Values <= 0
// are ignored.
func WithMaxSegmentSize(n int64) Option {
	return func(c *config) {
		if n > 0 {
			c.maxSegmentSize = n
		}
	}
}

// WithLogger mirrors committed records to slog for operational visibility. It is
// best-effort and strictly off the durability path (ADR-0006 point 2):
// durability never depends on slog.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.logger = l }
}

// Writer is the concrete fsync-durable audit sink (implements Sink). All writes
// serialize through one mutex — the single-writer discipline that keeps segment
// bytes and the global sequence consistent under concurrent emitters.
type Writer struct {
	mu         sync.Mutex
	dir        string
	fs         fsOps
	cfg        config
	active     *os.File
	activeName string
	activeSize int64
	seq        int64 // global count of committed records == next sequence
	closed     bool
	// poison latches a commit-path durability failure (failed rotation or durable
	// frame write). Once set it is sticky: every later Emit returns it instead of
	// touching the possibly-nil active segment. Recovery is a process restart via
	// New, never in-process self-heal (ADR-0006 point 6).
	poison error
}

var _ Sink = (*Writer)(nil)

// New opens (creating if absent) the durable audit sink rooted at dir. On open it
// recovers: cleans leftover temp segments, discards a torn trailing record, and
// resumes the global sequence from the committed record count.
func New(dir string, opts ...Option) (*Writer, error) {
	cfg := config{maxSegmentSize: defaultMaxSegmentSize}
	for _, o := range opts {
		o(&cfg)
	}
	return openWriter(dir, realFS(), cfg)
}

func openWriter(dir string, fs fsOps, cfg config) (*Writer, error) {
	if cfg.maxSegmentSize <= 0 {
		cfg.maxSegmentSize = defaultMaxSegmentSize
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, failClosed("mkdir", err)
	}
	w := &Writer{dir: dir, fs: fs, cfg: cfg}

	// A crash between temp-create and rename (or before the dir fsync) can leave
	// a .tmp-* file. It is not a committed segment, so drop it before scanning.
	if err := w.cleanTemps(); err != nil {
		return nil, err
	}

	segs, err := listSegments(dir)
	if err != nil {
		return nil, failClosed("list-segments", err)
	}
	if len(segs) == 0 {
		if err := w.createSegment(0); err != nil {
			return nil, err
		}
		return w, nil
	}

	// Count committed records across all segments; only the last (active) one may
	// carry a torn tail. A torn frame in a sealed segment is corruption, not an
	// expected torn tail — refuse rather than silently undercount, because the
	// sequence derives from this committed count and an undercount would let a
	// future error key collide with a live record.
	var total int64
	for i, name := range segs {
		path := filepath.Join(dir, name)
		isLast := i == len(segs)-1
		count, goodOffset, torn, serr := scanSegment(path)
		if serr != nil {
			return nil, failClosed("scan-segment", serr)
		}
		total += count
		if torn && !isLast {
			return nil, failClosed("sealed-segment-corrupt",
				fmt.Errorf("torn frame in sealed segment %s", name))
		}
		if isLast {
			if torn {
				if terr := w.truncateActiveTail(path, goodOffset); terr != nil {
					return nil, terr
				}
			}
			if oerr := w.openActive(name, goodOffset); oerr != nil {
				return nil, oerr
			}
		}
	}
	w.seq = total
	return w, nil
}

// EmitOrderLifecycle durably records one order-intent lifecycle transition.
func (w *Writer) EmitOrderLifecycle(ctx context.Context, ev OrderLifecycleEvent) (Ack, error) {
	if err := ctx.Err(); err != nil {
		return Ack{}, err
	}
	rec := record{
		Kind:       KindOrderLifecycle,
		OccurredAt: ev.OccurredAt,
		IntentID:   ev.IntentID,
		OrderID:    ev.OrderID,
		Marker:     ev.Marker,
		Detail:     ev.Detail,
	}
	return w.commit(rec, func(int64) string {
		return orderLifecycleKey(ev.IntentID, ev.OrderID, ev.Marker)
	})
}

// EmitFill durably records one observed cumulative execution snapshot.
func (w *Writer) EmitFill(ctx context.Context, ev FillEvent) (Ack, error) {
	if err := ctx.Err(); err != nil {
		return Ack{}, err
	}
	snap := ev.Snapshot
	rec := record{
		Kind:       KindFill,
		OccurredAt: ev.OccurredAt,
		OrderID:    ev.OrderID,
		Fill:       &snap,
	}
	return w.commit(rec, func(int64) string {
		return fillKey(ev.OrderID, ev.Snapshot)
	})
}

// EmitError synchronously durably records one error occurrence. Because errors
// are reconstruction-resistant (ADR-0006 point 3), it returns a nil error only
// after the record is fully committed; any durability failure returns a
// FailClosedError.
func (w *Writer) EmitError(ctx context.Context, ev ErrorEvent) (Ack, error) {
	if err := ctx.Err(); err != nil {
		return Ack{}, err
	}
	rec := record{
		Kind:       KindError,
		OccurredAt: ev.OccurredAt,
		IntentID:   ev.IntentID,
		OrderID:    ev.OrderID,
		Operation:  ev.Operation,
		ErrorClass: ev.ErrorClass,
		Message:    ev.Message,
	}
	return w.commit(rec, func(seq int64) string {
		return errorKey(ev.IntentID, ev.OrderID, ev.Operation, ev.ErrorClass, seq)
	})
}

// commit assigns the durable sequence, synthesizes the idempotency key (keyForSeq
// receives the sequence so error keys can fold it in), then durably appends the
// framed record. The sequence and key are assigned inside the lock, from the
// committed count — assignment and durable append are one atomic step, so a
// failed append never advances the sequence (ADR-0006 point 3).
func (w *Writer) commit(rec record, keyForSeq func(seq int64) string) (Ack, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// A latched durability failure is checked before closed: once the medium has
	// failed, that fail-closed signal must not be masked even if the writer is
	// later Close()d, and it must short-circuit before any use of w.active.
	if w.poison != nil {
		return Ack{}, w.poison
	}
	if w.closed {
		return Ack{}, &FailClosedError{Op: "emit", Err: ErrClosed}
	}

	seq := w.seq
	rec.Seq = seq
	rec.IdempotencyKey = keyForSeq(seq)
	if rec.OccurredAt.IsZero() {
		rec.OccurredAt = time.Now().UTC()
	}
	payload, err := json.Marshal(&rec)
	if err != nil {
		return Ack{}, &FailClosedError{Op: "marshal", Err: err}
	}
	frame := encodeFrame(payload)

	// Rotate if this frame would exceed the threshold, but a fresh segment always
	// accepts at least one record even if it is oversized.
	if w.activeSize > int64(len(segmentHeader)) && w.activeSize+int64(len(frame)) > w.cfg.maxSegmentSize {
		if err := w.rotate(); err != nil {
			return Ack{}, w.latch(err)
		}
	}

	offsetBefore := w.activeSize
	if err := w.writeFrameDurably(frame, offsetBefore); err != nil {
		return Ack{}, w.latch(err)
	}

	w.activeSize = offsetBefore + int64(len(frame))
	w.seq++
	ack := Ack{IdempotencyKey: rec.IdempotencyKey, Sequence: seq, Segment: w.activeName}
	if w.cfg.logger != nil {
		w.cfg.logger.Info("audit record durable",
			slog.String("kind", string(rec.Kind)),
			slog.String("idempotency_key", ack.IdempotencyKey),
			slog.Int64("sequence", ack.Sequence),
			slog.String("segment", ack.Segment),
		)
	}
	return ack, nil
}

// latch marks the writer permanently fail-closed after a commit-path durability
// failure (a failed rotation or durable frame write) and returns the sticky
// signal. It is set once — the first failing op wins, preserving that op's
// diagnostic tag ("fsync-dir"/"rename"/"write"/"fsync") rather than the
// misleading "invalid argument" a later nil-active WriteAt would surface. Only
// medium/durability failures reach here; ctx cancellation and json.Marshal
// errors are per-record and never poison the writer. Recovery is a process
// restart via New, which re-derives durable state from disk (ADR-0006 point 6).
// Callers hold w.mu.
func (w *Writer) latch(err error) error {
	if w.poison == nil {
		w.poison = failClosed("emit", err)
	}
	return w.poison
}

// writeFrameDurably writes the frame at offset and fsyncs the file. On any
// failure it discards the just-written (non-durable) bytes so a later read can
// never mistake them for a committed record, and returns a fail-closed signal.
func (w *Writer) writeFrameDurably(frame []byte, at int64) error {
	n, err := w.active.WriteAt(frame, at)
	if err != nil || n != len(frame) {
		w.discardTail(at)
		if err == nil {
			err = io.ErrShortWrite
		}
		return failClosed("write", err)
	}
	if err := w.fs.syncFile(w.active); err != nil {
		w.discardTail(at)
		return failClosed("fsync", err)
	}
	return nil
}

// discardTail truncates the active segment back to off, removing any partial or
// non-durable frame left by a failed append. Best-effort: a failure here still
// leaves the writer fail-closed, and torn-tail recovery covers the crash case.
func (w *Writer) discardTail(off int64) {
	if w.active == nil {
		return
	}
	if err := w.active.Truncate(off); err != nil {
		return
	}
	_ = w.fs.syncFile(w.active)
}

// rotate closes the active segment and creates the next one (named by the current
// sequence, which is the sequence of the first record it will hold).
func (w *Writer) rotate() error {
	if w.active != nil {
		if err := w.active.Close(); err != nil {
			return failClosed("close-active", err)
		}
		w.active = nil
	}
	return w.createSegment(w.seq)
}

// createSegment runs the ADR-0006 point 4 segment-durability protocol: write the
// header to a temp file, (i) content fsync, (ii) atomic rename to the final name,
// (iii) parent-directory fsync so the directory entry itself is durable. Without
// (iii) the freshly created segment could be lost whole on crash.
func (w *Writer) createSegment(startSeq int64) error {
	name := fmt.Sprintf("%s%020d%s", segmentPrefix, startSeq, segmentSuffix)
	tmp := filepath.Join(w.dir, tempPrefix+name)
	final := filepath.Join(w.dir, name)

	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, segmentPerm)
	if err != nil {
		return failClosed("create-temp", err)
	}
	if _, err := f.Write(segmentHeader); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return failClosed("write-header", err)
	}
	if err := w.fs.syncFile(f); err != nil { // (i) content fsync
		_ = f.Close()
		_ = os.Remove(tmp)
		return failClosed("fsync-temp", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return failClosed("close-temp", err)
	}
	if err := w.fs.rename(tmp, final); err != nil { // (ii) atomic rename
		_ = os.Remove(tmp)
		return failClosed("rename", err)
	}
	if err := w.fs.syncDir(w.dir); err != nil { // (iii) parent dir fsync
		return failClosed("fsync-dir", err)
	}

	af, err := os.OpenFile(final, os.O_WRONLY, segmentPerm)
	if err != nil {
		return failClosed("open-active", err)
	}
	w.active = af
	w.activeName = name
	w.activeSize = int64(len(segmentHeader))
	return nil
}

// openActive reopens an existing segment as the active one at the given size
// (the offset just past the last committed record).
func (w *Writer) openActive(name string, size int64) error {
	f, err := os.OpenFile(filepath.Join(w.dir, name), os.O_WRONLY, segmentPerm)
	if err != nil {
		return failClosed("open-active", err)
	}
	w.active = f
	w.activeName = name
	w.activeSize = size
	return nil
}

// truncateActiveTail drops a torn trailing record from the active segment and
// fsyncs the truncation so the discard is itself durable.
func (w *Writer) truncateActiveTail(path string, goodOffset int64) error {
	f, err := os.OpenFile(path, os.O_WRONLY, segmentPerm)
	if err != nil {
		return failClosed("open-truncate", err)
	}
	defer f.Close()
	if err := f.Truncate(goodOffset); err != nil {
		return failClosed("truncate-tail", err)
	}
	if err := w.fs.syncFile(f); err != nil {
		return failClosed("fsync-truncate", err)
	}
	return nil
}

// cleanTemps removes leftover temp segments and fsyncs the directory if any were
// removed, so the cleanup itself is durable.
func (w *Writer) cleanTemps() error {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return failClosed("readdir", err)
	}
	removed := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), tempPrefix) {
			continue
		}
		if err := os.Remove(filepath.Join(w.dir, e.Name())); err != nil {
			return failClosed("remove-temp", err)
		}
		removed = true
	}
	if removed {
		if err := w.fs.syncDir(w.dir); err != nil {
			return failClosed("fsync-dir-cleantemp", err)
		}
	}
	return nil
}

// Close closes the active segment. Records are already fsync-durable per emit, so
// Close flushes no buffer; it just releases the handle. Safe to call twice.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.active == nil {
		return nil
	}
	err := w.active.Close()
	w.active = nil
	return err
}

// --- recovery / read helpers ---

func listSegments(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, segmentPrefix) && strings.HasSuffix(name, segmentSuffix) {
			names = append(names, name)
		}
	}
	// Zero-padded start-sequence names sort lexically == numerically.
	sort.Strings(names)
	return names, nil
}

// scanSegment counts committed records in a segment and reports the offset just
// past the last one, plus whether the tail is torn. A structural problem (missing
// or wrong header) is returned as an error, not a torn tail.
func scanSegment(path string) (count int64, goodOffset int64, torn bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false, err
	}
	defer f.Close()

	hdr := make([]byte, len(segmentHeader))
	if _, err := io.ReadFull(f, hdr); err != nil {
		return 0, 0, false, fmt.Errorf("read segment header %s: %w", path, err)
	}
	if !bytes.Equal(hdr, segmentHeader) {
		return 0, 0, false, fmt.Errorf("bad segment header %s", path)
	}

	offset := int64(len(segmentHeader))
	for {
		_, n, ferr := readFrame(f)
		switch ferr {
		case nil:
			count++
			offset += int64(n)
		case io.EOF:
			return count, offset, false, nil
		case errTornFrame:
			return count, offset, true, nil
		default:
			return count, offset, false, ferr
		}
	}
}

// readAll reads back every committed record across all segments, in order. It is
// the reconstruction read used by the sink's own durability tests (and stops at a
// torn tail exactly as recovery does).
func readAll(dir string) ([]record, error) {
	segs, err := listSegments(dir)
	if err != nil {
		return nil, err
	}
	var out []record
	for _, name := range segs {
		recs, err := readSegmentRecords(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

func readSegmentRecords(path string) ([]record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	hdr := make([]byte, len(segmentHeader))
	if _, err := io.ReadFull(f, hdr); err != nil {
		return nil, fmt.Errorf("read segment header %s: %w", path, err)
	}
	if !bytes.Equal(hdr, segmentHeader) {
		return nil, fmt.Errorf("bad segment header %s", path)
	}

	var out []record
	for {
		payload, _, ferr := readFrame(f)
		if ferr == io.EOF || ferr == errTornFrame {
			return out, nil
		}
		if ferr != nil {
			return nil, ferr
		}
		var r record
		if err := json.Unmarshal(payload, &r); err != nil {
			return nil, fmt.Errorf("unmarshal audit record: %w", err)
		}
		out = append(out, r)
	}
}
