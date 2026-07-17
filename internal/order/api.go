package order

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/chnu-kim/toss-trade-bot/internal/toss"
)

// getter is the minimal slice of *toss.Client the order wrapper reads with: an
// authenticated GET whose response body the caller must close.
type getter interface {
	Get(ctx context.Context, path string, opts ...toss.RequestOption) (*http.Response, error)
}

// poster is the minimal slice of *toss.Client the order wrapper writes with: an
// authenticated POST whose response body the caller must close. The concrete
// *toss.Client never auto-retries a POST (a re-sent order could double-fill),
// so this wrapper makes exactly one write per SubmitOrder.
type poster interface {
	Post(ctx context.Context, path string, body io.Reader, opts ...toss.RequestOption) (*http.Response, error)
}

// tossAPI is the union the order Client depends on. Depending on the interface
// (not the concrete client) keeps the wrapper trivially testable with an
// httptest-backed *toss.Client or a stub.
type tossAPI interface {
	getter
	poster
}

// Client submits orders and reads order truth over the Toss Order API.
//
// It performs no retry of its own: SubmitOrder issues a single POST and returns
// the outcome or an error (retry of a write is forbidden — duplicate-fill
// risk; that policy lives with the caller, see #34). GetOrder issues a single
// GET; the underlying *toss.Client may back off and retry a read (a lookup is
// safe to repeat), but this wrapper adds no retry on top.
type Client struct {
	api tossAPI
}

// NewClient wraps an authenticated Toss API (typically *toss.Client).
func NewClient(api tossAPI) *Client {
	return &Client{api: api}
}

// Side is the order direction. Known values are SideBuy and SideSell; the type
// is a string so an unrecognized value from the API is preserved, not a decode
// failure.
type Side string

const (
	SideBuy  Side = "BUY"
	SideSell Side = "SELL"
)

// OrderType is the price type. Known values are OrderTypeLimit and
// OrderTypeMarket; unknown values are preserved on decode (the API documents
// that clients must tolerate unknown codes).
type OrderType string

const (
	OrderTypeLimit  OrderType = "LIMIT"
	OrderTypeMarket OrderType = "MARKET"
)

// TimeInForce is the order validity condition. Known values are DAY, CLS
// (at-the-close, US LIMIT only), and OPG (at-the-open, not currently
// supported). Unknown values are preserved on decode.
type TimeInForce string

const (
	TimeInForceDay TimeInForce = "DAY"
	TimeInForceCls TimeInForce = "CLS"
	TimeInForceOpg TimeInForce = "OPG"
)

// Currency is the trading currency. Known values are KRW and USD; unknown
// values are preserved on decode.
type Currency string

const (
	CurrencyKRW Currency = "KRW"
	CurrencyUSD Currency = "USD"
)

// OrderStatus is the lifecycle state of an order. All ten values Toss currently
// documents are declared below, but the type is a plain string so a status code
// Toss adds in the future is preserved verbatim rather than failing the decode
// (ADR-0003: unknown codes must not break the parser). Use IsKnown to tell a
// recognized code from a novel one.
type OrderStatus string

const (
	OrderStatusPending         OrderStatus = "PENDING"
	OrderStatusPendingCancel   OrderStatus = "PENDING_CANCEL"
	OrderStatusPendingReplace  OrderStatus = "PENDING_REPLACE"
	OrderStatusPartialFilled   OrderStatus = "PARTIAL_FILLED"
	OrderStatusFilled          OrderStatus = "FILLED"
	OrderStatusCanceled        OrderStatus = "CANCELED"
	OrderStatusRejected        OrderStatus = "REJECTED"
	OrderStatusCancelRejected  OrderStatus = "CANCEL_REJECTED"
	OrderStatusReplaceRejected OrderStatus = "REPLACE_REJECTED"
	OrderStatusReplaced        OrderStatus = "REPLACED"
)

// IsKnown reports whether s is one of the OrderStatus codes this client was
// built against. A false result means Toss returned a code newer than this
// build — the value is still preserved in the OrderStatus, so callers can log
// or fail-closed on it deliberately instead of silently mishandling it.
func (s OrderStatus) IsKnown() bool {
	switch s {
	case OrderStatusPending, OrderStatusPendingCancel, OrderStatusPendingReplace,
		OrderStatusPartialFilled, OrderStatusFilled, OrderStatusCanceled,
		OrderStatusRejected, OrderStatusCancelRejected, OrderStatusReplaceRejected,
		OrderStatusReplaced:
		return true
	default:
		return false
	}
}

