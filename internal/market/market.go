package market

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/chnu-kim/toss-trade-bot/internal/toss"
)

// getter is the minimal slice of *toss.Client this package needs: an
// authenticated GET whose response body the caller must close. Depending on the
// interface (not the concrete client) keeps the wrapper trivially testable with
// an httptest-backed *toss.Client or a stub.
type getter interface {
	Get(ctx context.Context, path string, opts ...toss.RequestOption) (*http.Response, error)
}

// Client reads market data (prices, orderbook) and trading-calendar info over
// the Toss Open API. None of these calls are account-scoped.
type Client struct {
	api getter
}

// NewClient wraps an authenticated getter (typically *toss.Client).
func NewClient(api getter) *Client {
	return &Client{api: api}
}

// Price is the latest price for one symbol. LastPrice is a decimal string to
// preserve precision. Timestamp is nil when no trade has set a time yet.
type Price struct {
	Symbol    string  `json:"symbol"`
	Timestamp *string `json:"timestamp"`
	LastPrice string  `json:"lastPrice"`
	Currency  string  `json:"currency"`
}

// OrderbookEntry is one price level and its resting volume, both decimal
// strings.
type OrderbookEntry struct {
	Price  string `json:"price"`
	Volume string `json:"volume"`
}

// Orderbook is the bid/ask ladder for a symbol. Asks ascend by price, bids
// descend. Timestamp is nil when no data time is available.
type Orderbook struct {
	Timestamp *string          `json:"timestamp"`
	Currency  string           `json:"currency"`
	Asks      []OrderbookEntry `json:"asks"`
	Bids      []OrderbookEntry `json:"bids"`
}

// Prices returns the latest price for up to 200 symbols
// (GET /api/v1/prices). At least one symbol is required.
func (c *Client) Prices(ctx context.Context, symbols ...string) ([]Price, error) {
	if len(symbols) == 0 {
		return nil, errors.New("market: Prices requires at least one symbol")
	}
	q := url.Values{"symbols": {strings.Join(symbols, ",")}}
	return fetch[[]Price](ctx, c.api, "/api/v1/prices?"+q.Encode())
}

// Orderbook returns the bid/ask ladder for one symbol
// (GET /api/v1/orderbook).
func (c *Client) Orderbook(ctx context.Context, symbol string) (Orderbook, error) {
	if symbol == "" {
		return Orderbook{}, errors.New("market: Orderbook requires a symbol")
	}
	q := url.Values{"symbol": {symbol}}
	return fetch[Orderbook](ctx, c.api, "/api/v1/orderbook?"+q.Encode())
}

// fetch performs an authenticated GET and decodes the {"result": T} success
// envelope. It always closes the response body, and turns a non-200 into a
// clear error carrying the status and the API error code/message.
func fetch[T any](ctx context.Context, api getter, path string, opts ...toss.RequestOption) (T, error) {
	var zero T
	resp, err := api.Get(ctx, path, opts...)
	if err != nil {
		return zero, err
	}
	defer drainClose(resp)

	if resp.StatusCode != http.StatusOK {
		return zero, decodeError(resp)
	}

	var env struct {
		Result T `json:"result"`
	}
	// toss.DecodeJSON caps how many bytes are decoded so an oversized body
	// cannot OOM the unattended process.
	if err := toss.DecodeJSON(resp.Body, &env); err != nil {
		return zero, fmt.Errorf("market: decode %s: %w", path, err)
	}
	return env.Result, nil
}

// decodeError reads the {"error":{code,message}} envelope for a non-200 and
// returns a clear error. It never fails on a malformed body — the status code
// alone is enough to surface the failure.
func decodeError(resp *http.Response) error {
	var er struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = toss.DecodeJSON(resp.Body, &er)
	if er.Error.Code != "" {
		return fmt.Errorf("market: request failed: status %d: %s: %s", resp.StatusCode, er.Error.Code, er.Error.Message)
	}
	return fmt.Errorf("market: request failed: status %d", resp.StatusCode)
}

// drainClose drains and closes a response body so the connection can be reused.
func drainClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
