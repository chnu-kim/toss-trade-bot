// Package order manages the order lifecycle against the Toss Order API.
//
// This package currently provides the typed API wrapper (api.go) that later
// layers build on:
//
//   - SubmitOrder — POST /api/v1/orders. Places an order and returns the
//     server-issued orderId. It issues exactly one write; a submission is never
//     auto-retried (a re-sent order could double-fill). Any retry decision, on a
//     re-checked state, belongs to the caller (#34).
//   - GetOrder — GET /api/v1/orders/{orderId}. Reads an order in any state (open
//     or closed). This is the primary post-submission truth source
//     (ADR-0002/0003); the primary key is orderId, not clientOrderId.
//
// Out of scope here (separate consumers): order cancel/modify wrappers, the
// submission sequence with its write-ahead journal and guards (#34), and the
// reconciler that recovers order truth on restart (#35).
//
// Encoding rules that matter for correctness:
//   - Monetary/quantity fields (price, quantity, orderAmount, and every
//     execution financial field) are raw decimal STRINGS, never floats, so the
//     exact value travels unchanged and an audit digest is precise (ADR-0006).
//   - OrderStatus (and the other enum types) preserve unknown codes instead of
//     failing the decode, so a status code Toss adds later cannot crash the
//     parser (ADR-0003).
//   - clientOrderId is an optional idempotency key: max 36 chars, charset
//     ^[a-zA-Z0-9\-_]+$, 10-minute window; see ValidateClientOrderID, the input
//     contract for #34's deterministic derivation (ADR-0002 point 4).
//
// Unattended-safety rules:
//   - On startup, reconstruct open orders/positions by querying the API; do not
//     trust local state across restarts.
//   - Never auto-retry order submission (duplicate-fill risk). Re-check order
//     state via the API before any resubmission.
package order
