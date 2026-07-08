package enforcement

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- PATActorResolver ---

func newTestPATActorResolver(baseURL, token string) *PATActorResolver {
	r := NewPATActorResolver(token)
	r.baseURL = baseURL
	return r
}

func TestPATActorResolver_ResolveActor(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "chnu-kim"})
	}))
	defer srv.Close()

	r := newTestPATActorResolver(srv.URL, "test-pat")
	actor, err := r.ResolveActor(context.Background())
	if err != nil {
		t.Fatalf("ResolveActor: %v", err)
	}
	if actor != "chnu-kim" {
		t.Fatalf("actor = %q, want chnu-kim", actor)
	}
	if gotPath != "/user" {
		t.Fatalf("request path = %q, want /user", gotPath)
	}
	if gotAuth != "Bearer test-pat" {
		t.Fatalf("Authorization = %q, want Bearer test-pat", gotAuth)
	}
}

func TestPATActorResolver_ResolveActor_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	r := newTestPATActorResolver(srv.URL, "bad-token")
	if _, err := r.ResolveActor(context.Background()); err == nil {
		t.Fatal("non-200 /user response must return an error")
	}
}

func TestPATActorResolver_ResolveActor_EmptyLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()

	r := newTestPATActorResolver(srv.URL, "test-pat")
	if _, err := r.ResolveActor(context.Background()); err == nil {
		t.Fatal("empty login must return an error")
	}
}

// --- AppActorResolver ---

func newTestAppActorResolver(t *testing.T, baseURL string) *AppActorResolver {
	t.Helper()
	key := generateTestRSAKey(t)
	r, err := NewAppActorResolver("4244791", key)
	if err != nil {
		t.Fatalf("NewAppActorResolver: %v", err)
	}
	r.baseURL = baseURL
	return r
}

func TestAppActorResolver_ResolveActor_Success(t *testing.T) {
	var gotPath, gotAuth, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":   4244791,
			"slug": "mechanu",
			"name": "Mechanu",
		})
	}))
	defer srv.Close()

	r := newTestAppActorResolver(t, srv.URL)
	actor, err := r.ResolveActor(context.Background())
	if err != nil {
		t.Fatalf("ResolveActor: %v", err)
	}
	if actor != "mechanu[bot]" {
		t.Fatalf("actor = %q, want mechanu[bot]", actor)
	}
	if gotPath != "/app" {
		t.Fatalf("request path = %q, want /app", gotPath)
	}
	if !strings.HasPrefix(gotAuth, "Bearer ") {
		t.Fatalf("Authorization = %q, want Bearer <jwt>", gotAuth)
	}
	if parts := strings.Split(strings.TrimPrefix(gotAuth, "Bearer "), "."); len(parts) != 3 {
		t.Fatalf("Authorization JWT has %d parts, want 3", len(parts))
	}
	if gotAccept != "application/vnd.github+json" {
		t.Fatalf("Accept = %q, want application/vnd.github+json", gotAccept)
	}
}

func TestAppActorResolver_ResolveActor_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	r := newTestAppActorResolver(t, srv.URL)
	if _, err := r.ResolveActor(context.Background()); err == nil {
		t.Fatal("non-200 /app response must return an error")
	}
}

func TestAppActorResolver_ResolveActor_MissingSlug(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 4244791})
	}))
	defer srv.Close()

	r := newTestAppActorResolver(t, srv.URL)
	if _, err := r.ResolveActor(context.Background()); err == nil {
		t.Fatal("missing slug must return an error")
	}
}

func TestNewAppActorResolver_NilKeyFailsClosed(t *testing.T) {
	if _, err := NewAppActorResolver("4244791", nil); err == nil {
		t.Fatal("NewAppActorResolver with nil key must return an error")
	}
}

func TestNewAppActorResolverFromPEM_PKCS1(t *testing.T) {
	key := generateTestRSAKey(t)
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if _, err := NewAppActorResolverFromPEM("4244791", pemBytes); err != nil {
		t.Fatalf("NewAppActorResolverFromPEM (PKCS1): %v", err)
	}
}

func TestNewAppActorResolverFromPEM_PKCS8(t *testing.T) {
	key := generateTestRSAKey(t)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if _, err := NewAppActorResolverFromPEM("4244791", pemBytes); err != nil {
		t.Fatalf("NewAppActorResolverFromPEM (PKCS8): %v", err)
	}
}

func TestNewAppActorResolverFromPEM_InvalidPEMFailsClosed(t *testing.T) {
	if _, err := NewAppActorResolverFromPEM("4244791", []byte("not a pem file")); err == nil {
		t.Fatal("invalid PEM must return an error")
	}
}

// --- CheckIdentity ---

// fakeActorResolver is a stub ActorResolver for exercising CheckIdentity
// without a network round trip.
type fakeActorResolver struct {
	actor string
	err   error
}

func (f fakeActorResolver) ResolveActor(context.Context) (string, error) {
	return f.actor, f.err
}

func TestCheckIdentity_ActorMatchesExpected(t *testing.T) {
	got := CheckIdentity(context.Background(), fakeActorResolver{actor: "mechanu[bot]"}, "mechanu[bot]")
	if !got.Satisfied {
		t.Fatalf("CheckIdentity() = %+v, want Satisfied=true", got)
	}
	if got.Name != CheckNameIdentity {
		t.Fatalf("Name = %q, want %q", got.Name, CheckNameIdentity)
	}
}

func TestCheckIdentity_ActorIsStillHuman(t *testing.T) {
	got := CheckIdentity(context.Background(), fakeActorResolver{actor: "chnu-kim"}, "mechanu[bot]")
	if got.Satisfied {
		t.Fatal("actor still chnu-kim must not satisfy the identity check")
	}
	if !strings.Contains(got.Reason, "chnu-kim") {
		t.Fatalf("Reason = %q, want it to mention the observed actor chnu-kim", got.Reason)
	}
}

func TestCheckIdentity_ResolverError(t *testing.T) {
	got := CheckIdentity(context.Background(), fakeActorResolver{err: errors.New("boom")}, "mechanu[bot]")
	if got.Satisfied {
		t.Fatal("resolver error must fail-closed, not satisfy the check")
	}
	if got.Reason == "" {
		t.Fatal("unmet result must carry a reason")
	}
}

func TestCheckIdentity_NilResolverFailsClosed(t *testing.T) {
	got := CheckIdentity(context.Background(), nil, "mechanu[bot]")
	if got.Satisfied {
		t.Fatal("nil resolver must fail-closed, not satisfy the check")
	}
}

func TestWithdrawnActorResolver_AlwaysFailsClosed(t *testing.T) {
	// ADR-0011 point 10 withdrew App-key possession (GET /app) as identity
	// evidence — holding the key proves nothing about which identity authors
	// PRs (semantic false positive, empirically demonstrated: the probe passed
	// while every loop PR was still authored by chnu-kim). Until the c-1/c-2
	// redefinition lands, check (c) must fail-closed no matter what
	// credentials are configured (codex adversarial-review finding, PR #45).
	got := CheckIdentity(context.Background(), WithdrawnActorResolver{}, "mechanu[bot]")
	if got.Satisfied {
		t.Fatal("withdrawn resolver must fail-closed, not satisfy the check")
	}
	if !strings.Contains(got.Reason, "ADR-0011") {
		t.Fatalf("Reason = %q, want it to cite ADR-0011 so an operator reading the log knows why", got.Reason)
	}
}