// ClientOrderIDMaxLen is the maximum length of a clientOrderId accepted by
// POST /api/v1/orders (OpenAPI OrderCreateRequest, verified against
// openapi.json). See ValidateClientOrderID for the full constraint.
const ClientOrderIDMaxLen = 36

// clientOrderIDPattern is the server-side charset for clientOrderId, copied
// verbatim from the OpenAPI OrderCreateRequest schema: ASCII letters, digits,
// hyphen, and underscore, one or more characters.
var clientOrderIDPattern = regexp.MustCompile(`^[a-zA-Z0-9\-_]+$`)

// ValidateClientOrderID reports whether id satisfies the server's clientOrderId
// constraint (POST /api/v1/orders), so a malformed idempotency key is rejected
// locally before a real order is ever sent, rather than round-tripping to a
// server rejection.
//
// The constraint, measured directly from openapi.json (do not guess):
//   - Optional: an empty id is valid and means "no idempotency" — every request
//     is treated as a distinct order.
//   - When present: at most ClientOrderIDMaxLen (36) characters, matching
//     ^[a-zA-Z0-9\-_]+$ (ASCII letters, digits, '-', '_').
//   - The idempotency window is 10 minutes: the same id re-sent within 10
//     minutes replays the prior order result; after that it is a new order. The
//     server never auto-generates the id.
//
// This is the input contract for #34's deterministic clientOrderId derivation
// (ADR-0002 point 4): that hash-to-clientOrderId function MUST emit values
// within these bounds. It is exported so #34 can reuse it rather than
// re-encoding the constraint.
func ValidateClientOrderID(id string) error {
	if id == "" {
		return nil
	}
	if len(id) > ClientOrderIDMaxLen {
		return fmt.Errorf("order: clientOrderId %q exceeds %d characters", id, ClientOrderIDMaxLen)
	}
	if !clientOrderIDPattern.MatchString(id) {
		return fmt.Errorf("order: clientOrderId %q must match %s (ASCII letters, digits, '-', '_')", id, clientOrderIDPattern)
	}
	return nil
}

// OrderRequest is a create-order payload for POST /api/v1/orders. Numeric
// fields are decimal STRINGS on purpose (ADR-0006): they travel to the API
// byte-for-byte, so a caller's exact price/quantity is never mangled by a
// float round-trip.
//
// The Toss schema is a oneOf: exactly one of Quantity (quantity-based) or
// OrderAmount (amount-based, US MARKET only) must be set. SubmitOrder enforces
// that structural rule plus the required fields and the clientOrderId
// constraint before sending; all other business rules (tick size, market
// hours, fractional-quantity eligibility, high-value confirmation, …) are the
// server's authority and surface as an *APIError.
type OrderRequest struct {
	// ClientOrderID is the optional idempotency key (see ValidateClientOrderID).
	// Empty means no idempotency.
	ClientOrderID string `json:"clientOrderId,omitempty"`
	// Symbol is the instrument (KRX: 6-digit code, US: ticker). Required.
	Symbol string `json:"symbol"`
	// Side is BUY or SELL. Required.
	Side Side `json:"side"`
	// OrderType is LIMIT or MARKET. Required.
	OrderType OrderType `json:"orderType"`
	// TimeInForce defaults to DAY at the server when omitted.
	TimeInForce TimeInForce `json:"timeInForce,omitempty"`
	// Quantity is the share count (decimal string). Set this XOR OrderAmount.
	Quantity string `json:"quantity,omitempty"`
	// Price is required for LIMIT and must be absent for MARKET (decimal
	// string, native currency). The server enforces which; the wrapper does
	// not couple price to orderType to avoid rejecting valid orders.
	Price string `json:"price,omitempty"`
	// OrderAmount is the notional in USD (decimal string), US MARKET only. Set
	// this XOR Quantity.
	OrderAmount string `json:"orderAmount,omitempty"`
	// ConfirmHighValueOrder acknowledges an order at/above the high-value
	// threshold (default false = omitted).
	ConfirmHighValueOrder bool `json:"confirmHighValueOrder,omitempty"`
}

