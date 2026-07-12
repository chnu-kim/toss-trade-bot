package toss

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- L-4: issuance failures are shared by the single flight ---

func TestTokenManager_SharesFailureAcrossConcurrentWaiters(t *testing.T) {
	var calls int32
	release := make(chan struct{})
	issueErr := errors.New("toss: token issuance failed (401): invalid_client")
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		atomic.AddInt32(&calls, 1)
		<-release
		return "", 0, issueErr
	})

	const n = 50
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = m.get(context.Background())
		}(i)
	}
	time.Sleep(50 * time.Millisecond) // let waiters pile onto the flight
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("issue called %d times for one failing refresh, want 1 (failure must be shared, not retried per waiter)", got)
	}
	for i, err := range errs {
		if !errors.Is(err, issueErr) {
			t.Fatalf("waiter %d error = %v, want the shared issuance error", i, err)
		}
	}
}

func TestTokenManager_WaiterAbandonsWaitOnContextCancel(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		close(entered)
		<-release
		return "tok-1", time.Hour, nil
	})

	leaderDone := make(chan error, 1)
	go func() {
		_, err := m.get(context.Background())
		leaderDone <- err
	}()
	<-entered // the flight is in progress

	ctx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, err := m.get(ctx)
		waiterDone <- err
	}()
	cancel()

	select {
	case err := <-waiterDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled waiter error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled waiter is stuck behind the in-flight issuance; waits must be context-interruptible")
	}

	close(release)
	if err := <-leaderDone; err != nil {
		t.Fatalf("leader get: %v", err)
	}
}

// --- M-1/L-4: terminal failures are negative-cached briefly ---

func TestTokenManager_TerminalFailureIsNegativeCached(t *testing.T) {
	var calls int32
	issueErr := errors.New("terminal: invalid_client")
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		atomic.AddInt32(&calls, 1)
		return "", 0, issueErr
	})

	for i := 0; i < 5; i++ {
		if _, err := m.get(context.Background()); !errors.Is(err, issueErr) {
			t.Fatalf("get #%d error = %v, want cached issuance error", i, err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("issue called %d times across 5 gets, want 1 (terminal failures must be negative-cached)", got)
	}
}

func TestTokenManager_RetriesAfterHoldoffExpires(t *testing.T) {
	var calls int32
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return "", 0, errors.New("terminal: transient outage misclassified once")
		}
		return "tok-2", time.Hour, nil
	})
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m.now = clock.now

	if _, err := m.get(context.Background()); err == nil {
		t.Fatal("first get should fail")
	}
	clock.advance(m.holdoff + time.Second)

	tok, err := m.get(context.Background())
	if err != nil {
		t.Fatalf("get after holdoff: %v", err)
	}
	if tok != "tok-2" {
		t.Fatalf("token = %q, want tok-2 (reissue after holdoff)", tok)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("issue called %d times, want 2", got)
	}
}

func TestTokenManager_TransientFailureIsNotNegativeCached(t *testing.T) {
	var calls int32
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return "", 0, &transientError{err: errors.New("token endpoint 503")}
		}
		return "tok-2", time.Hour, nil
	})

	if _, err := m.get(context.Background()); err == nil || !isTransient(err) {
		t.Fatal("first get should surface the transient error")
	}
	// The caller's backoff loop retries immediately in tests; the manager must
	// actually re-attempt issuance rather than serve the cached failure,
	// otherwise acquireToken's backoff retries become no-ops.
	tok, err := m.get(context.Background())
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if tok != "tok-2" {
		t.Fatalf("token = %q, want tok-2", tok)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("issue called %d times, want 2 (transient failures are retried, not cached)", got)
	}
}

// --- M-1: leeway clamp for short-lived tokens ---

