// Package config loads runtime configuration from the environment.
//
// Secrets (client_id/secret) are injected via environment variables and never
// committed; see configs/env.example for the expected variables.
package config

import (
	"fmt"
	"os"
)

// DefaultBaseURL is the Toss Open API base URL.
const DefaultBaseURL = "https://openapi.tossinvest.com"

// Config holds all runtime settings for the bot.
type Config struct {
	BaseURL      string
	ClientID     string
	ClientSecret string
	AccountSeq   string // X-Tossinvest-Account header value, from /api/v1/accounts
}

// Load reads configuration from the environment, applying defaults. It returns
// an error if required credentials are missing so the process fails fast at
// startup rather than mid-trade.
func Load() (Config, error) {
	cfg := Config{
		BaseURL:      getenv("TOSS_BASE_URL", DefaultBaseURL),
		ClientID:     os.Getenv("TOSS_CLIENT_ID"),
		ClientSecret: os.Getenv("TOSS_CLIENT_SECRET"),
		AccountSeq:   os.Getenv("TOSS_ACCOUNT_SEQ"),
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return Config{}, fmt.Errorf("config: TOSS_CLIENT_ID and TOSS_CLIENT_SECRET are required")
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
