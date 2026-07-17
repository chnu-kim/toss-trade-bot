package order

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chnu-kim/toss-trade-bot/internal/toss"
)

// tokenAndThen wraps a handler so the injected *toss.Client can mint a token
// before the real request. Tests only care about the API call itself.
func tokenAndThen(t *testing.T, api http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth2/token" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "tok-1",
				"token_type":   "Bearer",
				"expires_in":   86400,
			})
			return
		}
		api(w, r)
	}))
}

func newClient(srv *httptest.Server) *Client {
	api, err := toss.NewClient(srv.URL, "id", "secret")
	if err != nil {
		panic(err) // httptest URLs are loopback, which always validates
	}
	return NewClient(api)
}

// failIfCalled fails the test if either the GET or POST path is exercised. It
// proves the request-shaping guards reject a bad OrderRequest BEFORE any real
// order ever reaches the network (money-critical: a malformed submit must never
// be sent).
type failIfCalled struct{ t *testing.T }

func (f failIfCalled) Get(context.Context, string, ...toss.RequestOption) (*http.Response, error) {
	f.t.Helper()
	f.t.Fatal("Get must not be called for a rejected request")
	return nil, nil
}

func (f failIfCalled) Post(context.Context, string, io.Reader, ...toss.RequestOption) (*http.Response, error) {
	f.t.Helper()
	f.t.Fatal("Post must not be called for a rejected request")
	return nil, nil
}

// --- SubmitOrder -----------------------------------------------------------

func TestSubmitOrder_QuantityBasedRoundTrip(t *testing.T) {
	var (
		seenPath, seenMethod, seenAccount, seenContentType, seenAuth string
		body                                                         map[string]any
		calls                                                        int
	)
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		seenPath = r.URL.Path
		seenMethod = r.Method
		seenAccount = r.Header.Get("X-Tossinvest-Account")
		seenContentType = r.Header.Get("Content-Type")
		seenAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"orderId":"srv-order-1","clientOrderId":"intent-abc"}}`))
	})
	defer srv.Close()

	resp, err := newClient(srv).SubmitOrder(context.Background(), 7, OrderRequest{
		ClientOrderID: "intent-abc",
		Symbol:        "005930",
		Side:          SideBuy,
		OrderType:     OrderTypeLimit,
		Quantity:      "10",
		Price:         "70000",
	})
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if calls != 1 {
		t.Fatalf("server hit %d times, want exactly 1 (no retry on write path)", calls)
	}
	if seenMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", seenMethod)
	}
	if seenPath != "/api/v1/orders" {
		t.Fatalf("path = %q, want /api/v1/orders", seenPath)
	}
	if seenAccount != "7" {
		t.Fatalf("X-Tossinvest-Account = %q, want 7", seenAccount)
	}
	if seenContentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", seenContentType)
	}
	if seenAuth != "Bearer tok-1" {
		t.Fatalf("Authorization = %q", seenAuth)
	}
	// The request body must encode exactly the intended fields — no spurious
	// keys (an encoding bug here means a wrong real order).
	wantKeys := map[string]any{
		"clientOrderId": "intent-abc",
		"symbol":        "005930",
		"side":          "BUY",
		"orderType":     "LIMIT",
		"quantity":      "10",
		"price":         "70000",
	}
	if len(body) != len(wantKeys) {
		t.Fatalf("request body = %v, want exactly keys %v", body, wantKeys)
	}
	for k, want := range wantKeys {
		if got := body[k]; got != want {
			t.Fatalf("body[%q] = %v, want %v", k, got, want)
		}
	}
	if resp.OrderID != "srv-order-1" {
		t.Fatalf("orderId = %q, want srv-order-1", resp.OrderID)
	}
	if resp.ClientOrderID == nil || *resp.ClientOrderID != "intent-abc" {
		t.Fatalf("clientOrderId = %v, want intent-abc", resp.ClientOrderID)
	}
}

func TestSubmitOrder_AmountBasedOmitsQuantityAndPrice(t *testing.T) {
	var body map[string]any
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"orderId":"srv-order-2","clientOrderId":null}}`))
	})
	defer srv.Close()

	resp, err := newClient(srv).SubmitOrder(context.Background(), 7, OrderRequest{
		Symbol:      "AAPL",
		Side:        SideBuy,
		OrderType:   OrderTypeMarket,
		OrderAmount: "100.5",
	})
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if _, ok := body["quantity"]; ok {
		t.Fatalf("amount-based body must omit quantity, got %v", body)
	}
	if _, ok := body["price"]; ok {
		t.Fatalf("amount-based body must omit price, got %v", body)
	}
	if _, ok := body["clientOrderId"]; ok {
		t.Fatalf("empty clientOrderId must be omitted, got %v", body)
	}
	if _, ok := body["confirmHighValueOrder"]; ok {
		t.Fatalf("default confirmHighValueOrder must be omitted, got %v", body)
	}
	if body["orderAmount"] != "100.5" {
		t.Fatalf("orderAmount = %v, want 100.5", body["orderAmount"])
	}
	// A null clientOrderId in the response decodes to a nil pointer.
	if resp.ClientOrderID != nil {
		t.Fatalf("clientOrderId = %v, want nil", resp.ClientOrderID)
	}
}

