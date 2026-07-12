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

// defaultHoldoff paces token issuance manager-wide after a failure: for its
// duration, no caller starts a new issuance (the cached error or stale token is
// served instead). It is the single gate that keeps a failure from fanning out
// into an AUTH-endpoint storm.
//
// Why a manager-wide gate is necessary and not just per-caller backoff: the
// stale-token fallback returns refresh failures to callers as *successes*,
// which bypasses acquireToken's backoff entirely; and even without stale,
// concurrent callers hitting a fast-failing issuer each complete their own
// flight (the in-flight window is too short to coalesce them) and, per Toss,
// each fresh issuance invalidates the previous token — the ADR-0001 herd.
//
// Why it equals backoffBase (the smallest retry step): a single caller that
// genuinely backs off crosses the holdoff on its first retry and recovers from
// a brief blip, while a concurrent burst — which does not wait between
// attempts — stays inside one holdoff and is coalesced to a single issuance.
const defaultHoldoff = backoffBase

// defaultRefreshBudget bounds how long a caller waits for a refresh while it
// still holds a stale-but-valid token. A healthy refresh completes well within
// it and the caller gets the fresh token; a slow or hung AUTH endpoint hits the
// budget and the caller is served the stale (still server-accepted) token
// instead of blocking until the HTTP timeout — the availability guarantee of
// the stale fallback (L-3) would be defeated if callers blacked out whenever
// the token endpoint merely slowed down.
const defaultRefreshBudget = 3 * time.Second

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

	// Failure state. lastErr + holdUntil pace retries manager-wide (any
	// failure). terminalErr additionally marks a non-transient failure so the
	// stale fallback fails fast instead of masking a credential problem until
	// hard expiry; it is cleared on the next successful issuance.
	lastErr     error
	terminalErr error
	holdUntil   time.Time

	inflight *issuance

	leeway        time.Duration
	holdoff       time.Duration
	refreshBudget time.Duration
	now           func() time.Time
	issue         issueFunc
	logger        *slog.Logger
}

