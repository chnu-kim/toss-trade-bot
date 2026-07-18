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
	"strconv"
	"time"

	"github.com/chnu-kim/toss-trade-bot/internal/toss"
)

// DefaultBaseURL is the Toss Open API base URL.
const DefaultBaseURL = "https://openapi.tossinvest.com"

// Durable-state locations. Both are relative by default so a checkout runs out
// of the box; production injects absolute paths.
const (
	// DefaultStorePath is the SQLite journal/halt/sentinel database.
	DefaultStorePath = "data/bot.db"
	// DefaultAuditDir is the append-only durable audit sink directory.
	DefaultAuditDir = "data/audit"
)

// Escalation and pacing defaults. They are conservative on purpose: each one,
// if it were allowed to be zero, would silently mean "never escalates".
const (
	// DefaultOrderFailureThreshold is the consecutive order-failure count that
	// trips the global halt (ADR-0012 Decision 3).
	DefaultOrderFailureThreshold = 3
	// DefaultTokenRefreshThreshold is the token-refresh failure count within
	// DefaultTokenRefreshWindow that trips the global halt (ADR-0004 point 7).
	DefaultTokenRefreshThreshold = 3
	// DefaultTokenRefreshWindow is the sliding window for the token counter.
	DefaultTokenRefreshWindow = 15 * time.Minute
	// DefaultAmbiguousBacklogThreshold is the unresolved-ambiguous backlog at
	// which the reconciler trips the global halt (ADR-0014 Decision 1.2).
	DefaultAmbiguousBacklogThreshold = 3
	// DefaultSettleWindow is how long a submit-attempted intent with no orderId
	// may stay in flight before it is declared ambiguous.
	DefaultSettleWindow = 30 * time.Second
	// DefaultReevalInterval is the reconciler's supervised re-evaluation cadence
	// (ADR-0014 Decision 11), which bounds the escalation windows in a quiet
	// market where no submit ever fires the wake seam.
	DefaultReevalInterval = time.Minute
	// DefaultShutdownTimeout bounds how long shutdown waits for supervised
	// goroutines to drain. A drain timeout is treated as an unclean exit.
	DefaultShutdownTimeout = 10 * time.Second
)

// redactedMarker replaces secret values on the formatting/serialization
// surfaces that redaction can reach. Leaking client_secret is equivalent to
// handing over the brokerage account, so redaction is structural (a type)
// rather than a per-call convention — on every verb fmt lets a method see (see
// the residual limit on Format).
const redactedMarker = "[REDACTED]"

// Secret is a credential value that must never appear in logs, error strings,
// or serialized output. The standard formatting and serialization surfaces
// (fmt verbs, slog, encoding/json, encoding.TextMarshaler) emit a redaction
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

// Format implements fmt.Formatter so that every verb fmt routes through a
// method — %v/%+v/%s/%q and mismatched verbs like %d — is redacted. Without it,
// a bad verb (e.g. %d on this string kind) hits fmt's error format
// "%!d(config.Secret=<raw>)", whose internal erroring flag bypasses
// String()/GoString().
//
// Residual limit (honest, not "every verb"): fmt special-cases %p and %T BEFORE
// consulting any Formatter, so they escape this method. %T prints only the type
// name (no value — safe), but %p on a Secret reflects the underlying string and
// leaks it ("%!p(config.Secret=<raw>)"). %p is not a meaningful verb for a
// string value; callers must not format secrets with %p.
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
	// AccountSeqNum is AccountSeq parsed for the numeric API surfaces (order,
	// reconciler). Zero when AccountSeq is unset; the assembly rejects that.
	AccountSeqNum int64

	// StorePath is the SQLite database holding the journal, halt lifecycle and
	// clean-shutdown sentinel. AuditDir is the durable audit sink directory.
	StorePath string
	AuditDir  string

	// Kill-switch escalation thresholds (ADR-0004 point 7, ADR-0012).
	OrderFailureThreshold int
	TokenRefreshThreshold int
	TokenRefreshWindow    time.Duration

	// Reconciler escalation/pacing (ADR-0014).
	AmbiguousBacklogThreshold int
	SettleWindow              time.Duration
	ReevalInterval            time.Duration

	// ShutdownTimeout bounds the supervised drain on shutdown.
	ShutdownTimeout time.Duration
}

