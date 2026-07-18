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
//
// Transport hardening: the base URL must be https (plain http is allowed only
// for loopback test servers), redirects are never followed (a token-endpoint
// redirect would re-send the credential form body elsewhere), and response
// decoding is byte-capped so a misbehaving upstream cannot OOM the process.
package toss

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
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

	// maxResponseBytes caps how much of any response body is read into memory
	// while decoding (1 MiB). The HTTP client timeout bounds time, not bytes: a
	// malicious or misbehaving upstream could otherwise stream hundreds of MB
	// of JSON onto the heap and OOM the unattended process, killing the order
	// loop. Real Toss payloads are orders of magnitude below this cap.
	maxResponseBytes = 1 << 20
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

// NewClient constructs a Client with a sane default HTTP timeout. It fails
// when baseURL does not pass ValidateBaseURL, so a plain-http typo can never
// silently boot a process that would send credentials unencrypted.
func NewClient(baseURL, clientID, clientSecret string) (*Client, error) {
	if err := ValidateBaseURL(baseURL); err != nil {
		return nil, err
	}
	c := &Client{
		baseURL:      strings.TrimRight(baseURL, "/"),
		clientID:     clientID,
		clientSecret: clientSecret,
		http: &http.Client{
			Timeout: 10 * time.Second,
			// Never follow redirects. Go's default policy strips the
			// Authorization header cross-host but re-sends request BODIES, so
			// a 307/308 from the token endpoint would forward the
			// client_id/client_secret form to an arbitrary Location target.
			// The Toss API never legitimately redirects; surface the status.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		sleep: sleepCtx,
	}
	c.tokens = newTokenManager(c.issueToken)
	return c, nil
}

// ValidateBaseURL enforces that credentials and bearer tokens only ever travel
// over TLS: the base URL must be https. Plain http is allowed solely for
// loopback hosts (httptest servers, local stubs) so tests never need the
// production scheme relaxed.
func ValidateBaseURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("toss: base URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("toss: invalid base URL: %w", err)
	}
	switch u.Scheme {
	case "https":
		// A host-less URL ("https://", "https:///path") would pass a
		// scheme-only check, boot fine, and then fail on the first request —
		// defeating the fail-fast contract of this validator.
		if u.Hostname() == "" {
			return fmt.Errorf("toss: base URL %s has no host", u.Redacted())
		}
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("toss: base URL %s uses plain http: credentials and tokens would be sent unencrypted (use https; http is allowed only for loopback hosts)", u.Redacted())
	default:
		return fmt.Errorf("toss: base URL %s must use https, got scheme %q", u.Redacted(), u.Scheme)
	}
}

// isLoopbackHost reports whether host (no port) is localhost or a loopback IP.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// SetLogger routes the client's operational warnings (e.g. serving a cached
// token because a refresh failed) to l. Call it at boot; the default is
// slog.Default() so warnings are never silently dropped.
func (c *Client) SetLogger(l *slog.Logger) {
	c.tokens.setLogger(l)
}

// SetTokenRefreshFailureHook registers fn to be called once for every FAILED
// token issuance attempt, with the time the failure was observed.
//
// This is the escalation seam for "we can no longer authenticate" (ADR-0004
// point 7). Token-refresh failure is non-reconstructable — unlike an ambiguous
// order it leaves no journal evidence a restart could replay — so the kill
// switch counts it in a persisted window. #36 wires this to
// killswitch.ReportTokenRefreshFailure; it deliberately does NOT wire a direct
// global trip, which would bypass the counting/threshold contract killswitch
// owns.
//
// Contract for fn: it is invoked from the issuance goroutine after that
// issuance's waiters have been released and never while the token lock is held,
// so it may block on a durable write without stalling token callers. A panic in
// fn is recovered and logged. It fires once per refresh ATTEMPT (issuance is
// single-flight), not once per waiting caller, so a burst of callers sharing one
// failure counts as one failure. Passing nil disables the hook.
func (c *Client) SetTokenRefreshFailureHook(fn func(occurredAt time.Time)) {
	c.tokens.setRefreshFailureHook(fn)
}

// WaitForRefreshFailureReports blocks until every observed token-refresh failure
// has finished being reported through the hook, or ctx expires.
//
// A graceful shutdown MUST call this before certifying the run clean. Token
// issuance runs on flights detached from any supervisor, so a shutdown can
// otherwise race a refresh failure whose escalation has not been reported yet;
// if that report then fails (for instance because the store has just closed),
// the non-reconstructable failure survives only as an in-memory latch in a
// process that is exiting, and the next boot trusts the clean marker and comes
// up unhalted. Waiting here makes the outcome deterministic: the report either
// persists (counted) or fails into the kill switch's latch (which refuses the
// clean sentinel).
//
// It returns ctx.Err() if reports are still outstanding when ctx expires; the
// caller should treat that as "not cleanly drained" rather than as success.
func (c *Client) WaitForRefreshFailureReports(ctx context.Context) error {
	return c.tokens.waitForFailureReports(ctx)
}

// Format, String and GoString use VALUE receivers on purpose: with pointer
// receivers only *Client satisfies fmt.Stringer, so formatting a Client value
// (logging `*client`, or embedding Client in a struct printed with %+v) would
// fall back to reflection and dump the unexported clientSecret in plaintext —
// leaking the brokerage credential (L-9). A value receiver protects both the
// value and the pointer, and Format additionally covers mismatched verbs (%d,
// %x, …) which would otherwise reach fmt's field-reflecting bad-verb path.
//
// Residual limit: fmt special-cases %p and %T before any Stringer/Formatter, so
// %p on a Client value still reflects fields. %p is not a realistic logging
// verb for a struct; do not rely on it being redacted.
func (c Client) Format(f fmt.State, verb rune) {
	if verb == 'v' && f.Flag('#') {
		_, _ = io.WriteString(f, c.GoString())
		return
	}
	if verb == 'q' {
		fmt.Fprintf(f, "%q", c.String())
		return
	}
	_, _ = io.WriteString(f, c.String())
}

