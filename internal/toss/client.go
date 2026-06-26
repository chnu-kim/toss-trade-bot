// Package toss is the HTTP client for the Toss Open API.
//
// Auth: OAuth 2.0 Client Credentials Grant. Only one token is valid per client
// at a time, so token issuance/caching/refresh MUST be centralized here — never
// let multiple goroutines or processes mint their own tokens.
//
// Retry policy is deliberately asymmetric for unattended safety: GET requests
// back off and retry on 5xx/network errors and reissue once on 401, while write
// requests (POST) are never auto-retried because a re-sent order risks a
// duplicate fill.
package toss

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// httpDoer is the minimal HTTP surface the client needs. *http.Client satisfies
// it; tests inject a stub or an httptest-backed client.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

const (
	tokenPath       = "/oauth2/token"
	maxRetries      = 4
	backoffBase     = 200 * time.Millisecond
	backoffCap      = 5 * time.Second
	headerAccount   = "X-Tossinvest-Account"
	formContentType = "application/x-www-form-urlencoded"
)

// Client talks to the Toss Open API. It owns the single OAuth token for the
// client credentials and reuses it until expiry.
type Client struct {
	baseURL      string
	clientID     string
	clientSecret string
	http         httpDoer
	tokens       *tokenManager

	// sleep waits for d or until ctx is cancelled; injected so tests skip real
	// backoff delays and so an unattended shutdown interrupts a pending wait.
	sleep func(ctx context.Context, d time.Duration) error
}

// NewClient constructs a Client with a sane default HTTP timeout.
func NewClient(baseURL, clientID, clientSecret string) *Client {
	c := &Client{
		baseURL:      strings.TrimRight(baseURL, "/"),
		clientID:     clientID,
		clientSecret: clientSecret,
		http:         &http.Client{Timeout: 10 * time.Second},
		sleep:        sleepCtx,
	}
	c.tokens = newTokenManager(c.issueToken)
	return c
}

// RequestOption mutates an outgoing request before it is sent.
type RequestOption func(*http.Request)

// WithAccount sets the X-Tossinvest-Account header required by account-scoped
// endpoints. accountSeq comes from GET /api/v1/accounts.
func WithAccount(accountSeq string) RequestOption {
	return func(r *http.Request) {
		if accountSeq != "" {
			r.Header.Set(headerAccount, accountSeq)
		}
	}
}

// Get performs an authenticated GET. It retries 429/5xx/network failures with
// exponential backoff and reissues the token once on a 401, both bounded.
func (c *Client) Get(ctx context.Context, path string, opts ...RequestOption) (*http.Response, error) {
	backoffs := 0
	triedRefresh := false
	for {
		token, err := c.acquireToken(ctx)
		if err != nil {
			return nil, err
		}

		req, err := c.newRequest(ctx, http.MethodGet, path, nil, token, opts)
		if err != nil {
			return nil, err
		}

		resp, err := c.http.Do(req)
		if err != nil {
			if backoffs < maxRetries {
				backoffs++
				if werr := c.sleep(ctx, backoffDelay(backoffs)); werr != nil {
					return nil, werr
				}
				continue
			}
			return nil, fmt.Errorf("toss: GET %s: %w", path, err)
		}

		if resp.StatusCode == http.StatusUnauthorized && !triedRefresh {
			triedRefresh = true
			drainClose(resp)
			c.tokens.invalidate(token)
			continue
		}

		if isRetryableStatus(resp.StatusCode) && backoffs < maxRetries {
			backoffs++
			drainClose(resp)
			if werr := c.sleep(ctx, backoffDelay(backoffs)); werr != nil {
				return nil, werr
			}
			continue
		}

		return resp, nil
	}
}

