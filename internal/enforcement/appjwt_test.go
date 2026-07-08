package enforcement

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func generateTestRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	return key
}

func TestSignAppJWT_ClaimsAndSignature(t *testing.T) {
	key := generateTestRSAKey(t)
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)

	token, err := signAppJWT("4244791", key, now)
	if err != nil {
		t.Fatalf("signAppJWT: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts, want 3 (header.payload.signature)", len(parts))
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if header.Alg != "RS256" {
		t.Fatalf("alg = %q, want RS256", header.Alg)
	}
	if header.Typ != "JWT" {
		t.Fatalf("typ = %q, want JWT", header.Typ)
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var payload struct {
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
	}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Iss != "4244791" {
		t.Fatalf("iss = %q, want 4244791", payload.Iss)
	}
	// iat must be set at most a few minutes in the past (clock-drift buffer),
	// never in the future.
	if payload.Iat > now.Unix() || now.Unix()-payload.Iat > 120 {
		t.Fatalf("iat = %d, want within 120s before now (%d)", payload.Iat, now.Unix())
	}
	// exp must be in the future and within GitHub's 10-minute ceiling from iat.
	if payload.Exp <= now.Unix() {
		t.Fatalf("exp = %d, want > now (%d)", payload.Exp, now.Unix())
	}
	if payload.Exp-payload.Iat > 600 {
		t.Fatalf("exp-iat = %ds, want <= 600s (GitHub's ceiling)", payload.Exp-payload.Iat)
	}

	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	hashed := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, hashed[:], sig); err != nil {
		t.Fatalf("signature does not verify against the signing key: %v", err)
	}
}

func TestSignAppJWT_NilKeyFailsClosed(t *testing.T) {
	if _, err := signAppJWT("4244791", nil, time.Now()); err == nil {
		t.Fatal("signAppJWT with nil key must return an error, not a token")
	}
}

func TestSignAppJWT_EmptyAppIDFailsClosed(t *testing.T) {
	key := generateTestRSAKey(t)
	if _, err := signAppJWT("", key, time.Now()); err == nil {
		t.Fatal("signAppJWT with empty appID must return an error, not a token")
	}
}