// LogValue implements slog.LogValuer so logging a whole Config (a one-line
// mistake away in every new call site) emits the redacted secret.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("base_url", c.BaseURL),
		slog.String("client_id", c.ClientID),
		slog.Any("client_secret", c.ClientSecret),
		slog.String("account_seq", c.AccountSeq),
		slog.String("store_path", c.StorePath),
		slog.String("audit_dir", c.AuditDir),
		slog.Int("order_failure_threshold", c.OrderFailureThreshold),
		slog.Int("token_refresh_threshold", c.TokenRefreshThreshold),
		slog.String("token_refresh_window", c.TokenRefreshWindow.String()),
		slog.Int("ambiguous_backlog_threshold", c.AmbiguousBacklogThreshold),
		slog.String("settle_window", c.SettleWindow.String()),
		slog.String("reeval_interval", c.ReevalInterval.String()),
		slog.String("shutdown_timeout", c.ShutdownTimeout.String()),
	)
}

// String implements fmt.Stringer so the plain formatting verbs render a
// redacted view instead of the raw struct. Without it, %v/%+v would print every
// field including the embedded Secret's own redaction — which is safe — but %s
// on a struct carrying non-string fields is not even a valid verb, so a caller
// reaching for it would get fmt's error format. Rendering explicitly keeps the
// value both printable and redacted on every verb.
func (c Config) String() string {
	return fmt.Sprintf("config.Config{base_url:%s client_id:%s client_secret:%s account_seq:%s "+
		"store_path:%s audit_dir:%s order_failure_threshold:%d token_refresh_threshold:%d "+
		"token_refresh_window:%s ambiguous_backlog_threshold:%d settle_window:%s "+
		"reeval_interval:%s shutdown_timeout:%s}",
		c.BaseURL, c.ClientID, c.ClientSecret.redacted(), c.AccountSeq,
		c.StorePath, c.AuditDir, c.OrderFailureThreshold, c.TokenRefreshThreshold,
		c.TokenRefreshWindow, c.AmbiguousBacklogThreshold, c.SettleWindow,
		c.ReevalInterval, c.ShutdownTimeout)
}

// GoString keeps %#v redacted too.
func (c Config) GoString() string { return c.String() }

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

	if cfg.AccountSeq != "" {
		seq, err := strconv.ParseInt(cfg.AccountSeq, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("config: TOSS_ACCOUNT_SEQ must be numeric, got %q", cfg.AccountSeq)
		}
		cfg.AccountSeqNum = seq
	}

	cfg.StorePath = getenv("TOSS_BOT_STORE_PATH", DefaultStorePath)
	cfg.AuditDir = getenv("TOSS_BOT_AUDIT_DIR", DefaultAuditDir)

	// Every knob below is validated positive rather than defaulted silently: a
	// zero threshold or window means "never escalates", so a deployment typo
	// would disarm the money guard instead of stopping the process (the twin of
	// killswitch/reconciler Config.validate — see ADR-0014's twin-artifact note).
	var err error
	if cfg.OrderFailureThreshold, err = positiveInt("TOSS_BOT_ORDER_FAILURE_THRESHOLD", DefaultOrderFailureThreshold); err != nil {
		return Config{}, err
	}
	if cfg.TokenRefreshThreshold, err = positiveInt("TOSS_BOT_TOKEN_REFRESH_THRESHOLD", DefaultTokenRefreshThreshold); err != nil {
		return Config{}, err
	}
	if cfg.AmbiguousBacklogThreshold, err = positiveInt("TOSS_BOT_AMBIGUOUS_BACKLOG_THRESHOLD", DefaultAmbiguousBacklogThreshold); err != nil {
		return Config{}, err
	}
	if cfg.TokenRefreshWindow, err = positiveDuration("TOSS_BOT_TOKEN_REFRESH_WINDOW", DefaultTokenRefreshWindow); err != nil {
		return Config{}, err
	}
	if cfg.SettleWindow, err = positiveDuration("TOSS_BOT_SETTLE_WINDOW", DefaultSettleWindow); err != nil {
		return Config{}, err
	}
	if cfg.ReevalInterval, err = positiveDuration("TOSS_BOT_REEVAL_INTERVAL", DefaultReevalInterval); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = positiveDuration("TOSS_BOT_SHUTDOWN_TIMEOUT", DefaultShutdownTimeout); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// positiveInt reads key as a positive integer, falling back to def when unset.
// A present-but-invalid value is an error, never a silent fallback.
func positiveInt(key string, def int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be an integer, got %q", key, raw)
	}
	if v <= 0 {
		return 0, fmt.Errorf("config: %s must be > 0 (a non-positive threshold would never escalate), got %d", key, v)
	}
	return v, nil
}

// positiveDuration reads key as a positive Go duration (e.g. "15m"), falling
// back to def when unset.
func positiveDuration(key string, def time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return def, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be a duration such as \"15m\", got %q", key, raw)
	}
	if v <= 0 {
		return 0, fmt.Errorf("config: %s must be > 0, got %s", key, v)
	}
	return v, nil
}
