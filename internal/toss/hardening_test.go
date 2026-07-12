package toss

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- M-4: base URL scheme validation ---

func TestValidateBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"https production", "https://openapi.tossinvest.com", false},
		{"https any host", "https://example.test", false},
		{"http localhost", "http://localhost:8080", false},
		{"http 127.0.0.1", "http://127.0.0.1:9999", false},
		{"http ipv6 loopback", "http://[::1]:8080", false},
		{"http public host", "http://openapi.tossinvest.com", true},
		{"http typo of production", "http://example.test", true},
		{"scheme missing", "openapi.tossinvest.com", true},
		{"ftp scheme", "ftp://example.test", true},
		{"empty", "", true},
		{"whitespace", "   ", true},
		{"https without host", "https://", true},
		{"https path only", "https:///foo", true},
		{"http without host", "http://", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBaseURL(tt.raw)
			if tt.wantErr && err == nil {
				t.Fatalf("ValidateBaseURL(%q) = nil, want error", tt.raw)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ValidateBaseURL(%q) = %v, want nil", tt.raw, err)
			}
		})
	}
}

func TestNewClient_RejectsPlainHTTPBaseURL(t *testing.T) {
	c, err := NewClient("http://openapi.tossinvest.com", "id", "secret")
	if err == nil {
		t.Fatal("NewClient with plain-http base URL must fail: credentials would travel unencrypted")
	}
	if c != nil {
		t.Fatal("NewClient must not return a usable client alongside an error")
	}
}

func TestNewClient_AllowsHTTPSAndLoopbackHTTP(t *testing.T) {
	for _, raw := range []string{"https://openapi.tossinvest.com", "http://127.0.0.1:1", "http://localhost:1"} {
		if _, err := NewClient(raw, "id", "secret"); err != nil {
			t.Fatalf("NewClient(%q) = %v, want nil", raw, err)
		}
	}
}

// --- L-5: redirects must not be followed ---

// TestClient_DoesNotFollowTokenEndpointRedirect guards the credential-exfil
// path: a 307/308 from the token endpoint would make Go's default redirect
// policy re-send the client_id/client_secret form body to the Location target
// (cross-host bodies are NOT stripped, only Authorization headers are). The
// client must surface the redirect status instead of following it.
func TestClient_DoesNotFollowTokenEndpointRedirect(t *testing.T) {
	var attackerHits int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attackerHits, 1)
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)
		if strings.Contains(string(body[:n]), "test-secret") {
			t.Error("client_secret was re-sent to the redirect target")
		}
	}))
	defer attacker.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			w.Header().Set("Location", attacker.URL+"/oauth2/token")
			w.WriteHeader(http.StatusTemporaryRedirect)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.Get(context.Background(), "/api/v1/ping")
	if err == nil {
		t.Fatal("expected token issuance to fail on a redirect response, got nil")
	}
	if n := atomic.LoadInt32(&attackerHits); n != 0 {
		t.Fatalf("redirect target received %d request(s), want 0 (redirects must not be followed)", n)
	}
}

// --- L-6: response body decode cap ---

func TestDecodeJSON_CapsBodySize(t *testing.T) {
	var small struct {
		A string `json:"a"`
	}
	if err := DecodeJSON(strings.NewReader(`{"a":"ok"}`), &small); err != nil {
		t.Fatalf("small body: %v", err)
	}
	if small.A != "ok" {
		t.Fatalf("small body decoded %q, want ok", small.A)
	}

	huge := `{"a":"` + strings.Repeat("x", 2<<20) + `"}`
	var v struct {
		A string `json:"a"`
	}
	err := DecodeJSON(strings.NewReader(huge), &v)
	if err == nil {
		t.Fatal("oversized body must be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error %q should say the body exceeds the cap", err)
	}
}

func TestClient_OversizedTokenResponseRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":86400}`, strings.Repeat("x", 2<<20))
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.Get(context.Background(), "/api/v1/ping")
	if err == nil {
		t.Fatal("expected oversized token response to fail decoding, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error %q should say the body exceeds the cap", err)
	}
}

// --- M-1: anomalous expires_in must not cause a reissue storm ---

