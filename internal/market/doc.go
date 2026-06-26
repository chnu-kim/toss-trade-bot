// Package market provides market data (orderbook, prices, candles) and trading
// calendar awareness. The loop runs 24/7, so callers must check trading-day and
// market-open status here before placing orders. WebSocket availability is
// unconfirmed; assume REST polling until verified against the OpenAPI spec.
package market
