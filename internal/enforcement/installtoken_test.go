package enforcement

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestInstallationTokenMinter(t *testing.T, baseURL string) *InstallationTokenMinter {
	t.Helper()
	key := generateTestRSAKey(t)
	m, err := NewInstallationTokenMinter("4244791", "145160347", "toss-trade-bot", key)
	if err != nil {
		t.Fatalf("NewInstallationTokenMinter: %v", err)
	}
	m.baseURL = baseURL
	return m
}

func TestInstallationTokenMinter_Mint_Success(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotAccept, gotContentType string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotContentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":                "ghs_mocktoken1234567890",
			"expires_at":           "2026-07-08T13:00:00Z",
			"repository_selection": "selected",
			"permissions": map[string]string{
				"contents":      "write",
				"pull_requests": "write",
			},
		})
	}))
	defer srv.Close()

	m := newTestInstallationTokenMinter(t, srv.URL)
	tok, err := m.Mint(context.Background())
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	if tok.Token != "ghs_mocktoken1234567890" {
		t.Fatalf("Token = %q, want ghs_mocktoken1234567890", tok.Token)
	}
	wantExpiry := time.Date(2026, 7, 8, 13, 0, 0, 0, time.UTC)
	if !tok.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("ExpiresAt = %v, want %v", tok.ExpiresAt, wantExpiry)
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/app/installations/145160347/access_tokens" {
		t.Fatalf("path = %q, want /app/installations/145160347/access_tokens", gotPath)
	}
	if !strings.HasPrefix(gotAuth, "Bearer ") {
		t.Fatalf("Authorization = %q, want Bearer <jwt>", gotAuth)
	}
	if parts := strings.Split(strings.TrimPrefix(gotAuth, "Bearer "), "."); len(parts) != 3 {
		t.Fatalf("Authorization JWT has %d parts, want 3 (App JWT, not an installation token)", len(parts))
	}
	if gotAccept != "application/vnd.github+json" {
		t.Fatalf("Accept = %q, want application/vnd.github+json", gotAccept)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", gotContentType)
	}

	// The token must be narrowed to exactly the target repo and the minimal
	// permissions this loop's git-push + PR-create workflow needs — an
	// unnarrowed ({}) body would request every repo/permission the
	// installation was ever granted (codex adversarial-review finding, PR #44).
	wantRepos := []any{"toss-trade-bot"}
	if gotRepos, _ := gotBody["repositories"].([]any); len(gotRepos) != 1 || gotRepos[0] != wantRepos[0] {
		t.Fatalf("request body repositories = %v, want %v", gotBody["repositories"], wantRepos)
	}
	gotPerms, _ := gotBody["permissions"].(map[string]any)
	if gotPerms["contents"] != "write" {
		t.Fatalf("request body permissions.contents = %v, want write", gotPerms["contents"])
	}
	if gotPerms["pull_requests"] != "write" {
		t.Fatalf("request body permissions.pull_requests = %v, want write", gotPerms["pull_requests"])
	}
	if len(gotPerms) != 2 {
		t.Fatalf("request body permissions = %v, want exactly {contents, pull_requests}", gotPerms)
	}
}

func TestInstallationTokenMinter_Mint_NonCreatedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	m := newTestInstallationTokenMinter(t, srv.URL)
	if _, err := m.Mint(context.Background()); err == nil {
		t.Fatal("non-201 response must return an error, not a token")
	}
}

func TestInstallationTokenMinter_Mint_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	m := newTestInstallationTokenMinter(t, srv.URL)
	if _, err := m.Mint(context.Background()); err == nil {
		t.Fatal("5xx response must return an error, not a token")
	}
}

func TestInstallationTokenMinter_Mint_MissingToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"expires_at": "2026-07-08T13:00:00Z",
		})
	}))
	defer srv.Close()

	m := newTestInstallationTokenMinter(t, srv.URL)
	if _, err := m.Mint(context.Background()); err == nil {
		t.Fatal("response missing token must return an error, not a zero-value token")
	}
}