func TestSubmitOrder_ConfirmHighValueOrderEncoded(t *testing.T) {
	var body map[string]any
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"orderId":"srv-order-3"}}`))
	})
	defer srv.Close()

	_, err := newClient(srv).SubmitOrder(context.Background(), 7, OrderRequest{
		Symbol:                "005930",
		Side:                  SideBuy,
		OrderType:             OrderTypeLimit,
		Quantity:              "10000",
		Price:                 "70000",
		ConfirmHighValueOrder: true,
	})
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if body["confirmHighValueOrder"] != true {
		t.Fatalf("confirmHighValueOrder = %v, want true", body["confirmHighValueOrder"])
	}
}

func TestSubmitOrder_TimeInForceEncoded(t *testing.T) {
	var body map[string]any
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"orderId":"srv-order-4"}}`))
	})
	defer srv.Close()

	_, err := newClient(srv).SubmitOrder(context.Background(), 7, OrderRequest{
		Symbol:      "AAPL",
		Side:        SideBuy,
		OrderType:   OrderTypeLimit,
		TimeInForce: TimeInForceCls,
		Quantity:    "10",
		Price:       "185.5",
	})
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if body["timeInForce"] != "CLS" {
		t.Fatalf("timeInForce = %v, want CLS", body["timeInForce"])
	}
}

func TestSubmitOrder_RejectsMissingRequiredFields(t *testing.T) {
	cases := []struct {
		name string
		req  OrderRequest
	}{
		{"no symbol", OrderRequest{Side: SideBuy, OrderType: OrderTypeLimit, Quantity: "1", Price: "1"}},
		{"no side", OrderRequest{Symbol: "005930", OrderType: OrderTypeLimit, Quantity: "1", Price: "1"}},
		{"no orderType", OrderRequest{Symbol: "005930", Side: SideBuy, Quantity: "1", Price: "1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient(failIfCalled{t})
			_, err := c.SubmitOrder(context.Background(), 7, tc.req)
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
		})
	}
}

func TestSubmitOrder_RejectsQuantityAmountAmbiguity(t *testing.T) {
	cases := []struct {
		name string
		req  OrderRequest
	}{
		{"neither quantity nor amount", OrderRequest{Symbol: "005930", Side: SideBuy, OrderType: OrderTypeLimit}},
		{"both quantity and amount", OrderRequest{Symbol: "AAPL", Side: SideBuy, OrderType: OrderTypeMarket, Quantity: "1", OrderAmount: "100"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient(failIfCalled{t})
			_, err := c.SubmitOrder(context.Background(), 7, tc.req)
			if err == nil {
				t.Fatal("expected exactly-one-of error, got nil")
			}
		})
	}
}