// validate applies the structural encoding guards that keep a malformed request
// from becoming a wrong real order. It is deliberately narrow: required-field
// presence, the quantity/amount oneOf, and the clientOrderId charset. It does
// NOT re-implement server business rules.
func (r OrderRequest) validate() error {
	if r.Symbol == "" {
		return fmt.Errorf("order: OrderRequest.Symbol is required")
	}
	if r.Side == "" {
		return fmt.Errorf("order: OrderRequest.Side is required")
	}
	if r.OrderType == "" {
		return fmt.Errorf("order: OrderRequest.OrderType is required")
	}
	hasQty := r.Quantity != ""
	hasAmt := r.OrderAmount != ""
	if hasQty == hasAmt {
		return fmt.Errorf("order: OrderRequest requires exactly one of Quantity or OrderAmount (got quantity=%q, orderAmount=%q)", r.Quantity, r.OrderAmount)
	}
	return ValidateClientOrderID(r.ClientOrderID)
}

// OrderResponse is the result of a successful POST /api/v1/orders. orderId is
// the server-issued handle used for all later truth lookups (ADR-0002 point 3).
// clientOrderId echoes the request value and is nil when none was sent — it is
// exposed only here, never on the detail lookup, so it is not a post-hoc
// matching key (ADR-0002 point 4).
type OrderResponse struct {
	OrderID       string  `json:"orderId"`
	ClientOrderID *string `json:"clientOrderId"`
}

// OrderExecution is the cumulative fill snapshot for an order — there is no
// per-fill identifier in the Toss API (ADR-0006 measurement), only this running
// aggregate. Every financial field is the raw API decimal string (no float
// conversion) so an audit digest over these fields is exact. Nullable fields
// are pointers so "not yet set" (null) stays distinct from "0".
type OrderExecution struct {
	FilledQuantity     string  `json:"filledQuantity"`
	AverageFilledPrice *string `json:"averageFilledPrice"`
	FilledAmount       *string `json:"filledAmount"`
	Commission         *string `json:"commission"`
	Tax                *string `json:"tax"`
	FilledAt           *string `json:"filledAt"`
	SettlementDate     *string `json:"settlementDate"`
}

// Order is the detail view from GET /api/v1/orders/{orderId}, available for an
// order in any state (open or closed) — the primary truth source for
// reconciliation (ADR-0002/0003). Price, Quantity and OrderAmount are raw
// decimal strings; nullable fields are pointers.
type Order struct {
	OrderID     string         `json:"orderId"`
	Symbol      string         `json:"symbol"`
	Side        Side           `json:"side"`
	OrderType   OrderType      `json:"orderType"`
	TimeInForce TimeInForce    `json:"timeInForce"`
	Status      OrderStatus    `json:"status"`
	Price       *string        `json:"price"`
	Quantity    string         `json:"quantity"`
	OrderAmount *string        `json:"orderAmount"`
	Currency    Currency       `json:"currency"`
	OrderedAt   string         `json:"orderedAt"`
	CanceledAt  *string        `json:"canceledAt"`
	Execution   OrderExecution `json:"execution"`
}

// SubmitOrder places an order (POST /api/v1/orders) for accountSeq and returns
// the server's orderId. It issues exactly one POST — a write is never
// auto-retried (duplicate-fill risk); the caller owns any retry decision on a
// re-checked state (#34).
//
// A structurally invalid request is rejected before any network call. A non-200
// response (including 409 request-in-progress and 4xx/5xx business errors) is
// returned as *APIError carrying the status and API error code.
func (c *Client) SubmitOrder(ctx context.Context, accountSeq int64, req OrderRequest) (OrderResponse, error) {
	if err := req.validate(); err != nil {
		return OrderResponse{}, err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return OrderResponse{}, fmt.Errorf("order: encode submit request: %w", err)
	}

	seq := strconv.FormatInt(accountSeq, 10)
	resp, err := c.api.Post(ctx, "/api/v1/orders", bytes.NewReader(payload),
		toss.WithAccount(seq), withJSONBody())
	if err != nil {
		return OrderResponse{}, err
	}
	defer drainClose(resp)

	if resp.StatusCode != http.StatusOK {
		return OrderResponse{}, decodeAPIError(resp)
	}

	var env struct {
		Result OrderResponse `json:"result"`
	}
	if err := toss.DecodeJSON(resp.Body, &env); err != nil {
		return OrderResponse{}, fmt.Errorf("order: decode submit response: %w", err)
	}
	// orderId is required on OrderResponse and is the ONLY durable truth handle
	// for this (possibly already-placed) irreversible order. A 200 without it
	// (schema drift, a partial body from a proxy) must NOT be reported as a
	// clean success: the caller would have no handle to ack/reconcile and could
	// treat a real submit as never-sent. Surface it as an error so the caller
	// routes it through its ambiguous-submit handling (ADR-0002 p3 / ADR-0003).
	if env.Result.OrderID == "" {
		return OrderResponse{}, fmt.Errorf("order: submit returned 200 but no orderId — outcome ambiguous, do not treat as sent-or-not-sent without reconciliation")
	}
	return env.Result, nil
}