func newTokenManager(issue issueFunc) *tokenManager {
	return &tokenManager{
		leeway:        defaultLeeway,
		holdoff:       defaultHoldoff,
		refreshBudget: defaultRefreshBudget,
		now:           time.Now,
		issue:         issue,
		logger:        slog.Default(),
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

// get returns a valid token. It has three regimes, by cache state:
//
//   - Fresh: served directly, no refresh.
//   - Stale-but-valid (past refreshAt, before hard expiry): the token is still
//     server-accepted, so the caller is never blacked out. A paced refresh is
//     triggered and the caller waits at most refreshBudget for it — getting the
//     fresh token if the endpoint is healthy, or the stale token if it is slow
//     or fails transiently. A terminal (credential) refresh failure fails fast
//     rather than being masked.
//   - No usable token (empty or hard-expired): the caller must block on a
//     refresh (or fail); there is nothing safe to serve meanwhile.
//
// Refresh is single-flight and every wait is select-based, so a caller whose
// context expires abandons the wait instead of wedging behind an uninterruptible
// lock. The flight runs on a context detached from any single caller (its
// outcome is shared), bounded by the HTTP client timeout.
func (m *tokenManager) get(ctx context.Context) (string, error) {
	m.mu.Lock()

	if m.valid() {
		token := m.token
		m.mu.Unlock()
		return token, nil
	}

	if m.stale() {
		return m.getStaleLocked(ctx)
	}

	// No usable token: block on a refresh, gated so a fast-failing endpoint is
	// paced (defect ②) and a dead caller never starts a detached flight
	// (defect ①). Joining an in-flight refresh is never gated — that shares a
	// result, it is not a fresh mint.
	if m.inflight == nil {
		if m.lastErr != nil && m.now().Before(m.holdUntil) {
			err := m.lastErr
			m.mu.Unlock()
			return "", err
		}
		if err := ctx.Err(); err != nil {
			m.mu.Unlock()
			return "", err
		}
		m.startFlightLocked(ctx)
	}
	fl := m.inflight
	m.mu.Unlock()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-fl.done:
	}
	if fl.err != nil {
		return "", fl.err
	}
	return fl.token, nil
}

// getStaleLocked serves a stale-but-valid token without ever blacking the
// caller out. Caller holds m.mu; this function releases it. See get for the
// regime description.
func (m *tokenManager) getStaleLocked(ctx context.Context) (string, error) {
	// Fail fast on a known terminal failure: masking revoked/rotated
	// credentials behind the stale token until hard expiry would let the bot
	// keep trading on broken auth (review P2). A paced refresh is still kicked
	// below via the earlier maybeStart so the state can recover if credentials
	// are fixed.
	m.maybeStartRefreshLocked(ctx)
	if m.terminalErr != nil {
		err := m.terminalErr
		m.mu.Unlock()
		return "", err
	}
	fl := m.inflight
	token := m.token
	budget := m.refreshBudget
	m.mu.Unlock()

	if fl == nil {
		// No refresh running (paced off between attempts, or a dead caller must
		// not start one): serve the stale token immediately.
		return token, nil
	}

	// A refresh is in flight. Wait at most the budget: a healthy endpoint
	// completes well within it (caller gets the fresh token); a slow/hung one
	// is not allowed to block the caller — serve the stale token and let the
	// detached refresh keep running.
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-fl.done:
	case <-timer.C:
		// Budget elapsed with the refresh still running. If the token is still
		// stale-but-valid, serve it and leave the refresh detached. If it has
		// hard-expired in the meantime there is nothing safe to serve, so wait
		// for the refresh — we must NOT read fl.token/fl.err here, the flight
		// writes them without the lock and only <-fl.done establishes the edge.
		m.mu.Lock()
		stillStale := m.stale()
		token := m.token
		m.mu.Unlock()
		if stillStale {
			return token, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-fl.done:
		}
	}

	// fl.done is closed here: reading fl.token/fl.err is safe.
	if fl.err == nil {
		return fl.token, nil
	}
	if !isTransient(fl.err) {
		// Terminal failure observed on completion: fail fast (do not mask).
		return "", fl.err
	}
	// Transient failure: the stale token still works, so serve it rather than
	// black out. Past hard expiry stale() is false and the error propagates.
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stale() {
		return m.token, nil
	}
	return "", fl.err
}

// maybeStartRefreshLocked starts a paced, detached refresh when warranted: none
// already in flight, past the failure holdoff, and the caller's context is live
// (a dead caller must not start a fresh mint — defect ①). Caller holds m.mu.
func (m *tokenManager) maybeStartRefreshLocked(ctx context.Context) {
	if m.inflight != nil {
		return // a refresh is already running; do not pile on
	}
	if m.lastErr != nil && m.now().Before(m.holdUntil) {
		return // paced: within the holdoff after a recent failure (defect ②)
	}
	if ctx.Err() != nil {
		return // dead caller: do not start a detached flight (defect ①)
	}
	m.startFlightLocked(ctx)
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
		// Every failure — transient or terminal — arms the holdoff: it is the
		// manager-wide pacing gate. Within the window the stale token or the
		// cached error is served without a new issuance; a backed-off caller
		// crosses the window and reaches a real retry. A terminal failure is
		// also recorded so the stale fallback fails fast (review P2) instead of
		// masking a credential problem.
		m.lastErr = fl.err
		m.holdUntil = m.now().Add(m.holdoff)
		if !isTransient(fl.err) {
			m.terminalErr = fl.err
		}
		if m.stale() {
			m.logger.Warn("toss: token refresh failed; cached token still valid until hard expiry",
				"expires_at", m.expiresAt, "terminal", !isTransient(fl.err), "err", fl.err.Error())
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
	m.terminalErr = nil
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
