package reconciler

import (
	"strings"
	"testing"
	"time"
)

// validConfig is a structurally complete Config; each subtest breaks exactly one
// field so the assertion is about that field alone.
func validConfig(t *testing.T) Config {
	t.Helper()
	db, _ := openStore(t)
	return Config{
		Journal:                   db,
		Guard:                     newSwitch(t, db, defaultKillswitchConfig()),
		API:                       newFakeAPI(),
		Audit:                     openAudit(t),
		AccountSeq:                testAccountSeq,
		AmbiguousBacklogThreshold: 3,
		SettleWindow:              30 * time.Second,
		ReevalInterval:            time.Second,
		Logger:                    discardLogger(),
	}
}

// TestNew_ZeroThresholdIsFailClosed is ADR-0014 Consequence (g) and the twin of
// killswitch's own Config.validate: a zero (or negative) escalation threshold is a
// configuration that cannot express the intended escalation, so it is refused at
// construction rather than degrading into a reconciler that never escalates.
func TestNew_ZeroThresholdIsFailClosed(t *testing.T) {
	for _, threshold := range []int{0, -1} {
		cfg := validConfig(t)
		cfg.AmbiguousBacklogThreshold = threshold
		if _, err := New(cfg); err == nil {
			t.Fatalf("New accepted AmbiguousBacklogThreshold=%d; a threshold that never escalates is a fail-open", threshold)
		} else if !strings.Contains(err.Error(), "AmbiguousBacklogThreshold") {
			t.Fatalf("error should name the offending field, got %v", err)
		}
	}
}

func TestNew_RejectsNonPositiveWindows(t *testing.T) {
	t.Run("settle window", func(t *testing.T) {
		cfg := validConfig(t)
		cfg.SettleWindow = 0
		if _, err := New(cfg); err == nil {
			t.Fatal("New accepted a zero SettleWindow")
		}
	})
	t.Run("reeval interval", func(t *testing.T) {
		cfg := validConfig(t)
		cfg.ReevalInterval = 0
		if _, err := New(cfg); err == nil {
			t.Fatal("New accepted a zero ReevalInterval; the loop that bounds every delayed-halt window would spin or die")
		}
	})
}

func TestNew_RejectsMissingDependencies(t *testing.T) {
	tests := map[string]func(*Config){
		"journal": func(c *Config) { c.Journal = nil },
		"guard":   func(c *Config) { c.Guard = nil },
		"api":     func(c *Config) { c.API = nil },
		"audit":   func(c *Config) { c.Audit = nil },
	}
	for name, break_ := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig(t)
			break_(&cfg)
			if _, err := New(cfg); err == nil {
				t.Fatalf("New accepted a nil %s", name)
			}
		})
	}
}

// TestNew_PreparedAbandonWindowIsFloored pins the money-safety floor: a short
// settle window may declare ambiguity quickly (fail-closed direction) but must
// never shorten how long a prepared-only intent is left alone, because a live
// submitter can legitimately sit between its prepared and submit-attempted
// commits for tens of seconds.
func TestNew_PreparedAbandonWindowIsFloored(t *testing.T) {
	cfg := validConfig(t)
	cfg.SettleWindow = time.Second
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if r.settleWindow != time.Second {
		t.Fatalf("settleWindow = %s, want it left as configured", r.settleWindow)
	}
	if r.preparedAbandonAfter < minPreparedAbandonWindow {
		t.Fatalf("preparedAbandonAfter = %s, want at least %s", r.preparedAbandonAfter, minPreparedAbandonWindow)
	}

	cfg.SettleWindow = 10 * time.Minute
	r, err = New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if r.preparedAbandonAfter != 10*time.Minute {
		t.Fatalf("preparedAbandonAfter = %s, want the (larger) configured settle window", r.preparedAbandonAfter)
	}
}
