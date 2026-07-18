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
// On top of the wrapper, submit.go implements the intent submit path (#34): a
// fail-closed, idempotent, 2-marker write-ahead sequence that turns a strategy
// Intent into at most one real POST.
//
//   - Submitter.SubmitIntent — the sequence: idempotent replay guard → kill-switch
//     CanSubmit (before prepared) → prepared (durable) + audit → TOCTOU Reconfirm
//     (before submit-attempted) → submit-attempted (durable) + audit → exactly one
//     POST → acked+orderId (durable) + audit, or leave unresolved and wake the
//     reconciler. Each marker is its own durable commit (ADR-0005 point 3); a
//     fail-closed audit write trips the global kill-switch (ADR-0006 point 6).
//   - DeriveClientOrderID — the deterministic intentId→clientOrderId map
//     (ADR-0002 point 4), reproducible from the intentId alone.
//
// order does not resolve order-failures or report them to the kill-switch — an
// ambiguous submit and an unresolved rejection are delegated to the reconciler
// (#35), which owns the count-first ordering (ADR-0012 Decision 3). The submit
// path's dependency seams are deliberately narrow so those couplings are
// structurally impossible (submit.go).
//
// Out of scope here (separate consumers): order cancel/modify wrappers, the
// reconciler that recovers order truth on restart (#35), and main wiring (#36).
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
