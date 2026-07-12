package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestLoad_RequiresCredentials(t *testing.T) {
	t.Setenv("TOSS_CLIENT_ID", "")
	t.Setenv("TOSS_CLIENT_SECRET", "")

	if _, err := Load(); err == nil {
		t.Fatal("expected error when credentials are missing, got nil")
	}
}

func TestLoad_DefaultsBaseURL(t *testing.T) {
	t.Setenv("TOSS_CLIENT_ID", "id")
	t.Setenv("TOSS_CLIENT_SECRET", "secret")
	t.Setenv("TOSS_BASE_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.BaseURL != DefaultBaseURL {
		t.Fatalf("BaseURL = %q, want default %q", cfg.BaseURL, DefaultBaseURL)
	}
}

func TestLoad_OverridesBaseURL(t *testing.T) {
	t.Setenv("TOSS_CLIENT_ID", "id")
	t.Setenv("TOSS_CLIENT_SECRET", "secret")
	t.Setenv("TOSS_BASE_URL", "https://example.test")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.BaseURL != "https://example.test" {
		t.Fatalf("BaseURL = %q, want override", cfg.BaseURL)
	}
}

// --- M-4: base URL scheme validation at boot ---

func TestLoad_RejectsPlainHTTPBaseURL(t *testing.T) {
	t.Setenv("TOSS_CLIENT_ID", "id")
	t.Setenv("TOSS_CLIENT_SECRET", "secret")
	t.Setenv("TOSS_BASE_URL", "http://openapi.tossinvest.com")

	if _, err := Load(); err == nil {
		t.Fatal("expected error for plain-http base URL (credentials would travel unencrypted), got nil")
	}
}

func TestLoad_RejectsSchemelessBaseURL(t *testing.T) {
	t.Setenv("TOSS_CLIENT_ID", "id")
	t.Setenv("TOSS_CLIENT_SECRET", "secret")
	t.Setenv("TOSS_BASE_URL", "openapi.tossinvest.com")

	if _, err := Load(); err == nil {
		t.Fatal("expected error for scheme-less base URL, got nil")
	}
}

func TestLoad_AllowsLoopbackHTTPBaseURL(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:8080", "http://localhost:8080"} {
		t.Setenv("TOSS_CLIENT_ID", "id")
		t.Setenv("TOSS_CLIENT_SECRET", "secret")
		t.Setenv("TOSS_BASE_URL", raw)

		if _, err := Load(); err != nil {
			t.Fatalf("Load() with %q = %v, want nil (loopback http is the test escape hatch)", raw, err)
		}
	}
}

// --- L-9: client_secret must never appear in logs or formatted output ---

const rawSecret = "raw-test-secret-do-not-log"

func maskedConfig() Config {
	return Config{
		BaseURL:      DefaultBaseURL,
		ClientID:     "client-abc",
		ClientSecret: Secret(rawSecret),
		AccountSeq:   "7",
	}
}

func TestSecret_RevealReturnsRawValue(t *testing.T) {
	if got := Secret(rawSecret).Reveal(); got != rawSecret {
		t.Fatalf("Reveal() = %q, want raw value", got)
	}
}

func TestSecret_FormattingIsRedacted(t *testing.T) {
	s := Secret(rawSecret)
	for _, out := range []string{
		fmt.Sprint(s),
		fmt.Sprintf("%v", s),
		fmt.Sprintf("%+v", s),
		fmt.Sprintf("%#v", s),
		fmt.Sprintf("%s", s),
		fmt.Sprintf("%q", s),
	} {
		if strings.Contains(out, rawSecret) {
			t.Fatalf("formatted secret %q leaks the raw value", out)
		}
	}
	if fmt.Sprint(s) != "[REDACTED]" {
		t.Fatalf("fmt.Sprint(secret) = %q, want [REDACTED]", fmt.Sprint(s))
	}
	// An empty secret renders empty so logs can distinguish set vs unset
	// without ever exposing a value.
	if fmt.Sprint(Secret("")) != "" {
		t.Fatalf("empty secret renders %q, want empty", fmt.Sprint(Secret("")))
	}
}

// TestSecret_MismatchedVerbsAreRedacted guards fmt's bad-verb fallback: for a
// verb the type does not support (e.g. %d on a string kind), fmt prints
// "%!d(config.Secret=<raw>)" and, because it sets its internal erroring flag,
// bypasses String()/GoString() entirely. Only fmt.Formatter covers every verb.
func TestSecret_MismatchedVerbsAreRedacted(t *testing.T) {
	s := Secret(rawSecret)
	// The verbs are table-driven precisely because vet's printf check (rightly)
	// rejects a constant mismatched format — but a runtime mistake or a format
	// string built dynamically still reaches this path.
	for _, verb := range []string{"%d", "%t", "%f", "%c", "%x"} {
		if out := fmt.Sprintf(verb, s); strings.Contains(out, rawSecret) {
			t.Fatalf("mismatched verb %s output %q leaks the raw secret", verb, out)
		}
		// Struct fields hit the same bad-verb fallback.
		if out := fmt.Sprintf(verb, maskedConfig()); strings.Contains(out, rawSecret) {
			t.Fatalf("mismatched verb %s on Config output %q leaks the raw secret", verb, out)
		}
	}
}

func TestConfig_FormattingIsRedacted(t *testing.T) {
	cfg := maskedConfig()
	err := fmt.Errorf("boot failed with cfg %+v", cfg)
	for _, out := range []string{
		fmt.Sprintf("%v", cfg),
		fmt.Sprintf("%+v", cfg),
		fmt.Sprintf("%#v", cfg),
		fmt.Sprintf("%s", cfg),
		err.Error(),
	} {
		if strings.Contains(out, rawSecret) {
			t.Fatalf("formatted config %q leaks client_secret", out)
		}
	}
}

func TestConfig_SlogOutputIsRedacted(t *testing.T) {
	cfg := maskedConfig()

	var jsonBuf, textBuf bytes.Buffer
	slog.New(slog.NewJSONHandler(&jsonBuf, nil)).Info("boot", "cfg", cfg, "secret", cfg.ClientSecret)
	slog.New(slog.NewTextHandler(&textBuf, nil)).Info("boot", "cfg", cfg, "secret", cfg.ClientSecret)

	for _, out := range []string{jsonBuf.String(), textBuf.String()} {
		if strings.Contains(out, rawSecret) {
			t.Fatalf("slog output %q leaks client_secret", out)
		}
		if !strings.Contains(out, "[REDACTED]") {
			t.Fatalf("slog output %q should carry the redaction marker", out)
		}
	}
}

func TestConfig_JSONMarshalIsRedacted(t *testing.T) {
	b, err := json.Marshal(maskedConfig())
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if strings.Contains(string(b), rawSecret) {
		t.Fatalf("json.Marshal(config) = %s leaks client_secret", b)
	}
}

func TestLoad_ClientSecretIsSecretType(t *testing.T) {
	t.Setenv("TOSS_CLIENT_ID", "id")
	t.Setenv("TOSS_CLIENT_SECRET", rawSecret)
	t.Setenv("TOSS_BASE_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ClientSecret.Reveal() != rawSecret {
		t.Fatalf("ClientSecret.Reveal() = %q, want the injected value", cfg.ClientSecret.Reveal())
	}
}
