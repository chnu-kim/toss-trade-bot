package config

import "testing"

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
