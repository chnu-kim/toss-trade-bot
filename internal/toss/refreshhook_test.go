package toss

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// The token-refresh-failure hook is the seam that lets the kill switch escalate
// "we can no longer authenticate" into a global halt (ADR-0004 point 7). Token
// failure is non-reconstructable — nothing in the journal can re-derive it — so
// if this seam does not fire, the escalation simply never happens.

func TestTokenRefreshFailureHook_FiresOncePerFailedIssuance(t *testing.T) {
	fired := make(chan time.Time, 4)
	issueErr := errors.New("toss: token issuance failed (500)")
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		return "", 0, issueErr
	})
	m.setRefreshFailureHook(func(at time.Time) { fired <- at })

	if _, err := m.get(context.Background()); err == nil {
		t.Fatal("expected the issuance error")
	}

	select {
	case at := <-fired:
		if at.IsZero() {
			// A zero occurredAt is fail-closed-latched by the kill switch
			// instead of being counted, so it would silently never reach the
			// threshold.
			t.Fatal("hook must supply a non-zero occurredAt")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hook never fired for a failed token issuance")
	}
}

// TestTokenRefreshFailureHook_FiresPerFlightNotPerWaiter keeps the escalation
// counting refresh ATTEMPTS. Issuance is single-flight (one failure is shared by
// every waiter), so firing per waiter would multiply one failure into a burst
// and trip the threshold on a single blip.
func TestTokenRefreshFailureHook_FiresPerFlightNotPerWaiter(t *testing.T) {
	var mu sync.Mutex
	var count int
	done := make(chan struct{})
	var once sync.Once

	release := make(chan struct{})
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		<-release
		return "", 0, errors.New("toss: token issuance failed (500)")
	})
	m.setRefreshFailureHook(func(at time.Time) {
		mu.Lock()
		count++
		mu.Unlock()
		once.Do(func() { close(done) })
	})

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _ = m.get(context.Background())
		}()
	}
	time.Sleep(50 * time.Millisecond) // let waiters join the one flight
	close(release)
	wg.Wait()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("hook never fired")
	}
	time.Sleep(50 * time.Millisecond) // give any stray extra calls a chance

	mu.Lock()
	defer mu.Unlock()
	if count != 1 {
		t.Fatalf("hook fired %d times for one shared flight, want 1", count)
	}
}

func TestTokenRefreshFailureHook_NotFiredOnSuccess(t *testing.T) {
	var mu sync.Mutex
	var count int
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		return "tok-1", time.Hour, nil
	})
	m.setRefreshFailureHook(func(at time.Time) {
		mu.Lock()
		count++
		mu.Unlock()
	})

	if _, err := m.get(context.Background()); err != nil {
		t.Fatalf("get: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if count != 0 {
		t.Fatalf("hook fired %d times on a successful refresh, want 0", count)
	}
}

// TestTokenRefreshFailureHook_PanicIsContained: the hook calls into the kill
// switch, which does durable I/O. A panic there must not kill the unattended
// process or wedge the token manager behind a flight that never completes.
func TestTokenRefreshFailureHook_PanicIsContained(t *testing.T) {
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		return "", 0, errors.New("toss: token issuance failed (500)")
	})
	m.setRefreshFailureHook(func(at time.Time) { panic("notifier exploded") })

	if _, err := m.get(context.Background()); err == nil {
		t.Fatal("expected the issuance error")
	}

	// The manager must still be usable: a second get completes rather than
	// blocking on a flight whose goroutine died mid-cleanup.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = m.get(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("token manager wedged after a panicking hook")
	}
}

// TestTokenRefreshFailureHook_DoesNotDelayWaiters pins that the hook runs
// AFTER waiters are released. The production hook performs a durable store
// write; running it inline would stall every caller waiting on the token behind
// an fsync (and, worse, behind the manager lock).
func TestTokenRefreshFailureHook_DoesNotDelayWaiters(t *testing.T) {
	blocked := make(chan struct{})
	entered := make(chan struct{})
	m := newTokenManager(func(ctx context.Context) (string, time.Duration, error) {
		return "", 0, errors.New("toss: token issuance failed (500)")
	})
	m.setRefreshFailureHook(func(at time.Time) {
		close(entered)
		<-blocked // a slow durable write
	})
	defer close(blocked)

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		_, _ = m.get(context.Background())
	}()

	<-entered
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter was blocked behind the refresh-failure hook")
	}
}

// TestClient_SetTokenRefreshFailureHook is the wiring assertion: cmd/bot
// registers the hook on the Client, not on the unexported manager.
func TestClient_SetTokenRefreshFailureHook(t *testing.T) {
	c, err := NewClient("https://openapi.tossinvest.com", "id", "secret")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	fired := make(chan time.Time, 1)
	c.SetTokenRefreshFailureHook(func(at time.Time) { fired <- at })

	// Swap the issuer for a failing one rather than talking to the network.
	c.tokens.issue = func(ctx context.Context) (string, time.Duration, error) {
		return "", 0, errors.New("toss: token issuance failed (500)")
	}
	if _, err := c.tokens.get(context.Background()); err == nil {
		t.Fatal("expected the issuance error")
	}

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("hook registered on the Client never fired")
	}
}

// TestClient_SetTokenRefreshFailureHook_NilIsIgnored keeps a nil registration
// from nil-panicking inside the flight goroutine.
func TestClient_SetTokenRefreshFailureHook_NilIsIgnored(t *testing.T) {
	c, err := NewClient("https://openapi.tossinvest.com", "id", "secret")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.SetTokenRefreshFailureHook(nil)
	c.tokens.issue = func(ctx context.Context) (string, time.Duration, error) {
		return "", 0, errors.New("boom")
	}
	if _, err := c.tokens.get(context.Background()); err == nil {
		t.Fatal("expected the issuance error")
	}
}
