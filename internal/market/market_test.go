package market

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chnu-kim/toss-trade-bot/internal/toss"
)

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

// TestClient_OversizedResponseRejected guards the decode byte cap: a huge
// (malicious or misbehaving) response body must fail decoding instead of being
// loaded onto the heap wholesale (OOM stops the order loop).
func TestClient_OversizedResponseRejected(t *testing.T) {
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"` + strings.Repeat("x", 2<<20) + `"}`))
	})
	defer srv.Close()

	_, err := newClient(srv).Prices(context.Background(), "005930")
	if err == nil {
		t.Fatal("expected oversized response to be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error %q should say the body exceeds the cap", err)
	}
}

func TestClient_Prices(t *testing.T) {
	var seenSymbols string
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/prices" {
			t.Errorf("path = %q, want /api/v1/prices", r.URL.Path)
		}
		seenSymbols = r.URL.Query().Get("symbols")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":[
			{"symbol":"005930","timestamp":"2026-03-25T09:30:00.123+09:00","lastPrice":"72000","currency":"KRW"},
			{"symbol":"AAPL","timestamp":null,"lastPrice":"185.70","currency":"USD"}
		]}`))
	})
	defer srv.Close()

	prices, err := newClient(srv).Prices(context.Background(), "005930", "AAPL")
	if err != nil {
		t.Fatalf("Prices: %v", err)
	}
	if seenSymbols != "005930,AAPL" {
		t.Fatalf("symbols query = %q, want 005930,AAPL", seenSymbols)
	}
	if len(prices) != 2 {
		t.Fatalf("got %d prices, want 2", len(prices))
	}
	if prices[0].Symbol != "005930" || prices[0].LastPrice != "72000" || prices[0].Currency != "KRW" {
		t.Fatalf("price[0] = %+v", prices[0])
	}
	if prices[0].Timestamp == nil || *prices[0].Timestamp != "2026-03-25T09:30:00.123+09:00" {
		t.Fatalf("price[0].timestamp = %v", prices[0].Timestamp)
	}
	if prices[1].Timestamp != nil {
		t.Fatalf("price[1].timestamp should be nil (null), got %v", prices[1].Timestamp)
	}
}

func TestClient_PricesRequiresSymbols(t *testing.T) {
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("must not call API with no symbols, hit %q", r.URL.Path)
	})
	defer srv.Close()

	_, err := newClient(srv).Prices(context.Background())
	if err == nil {
		t.Fatal("expected error when no symbols given, got nil")
	}
}

func TestClient_Orderbook(t *testing.T) {
	var seenSymbol string
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/orderbook" {
			t.Errorf("path = %q, want /api/v1/orderbook", r.URL.Path)
		}
		seenSymbol = r.URL.Query().Get("symbol")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{
			"timestamp":"2026-03-25T09:30:00.123+09:00","currency":"KRW",
			"asks":[{"price":"72300","volume":"1200"},{"price":"72200","volume":"3400"}],
			"bids":[{"price":"72000","volume":"5200"},{"price":"71900","volume":"4100"}]
		}}`))
	})
	defer srv.Close()

	ob, err := newClient(srv).Orderbook(context.Background(), "005930")
	if err != nil {
		t.Fatalf("Orderbook: %v", err)
	}
	if seenSymbol != "005930" {
		t.Fatalf("symbol query = %q, want 005930", seenSymbol)
	}
	if ob.Currency != "KRW" {
		t.Fatalf("currency = %q", ob.Currency)
	}
	if len(ob.Asks) != 2 || ob.Asks[0].Price != "72300" || ob.Asks[0].Volume != "1200" {
		t.Fatalf("asks = %+v", ob.Asks)
	}
	if len(ob.Bids) != 2 || ob.Bids[0].Price != "72000" {
		t.Fatalf("bids = %+v", ob.Bids)
	}
}

func TestClient_PricesErrorResponse(t *testing.T) {
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"requestId":"01H","code":"invalid-request","message":"요청이 올바르지 않습니다."}}`))
	})
	defer srv.Close()

	_, err := newClient(srv).Prices(context.Background(), "005930")
	if err == nil {
		t.Fatal("expected error on 400, got nil")
	}
	if !strings.Contains(err.Error(), "invalid-request") || !strings.Contains(err.Error(), "400") {
		t.Fatalf("error %q should include code and status", err.Error())
	}
}