func TestInstallationTokenMinter_Mint_MissingExpiresAt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": "ghs_mocktoken1234567890",
		})
	}))
	defer srv.Close()

	m := newTestInstallationTokenMinter(t, srv.URL)
	if _, err := m.Mint(context.Background()); err == nil {
		t.Fatal("response missing expires_at must return an error — callers cannot judge re-mint timing without it")
	}
}

func TestInstallationTokenMinter_Mint_UnparseableExpiresAt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_mocktoken1234567890",
			"expires_at": "not-a-timestamp",
		})
	}))
	defer srv.Close()

	m := newTestInstallationTokenMinter(t, srv.URL)
	if _, err := m.Mint(context.Background()); err == nil {
		t.Fatal("malformed expires_at must return an error, not a zero-value expiry")
	}
}

func TestInstallationTokenMinter_Mint_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("{not json"))
	}))
	defer srv.Close()

	m := newTestInstallationTokenMinter(t, srv.URL)
	if _, err := m.Mint(context.Background()); err == nil {
		t.Fatal("malformed JSON body must return an error")
	}
}

func TestNewInstallationTokenMinter_NilKeyFailsClosed(t *testing.T) {
	if _, err := NewInstallationTokenMinter("4244791", "145160347", "toss-trade-bot", nil); err == nil {
		t.Fatal("NewInstallationTokenMinter with nil key must return an error")
	}
}

func TestNewInstallationTokenMinter_EmptyAppIDFailsClosed(t *testing.T) {
	key := generateTestRSAKey(t)
	if _, err := NewInstallationTokenMinter("", "145160347", "toss-trade-bot", key); err == nil {
		t.Fatal("NewInstallationTokenMinter with empty appID must return an error")
	}
}

func TestNewInstallationTokenMinter_EmptyInstallationIDFailsClosed(t *testing.T) {
	key := generateTestRSAKey(t)
	if _, err := NewInstallationTokenMinter("4244791", "", "toss-trade-bot", key); err == nil {
		t.Fatal("NewInstallationTokenMinter with empty installationID must return an error")
	}
}

func TestNewInstallationTokenMinter_EmptyRepoFailsClosed(t *testing.T) {
	key := generateTestRSAKey(t)
	if _, err := NewInstallationTokenMinter("4244791", "145160347", "", key); err == nil {
		t.Fatal("NewInstallationTokenMinter with empty repo must return an error — an unset repo would silently request an unnarrowed token")
	}
}

func TestNewInstallationTokenMinterFromPEM_Valid(t *testing.T) {
	key := generateTestRSAKey(t)
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if _, err := NewInstallationTokenMinterFromPEM("4244791", "145160347", "toss-trade-bot", pemBytes); err != nil {
		t.Fatalf("NewInstallationTokenMinterFromPEM: %v", err)
	}
}

func TestNewInstallationTokenMinterFromPEM_InvalidPEMFailsClosed(t *testing.T) {
	if _, err := NewInstallationTokenMinterFromPEM("4244791", "145160347", "toss-trade-bot", []byte("not a pem file")); err == nil {
		t.Fatal("invalid PEM must return an error")
	}
}

// --- InstallationToken.NeedsRefresh ---

func TestInstallationToken_NeedsRefresh_EmptyToken(t *testing.T) {
	var tok InstallationToken
	if !tok.NeedsRefresh(time.Now(), time.Minute) {
		t.Fatal("zero-value InstallationToken must always need a refresh")
	}
}

func TestInstallationToken_NeedsRefresh_WithinLeeway(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 55, 0, 0, time.UTC)
	tok := InstallationToken{Token: "ghs_x", ExpiresAt: time.Date(2026, 7, 8, 13, 0, 0, 0, time.UTC)}
	if !tok.NeedsRefresh(now, 10*time.Minute) {
		t.Fatal("token expiring within the leeway window must report NeedsRefresh=true")
	}
}

func TestInstallationToken_NeedsRefresh_StillValid(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	tok := InstallationToken{Token: "ghs_x", ExpiresAt: time.Date(2026, 7, 8, 13, 0, 0, 0, time.UTC)}
	if tok.NeedsRefresh(now, 10*time.Minute) {
		t.Fatal("token well before expiry minus leeway must not need a refresh")
	}
}