// GetOrder reads the detail for orderId (GET /api/v1/orders/{orderId}) on
// accountSeq — valid for any order state, open or closed. It is the primary
// post-submission truth lookup (ADR-0002 point 3). A non-200 (including 404
// order-not-found) is returned as *APIError.
func (c *Client) GetOrder(ctx context.Context, accountSeq int64, orderID string) (Order, error) {
	if orderID == "" {
		return Order{}, fmt.Errorf("order: GetOrder requires an orderId")
	}
	// orderId is an opaque server token; escape it as a single path segment so
	// URL-significant bytes cannot alter the request path.
	path := "/api/v1/orders/" + url.PathEscape(orderID)

	seq := strconv.FormatInt(accountSeq, 10)
	resp, err := c.api.Get(ctx, path, toss.WithAccount(seq))
	if err != nil {
		return Order{}, err
	}
	defer drainClose(resp)

	if resp.StatusCode != http.StatusOK {
		return Order{}, decodeAPIError(resp)
	}

	var env struct {
		Result Order `json:"result"`
	}
	if err := toss.DecodeJSON(resp.Body, &env); err != nil {
		return Order{}, fmt.Errorf("order: decode order detail: %w", err)
	}
	// This detail is authoritative truth for reconciliation/audit, so verify the
	// response is actually ABOUT the order we asked for before returning it. An
	// empty orderId (malformed/partial 200) or a mismatched one (proxy/cache
	// mixup, schema drift) must not be surfaced as this order's truth — acting
	// on the wrong or empty order state is money-unsafe. Downstream policy
	// decides on unknown status codes; the wrapper guards only order identity.
	if env.Result.OrderID == "" {
		return Order{}, fmt.Errorf("order: detail for %q returned 200 but no orderId in body", orderID)
	}
	if env.Result.OrderID != orderID {
		return Order{}, fmt.Errorf("order: detail for %q returned a different orderId %q", orderID, env.Result.OrderID)
	}
	return env.Result, nil
}

// withJSONBody sets Content-Type: application/json on an outgoing request. The
// toss.Client does not set a body content type, and POST /api/v1/orders
// requires application/json.
func withJSONBody() toss.RequestOption {
	return func(r *http.Request) {
		r.Header.Set("Content-Type", "application/json")
	}
}

// APIError is a non-2xx Toss API response. It carries the HTTP status and the
// API error code so callers can branch programmatically (e.g. #34 on
// request-in-progress vs a terminal business rejection, #35 on order-not-found)
// instead of string-matching. It is returned by both SubmitOrder and GetOrder.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	RequestID  string
}

func (e *APIError) Error() string {
	switch {
	case e.Code != "" && e.Message != "":
		return fmt.Sprintf("order: request failed: status %d: %s: %s", e.StatusCode, e.Code, e.Message)
	case e.Code != "":
		return fmt.Sprintf("order: request failed: status %d: %s", e.StatusCode, e.Code)
	default:
		return fmt.Sprintf("order: request failed: status %d", e.StatusCode)
	}
}

// decodeAPIError reads the {"error":{requestId,code,message}} envelope for a
// non-200 and returns an *APIError. It never fails on a malformed body — the
// status code alone is enough to surface the failure.
func decodeAPIError(resp *http.Response) error {
	var er struct {
		Error struct {
			RequestID string `json:"requestId"`
			Code      string `json:"code"`
			Message   string `json:"message"`
		} `json:"error"`
	}
	_ = toss.DecodeJSON(resp.Body, &er)
	return &APIError{
		StatusCode: resp.StatusCode,
		Code:       er.Error.Code,
		Message:    er.Error.Message,
		RequestID:  er.Error.RequestID,
	}
}

// drainClose drains and closes a response body so the connection can be reused.
func drainClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
