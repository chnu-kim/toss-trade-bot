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

// TestTokenManager_TransientFailureIsPacedThenRetried encodes the manager-wide
// pacing contract for transient failures (issue #26 L-4, completed): within the
// holdoff the endpoint is not re-hit (the cached transient error is served, so a
// storm of callers cannot each fire their own issuance — the stale fallback
// makes refresh failures look like successes and bypasses acquireToken's
// backoff, so only a manager-wide gate can pace them), yet the failure is NOT
// permanently cached like a terminal error: past the holdoff a genuine retry
// happens.
func TestTokenManager_TransientFailureIsPacedThenRetried(t *testing.T) {
	var calls int32
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return "", 0, &transientError{err: errors.New("token endpoint 503")}
		}
		return "tok-2", time.Hour, nil
	})
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m.now = clock.now

	if _, err := m.get(context.Background()); err == nil || !isTransient(err) {
		t.Fatal("first get should surface the transient error")
	}
	// Within the holdoff: no new issuance, cached error served (paced).
	if _, err := m.get(context.Background()); err == nil {
		t.Fatal("second get within holdoff should still fail without re-issuing")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("issue called %d times within holdoff, want 1 (manager-wide pacing)", got)
	}
	// Past the holdoff: a transient failure is genuinely retried.
	clock.advance(m.holdoff + time.Second)
	tok, err := m.get(context.Background())
	if err != nil {
		t.Fatalf("get after holdoff: %v", err)
	}
	if tok != "tok-2" {
		t.Fatalf("token = %q, want tok-2", tok)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("issue called %d times, want 2 (retried after holdoff)", got)
	}
}

// TestTokenManager_SequentialTransientFailuresPacedByHoldoff is the
// deterministic core of defect ②: a fast-failing issuer must be coalesced
// manager-wide. With a frozen clock, repeated gets after one transient failure
// must NOT each start a new flight (the old code fell through and did exactly
// that, N callers × retries hammering the rate-limited AUTH endpoint during the
// very outage the bot is trying to recover from).
func TestTokenManager_SequentialTransientFailuresPacedByHoldoff(t *testing.T) {
	var calls int32
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		atomic.AddInt32(&calls, 1)
		return "", 0, &transientError{err: errors.New("token endpoint 503")}
	})
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)} // frozen
	m.now = clock.now

	for i := 0; i < 5; i++ {
		if _, err := m.get(context.Background()); err == nil {
			t.Fatalf("get #%d should fail (issuer always transient-fails)", i)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("issue called %d times across 5 gets within one holdoff, want 1 (fast-failing issuer must be paced, not re-attempted per call)", got)
	}

	// Past the holdoff exactly one further attempt is allowed.
	clock.advance(m.holdoff + time.Second)
	if _, err := m.get(context.Background()); err == nil {
		t.Fatal("get after holdoff should still fail")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("issue called %d times, want 2 (one attempt per holdoff interval)", got)
	}
}

// TestTokenManager_ConcurrentTransientFailuresCoalesceToOneIssuance proves the
// pacing gate holds under contention (-race): N concurrent callers hitting a
// fast-failing issuer must produce exactly one endpoint hit per interval, not
// one per caller.
func TestTokenManager_ConcurrentTransientFailuresCoalesceToOneIssuance(t *testing.T) {
	var calls int32
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		atomic.AddInt32(&calls, 1)
		return "", 0, &transientError{err: errors.New("token endpoint 503")}
	})
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)} // frozen: one interval
	m.now = clock.now

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _ = m.get(context.Background())
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("token endpoint hit %d times for %d concurrent callers within one interval, want 1 (manager-wide coalescing — ADR-0001 / unattended AUTH rate-limit contract)", got, n)
	}
}

