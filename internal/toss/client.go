// Package toss is the HTTP client for the Toss Open API.
//
// Auth: OAuth 2.0 Client Credentials Grant. Only one token is valid per client
// at a time, so token issuance/caching/refresh MUST be centralized here — never
// let multiple goroutines or processes mint their own tokens.
package toss

import (
	"net/http"
	"time"
)

// Client talks to the Toss Open API. It owns the single OAuth token for the
// client credentials and reuses it until expiry.
type Client struct {
	baseURL      string
	clientID     string
	clientSecret string
	http         *http.Client
}

// NewClient constructs a Client with a sane default HTTP timeout.
func NewClient(baseURL, clientID, clientSecret string) *Client {
	return &Client{
		baseURL:      baseURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		http:         &http.Client{Timeout: 10 * time.Second},
	}
}