func TestSubmitOrder_RejectsInvalidClientOrderID(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"too long", strings.Repeat("a", ClientOrderIDMaxLen+1)},
		{"disallowed char space", "bad id"},
		{"disallowed char slash", "a/b"},
		{"disallowed char dot", "a.b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient(failIfCalled{t})
			_, err := c.SubmitOrder(context.Background(), 7, OrderRequest{
				ClientOrderID: tc.id,
				Symbol:        "005930",
				Side:          SideBuy,
				OrderType:     OrderTypeLimit,
				Quantity:      "10",
				Price:         "70000",
			})
			if err == nil {
				t.Fatalf("expected clientOrderId validation error for %q, got nil", tc.id)
			}
		})
	}
}

func TestValidateClientOrderID_AcceptsSpecCharset(t *testing.T) {
	ok := []string{
		"",                                       // absent is allowed (no idempotency)
		"my-order-001",                           // hyphen
		"a_b_c",                                  // underscore
		"ABCabc0123456789",                       // alphanumeric mix
		strings.Repeat("z", ClientOrderIDMaxLen), // exactly the max length
	}
	for _, id := range ok {
		if err := ValidateClientOrderID(id); err != nil {
			t.Fatalf("ValidateClientOrderID(%q) = %v, want nil", id, err)
		}
	}
	bad := []string{"bad id", "a/b", "café", strings.Repeat("z", ClientOrderIDMaxLen+1)}
	for _, id := range bad {
		if err := ValidateClientOrderID(id); err == nil {
			t.Fatalf("ValidateClientOrderID(%q) = nil, want error", id)
		}
	}
}

func TestSubmitOrder_APIErrorDecoded(t *testing.T) {
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"requestId":"01HXYZ","code":"insufficient-buying-power","message":"주문 가능 금액이 부족합니다."}}`))
	})
	defer srv.Close()

	_, err := newClient(srv).SubmitOrder(context.Background(), 7, OrderRequest{
		Symbol:    "005930",
		Side:      SideBuy,
		OrderType: OrderTypeLimit,
		Quantity:  "10",
		Price:     "70000",
	})
	if err == nil {
		t.Fatal("expected API error, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not *APIError", err)
	}
	if apiErr.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("StatusCode = %d, want 422", apiErr.StatusCode)
	}
	if apiErr.Code != "insufficient-buying-power" {
		t.Fatalf("Code = %q", apiErr.Code)
	}
	if apiErr.RequestID != "01HXYZ" {
		t.Fatalf("RequestID = %q", apiErr.RequestID)
	}
	if !strings.Contains(err.Error(), "insufficient-buying-power") {
		t.Fatalf("error string %q should name the code", err.Error())
	}
	if !strings.Contains(err.Error(), "422") {
		t.Fatalf("error string %q should include the status", err.Error())
	}
}

func TestSubmitOrder_ServerErrorNotRetried(t *testing.T) {
	var calls int
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"requestId":"01HXYZ","code":"internal-error","message":"처리 중 문제가 생겼어요."}}`))
	})
	defer srv.Close()

	_, err := newClient(srv).SubmitOrder(context.Background(), 7, OrderRequest{
		Symbol:    "005930",
		Side:      SideBuy,
		OrderType: OrderTypeLimit,
		Quantity:  "10",
		Price:     "70000",
	})
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
	// A write must never be auto-retried: a re-sent order could double-fill.
	if calls != 1 {
		t.Fatalf("POST hit %d times, want exactly 1 (submit is never retried)", calls)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("error = %v, want *APIError with status 500", err)
	}
}

// --- GetOrder --------------------------------------------------------------