// String keeps the client printable without exposing credentials.
func (c Client) String() string { return "toss.Client(" + c.baseURL + ")" }

// GoString keeps %#v from dumping unexported credential fields via reflection.
func (c Client) GoString() string { return c.String() }

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
	if err := DecodeJSON(resp.Body, &tr); err != nil {
		// Classify by shape, not blanket-transient. Only io.ErrUnexpectedEOF —
		// a body that started well then was cut off mid-value (a mid-response
		// TCP reset / truncation) — is a recoverable network glitch worth a
		// retry; classifying it transient lets GET retry and keeps it from
		// poisoning terminalErr in the stale window (L-3).
		//
		// Everything else is a terminal contract violation that does not heal
		// on retry, so a bounded retry loop must not be spent on it and, in the
		// stale window, it must fail fast rather than mask a broken contract
		// until hard expiry: an EMPTY body (bare io.EOF — the server sent no
		// token at all, not a mid-stream cut), a type mismatch
		// (*json.UnmarshalTypeError), malformed JSON (*json.SyntaxError),
		// trailing data, or an oversized body over the 1 MiB cap. (Semantic
		// problems in a fully decoded body — missing access_token, bad
		// expires_in below — are terminal for the same reason.)
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return "", 0, &transientError{err: fmt.Errorf("toss: decode token response: %w", err)}
		}
		return "", 0, fmt.Errorf("toss: decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", 0, errors.New("toss: token response missing access_token")
	}
	if tr.ExpiresIn <= 0 {
		// Fail fast on an anomalous lifetime instead of caching a token that
		// is expired the moment it is stored: that would silently turn every
		// call into a reissue, each new token invalidating the previous one
		// (the ADR-0001 thundering herd, self-inflicted). Terminal on purpose
		// — retrying would mint more mutually invalidating tokens.
		return "", 0, fmt.Errorf("toss: token response has invalid expires_in %d (want positive seconds per OAuth2 spec)", tr.ExpiresIn)
	}
	return tr.AccessToken, time.Duration(tr.ExpiresIn) * time.Second, nil
}

// DecodeJSON decodes exactly one JSON value from r into v, reading at most
// maxResponseBytes (1 MiB). It is the OOM guard for every response decode in
// this module (the account/market wrappers use it too): an oversized body
// fails with a clear error instead of growing the heap without bound.
//
// It also requires the body to be a single value: trailing data after the
// value (a second value, garbage, or an oversized blob) is rejected within the
// same cap so the byte limit stays exhaustive — a small valid value cannot be
// used to smuggle a huge trailing payload past the limit. (Go decodes the
// trailing lazily and never buffers it whole, so this is contract hygiene, not
// an OOM fix.) Trailing whitespace — e.g. the newline json.Encoder emits — is
// allowed.
//
// Phase boundary: only a truncated PRIMARY value (the first Decode below) may
// surface io.ErrUnexpectedEOF — that is the sole "mid-stream transport cut"
// signal callers treat as transient (retryable). Every trailing-stage error is
// a contract violation and is wrapped with %v (NOT %w) so it cannot carry the
// io.ErrUnexpectedEOF chain upward and be misclassified as transient. This
// keeps the transient/terminal decision encapsulated here: callers just test
// errors.Is(err, io.ErrUnexpectedEOF) without guessing which phase produced it.
func DecodeJSON(r io.Reader, v any) error {
	lr := &io.LimitedReader{R: r, N: maxResponseBytes + 1}
	dec := json.NewDecoder(lr)
	if err := dec.Decode(v); err != nil {
		if lr.N <= 0 {
			return errResponseTooLarge()
		}
		return err
	}
	// dec.Token past the decoded value returns io.EOF once only whitespace
	// remains; anything else (another token, malformed bytes, or a cap hit
	// while reading an oversized trailing value) is a contract violation.
	if _, err := dec.Token(); err != io.EOF {
		if lr.N <= 0 {
			return errResponseTooLarge()
		}
		if err != nil {
			// %v, not %w: sever any io.ErrUnexpectedEOF chain so a truncated
			// TRAILING value is terminal, not a transient transport glitch.
			return fmt.Errorf("toss: malformed trailing data after JSON response: %v", err)
		}
		return errors.New("toss: unexpected trailing data after JSON response")
	}
	// Success path: the byte cap must hold here too. A value (plus any trailing
	// whitespace) that consumed the whole limit is oversized even though it
	// decoded cleanly and left nothing after it — without this check a
	// cap-aligned value would slip past, since the failure/trailing branches
	// above are the only other places lr.N is inspected.
	if lr.N <= 0 {
		return errResponseTooLarge()
	}
	return nil
}

func errResponseTooLarge() error {
	return fmt.Errorf("toss: response body exceeds %d bytes", maxResponseBytes)
}

// decodeOAuthError reads the OAuth2 standard error envelope ({error,
// error_description}). It never echoes credentials.
func decodeOAuthError(resp *http.Response) error {
	var oe struct {
		Err  string `json:"error"`
		Desc string `json:"error_description"`
	}
	_ = DecodeJSON(resp.Body, &oe)
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
