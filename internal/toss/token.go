package toss

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// defaultLeeway is how long before a token's stated expiry we proactively
// refresh. The Toss token lives 86400s; refreshing a few minutes early avoids
// racing a request against the exact expiry boundary. For anomalously short
// ttls the effective leeway is clamped to ttl/2 (see finishLocked) so a stored
// token always has a usable validity window.
const defaultLeeway = 5 * time.Minute

// defaultHoldoff is how long a terminal issuance failure is negative-cached.
// Terminal failures (bad credentials, malformed token response) do not heal by
// retrying, but an unattended polling loop keeps calling get(); without the
// holdoff every call would hit the rate-limited AUTH endpoint — and each
// server-side issuance invalidates the previous token (the ADR-0001 herd).
// Transient failures are NOT held off: acquireToken's backoff paces those.
const defaultHoldoff = 5 * time.Second

// issueFunc mints a fresh access token, returning the token string and its
// lifetime. It is the single point of token issuance for a client.
type issueFunc func(ctx context.Context) (token string, ttl time.Duration, err error)

// issuance is one in-flight token refresh. Its result fields are written by
// the flight goroutine strictly before done is closed; waiters read them only
// after <-done, so the channel provides the happens-before edge.
type issuance struct {
	done  chan struct{}
	token string
	ttl   time.Duration
	err   error
}

// tokenManager owns the one valid OAuth token for a client. Per Toss, issuing
// a new token invalidates the previous one, so issuance is single-flight: at
// most one refresh in flight, every concurrent caller shares its outcome
// (success AND failure — otherwise N waiters would serially mint N mutually
// invalidating tokens after one failure), and cached reuse until the token
// nears expiry.
type tokenManager struct {
	mu        sync.Mutex
	token     string
	expiresAt time.Time // hard expiry: the server stops accepting the token
	refreshAt time.Time // soft expiry: proactively refresh once now >= refreshAt

	// Negative cache for terminal issuance failures.
	lastErr   error
	holdUntil time.Time

	inflight *issuance

	leeway  time.Duration
	holdoff time.Duration
	now     func() time.Time
	issue   issueFunc
	logger  *slog.Logger
}

func newTokenManager(issue issueFunc) *tokenManager {
	return &tokenManager{
		leeway:  defaultLeeway,
		holdoff: defaultHoldoff,
		now:     time.Now,
		issue:   issue,
		logger:  slog.Default(),
	}
}

// setLogger routes the manager's operational warnings (stale-token fallback)
// to l. Safe to call concurrently, though it is meant for boot-time wiring.
func (m *tokenManager) setLogger(l *slog.Logger) {
	if l == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logger = l
}

// get returns a valid token, minting one if the cache is empty or near expiry.
//
// Refresh is single-flight: the first caller past the cache check starts one
// issuance goroutine and every caller — starter included — waits on it via
// select, so a caller whose context expires abandons the wait instead of being
// wedged behind an uninterruptible lock. The flight itself runs on a context
// detached from any single caller (its outcome is shared state), bounded by
// the HTTP client timeout.
func (m *tokenManager) get(ctx context.Context) (string, error) {
	m.mu.Lock()
	if m.valid() {
		token := m.token
		m.mu.Unlock()
		return token, nil
	}

	// Terminal-failure holdoff: don't re-hit the token endpoint for every
	// call while the last failure cannot have healed yet. Serve the stale
	// token if the server still accepts it, else the cached error.
	if m.inflight == nil && m.lastErr != nil && m.now().Before(m.holdUntil) {
		if m.stale() {
			token := m.token
			m.mu.Unlock()
			return token, nil
		}
		err := m.lastErr
		m.mu.Unlock()
		return "", err
	}

	fl := m.inflight
	if fl == nil {
		fl = m.startFlightLocked(ctx)
	}
	m.mu.Unlock()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-fl.done:
	}
	if fl.err == nil {
		return fl.token, nil
	}

	// Stale-but-valid fallback: the refresh failed inside the leeway window,
	// but the previous token has not hard-expired — the server still accepts
	// it, so serving it beats blacking out every API call for up to the
	// leeway duration. Past hard expiry the failure propagates.
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stale() {
		m.logger.Warn("toss: token refresh failed; serving cached token until hard expiry",
			"expires_at", m.expiresAt, "err", fl.err.Error())
		return m.token, nil
	}
	return "", fl.err
}

// startFlightLocked begins a new issuance flight. Caller must hold m.mu.
//
// The flight runs detached from the initiating caller's cancellation
// (context.WithoutCancel): its outcome is shared by every waiter and cached
// for the next caller, so one cancelled initiator must not poison the refresh
// for everyone. The HTTP client's own timeout bounds the detached call.
func (m *tokenManager) startFlightLocked(ctx context.Context) *issuance {
	fl := &issuance{done: make(chan struct{})}
	m.inflight = fl
	ictx := context.WithoutCancel(ctx)
	go func() {
		// Recover boundary: a panicking issuer becomes a shared error instead
		// of killing the unattended process or wedging every future refresh
		// behind a flight that never completes.
		defer func() {
			if r := recover(); r != nil {
				fl.token, fl.err = "", fmt.Errorf("toss: token issuance panicked: %v", r)
			}
			m.mu.Lock()
			m.finishLocked(fl)
			m.mu.Unlock()
			close(fl.done)
		}()
		fl.token, fl.ttl, fl.err = m.issue(ictx)
		if fl.err == nil && fl.ttl <= 0 {
			// Defense in depth: issueToken validates expires_in, but storing
			// a non-positive ttl would make every subsequent get() reissue —
			// exactly the thundering herd ADR-0001 forbids.
			fl.token, fl.err = "", fmt.Errorf("toss: token issuer returned non-positive ttl %s", fl.ttl)
		}
	}()
	return fl
}

// finishLocked publishes a flight's outcome into the cache. Caller holds m.mu.
func (m *tokenManager) finishLocked(fl *issuance) {
	if m.inflight == fl {
		m.inflight = nil
	}
	if fl.err != nil {
		if !isTransient(fl.err) {
			m.lastErr = fl.err
			m.holdUntil = m.now().Add(m.holdoff)
		}
		return
	}
	// Clamp the effective leeway to half the ttl: a ttl at or below the
	// configured leeway would otherwise be "expired" the moment it is stored,
	// turning every get() into a mutually invalidating reissue (M-1).
	leeway := m.leeway
	if half := fl.ttl / 2; half < leeway {
		leeway = half
	}
	now := m.now()
	m.token = fl.token
	m.expiresAt = now.Add(fl.ttl)
	m.refreshAt = m.expiresAt.Add(-leeway)
	m.lastErr = nil
	m.holdUntil = time.Time{}
}

// invalidate drops the cached token, but only if it still matches old. This
// guards the 401 path: several requests may fail with the same stale token,
// yet only the first clears it, so the subsequent get() performs a single
// reissue rather than one per failed request. An invalidated token is proven
// rejected by the server, so it is also removed from stale-fallback service.
func (m *tokenManager) invalidate(old string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token == old {
		m.token = ""
		m.expiresAt = time.Time{}
		m.refreshAt = time.Time{}
	}
}

// valid reports whether the cached token can be served without a refresh.
func (m *tokenManager) valid() bool {
	return m.token != "" && m.now().Before(m.refreshAt)
}

// stale reports whether a token exists that is due for refresh but still
// accepted by the server (past refreshAt, before hard expiry).
func (m *tokenManager) stale() bool {
	return m.token != "" && m.now().Before(m.expiresAt)
}
