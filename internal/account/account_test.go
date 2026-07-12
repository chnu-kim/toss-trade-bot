package account

import (
	"context"
	"encoding/json"
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

// TestClient_OversizedResponseRejected guards the decode byte cap: a huge
// (malicious or misbehaving) response body must fail decoding instead of being
// loaded onto the heap wholesale (OOM stops the order loop).
func TestClient_OversizedResponseRejected(t *testing.T) {
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"` + strings.Repeat("x", 2<<20) + `"}`))
	})
	defer srv.Close()

	_, err := newClient(srv).Accounts(context.Background())
	if err == nil {
		t.Fatal("expected oversized response to be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error %q should say the body exceeds the cap", err)
	}
}

func TestClient_Accounts(t *testing.T) {
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/accounts" {
			t.Errorf("path = %q, want /api/v1/accounts", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok-1" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":[{"accountNo":"12345678901","accountSeq":7,"accountType":"BROKERAGE"}]}`))
	})
	defer srv.Close()

	accounts, err := newClient(srv).Accounts(context.Background())
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("got %d accounts, want 1", len(accounts))
	}
	got := accounts[0]
	if got.AccountNo != "12345678901" || got.AccountSeq != 7 || got.AccountType != "BROKERAGE" {
		t.Fatalf("account = %+v", got)
	}
}

func TestClient_AccountsEmpty(t *testing.T) {
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":[]}`))
	})
	defer srv.Close()

	accounts, err := newClient(srv).Accounts(context.Background())
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("got %d accounts, want 0", len(accounts))
	}
}

func TestClient_HoldingsSendsAccountHeaderAndMapsFields(t *testing.T) {
	var seenAccount string
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/holdings" {
			t.Errorf("path = %q, want /api/v1/holdings", r.URL.Path)
		}
		seenAccount = r.Header.Get("X-Tossinvest-Account")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{
			"totalPurchaseAmount":{"krw":"6500000","usd":"1553"},
			"marketValue":{"amount":{"krw":"7200000","usd":"1785"},"amountAfterCost":{"krw":"7050000","usd":"1771.43"}},
			"profitLoss":{"amount":{"krw":"700000","usd":"232"},"amountAfterCost":{"krw":"550000","usd":"218.43"},"rate":"0.1179","rateAfterCost":"0.0983"},
			"dailyProfitLoss":{"amount":{"krw":"100000","usd":"25"},"rate":"0.0141"},
			"items":[
				{"symbol":"005930","name":"삼성전자","marketCountry":"KR","currency":"KRW","quantity":"100","lastPrice":"72000","averagePurchasePrice":"65000",
				 "marketValue":{"purchaseAmount":"6500000","amount":"7200000","amountAfterCost":"7050000"},
				 "profitLoss":{"amount":"700000","amountAfterCost":"550000","rate":"0.1077","rateAfterCost":"0.0846"},
				 "dailyProfitLoss":{"amount":"100000","rate":"0.0141"},
				 "cost":{"commission":"14400","tax":"135600"}},
				{"symbol":"AAPL","name":"Apple Inc.","marketCountry":"US","currency":"USD","quantity":"10","lastPrice":"178.5","averagePurchasePrice":"155.3",
				 "marketValue":{"purchaseAmount":"1553","amount":"1785","amountAfterCost":"1771.43"},
				 "profitLoss":{"amount":"232","amountAfterCost":"218.43","rate":"0.1494","rateAfterCost":"0.1406"},
				 "dailyProfitLoss":{"amount":"25","rate":"0.0142"},
				 "cost":{"commission":"3.57","tax":null}}
			]}}`))
	})
	defer srv.Close()

	overview, err := newClient(srv).Holdings(context.Background(), 42)
	if err != nil {
		t.Fatalf("Holdings: %v", err)
	}
	if seenAccount != "42" {
		t.Fatalf("X-Tossinvest-Account = %q, want 42", seenAccount)
	}
	if overview.TotalPurchaseAmount.KRW != "6500000" {
		t.Fatalf("totalPurchaseAmount.krw = %q", overview.TotalPurchaseAmount.KRW)
	}
	if overview.TotalPurchaseAmount.USD == nil || *overview.TotalPurchaseAmount.USD != "1553" {
		t.Fatalf("totalPurchaseAmount.usd = %v", overview.TotalPurchaseAmount.USD)
	}
	if overview.MarketValue.AmountAfterCost.KRW != "7050000" {
		t.Fatalf("marketValue.amountAfterCost.krw = %q", overview.MarketValue.AmountAfterCost.KRW)
	}
	if overview.ProfitLoss.Rate != "0.1179" {
		t.Fatalf("profitLoss.rate = %q", overview.ProfitLoss.Rate)
	}
	if len(overview.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(overview.Items))
	}
	kr := overview.Items[0]
	if kr.Symbol != "005930" || kr.MarketCountry != "KR" || kr.Currency != "KRW" || kr.Quantity != "100" {
		t.Fatalf("kr item = %+v", kr)
	}
	if kr.Cost.Tax == nil || *kr.Cost.Tax != "135600" {
		t.Fatalf("kr cost.tax = %v, want 135600", kr.Cost.Tax)
	}
	us := overview.Items[1]
	if us.Symbol != "AAPL" || us.Cost.Tax != nil {
		t.Fatalf("us item cost.tax should be nil, got %+v", us.Cost)
	}
}

func TestClient_HoldingsErrorResponse(t *testing.T) {
	srv := tokenAndThen(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"requestId":"01H","code":"account-header-required","message":"x-tossinvest-account 헤더가 필요합니다."}}`))
	})
	defer srv.Close()

	_, err := newClient(srv).Holdings(context.Background(), 42)
	if err == nil {
		t.Fatal("expected error on 400, got nil")
	}
	if !strings.Contains(err.Error(), "account-header-required") {
		t.Fatalf("error %q should name the API error code", err.Error())
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("error %q should include the status code", err.Error())
	}
}