// A ttl at or below the configured leeway (default 5m) used to make valid()
// false the instant the token was stored — every get() reissued, and each
// fresh token invalidated the previous one (the ADR-0001 herd, self-inflicted
// by one anomalous server response). Clamping the effective leeway to ttl/2
// guarantees any positive ttl yields a usable validity window.
func TestTokenManager_ShortTTLRefreshesAtHalfLife(t *testing.T) {
	var calls int32
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		atomic.AddInt32(&calls, 1)
		return "tok-" + itoa(int(atomic.LoadInt32(&calls))), 30 * time.Second, nil
	})
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m.now = clock.now

	if _, err := m.get(context.Background()); err != nil {
		t.Fatalf("first get: %v", err)
	}
	clock.advance(14 * time.Second) // before half-life: cached
	if _, err := m.get(context.Background()); err != nil {
		t.Fatalf("get at 14s: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("issue called %d times before half-life, want 1", got)
	}

	clock.advance(2 * time.Second) // 16s: past the clamped refresh point
	tok, err := m.get(context.Background())
	if err != nil {
		t.Fatalf("get at 16s: %v", err)
	}
	if tok != "tok-2" {
		t.Fatalf("token = %q, want tok-2 (refresh at clamped half-life)", tok)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("issue called %d times, want 2", got)
	}
}

// --- L-3: stale-but-valid fallback while refresh fails ---

func TestTokenManager_ServesStaleTokenUntilHardExpiry(t *testing.T) {
	var calls int32
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return "tok-1", time.Hour, nil
		}
		return "", 0, &transientError{err: errors.New("token endpoint down")}
	})
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m.now = clock.now

	if _, err := m.get(context.Background()); err != nil {
		t.Fatalf("first get: %v", err)
	}

	// Inside the leeway window: refresh is due (and fails), but the token is
	// still accepted by the server for up to 5 more minutes. Blacking out
	// every API call would be strictly worse than serving it.
	clock.advance(time.Hour - 2*time.Minute)
	tok, err := m.get(context.Background())
	if err != nil {
		t.Fatalf("get inside leeway window = %v, want stale fallback", err)
	}
	if tok != "tok-1" {
		t.Fatalf("token = %q, want stale tok-1", tok)
	}

	// Past hard expiry the server rejects the token: fail, never serve it.
	clock.advance(3 * time.Minute)
	if _, err := m.get(context.Background()); err == nil {
		t.Fatal("get past hard expiry must fail, not serve a token the server rejects")
	}
}

func TestTokenManager_InvalidatedTokenIsNeverServedAsFallback(t *testing.T) {
	var calls int32
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return "tok-1", time.Hour, nil
		}
		return "", 0, errors.New("terminal issuance failure")
	})

	if _, err := m.get(context.Background()); err != nil {
		t.Fatalf("first get: %v", err)
	}
	m.invalidate("tok-1") // a 401 proved the server rejects tok-1

	if tok, err := m.get(context.Background()); err == nil {
		t.Fatalf("get returned %q, want error: a 401-invalidated token must not be served as fallback", tok)
	}
}

// --- defensive boundaries ---

func TestTokenManager_NonPositiveTTLFromIssuerIsAnError(t *testing.T) {
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		return "tok-1", 0, nil
	})
	if tok, err := m.get(context.Background()); err == nil {
		t.Fatalf("get returned %q, want error: storing a zero-ttl token would make every get() reissue (the ADR-0001 herd)", tok)
	}
}

func TestTokenManager_IssuePanicBecomesError(t *testing.T) {
	var calls int32
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			panic("boom")
		}
		return "tok-2", time.Hour, nil
	})
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m.now = clock.now

	_, err := m.get(context.Background())
	if err == nil {
		t.Fatal("a panicking issuer must surface as an error, not kill the process or wedge the flight")
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Fatalf("error %q should mention the panic", err)
	}

	// The manager must not be wedged: after the holdoff a fresh flight works.
	clock.advance(m.holdoff + time.Second)
	tok, err := m.get(context.Background())
	if err != nil {
		t.Fatalf("get after panic recovery: %v", err)
	}
	if tok != "tok-2" {
		t.Fatalf("token = %q, want tok-2", tok)
	}
}
