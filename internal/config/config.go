// Package config loads runtime configuration from the environment.
//
// Secrets (client_id/secret) are injected via environment variables and never
// committed; see configs/env.example for the expected variables.
package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/chnu-kim/toss-trade-bot/internal/toss"
)

// DefaultBaseURL is the Toss Open API base URL.
const DefaultBaseURL = "https://openapi.tossinvest.com"

// redactedMarker replaces secret values on every formatting/serialization
// surface. Leaking client_secret is equivalent to handing over the brokerage
// account, so redaction is structural (a type), not a per-call convention.
const redactedMarker = "[REDACTED]"

// Secret is a credential value that must never appear in logs, error strings,
// or serialized output. Every standard formatting and serialization surface
// (fmt verbs, slog, encoding/json, encoding.TextMarshaler) emits a redaction
// marker instead of the value; call Reveal at the single point of use (client
// wiring) to obtain the raw string. An empty secret renders empty so logs can
// distinguish set vs unset without exposing anything.
type Secret string

func (s Secret) redacted() string {
	if s == "" {
		return ""
	}
	return redactedMarker
}

// Format implements fmt.Formatter, which covers EVERY verb — including
// mismatched ones. Without it, a bad verb (e.g. %d on this string kind) makes
// fmt fall back to its error format "%!d(config.Secret=<raw>)", and that path
// sets fmt's internal erroring flag, bypassing String()/GoString() entirely.
func (s Secret) Format(f fmt.State, verb rune) {
	if verb == 'q' {
		fmt.Fprintf(f, "%q", s.redacted())
		return
	}
	fmt.Fprint(f, s.redacted())
}

// String implements fmt.Stringer for direct .String() calls and non-fmt
// consumers (fmt itself resolves Format first).
func (s Secret) String() string { return s.redacted() }

// GoString implements fmt.GoStringer as belt-and-suspenders for %#v.
func (s Secret) GoString() string { return s.redacted() }

// LogValue implements slog.LogValuer, so slog never records the raw value.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.redacted()) }

// MarshalJSON keeps json.Marshal (including slog's JSON handler encoding
// structs that embed a Secret) from serializing the raw value.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(s.redacted()) }

// MarshalText keeps text-based encoders from serializing the raw value.
func (s Secret) MarshalText() ([]byte, error) { return []byte(s.redacted()), nil }

// Reveal returns the raw secret. Call it only where the value leaves the
// process on purpose (e.g. toss client construction) — never in logging.
func (s Secret) Reveal() string { return string(s) }

// Config holds all runtime settings for the bot.
type Config struct {
	BaseURL      string
	ClientID     string
	ClientSecret Secret
	AccountSeq   string // X-Tossinvest-Account header value, from /api/v1/accounts
}

// LogValue implements slog.LogValuer so logging a whole Config (a one-line
// mistake away in every new call site) emits the redacted secret.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("base_url", c.BaseURL),
		slog.String("client_id", c.ClientID),
		slog.Any("client_secret", c.ClientSecret),
		slog.String("account_seq", c.AccountSeq),
	)
}

// Load reads configuration from the environment, applying defaults. It returns
// an error if required credentials are missing or the base URL would send
// credentials over plain http, so the process fails fast at startup rather
// than mid-trade.
func Load() (Config, error) {
	cfg := Config{
		BaseURL:      getenv("TOSS_BASE_URL", DefaultBaseURL),
		ClientID:     os.Getenv("TOSS_CLIENT_ID"),
		ClientSecret: Secret(os.Getenv("TOSS_CLIENT_SECRET")),
		AccountSeq:   os.Getenv("TOSS_ACCOUNT_SEQ"),
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return Config{}, fmt.Errorf("config: TOSS_CLIENT_ID and TOSS_CLIENT_SECRET are required")
	}
	if err := toss.ValidateBaseURL(cfg.BaseURL); err != nil {
		return Config{}, fmt.Errorf("config: TOSS_BASE_URL: %w", err)
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
