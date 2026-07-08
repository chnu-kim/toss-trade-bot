package enforcement

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// InstallationToken is a minted GitHub App installation access token. GitHub
// mints these short-lived (documented ~1 hour) tokens per "Authenticating as
// a GitHub App installation" and offers no refresh call — the only way to get
// a new one is to mint again with a fresh App JWT. ExpiresAt lets a caller
// (e.g. a future git-credential helper feeding this into git/gh for loop-made
// commits/PRs — see ADR-0009 point 5) decide when to re-mint instead of
// discovering expiry via a failed push.
type InstallationToken struct {
	Token     string
	ExpiresAt time.Time
}

// NeedsRefresh reports whether tok should be re-minted: either it is the
// zero value (never minted) or now is within leeway of ExpiresAt. Comparing
// against "expires_at minus leeway" rather than the raw expiry mirrors
// internal/toss's tokenManager.valid() — proactively refreshing avoids racing
// a git/gh call against the exact expiry boundary.
func (tok InstallationToken) NeedsRefresh(now time.Time, leeway time.Duration) bool {
	if tok.Token == "" {
		return true
	}
	return !now.Before(tok.ExpiresAt.Add(-leeway))
}

// InstallationTokenMinter mints fresh installation access tokens for one
// GitHub App installation, authenticating with the App's own JWT (signAppJWT
// — the same signing logic AppActorResolver uses, per ADR-0009 point 5/#43:
// "이 코드를 installation token 발급에도 재사용한다"). It is the seam a future
// loop-facing git-credential helper depends on, so tests never need a real
// network call or a real App private key.
type InstallationTokenMinter struct {
	baseURL        string
	appID          string
	installationID string
	key            *rsa.PrivateKey
	http           httpDoer
	now            func() time.Time
}

// NewInstallationTokenMinter builds a minter for the App identified by appID,
// signing with key, targeting installationID (the numeric ID GitHub assigns
// when the App is installed on an org/repo). It fails closed at construction
// time on any missing argument, mirroring NewAppActorResolver.
func NewInstallationTokenMinter(appID, installationID string, key *rsa.PrivateKey) (*InstallationTokenMinter, error) {
	if key == nil {
		return nil, errors.New("enforcement: NewInstallationTokenMinter: private key is nil")
	}
	if appID == "" {
		return nil, errors.New("enforcement: NewInstallationTokenMinter: appID is empty")
	}
	if installationID == "" {
		return nil, errors.New("enforcement: NewInstallationTokenMinter: installationID is empty")
	}
	return &InstallationTokenMinter{
		baseURL:        defaultGitHubAPIBaseURL,
		appID:          appID,
		installationID: installationID,
		key:            key,
		http:           &http.Client{Timeout: 10 * time.Second},
		now:            time.Now,
	}, nil
}

// NewInstallationTokenMinterFromPEM parses a PEM-encoded RSA private key
// (accepting both the PKCS#1 and PKCS#8 forms GitHub is known to hand out)
// and builds an InstallationTokenMinter from it, so callers never need to
// touch crypto/rsa directly.
func NewInstallationTokenMinterFromPEM(appID, installationID string, pemBytes []byte) (*InstallationTokenMinter, error) {
	key, err := parseRSAPrivateKeyPEM(pemBytes)
	if err != nil {
		return nil, err
	}
	return NewInstallationTokenMinter(appID, installationID, key)
}

// Mint signs a fresh App JWT and exchanges it for an installation access
// token via POST /app/installations/{installation_id}/access_tokens (GitHub
// REST API "Create an installation access token for an app"). An empty JSON
// object body requests a token scoped to everything the installation was
// granted at install time (no narrowing) — this loop only has one
// installation to mint for, so there is nothing to narrow.
//
// Mint does not retry. Minting is what a caller then uses to authenticate a
// write (commit/PR creation); masking a transient failure behind a retry here
// risks the same "duplicate write" class of risk CLAUDE.md forbids for order
// submission — a caller that wants retry-on-failure must decide that itself,
// with the same care CLAUDE.md applies to read-only retries.
func (m *InstallationTokenMinter) Mint(ctx context.Context) (InstallationToken, error) {
	jwt, err := signAppJWT(m.appID, m.key, m.now())
	if err != nil {
		return InstallationToken{}, err
	}

	path := fmt.Sprintf("/app/installations/%s/access_tokens", m.installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+path, bytes.NewReader([]byte("{}")))
	if err != nil {
		return InstallationToken{}, fmt.Errorf("enforcement: build request POST %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.http.Do(req)
	if err != nil {
		return InstallationToken{}, fmt.Errorf("enforcement: POST %s: %w", path, err)
	}
	defer drainClose(resp)

	if resp.StatusCode != http.StatusCreated {
		return InstallationToken{}, fmt.Errorf("enforcement: POST %s: status %d", path, resp.StatusCode)
	}

	var parsed struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return InstallationToken{}, fmt.Errorf("enforcement: decode POST %s response: %w", path, err)
	}
	if parsed.Token == "" {
		return InstallationToken{}, fmt.Errorf("enforcement: POST %s response missing token", path)
	}
	if parsed.ExpiresAt == "" {
		return InstallationToken{}, fmt.Errorf("enforcement: POST %s response missing expires_at", path)
	}
	expiresAt, err := time.Parse(time.RFC3339, parsed.ExpiresAt)
	if err != nil {
		return InstallationToken{}, fmt.Errorf("enforcement: POST %s response has unparseable expires_at %q: %w", path, parsed.ExpiresAt, err)
	}

	return InstallationToken{Token: parsed.Token, ExpiresAt: expiresAt}, nil
}