func TestClient_KrCalendar(t *testing.T) {
	var seenDate string
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/market-calendar/KR" {
			t.Errorf("path = %q", r.URL.Path)
		}
		seenDate = r.URL.Query().Get("date")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{
			"today":{"date":"2026-03-25","integrated":{
				"preMarket":{"startTime":"2026-03-25T08:00:00+09:00","singlePriceAuctionStartTime":"2026-03-25T08:50:00+09:00","endTime":"2026-03-25T09:00:00+09:00"},
				"regularMarket":{"startTime":"2026-03-25T09:00:00+09:00","singlePriceAuctionStartTime":"2026-03-25T15:20:00+09:00","endTime":"2026-03-25T15:30:00+09:00"},
				"afterMarket":{"startTime":"2026-03-25T15:30:00+09:00","singlePriceAuctionEndTime":"2026-03-25T15:40:00+09:00","endTime":"2026-03-25T20:00:00+09:00"}}},
			"previousBusinessDay":{"date":"2026-03-24","integrated":null},
			"nextBusinessDay":{"date":"2026-03-26","integrated":null}
		}}`))
	})
	defer srv.Close()

	cal, err := newClient(srv).KrCalendar(context.Background(), "2026-03-25")
	if err != nil {
		t.Fatalf("KrCalendar: %v", err)
	}
	if seenDate != "2026-03-25" {
		t.Fatalf("date query = %q", seenDate)
	}
	if !cal.Today.IsTradingDay() {
		t.Fatal("today should be a trading day")
	}
	if cal.Today.Integrated.RegularMarket == nil || cal.Today.Integrated.RegularMarket.StartTime != "2026-03-25T09:00:00+09:00" {
		t.Fatalf("regularMarket = %+v", cal.Today.Integrated.RegularMarket)
	}
}

func TestClient_KrCalendarHolidayIsNotTradingDay(t *testing.T) {
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{
			"today":{"date":"2026-05-05","integrated":null},
			"previousBusinessDay":{"date":"2026-05-04","integrated":null},
			"nextBusinessDay":{"date":"2026-05-06","integrated":null}
		}}`))
	})
	defer srv.Close()

	cal, err := newClient(srv).KrCalendar(context.Background(), "")
	if err != nil {
		t.Fatalf("KrCalendar: %v", err)
	}
	if cal.Today.IsTradingDay() {
		t.Fatal("holiday should NOT be a trading day")
	}
}

func TestClient_KrCalendarNoDateOmitsQuery(t *testing.T) {
	var hadDate bool
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		_, hadDate = r.URL.Query()["date"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"today":{"date":"2026-05-05","integrated":null},"previousBusinessDay":{"date":"2026-05-04","integrated":null},"nextBusinessDay":{"date":"2026-05-06","integrated":null}}}`))
	})
	defer srv.Close()

	if _, err := newClient(srv).KrCalendar(context.Background(), ""); err != nil {
		t.Fatalf("KrCalendar: %v", err)
	}
	if hadDate {
		t.Fatal("date query param should be omitted when date is empty")
	}
}

func TestClient_UsCalendarHolidayIsNotTradingDay(t *testing.T) {
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/market-calendar/US" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{
			"today":{"date":"2026-07-03","dayMarket":null,"preMarket":null,"regularMarket":null,"afterMarket":null},
			"previousBusinessDay":{"date":"2026-07-02","dayMarket":null,"preMarket":null,
				"regularMarket":{"startTime":"2026-07-02T22:30:00+09:00","endTime":"2026-07-03T05:00:00+09:00"},"afterMarket":null},
			"nextBusinessDay":{"date":"2026-07-06","dayMarket":null,"preMarket":null,
				"regularMarket":{"startTime":"2026-07-06T22:30:00+09:00","endTime":"2026-07-07T05:00:00+09:00"},"afterMarket":null}
		}}`))
	})
	defer srv.Close()

	cal, err := newClient(srv).UsCalendar(context.Background(), "2026-07-03")
	if err != nil {
		t.Fatalf("UsCalendar: %v", err)
	}
	if cal.Today.IsTradingDay() {
		t.Fatal("US holiday should NOT be a trading day")
	}
	if !cal.PreviousBusinessDay.IsTradingDay() {
		t.Fatal("previous business day should be a trading day")
	}
}