// tokenNoExpiry writes a token response missing expires_in entirely.
func tokenNoExpiry(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"access_token":"tok-x","token_type":"Bearer"}`))
}

func TestClient_ZeroExpiresInFailsFastWithoutReissueStorm(t *testing.T) {
	var issues int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			atomic.AddInt32(&issues, 1)
			tokenJSON(t, w, "tok-1", 0)
		default:
			t.Errorf("API must not be reached without a valid token, hit %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	for i := 0; i < 5; i++ {
		_, err := c.Get(context.Background(), "/api/v1/ping")
		if err == nil {
			t.Fatalf("Get #%d = nil error, want fail-fast on expires_in=0", i)
		}
		if !strings.Contains(err.Error(), "expires_in") {
			t.Fatalf("Get #%d error %q should name expires_in", i, err)
		}
	}
	// One anomalous response must not turn every call into a token issuance:
	// each issuance invalidates the previous token (ADR-0001 herd) and hammers
	// the rate-limited AUTH group.
	if n := atomic.LoadInt32(&issues); n != 1 {
		t.Fatalf("token endpoint hit %d times across 5 Gets, want 1 (negative cache)", n)
	}
}

func TestClient_MissingExpiresInFailsFast(t *testing.T) {
	var issues int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			atomic.AddInt32(&issues, 1)
			tokenNoExpiry(w)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	for i := 0; i < 5; i++ {
		if _, err := c.Get(context.Background(), "/api/v1/ping"); err == nil {
			t.Fatalf("Get #%d = nil error, want fail-fast on missing expires_in", i)
		}
	}
	if n := atomic.LoadInt32(&issues); n != 1 {
		t.Fatalf("token endpoint hit %d times across 5 Gets, want 1 (negative cache)", n)
	}
}

func TestClient_ShortExpiresInDoesNotReissuePerCall(t *testing.T) {
	var issues int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			n := atomic.AddInt32(&issues, 1)
			tokenJSON(t, w, "tok-"+itoa(int(n)), 30) // 30s ≤ default 5m leeway
		case "/api/v1/ping":
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c.tokens.now = clock.now

	for i := 0; i < 5; i++ {
		resp, err := c.Get(context.Background(), "/api/v1/ping")
		if err != nil {
			t.Fatalf("Get #%d: %v", i, err)
		}
		drainClose(resp)
	}
	if n := atomic.LoadInt32(&issues); n != 1 {
		t.Fatalf("token issued %d times across 5 Gets, want 1 (leeway clamped to ttl/2)", n)
	}

	clock.advance(16 * time.Second) // past the clamped half-life
	resp, err := c.Get(context.Background(), "/api/v1/ping")
	if err != nil {
		t.Fatalf("Get after half-life: %v", err)
	}
	drainClose(resp)
	if n := atomic.LoadInt32(&issues); n != 2 {
		t.Fatalf("token issued %d times after half-life, want 2", n)
	}
}

// --- L-3: stale-but-valid fallback at the client level ---

func TestClient_ServesCachedTokenWhenRefreshFails(t *testing.T) {
	var issues int32
	var failToken atomic.Bool
	var lastAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			atomic.AddInt32(&issues, 1)
			if failToken.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			tokenJSON(t, w, "tok-1", 3600)
		case "/api/v1/ping":
			lastAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c.tokens.now = clock.now

	resp, err := c.Get(context.Background(), "/api/v1/ping")
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	drainClose(resp)

	// Move into the leeway window (refresh due, hard expiry not reached) and
	// break the token endpoint: API calls must keep working on the cached
	// token instead of blacking out for up to leeway duration.
	failToken.Store(true)
	clock.advance(3600*time.Second - 2*time.Minute)

	resp, err = c.Get(context.Background(), "/api/v1/ping")
	if err != nil {
		t.Fatalf("Get inside leeway window = %v, want stale-token fallback", err)
	}
	drainClose(resp)
	if lastAuth != "Bearer tok-1" {
		t.Fatalf("auth = %q, want cached Bearer tok-1", lastAuth)
	}

	// Past hard expiry the fallback is gone: the call must fail.
	clock.advance(3 * time.Minute)
	if _, err := c.Get(context.Background(), "/api/v1/ping"); err == nil {
		t.Fatal("Get past hard expiry = nil error, want failure (server rejects the token)")
	}
}

// TestClient_StaleWindowRefreshFailuresArePaced guards the AUTH endpoint from
// an unpaced hammer: the stale fallback makes get() return success, so
// acquireToken's backoff never paces refresh attempts — without a holdoff,
// every API call during a token-endpoint outage would fire its own issuance
// attempt for the whole leeway window.
func TestClient_StaleWindowRefreshFailuresArePaced(t *testing.T) {
	var issues int32
	var failToken atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			atomic.AddInt32(&issues, 1)
			if failToken.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			tokenJSON(t, w, "tok-1", 3600)
		case "/api/v1/ping":
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c.tokens.now = clock.now

	resp, err := c.Get(context.Background(), "/api/v1/ping")
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	drainClose(resp)

	failToken.Store(true)
	clock.advance(3600*time.Second - 2*time.Minute) // inside the leeway window

	for i := 0; i < 5; i++ {
		resp, err := c.Get(context.Background(), "/api/v1/ping")
		if err != nil {
			t.Fatalf("Get #%d in stale window: %v", i, err)
		}
		drainClose(resp)
	}
	// 1 initial issuance + exactly 1 failed refresh attempt for the whole
	// burst; the remaining calls serve the stale token from the holdoff.
	if n := atomic.LoadInt32(&issues); n != 2 {
		t.Fatalf("token endpoint hit %d times across 5 stale-window Gets, want 2 (refresh attempts must be paced)", n)
	}
}

// --- credential leak guard on the client itself ---

func TestClient_FormattingDoesNotLeakCredentials(t *testing.T) {
	c, err := NewClient("https://example.test", "leak-id", "leak-secret")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	for _, s := range []string{
		fmt.Sprintf("%v", c),
		fmt.Sprintf("%+v", c),
		fmt.Sprintf("%#v", c),
		fmt.Sprintf("%s", c),
	} {
		if strings.Contains(s, "leak-secret") {
			t.Fatalf("formatted client %q leaks client_secret", s)
		}
	}
}