func TestGetOrder_RoundTrip(t *testing.T) {
	var seenPath, seenMethod, seenAccount string
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenMethod = r.Method
		seenAccount = r.Header.Get("X-Tossinvest-Account")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{
			"orderId":"srv-order-1",
			"symbol":"005930",
			"side":"BUY",
			"orderType":"LIMIT",
			"timeInForce":"DAY",
			"status":"FILLED",
			"price":"70000",
			"quantity":"10",
			"orderAmount":null,
			"currency":"KRW",
			"orderedAt":"2026-03-28T09:30:00+09:00",
			"canceledAt":null,
			"execution":{
				"filledQuantity":"10",
				"averageFilledPrice":"70000",
				"filledAmount":"700000",
				"commission":"1400",
				"tax":"0",
				"filledAt":"2026-03-28T09:31:15+09:00",
				"settlementDate":"2026-03-30"
			}
		}}`))
	})
	defer srv.Close()

	got, err := newClient(srv).GetOrder(context.Background(), 42, "srv-order-1")
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if seenMethod != http.MethodGet {
		t.Fatalf("method = %q, want GET", seenMethod)
	}
	if seenPath != "/api/v1/orders/srv-order-1" {
		t.Fatalf("path = %q, want /api/v1/orders/srv-order-1", seenPath)
	}
	if seenAccount != "42" {
		t.Fatalf("X-Tossinvest-Account = %q, want 42", seenAccount)
	}
	if got.OrderID != "srv-order-1" || got.Symbol != "005930" {
		t.Fatalf("order = %+v", got)
	}
	if got.Side != SideBuy || got.OrderType != OrderTypeLimit || got.TimeInForce != TimeInForceDay {
		t.Fatalf("enums = %+v", got)
	}
	if got.Status != OrderStatusFilled || !got.Status.IsKnown() {
		t.Fatalf("status = %q, IsKnown=%v", got.Status, got.Status.IsKnown())
	}
	if got.Currency != CurrencyKRW {
		t.Fatalf("currency = %q", got.Currency)
	}
	if got.Price == nil || *got.Price != "70000" {
		t.Fatalf("price = %v, want 70000", got.Price)
	}
	if got.OrderAmount != nil {
		t.Fatalf("orderAmount = %v, want nil", got.OrderAmount)
	}
	if got.CanceledAt != nil {
		t.Fatalf("canceledAt = %v, want nil", got.CanceledAt)
	}
	ex := got.Execution
	if ex.FilledQuantity != "10" {
		t.Fatalf("filledQuantity = %q", ex.FilledQuantity)
	}
	if ex.SettlementDate == nil || *ex.SettlementDate != "2026-03-30" {
		t.Fatalf("settlementDate = %v", ex.SettlementDate)
	}
}

func TestGetOrder_UnknownStatusPreserved(t *testing.T) {
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A status code Toss adds in the future must NOT fail decoding.
		_, _ = w.Write([]byte(`{"result":{
			"orderId":"srv-order-9","symbol":"005930","side":"BUY","orderType":"LIMIT",
			"timeInForce":"GTC","status":"SOME_FUTURE_STATE","quantity":"1","currency":"XYZ",
			"orderedAt":"2026-03-28T09:30:00+09:00",
			"execution":{"filledQuantity":"0","averageFilledPrice":null,"filledAmount":null,
			"commission":null,"tax":null,"filledAt":null,"settlementDate":null}
		}}`))
	})
	defer srv.Close()

	got, err := newClient(srv).GetOrder(context.Background(), 42, "srv-order-9")
	if err != nil {
		t.Fatalf("GetOrder must not fail on an unknown status code: %v", err)
	}
	if got.Status != "SOME_FUTURE_STATE" {
		t.Fatalf("status = %q, want SOME_FUTURE_STATE preserved verbatim", got.Status)
	}
	if got.Status.IsKnown() {
		t.Fatalf("IsKnown() = true for an unknown status")
	}
	// Unknown enum values on the other fields must survive too.
	if got.TimeInForce != "GTC" {
		t.Fatalf("timeInForce = %q, want GTC preserved", got.TimeInForce)
	}
	if got.Currency != "XYZ" {
		t.Fatalf("currency = %q, want XYZ preserved", got.Currency)
	}
}

func TestGetOrder_ExecutionFinancialsRawStrings(t *testing.T) {
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A high-precision decimal that a float64 round-trip would corrupt.
		_, _ = w.Write([]byte(`{"result":{
			"orderId":"srv-order-7","symbol":"AAPL","side":"BUY","orderType":"MARKET",
			"timeInForce":"DAY","status":"PARTIAL_FILLED","price":null,"quantity":"5","orderAmount":null,
			"currency":"USD","orderedAt":"2026-03-28T23:30:00+09:00","canceledAt":null,
			"execution":{"filledQuantity":"3","averageFilledPrice":"185.256789012345678","filledAmount":"555.770367",
			"commission":null,"tax":"0","filledAt":"2026-03-28T23:30:05+09:00","settlementDate":null}
		}}`))
	})
	defer srv.Close()

	got, err := newClient(srv).GetOrder(context.Background(), 42, "srv-order-7")
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	ex := got.Execution
	// Financial fields must be surfaced byte-identical to the wire — no float
	// conversion, no reformatting (ADR-0006: preserve decimal precision so the
	// audit digest is exact).
	if ex.AverageFilledPrice == nil || *ex.AverageFilledPrice != "185.256789012345678" {
		t.Fatalf("averageFilledPrice = %v, want raw 185.256789012345678", ex.AverageFilledPrice)
	}
	if ex.FilledAmount == nil || *ex.FilledAmount != "555.770367" {
		t.Fatalf("filledAmount = %v, want raw 555.770367", ex.FilledAmount)
	}
	// A null financial field stays nil (distinct from "0").
	if ex.Commission != nil {
		t.Fatalf("commission = %v, want nil", ex.Commission)
	}
	if ex.Tax == nil || *ex.Tax != "0" {
		t.Fatalf("tax = %v, want raw 0", ex.Tax)
	}
}

func TestGetOrder_NotFoundAPIError(t *testing.T) {
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"requestId":"01HXYZ","code":"order-not-found","message":"주문을 찾을 수 없습니다."}}`))
	})
	defer srv.Close()

	_, err := newClient(srv).GetOrder(context.Background(), 42, "does-not-exist")
	if err == nil {
		t.Fatal("expected error on 404, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not *APIError", err)
	}
	if apiErr.StatusCode != http.StatusNotFound || apiErr.Code != "order-not-found" {
		t.Fatalf("apiErr = %+v, want 404 order-not-found", apiErr)
	}
}

