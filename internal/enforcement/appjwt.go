package enforcement

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"time"
)

// jwtClockDriftBuffer backdates iat per GitHub's guidance ("set this 60
// seconds in the past to protect against clock drift").
const jwtClockDriftBuffer = 60 * time.Second

// jwtLifetime is how long the JWT is valid for. GitHub caps this at 10
// minutes; we use less than the ceiling to leave margin.
const jwtLifetime = 9 * time.Minute

// signAppJWT builds and signs a GitHub App JWT (RS256) per GitHub's
// "Generating a JSON Web Token" spec: iss is the App ID, iat is backdated by
// jwtClockDriftBuffer, exp is bounded by GitHub's 10-minute ceiling. This JWT
// is only valid for App-level endpoints (GET /app, installation-token minting)
// — it is deliberately not an installation access token, which would require
// an extra network round trip this check doesn't need.
func signAppJWT(appID string, key *rsa.PrivateKey, now time.Time) (string, error) {
	if key == nil {
		return "", errors.New("enforcement: signAppJWT: private key is nil")
	}
	if appID == "" {
		return "", errors.New("enforcement: signAppJWT: appID is empty")
	}

	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	payload := map[string]any{
		"iat": now.Add(-jwtClockDriftBuffer).Unix(),
		"exp": now.Add(jwtLifetime).Unix(),
		"iss": appID,
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("enforcement: marshal JWT header: %w", err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("enforcement: marshal JWT payload: %w", err)
	}

	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(payloadJSON)

	hashed := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hashed[:])
	if err != nil {
		return "", fmt.Errorf("enforcement: sign JWT: %w", err)
	}

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// parseRSAPrivateKeyPEM parses a GitHub App private key, accepting both the
// PKCS#1 ("BEGIN RSA PRIVATE KEY") and PKCS#8 ("BEGIN PRIVATE KEY") PEM forms
// GitHub is known to hand out.
func parseRSAPrivateKeyPEM(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("enforcement: no PEM block found in App private key")
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("enforcement: parse App private key (tried PKCS1 and PKCS8): %w", err)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("enforcement: App private key is not an RSA key")
	}
	return rsaKey, nil
}
