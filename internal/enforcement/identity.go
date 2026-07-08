package enforcement

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ActorResolver answers "which GitHub identity would author a commit/PR
// right now, given the credential this resolver holds?" It is the seam
// CheckIdentity depends on, so tests never need a real network call or a real
// App private key.
type ActorResolver interface {
	ResolveActor(ctx context.Context) (string, error)
}

// --- PATActorResolver: resolves identity for a classic PAT/OAuth token ---

// PATActorResolver resolves the acting identity of a personal-access-token
// (or OAuth) style credential via GET /user. This is the credential
// CLAUDE.md's existing "gh auth switch --user chnu-kim" workflow uses — so in
// the pre-migration state this resolver correctly reports "chnu-kim", which is
// exactly the signal CheckIdentity needs to fail-closed until the loop
// actually switches to the App.
type PATActorResolver struct {
	baseURL string
	token   string
	http    httpDoer
}

// NewPATActorResolver builds a PATActorResolver authenticating with token.
func NewPATActorResolver(token string) *PATActorResolver {
	return &PATActorResolver{
		baseURL: defaultGitHubAPIBaseURL,
		token:   token,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// ResolveActor calls GET /user and returns the authenticated login.
func (r *PATActorResolver) ResolveActor(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/user", nil)
	if err != nil {
		return "", fmt.Errorf("enforcement: build request GET /user: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := r.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("enforcement: GET /user: %w", err)
	}
	defer drainClose(resp)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("enforcement: GET /user: status %d", resp.StatusCode)
	}

	var parsed struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("enforcement: decode GET /user response: %w", err)
	}
	if parsed.Login == "" {
		return "", errors.New("enforcement: GET /user response missing login")
	}
	return parsed.Login, nil
}

// --- AppActorResolver: resolves identity for the GitHub App itself ---

// AppActorResolver resolves the loop's App identity by authenticating as the
// GitHub App (a JWT signed with the App's own private key, per GitHub's
// "Generating a JSON Web Token" spec) and calling GET /app — the one GitHub
// REST endpoint that *requires* App-JWT auth and therefore proves the caller
// genuinely holds the App's private key, not just any token.
//
// GitHub attributes every commit/PR the App creates (via its installation
// token) as "<slug>[bot]", so a successful GET /app returning slug "mechanu"
// is the strongest same-call evidence available that the loop's authoring
// identity is the App, without creating a side-effecting test commit/PR.
type AppActorResolver struct {
	baseURL string
	appID   string
	key     *rsa.PrivateKey
	http    httpDoer
	now     func() time.Time
}

// NewAppActorResolver builds an AppActorResolver for the App identified by
// appID, signing with key. It fails closed at construction time if key is
// nil, so a misconfigured caller gets a clear error instead of a resolver that
// always fails opaquely at call time.
func NewAppActorResolver(appID string, key *rsa.PrivateKey) (*AppActorResolver, error) {
	if key == nil {
		return nil, errors.New("enforcement: NewAppActorResolver: private key is nil")
	}
	if appID == "" {
		return nil, errors.New("enforcement: NewAppActorResolver: appID is empty")
	}
	return &AppActorResolver{
		baseURL: defaultGitHubAPIBaseURL,
		appID:   appID,
		key:     key,
		http:    &http.Client{Timeout: 10 * time.Second},
		now:     time.Now,
	}, nil
}

// NewAppActorResolverFromPEM parses a PEM-encoded RSA private key (accepting
// both the PKCS#1 and PKCS#8 forms GitHub is known to hand out for a new App)
// and builds an AppActorResolver from it. This is the constructor
// cmd/presence-check uses so callers never need to touch crypto/rsa directly.
func NewAppActorResolverFromPEM(appID string, pemBytes []byte) (*AppActorResolver, error) {
	key, err := parseRSAPrivateKeyPEM(pemBytes)
	if err != nil {
		return nil, err
	}
	return NewAppActorResolver(appID, key)
}

// ResolveActor signs a fresh App JWT and calls GET /app, returning
// "<slug>[bot]".
func (r *AppActorResolver) ResolveActor(ctx context.Context) (string, error) {
	jwt, err := signAppJWT(r.appID, r.key, r.now())
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/app", nil)
	if err != nil {
		return "", fmt.Errorf("enforcement: build request GET /app: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := r.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("enforcement: GET /app: %w", err)
	}
	defer drainClose(resp)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("enforcement: GET /app: status %d", resp.StatusCode)
	}

	var parsed struct {
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("enforcement: decode GET /app response: %w", err)
	}
	if parsed.Slug == "" {
		return "", errors.New("enforcement: GET /app response missing slug")
	}
	return parsed.Slug + "[bot]", nil
}

// --- WithdrawnActorResolver ---

// WithdrawnActorResolver always fails to resolve, citing ADR-0011 point 10:
// App-key possession (a successful App-JWT GET /app) proves nothing about
// which identity actually authors the loop's PRs — the probe passed while
// every loop PR was still authored by the human account (semantic false
// positive, empirically demonstrated). cmd/presence-check wires this resolver
// so check (c) stays fail-closed — regardless of what credentials are
// configured — until the ADR-0011 c-1/c-2 redefinition (PR-creation workflow
// existence + actual recent loop-PR author) is implemented in its follow-up
// issue. AppActorResolver is kept for that future reuse (App-JWT signing),
// not as identity evidence.
type WithdrawnActorResolver struct{}

// ResolveActor always returns the withdrawal error — CheckIdentity turns it
// into an unmet, fail-closed result whose reason an operator can act on.
func (WithdrawnActorResolver) ResolveActor(context.Context) (string, error) {
	return "", errors.New("ADR-0011 point 10: App-key 보유 증명(GET /app)은 PR 작성 identity의 증거에서 폐기됨(의미상 false positive) — c-1/c-2 재정의 구현 전까지 check (c)는 fail-closed")
}

// --- CheckIdentity ---

// CheckIdentity implements ADR-0009 point 8(c): the identity that would
// author the loop's next commit/PR must have genuinely flipped from the human
// reviewer to expectedActor (the GitHub App's bot identity, e.g.
// "mechanu[bot]"). A resolver error, a nil resolver, or an actor that doesn't
// match expectedActor (most notably: still "chnu-kim") all fail-closed.
func CheckIdentity(ctx context.Context, resolver ActorResolver, expectedActor string) CheckResult {
	if resolver == nil {
		return unmetResult(CheckNameIdentity, "identity resolver가 설정되지 않음(App/PAT 자격증명 없음)")
	}

	actor, err := resolver.ResolveActor(ctx)
	if err != nil {
		return unmetResult(CheckNameIdentity, fmt.Sprintf("actor 확인 불가: %v", err))
	}
	if actor == "" {
		return unmetResult(CheckNameIdentity, "actor 확인 불가: 빈 응답")
	}
	if !strings.EqualFold(actor, expectedActor) {
		return unmetResult(CheckNameIdentity, fmt.Sprintf(
			"PR 작성 identity가 %s로 전환되지 않음(실측 actor: %s)", expectedActor, actor,
		))
	}
	return metResult(CheckNameIdentity)
}
