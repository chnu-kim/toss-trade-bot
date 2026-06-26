package toss

import (
	"context"
	"sync"
	"time"
)

// defaultLeeway is how long before a token's stated expiry we proactively
// refresh. The Toss token lives 86400s; refreshing a few minutes early avoids
// racing a request against the exact expiry boundary.
const defaultLeeway = 5 * time.Minute

// issueFunc mints a fresh access token, returning the token string and its
// lifetime. It is the single point of token issuance for a client.
type issueFunc func(ctx context.Context) (token string, ttl time.Duration, err error)

// tokenManager owns the one valid OAuth token for a client. Per Toss, issuing a
// new token invalidates the previous one, so issuance is serialized here: at
// most one in-flight refresh, and cached reuse until the token nears expiry.
type tokenManager struct {
	mu        sync.Mutex
	token     string
	expiresAt time.Time

	leeway time.Duration
	now    func() time.Time
	issue  issueFunc
}

func newTokenManager(issue issueFunc) *tokenManager {
	return &tokenManager{
		leeway: defaultLeeway,
		now:    time.Now,
		issue:  issue,
	}
}

// get returns a valid token, minting one if the cache is empty or near expiry.
//
// The mutex is held across issue() on purpose: it makes refresh single-flight.
// Concurrent callers that arrive during a refresh block here, and once the
// first caller stores the new token the rest observe it as still-valid and
// return it without issuing again — exactly one network issuance per refresh.
func (m *tokenManager) get(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.valid() {
		return m.token, nil
	}

	token, ttl, err := m.issue(ctx)
	if err != nil {
		return "", err
	}
	m.token = token
	m.expiresAt = m.now().Add(ttl)
	return m.token, nil
}

// invalidate drops the cached token, but only if it still matches old. This
// guards the 401 path: several requests may fail with the same stale token, yet
// only the first clears it, so the subsequent get() performs a single reissue
// rather than one per failed request.
func (m *tokenManager) invalidate(old string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token == old {
		m.token = ""
		m.expiresAt = time.Time{}
	}
}

func (m *tokenManager) valid() bool {
	return m.token != "" && m.now().Before(m.expiresAt.Add(-m.leeway))
}