func TestGetOrder_EscapesOrderIDPathSegment(t *testing.T) {
	var seenRawPath string
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		seenRawPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{
			"orderId":"weird","symbol":"005930","side":"BUY","orderType":"LIMIT",
			"timeInForce":"DAY","status":"FILLED","quantity":"1","currency":"KRW",
			"orderedAt":"2026-03-28T09:30:00+09:00",
			"execution":{"filledQuantity":"1","averageFilledPrice":"1","filledAmount":"1",
			"commission":"0","tax":"0","filledAt":"2026-03-28T09:30:00+09:00","settlementDate":"2026-03-30"}
		}}`))
	})
	defer srv.Close()

	// orderId is an opaque server token; a value with URL-significant bytes must
	// be percent-encoded into the path, not injected raw.
	_, err := newClient(srv).GetOrder(context.Background(), 42, "a b/c?d")
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if strings.ContainsAny(seenRawPath[len("/api/v1/orders/"):], " ?") {
		t.Fatalf("escaped path %q leaked raw URL-significant bytes", seenRawPath)
	}
}

func TestGetOrder_RejectsEmptyOrderID(t *testing.T) {
	c := NewClient(failIfCalled{t})
	_, err := c.GetOrder(context.Background(), 42, "")
	if err == nil {
		t.Fatal("expected error for empty orderId, got nil")
	}
}

func TestGetOrder_OversizedResponseRejected(t *testing.T) {
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"orderId":"` + strings.Repeat("x", 2<<20) + `"}}`))
	})
	defer srv.Close()

	_, err := newClient(srv).GetOrder(context.Background(), 42, "srv-order-1")
	if err == nil {
		t.Fatal("expected oversized response to be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error %q should say the body exceeds the cap", err)
	}
}