// acquireToken returns a valid token, retrying transient (429/5xx/network)
// issuance failures with bounded backoff. This is safe for any caller — GET or
// POST — because it runs before a request is sent: retrying token issuance
// never resubmits a write. Credential/4xx failures are terminal and returned
// immediately.
func (c *Client) acquireToken(ctx context.Context) (string, error) {
	backoffs := 0
	for {
		token, err := c.tokens.get(ctx)
		if err == nil {
			return token, nil
		}
		if isTransient(err) && backoffs < maxRetries {
			backoffs++
			if werr := c.sleep(ctx, backoffDelay(backoffs)); werr != nil {
				return "", werr
			}
			continue
		}
		return "", err
	}
}

// Post performs an authenticated POST. The pre-send token acquisition retries
// transient outages (no write is in flight yet), but the write itself is NEVER
// auto-retried: a re-sent order could double-fill. A 401 invalidates the cached
// token (it is known bad) so the next call refreshes, but the body is never
// resubmitted here.
func (c *Client) Post(ctx context.Context, path string, body io.Reader, opts ...RequestOption) (*http.Response, error) {
	token, err := c.acquireToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, body, token, opts)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("toss: POST %s: %w", path, err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		c.tokens.invalidate(token)
	}
	return resp, nil
}

// newRequest builds an authenticated request, applying caller options last.
func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader, token string, opts []RequestOption) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("toss: build request %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for _, opt := range opts {
		opt(req)
	}
	return req, nil
}

// issueToken mints a fresh access token via the client_credentials grant. It is
// the only place tokens are minted. Credential/4xx failures fail fast with a
// clear error; transient 5xx/network failures are wrapped as retryable so the
// GET loop can back off. The client_secret is never placed in an error string.
func (c *Client) issueToken(ctx context.Context) (string, time.Duration, error) {
	if c.clientID == "" || c.clientSecret == "" {
		return "", 0, errors.New("toss: client credentials not configured (set TOSS_CLIENT_ID and TOSS_CLIENT_SECRET)")
	}

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+tokenPath, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("toss: build token request: %w", err)
	}
	req.Header.Set("Content-Type", formContentType)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", 0, &transientError{err: fmt.Errorf("toss: token request: %w", err)}
	}
	defer drainClose(resp)

	if resp.StatusCode != http.StatusOK {
		oerr := decodeOAuthError(resp)
		if isRetryableStatus(resp.StatusCode) {
			return "", 0, &transientError{err: oerr}
		}
		return "", 0, oerr
	}

	var tr struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", 0, fmt.Errorf("toss: decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", 0, errors.New("toss: token response missing access_token")
	}
	return tr.AccessToken, time.Duration(tr.ExpiresIn) * time.Second, nil
}

// decodeOAuthError reads the OAuth2 standard error envelope ({error,
// error_description}). It never echoes credentials.
func decodeOAuthError(resp *http.Response) error {
	var oe struct {
		Err  string `json:"error"`
		Desc string `json:"error_description"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&oe)
	switch {
	case oe.Err != "" && oe.Desc != "":
		return fmt.Errorf("toss: token issuance failed (%d): %s: %s", resp.StatusCode, oe.Err, oe.Desc)
	case oe.Err != "":
		return fmt.Errorf("toss: token issuance failed (%d): %s", resp.StatusCode, oe.Err)
	default:
		return fmt.Errorf("toss: token issuance failed: status %d", resp.StatusCode)
	}
}

// transientError marks a token-issuance failure as worth retrying with backoff
// (5xx/network), as opposed to a terminal credential error.
type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

func isTransient(err error) bool {
	var t *transientError
	return errors.As(err, &t)
}

// isRetryableStatus reports whether an HTTP status warrants a backoff retry for
// safe (read/token) requests: rate limiting (429) and server errors (5xx).
func isRetryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// backoffDelay is exponential with a cap; attempt starts at 1.
func backoffDelay(attempt int) time.Duration {
	d := backoffBase << (attempt - 1)
	if d > backoffCap || d <= 0 {
		return backoffCap
	}
	return d
}

// sleepCtx waits for d or returns early if ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// drainClose drains and closes a response body so the connection can be reused.
func drainClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
