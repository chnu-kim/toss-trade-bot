package runtime

import (
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

// Supervisor runs goroutines behind a panic-recover boundary and tracks them so
// shutdown can wait for them to drain.
//
// It carries the two unattended invariants of this package together: Go gives
// each goroutine a recover boundary (a panic is logged with its stack and
// contained, never fatal to the process), and Wait lets a graceful shutdown
// block until supervised work finishes or a deadline elapses.
//
// Panic policy (deliberate, see PR / package doc): the boundary logs the panic
// and lets that one goroutine stop; the process keeps running. It does NOT
// restart the goroutine and does NOT escalate to halting the process on
// repeated panics — a panic-count trip is a kill switch, which ADR-0004 owns
// and this issue explicitly excludes.
type Supervisor struct {
	logger *slog.Logger
	wg     sync.WaitGroup
}

// NewSupervisor returns a Supervisor that logs recovered panics to logger.
func NewSupervisor(logger *slog.Logger) *Supervisor {
	return &Supervisor{logger: logger}
}

// Go runs fn in a new goroutine wrapped in a panic-recover boundary and tracks
// it for graceful shutdown. name labels the goroutine in recovery logs. fn
// closes over any context it needs to observe cancellation.
func (s *Supervisor) Go(name string, fn func()) {
	s.wg.Add(1)
	go func() {
		// defer order: Done runs after recover so a panicking goroutine is
		// still marked finished and never wedges Wait. recover MUST be
		// deferred here, inside the spawned goroutine — a recover in the
		// caller's goroutine would catch nothing.
		defer s.wg.Done()
		defer s.recover(name)
		fn()
	}()
}

// recover is the boundary itself: it turns a panic into a structured log record
// (with the goroutine name and a stack trace) and swallows it so the process
// survives. Errors are logged, never silently dropped.
func (s *Supervisor) recover(name string) {
	if r := recover(); r != nil {
		s.logger.Error("recovered from panic in supervised goroutine",
			"goroutine", name,
			"panic", r,
			"stack", string(debug.Stack()),
		)
	}
}

// Wait blocks until all supervised goroutines return or timeout elapses,
// whichever comes first. It reports true if everything drained in time and
// false on timeout, so an unattended restart is never blocked indefinitely by a
// stuck goroutine. A Supervisor with no goroutines returns true immediately.
func (s *Supervisor) Wait(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