// TestTokenManager_CancelledCallerDoesNotStartFlight is the core of defect ①:
// an already-cancelled caller must never start a NEW issuance. startFlight
// strips cancellation (context.WithoutCancel) so the detached flight would
// outlive the dead caller and could invalidate a token a live request is still
// using — the ADR-0001 herd triggered by a corpse.
func TestTokenManager_CancelledCallerDoesNotStartFlight(t *testing.T) {
	issuerEntered := make(chan struct{}, 8)
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		issuerEntered <- struct{}{}
		return "tok-1", time.Hour, nil
	})
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m.now = clock.now

	// Seed a token, then move into the leeway window so it is stale-but-valid
	// (a refresh is due, but the token is still server-accepted).
	if _, err := m.get(context.Background()); err != nil {
		t.Fatalf("seed get: %v", err)
	}
	<-issuerEntered // drain the seed issuance
	clock.advance(time.Hour - 2*time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = m.get(ctx)

	select {
	case <-issuerEntered:
		t.Fatal("an already-cancelled caller started a token issuance: a detached flight can outlive the dead caller and invalidate a token a live request is using (ADR-0001)")
	case <-time.After(100 * time.Millisecond):
		// good: no issuance was started
	}
}

// TestTokenManager_CancelledCallerDoesNotConsumePacingGate fixes the
// interaction of ① and ②: when the pacing gate has just opened (one attempt
// allowed), a cancelled caller arriving first must not consume that budget on a
// flight it will only abandon — the next live caller must be the one to issue.
func TestTokenManager_CancelledCallerDoesNotConsumePacingGate(t *testing.T) {
	var calls int32
	var fail atomic.Bool
	fail.Store(true)
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		atomic.AddInt32(&calls, 1)
		if fail.Load() {
			return "", 0, &transientError{err: errors.New("token endpoint 503")}
		}
		return "tok-live", time.Hour, nil
	})
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m.now = clock.now

	// Arm the pacing gate with a transient failure (no token cached).
	if _, err := m.get(context.Background()); err == nil {
		t.Fatal("seed get should fail transiently")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("after seed, calls=%d want 1", got)
	}

	// Open the gate: move just past the holdoff.
	clock.advance(m.holdoff + time.Second)

	// A cancelled caller arrives first — it must neither issue nor spend the budget.
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	if _, err := m.get(cctx); err == nil {
		t.Fatal("cancelled caller should get its context error, not a token")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("cancelled caller triggered issuance (calls=%d, want 1): a dead caller must not consume the pacing gate", got)
	}

	// A live caller then consumes the still-open gate and issues successfully.
	fail.Store(false)
	tok, err := m.get(context.Background())
	if err != nil {
		t.Fatalf("live get after cancelled caller: %v", err)
	}
	if tok != "tok-live" {
		t.Fatalf("token = %q, want tok-live", tok)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls=%d want 2 (seed fail + live success)", got)
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

func TestTokenManager_StaleWindowTransientFailuresArePaced(t *testing.T) {
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
	clock.advance(time.Hour - 2*time.Minute) // stale-but-valid window

	// The fallback serves these as successes, so the caller's backoff never
	// paces them — the manager itself must hold off repeat refresh attempts.
	for i := 0; i < 5; i++ {
		tok, err := m.get(context.Background())
		if err != nil {
			t.Fatalf("get #%d: %v", i, err)
		}
		if tok != "tok-1" {
			t.Fatalf("get #%d token = %q, want stale tok-1", i, tok)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("issue called %d times across 5 stale-window gets, want 2 (1 initial + 1 paced refresh attempt)", got)
	}

	// After the holdoff the manager genuinely retries.
	clock.advance(m.holdoff + time.Second)
	if _, err := m.get(context.Background()); err != nil {
		t.Fatalf("get after holdoff: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("issue called %d times after holdoff elapsed, want 3", got)
	}
}

// TestTokenManager_SlowRefreshServesStalePromptly is the adversarial-review
// high: a stale-but-valid token must be served promptly even when the AUTH
// endpoint is merely slow. Blocking the caller on a hung refresh until the HTTP
// timeout would black out API calls during the very window L-3 exists to keep
// available.
func TestTokenManager_SlowRefreshServesStalePromptly(t *testing.T) {
	block := make(chan struct{})
	var calls int32
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return "tok-1", time.Hour, nil // seed
		}
		<-block // a degraded AUTH endpoint hangs
		return "tok-2", time.Hour, nil
	})
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m.now = clock.now
	m.refreshBudget = 20 * time.Millisecond // keep the test snappy

	if _, err := m.get(context.Background()); err != nil {
		t.Fatalf("seed get: %v", err)
	}
	clock.advance(time.Hour - 2*time.Minute) // stale-but-valid window

	done := make(chan string, 1)
	go func() {
		tok, _ := m.get(context.Background())
		done <- tok
	}()
	select {
	case tok := <-done:
		if tok != "tok-1" {
			t.Fatalf("stale get returned %q, want stale tok-1 (must not block on the hung refresh)", tok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("get blocked on a hung refresh instead of serving the stale token promptly (L-3 availability)")
	}
	close(block) // let the detached refresh finish so it does not leak
}

// TestTokenManager_SlowRefreshStaleExpiresDuringBudget guards the budget-timeout
// path when the stale token hard-expires *while* the caller is waiting out the
// refresh budget. At that point there is nothing safe to serve, so the caller
// must wait for the (still-running) refresh to finish and use its result — and
// must read that result only after the flight completes. Reading fl.token/fl.err
// before <-fl.done is a data race (the flight writes them without the manager
// lock) and returns an empty token; -race and the early-return assertion here
// both catch it.
func TestTokenManager_SlowRefreshStaleExpiresDuringBudget(t *testing.T) {
	release := make(chan struct{})
	var calls int32
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return "tok-1", time.Hour, nil // seed
		}
		<-release // refresh is slow
		return "tok-2", time.Hour, nil
	})
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m.now = clock.now
	m.refreshBudget = 50 * time.Millisecond

	if _, err := m.get(context.Background()); err != nil {
		t.Fatalf("seed get: %v", err)
	}
	clock.advance(time.Hour - 2*time.Minute) // into the stale window (still valid)

	got := make(chan result, 1)
	go func() {
		tok, err := m.get(context.Background())
		got <- result{tok, err}
	}()

	// Let the caller enter the budget wait, then push the clock past hard expiry
	// so that when the budget fires the stale token is gone.
	time.Sleep(10 * time.Millisecond)
	clock.advance(5 * time.Minute)

	// With no usable token, the caller must wait for the slow refresh, not
	// return early with an empty token read from an in-flight issuance.
	select {
	case r := <-got:
		t.Fatalf("get returned %+v before the slow refresh finished; with no usable token it must wait", r)
	case <-time.After(200 * time.Millisecond): // well past the 50ms budget
	}

	close(release)
	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("get after refresh: %v", r.err)
		}
		if r.tok != "tok-2" {
			t.Fatalf("token = %q, want fresh tok-2", r.tok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("get did not return after the refresh completed")
	}
}

type result struct {
	tok string
	err error
}

// TestTokenManager_TerminalRefreshFailureFailsFastInStaleWindow is the
// review's P2: a refresh that fails with a terminal (non-transient) auth error
// — revoked/rotated credentials, malformed response — must fail fast rather
// than be masked by the stale token until hard expiry.
func TestTokenManager_TerminalRefreshFailureFailsFastInStaleWindow(t *testing.T) {
	var calls int32
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return "tok-1", time.Hour, nil
		}
		return "", 0, errors.New("toss: token issuance failed (401): invalid_client")
	})
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m.now = clock.now

	if _, err := m.get(context.Background()); err != nil {
		t.Fatalf("seed get: %v", err)
	}
	clock.advance(time.Hour - 2*time.Minute) // stale window

	// The terminal failure must surface (the first stale get that observes the
	// completed refresh, or the next one via the sticky terminal state), not be
	// masked indefinitely.
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := m.get(context.Background())
		if err != nil {
			if !strings.Contains(err.Error(), "invalid_client") {
				t.Fatalf("error = %v, want the terminal credential error surfaced", err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal refresh failure stayed masked by the stale token; it must fail fast (auth fail-fast contract)")
		}
		time.Sleep(5 * time.Millisecond)
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
