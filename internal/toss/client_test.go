package toss

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tokenJSON writes a standard OAuth2 token response.
func tokenJSON(t *testing.T, w http.ResponseWriter, accessToken string, expiresIn int) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   expiresIn,
	}); err != nil {
		t.Fatalf("encode token response: %v", err)
	}
}

// newTestClient builds a Client pointed at srv with deterministic, non-blocking
// sleep so retry/backoff paths run instantly under -race.
func newTestClient(srv *httptest.Server) *Client {
	c := NewClient(srv.URL, "test-id", "test-secret")
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestClient_IssuesAndCachesToken(t *testing.T) {
	var issues int32
	var lastAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-www-form-urlencoded") {
				t.Errorf("token Content-Type = %q, want form-urlencoded", got)
			}
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
			}
			if r.Form.Get("grant_type") != "client_credentials" {
				t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
			}
			if r.Form.Get("client_id") != "test-id" || r.Form.Get("client_secret") != "test-secret" {
				t.Errorf("credentials not sent in form body")
			}
			atomic.AddInt32(&issues, 1)
			tokenJSON(t, w, "tok-1", 86400)
		case "/api/v1/ping":
			lastAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		resp, err := c.Get(ctx, "/api/v1/ping")
		if err != nil {
			t.Fatalf("Get #%d: %v", i, err)
		}
		drainClose(resp)
	}

	if n := atomic.LoadInt32(&issues); n != 1 {
		t.Fatalf("token issued %d times, want 1 (cached)", n)
	}
	if lastAuth != "Bearer tok-1" {
		t.Fatalf("Authorization = %q, want %q", lastAuth, "Bearer tok-1")
	}
}

func TestClient_RefreshesBeforeExpiry(t *testing.T) {
	var issues int32
	var lastAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			n := atomic.AddInt32(&issues, 1)
			tokenJSON(t, w, "tok-"+itoa(int(n)), 100)
		case "/api/v1/ping":
			lastAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv)
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c.tokens.now = clock.now
	c.tokens.leeway = 10 * time.Second
	ctx := context.Background()

	resp, err := c.Get(ctx, "/api/v1/ping")
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	drainClose(resp)
	if lastAuth != "Bearer tok-1" {
		t.Fatalf("first auth = %q", lastAuth)
	}

	// Advance into the leeway window (valid-until = 100-10 = 90s); 95s is stale.
	clock.advance(95 * time.Second)

	resp, err = c.Get(ctx, "/api/v1/ping")
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	drainClose(resp)

	if n := atomic.LoadInt32(&issues); n != 2 {
		t.Fatalf("token issued %d times, want 2 (refresh before expiry)", n)
	}
	if lastAuth != "Bearer tok-2" {
		t.Fatalf("second auth = %q, want Bearer tok-2", lastAuth)
	}
}

func TestClient_SingleFlightRefresh(t *testing.T) {
	var issues int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			atomic.AddInt32(&issues, 1)
			<-release // hold so concurrent callers pile up behind the single flight
			tokenJSON(t, w, "tok-1", 86400)
		case "/api/v1/ping":
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv)
	ctx := context.Background()

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			resp, err := c.Get(ctx, "/api/v1/ping")
			if err != nil {
				t.Errorf("concurrent Get: %v", err)
				return
			}
			drainClose(resp)
		}()
	}
	// Give goroutines time to contend on the token, then release the issuer.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&issues); got != 1 {
		t.Fatalf("token issued %d times under concurrency, want exactly 1", got)
	}
}

func TestClient_RefreshesOn401(t *testing.T) {
	var issues, pings int32
	var seenTokens []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			n := atomic.AddInt32(&issues, 1)
			tokenJSON(t, w, "tok-"+itoa(int(n)), 86400)
		case "/api/v1/ping":
			mu.Lock()
			seenTokens = append(seenTokens, r.Header.Get("Authorization"))
			mu.Unlock()
			if atomic.AddInt32(&pings, 1) == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv)
	resp, err := c.Get(context.Background(), "/api/v1/ping")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after 401 reissue", resp.StatusCode)
	}
	drainClose(resp)

	if n := atomic.LoadInt32(&issues); n != 2 {
		t.Fatalf("token issued %d times, want 2 (forced reissue on 401)", n)
	}
	if n := atomic.LoadInt32(&pings); n != 2 {
		t.Fatalf("ping called %d times, want 2 (one retry)", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seenTokens) != 2 || seenTokens[0] != "Bearer tok-1" || seenTokens[1] != "Bearer tok-2" {
		t.Fatalf("token sequence = %v, want [Bearer tok-1 Bearer tok-2]", seenTokens)
	}
}

func TestClient_RetriesOn5xx(t *testing.T) {
	var pings int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			tokenJSON(t, w, "tok-1", 86400)
		case "/api/v1/ping":
			if atomic.AddInt32(&pings, 1) <= 2 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv)
	resp, err := c.Get(context.Background(), "/api/v1/ping")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after 5xx backoff", resp.StatusCode)
	}
	drainClose(resp)
	if n := atomic.LoadInt32(&pings); n != 3 {
		t.Fatalf("ping called %d times, want 3 (two 5xx then ok)", n)
	}
}

func TestClient_PostDoesNotRetryOn5xx(t *testing.T) {
	var posts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			tokenJSON(t, w, "tok-1", 86400)
		case "/api/v1/orders":
			atomic.AddInt32(&posts, 1)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv)
	resp, err := c.Post(context.Background(), "/api/v1/orders", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 surfaced (no retry)", resp.StatusCode)
	}
	drainClose(resp)
	if n := atomic.LoadInt32(&posts); n != 1 {
		t.Fatalf("order POST sent %d times, want 1 (writes must not auto-retry)", n)
	}
}

func TestClient_CredentialFailureReturnsClearError(t *testing.T) {
	var issues int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			atomic.AddInt32(&issues, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"invalid_client","error_description":"Client authentication failed."}`)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.Get(context.Background(), "/api/v1/ping")
	if err == nil {
		t.Fatal("expected error on credential failure, got nil")
	}
	if !strings.Contains(err.Error(), "invalid_client") {
		t.Fatalf("error %q should name the OAuth2 error code", err.Error())
	}
	if strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("error %q must not leak client_secret", err.Error())
	}
	if n := atomic.LoadInt32(&issues); n != 1 {
		t.Fatalf("token issue attempted %d times, want 1 (no backoff on 4xx)", n)
	}
}

func TestClient_MissingCredentialsFailFast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("must not call API when credentials are unset, hit %q", r.URL.Path)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	_, err := c.Get(context.Background(), "/api/v1/ping")
	if err == nil {
		t.Fatal("expected error when credentials are unset, got nil")
	}
}

func TestClient_WithAccountHeader(t *testing.T) {
	var account string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			tokenJSON(t, w, "tok-1", 86400)
		case "/api/v1/holdings":
			account = r.Header.Get("X-Tossinvest-Account")
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv)
	resp, err := c.Get(context.Background(), "/api/v1/holdings", WithAccount("acc-42"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	drainClose(resp)
	if account != "acc-42" {
		t.Fatalf("X-Tossinvest-Account = %q, want acc-42", account)
	}
}

// --- test helpers ---

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
