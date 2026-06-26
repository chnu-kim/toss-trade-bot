// Package order manages the order lifecycle: placement, modification, and
// cancellation against the Toss Order API.
//
// Unattended-safety rules:
//   - On startup, reconstruct open orders/positions by querying the API; do not
//     trust local state across restarts.
//   - Never auto-retry order submission (duplicate-fill risk). Re-check order
//     state via the API before any resubmission.
package order
