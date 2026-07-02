package runtime

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSupervisor_GoRecoversPanic is the core unattended-safety assertion: a
// panic inside a supervised goroutine must not crash the process. If recover
// were missing, the panic would take down the whole test binary, so the test
// reaching its assertions at all already proves survival. We additionally
// assert the panic was logged with a stack trace for post-mortem diagnosis.
func TestSupervisor_GoRecoversPanic(t *testing.T) {
	var buf bytes.Buffer
	sup := NewSupervisor(NewLogger(&buf))

	sup.Go("boom", func() {
		panic("kaboom")
	})

	// Read the buffer only after Wait returns so the read never races the
	// handler's writes (slog serializes writes, but the buffer read does not).
	if !sup.Wait(2 * time.Second) {
		t.Fatal("Wait timed out; supervised goroutine did not return after panic")
	}

	logged := buf.String()
	if !strings.Contains(logged, "kaboom") {
		t.Fatalf("log missing panic value; got: %s", logged)
	}
	if !strings.Contains(logged, "boom") {
		t.Fatalf("log missing goroutine name; got: %s", logged)
	}
	if !strings.Contains(logged, "stack") {
		t.Fatalf("log missing stack field; got: %s", logged)
	}
}

// TestSupervisor_GoSurvivesConcurrentPanics runs many panicking goroutines at
// once under -race to prove the recover boundary is per-goroutine and the
// shared logger is safe for concurrent use.
func TestSupervisor_GoSurvivesConcurrentPanics(t *testing.T) {
	var buf bytes.Buffer
	sup := NewSupervisor(NewLogger(&buf))

	const n = 50
	for i := 0; i < n; i++ {
		sup.Go("worker", func() {
			panic("concurrent boom")
		})
	}

	if !sup.Wait(5 * time.Second) {
		t.Fatal("Wait timed out; some goroutines did not return")
	}
	if got := strings.Count(buf.String(), "concurrent boom"); got != n {
		t.Fatalf("logged panic count = %d, want %d", got, n)
	}
}

// TestSupervisor_GoRunsNormalWork confirms the boundary is transparent for
// non-panicking work: fn runs to completion and Wait returns cleanly.
func TestSupervisor_GoRunsNormalWork(t *testing.T) {
	var buf bytes.Buffer
	sup := NewSupervisor(NewLogger(&buf))

	var mu sync.Mutex
	done := false
	sup.Go("work", func() {
		mu.Lock()
		done = true
		mu.Unlock()
	})

	if !sup.Wait(2 * time.Second) {
		t.Fatal("Wait timed out on normal work")
	}
	mu.Lock()
	defer mu.Unlock()
	if !done {
		t.Fatal("supervised fn did not run")
	}
}

// TestSupervisor_WaitTimesOut ensures Wait reports false when a goroutine
// outlives the shutdown budget, so an unattended restart is not blocked
// indefinitely by a stuck loop.
func TestSupervisor_WaitTimesOut(t *testing.T) {
	var buf bytes.Buffer
	sup := NewSupervisor(NewLogger(&buf))

	block := make(chan struct{})
	sup.Go("stuck", func() {
		<-block
	})

	if sup.Wait(50 * time.Millisecond) {
		t.Fatal("Wait returned true while a goroutine was still blocked")
	}
	close(block) // release the goroutine so the test does not leak it.
}

// TestSupervisor_WaitWithNoGoroutines returns immediately: a zero-goroutine
// Wait is the valid startup state before any loops attach.
func TestSupervisor_WaitWithNoGoroutines(t *testing.T) {
	sup := NewSupervisor(NewLogger(&bytes.Buffer{}))
	if !sup.Wait(time.Second) {
		t.Fatal("Wait should return true immediately with no goroutines")
	}
}
