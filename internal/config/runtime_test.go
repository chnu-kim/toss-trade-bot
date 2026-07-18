package config

import (
	"strings"
	"testing"
	"time"
)

// These cover the assembly settings cmd/bot needs beyond credentials: where the
// durable state lives and the escalation thresholds the kill switch and
// reconciler refuse to default silently.

func withCredentials(t *testing.T) {
	t.Helper()
	t.Setenv("TOSS_CLIENT_ID", "id")
	t.Setenv("TOSS_CLIENT_SECRET", "secret")
}

func TestLoad_DefaultsDurablePaths(t *testing.T) {
	withCredentials(t)
	t.Setenv("TOSS_BOT_STORE_PATH", "")
	t.Setenv("TOSS_BOT_AUDIT_DIR", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StorePath != DefaultStorePath {
		t.Fatalf("StorePath = %q, want %q", cfg.StorePath, DefaultStorePath)
	}
	if cfg.AuditDir != DefaultAuditDir {
		t.Fatalf("AuditDir = %q, want %q", cfg.AuditDir, DefaultAuditDir)
	}
}

func TestLoad_OverridesDurablePaths(t *testing.T) {
	withCredentials(t)
	t.Setenv("TOSS_BOT_STORE_PATH", "/srv/bot/state.db")
	t.Setenv("TOSS_BOT_AUDIT_DIR", "/srv/bot/audit")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StorePath != "/srv/bot/state.db" {
		t.Fatalf("StorePath = %q", cfg.StorePath)
	}
	if cfg.AuditDir != "/srv/bot/audit" {
		t.Fatalf("AuditDir = %q", cfg.AuditDir)
	}
}

func TestLoad_DefaultsEscalationKnobs(t *testing.T) {
	withCredentials(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OrderFailureThreshold != DefaultOrderFailureThreshold {
		t.Fatalf("OrderFailureThreshold = %d", cfg.OrderFailureThreshold)
	}
	if cfg.TokenRefreshThreshold != DefaultTokenRefreshThreshold {
		t.Fatalf("TokenRefreshThreshold = %d", cfg.TokenRefreshThreshold)
	}
	if cfg.TokenRefreshWindow != DefaultTokenRefreshWindow {
		t.Fatalf("TokenRefreshWindow = %s", cfg.TokenRefreshWindow)
	}
	if cfg.AmbiguousBacklogThreshold != DefaultAmbiguousBacklogThreshold {
		t.Fatalf("AmbiguousBacklogThreshold = %d", cfg.AmbiguousBacklogThreshold)
	}
	if cfg.SettleWindow != DefaultSettleWindow {
		t.Fatalf("SettleWindow = %s", cfg.SettleWindow)
	}
	if cfg.ReevalInterval != DefaultReevalInterval {
		t.Fatalf("ReevalInterval = %s", cfg.ReevalInterval)
	}
	if cfg.ShutdownTimeout != DefaultShutdownTimeout {
		t.Fatalf("ShutdownTimeout = %s", cfg.ShutdownTimeout)
	}
}

func TestLoad_OverridesEscalationKnobs(t *testing.T) {
	withCredentials(t)
	t.Setenv("TOSS_BOT_ORDER_FAILURE_THRESHOLD", "7")
	t.Setenv("TOSS_BOT_TOKEN_REFRESH_WINDOW", "90s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OrderFailureThreshold != 7 {
		t.Fatalf("OrderFailureThreshold = %d, want 7", cfg.OrderFailureThreshold)
	}
	if cfg.TokenRefreshWindow != 90*time.Second {
		t.Fatalf("TokenRefreshWindow = %s, want 90s", cfg.TokenRefreshWindow)
	}
}

// TestLoad_RejectsNonPositiveThresholds is the fail-closed rule: a zero or
// negative threshold means "never escalates", which silently disables the kill
// switch. A typo in a deployment env var must stop the process at boot, not
// disarm the money guard (the twin of killswitch/reconciler Config.validate).
func TestLoad_RejectsNonPositiveThresholds(t *testing.T) {
	vars := []string{
		"TOSS_BOT_ORDER_FAILURE_THRESHOLD",
		"TOSS_BOT_TOKEN_REFRESH_THRESHOLD",
		"TOSS_BOT_TOKEN_REFRESH_WINDOW",
		"TOSS_BOT_AMBIGUOUS_BACKLOG_THRESHOLD",
		"TOSS_BOT_SETTLE_WINDOW",
		"TOSS_BOT_REEVAL_INTERVAL",
		"TOSS_BOT_SHUTDOWN_TIMEOUT",
	}
	for _, v := range vars {
		for _, bad := range []string{"0", "-1"} {
			t.Run(v+"="+bad, func(t *testing.T) {
				withCredentials(t)
				value := bad
				if strings.Contains(v, "WINDOW") || strings.Contains(v, "INTERVAL") || strings.Contains(v, "TIMEOUT") {
					value = bad + "s"
				}
				t.Setenv(v, value)
				if _, err := Load(); err == nil {
					t.Fatalf("%s=%s must be rejected, got nil error", v, value)
				}
			})
		}
	}
}

func TestLoad_RejectsUnparseableKnobs(t *testing.T) {
	withCredentials(t)
	t.Setenv("TOSS_BOT_ORDER_FAILURE_THRESHOLD", "three")

	if _, err := Load(); err == nil {
		t.Fatal("an unparseable threshold must be rejected")
	}
}

func TestLoad_RejectsUnparseableDuration(t *testing.T) {
	withCredentials(t)
	t.Setenv("TOSS_BOT_SETTLE_WINDOW", "30 seconds")

	if _, err := Load(); err == nil {
		t.Fatal("an unparseable duration must be rejected")
	}
}

// TestLoad_ParsesAccountSeq: the reconciler and the order API take a numeric
// accountSeq, while the HTTP header carries the raw string. Both come from one
// env var, so the parse belongs here rather than at each call site.
func TestLoad_ParsesAccountSeq(t *testing.T) {
	withCredentials(t)
	t.Setenv("TOSS_ACCOUNT_SEQ", "12345")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AccountSeq != "12345" {
		t.Fatalf("AccountSeq = %q", cfg.AccountSeq)
	}
	if cfg.AccountSeqNum != 12345 {
		t.Fatalf("AccountSeqNum = %d, want 12345", cfg.AccountSeqNum)
	}
}

// TestLoad_RejectsNonNumericAccountSeq: a malformed accountSeq would otherwise
// silently become 0 and every account-scoped call would target the wrong (or no)
// account.
func TestLoad_RejectsNonNumericAccountSeq(t *testing.T) {
	withCredentials(t)
	t.Setenv("TOSS_ACCOUNT_SEQ", "acct-1")

	if _, err := Load(); err == nil {
		t.Fatal("a non-numeric accountSeq must be rejected")
	}
}

func TestLoad_AllowsAbsentAccountSeq(t *testing.T) {
	withCredentials(t)
	t.Setenv("TOSS_ACCOUNT_SEQ", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AccountSeqNum != 0 {
		t.Fatalf("AccountSeqNum = %d, want 0", cfg.AccountSeqNum)
	}
}
