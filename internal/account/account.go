package account

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/chnu-kim/toss-trade-bot/internal/toss"
)

// getter is the minimal slice of *toss.Client this package needs: an
// authenticated GET whose response body the caller must close. Depending on the
// interface (not the concrete client) keeps the wrapper trivially testable with
// an httptest-backed *toss.Client or a stub.
type getter interface {
	Get(ctx context.Context, path string, opts ...toss.RequestOption) (*http.Response, error)
}

// Client reads account and holdings data over the Toss Open API.
type Client struct {
	api getter
}

// NewClient wraps an authenticated getter (typically *toss.Client).
func NewClient(api getter) *Client {
	return &Client{api: api}
}

// Account is a single brokerage account. AccountSeq feeds the
// X-Tossinvest-Account header of every account-scoped call.
type Account struct {
	AccountNo   string `json:"accountNo"`
	AccountSeq  int64  `json:"accountSeq"`
	AccountType string `json:"accountType"`
}

// CurrencyAmount is a per-currency sum. USD is nil when there are no US
// holdings (the API sends null, not "0"), so it is a pointer.
type CurrencyAmount struct {
	KRW string  `json:"krw"`
	USD *string `json:"usd"`
}

// OverviewMarketValue is the portfolio-wide market value, summed per currency.
type OverviewMarketValue struct {
	Amount          CurrencyAmount `json:"amount"`
	AmountAfterCost CurrencyAmount `json:"amountAfterCost"`
}

// OverviewProfitLoss is the portfolio-wide profit/loss, summed per currency.
type OverviewProfitLoss struct {
	Amount          CurrencyAmount `json:"amount"`
	AmountAfterCost CurrencyAmount `json:"amountAfterCost"`
	Rate            string         `json:"rate"`
	RateAfterCost   string         `json:"rateAfterCost"`
}

// OverviewDailyProfitLoss is the portfolio-wide daily profit/loss.
type OverviewDailyProfitLoss struct {
	Amount CurrencyAmount `json:"amount"`
	Rate   string         `json:"rate"`
}

// HoldingsOverview is the full holdings payload: portfolio summary plus items.
type HoldingsOverview struct {
	TotalPurchaseAmount CurrencyAmount          `json:"totalPurchaseAmount"`
	MarketValue         OverviewMarketValue     `json:"marketValue"`
	ProfitLoss          OverviewProfitLoss      `json:"profitLoss"`
	DailyProfitLoss     OverviewDailyProfitLoss `json:"dailyProfitLoss"`
	Items               []HoldingsItem          `json:"items"`
}

// ItemMarketValue is a single holding's market value, in its trading currency.
type ItemMarketValue struct {
	PurchaseAmount  string `json:"purchaseAmount"`
	Amount          string `json:"amount"`
	AmountAfterCost string `json:"amountAfterCost"`
}

// ItemProfitLoss is a single holding's profit/loss, in its trading currency.
type ItemProfitLoss struct {
	Amount          string `json:"amount"`
	AmountAfterCost string `json:"amountAfterCost"`
	Rate            string `json:"rate"`
	RateAfterCost   string `json:"rateAfterCost"`
}

// ItemDailyProfitLoss is a single holding's daily profit/loss.
type ItemDailyProfitLoss struct {
	Amount string `json:"amount"`
	Rate   string `json:"rate"`
}

// Cost is commission and tax for a holding. Tax is nil when none applies.
type Cost struct {
	Commission string  `json:"commission"`
	Tax        *string `json:"tax"`
}

// HoldingsItem is one held position. Monetary fields are decimal strings to
// preserve precision exactly as the API sends them.
type HoldingsItem struct {
	Symbol               string              `json:"symbol"`
	Name                 string              `json:"name"`
	MarketCountry        string              `json:"marketCountry"`
	Currency             string              `json:"currency"`
	Quantity             string              `json:"quantity"`
	LastPrice            string              `json:"lastPrice"`
	AveragePurchasePrice string              `json:"averagePurchasePrice"`
	MarketValue          ItemMarketValue     `json:"marketValue"`
	ProfitLoss           ItemProfitLoss      `json:"profitLoss"`
	DailyProfitLoss      ItemDailyProfitLoss `json:"dailyProfitLoss"`
	Cost                 Cost                `json:"cost"`
}

// Accounts lists the brokerage accounts (GET /api/v1/accounts). The result is
// empty when the user has no eligible account.
func (c *Client) Accounts(ctx context.Context) ([]Account, error) {
	return fetch[[]Account](ctx, c.api, "/api/v1/accounts")
}

// Holdings returns the holdings overview for accountSeq
// (GET /api/v1/holdings). accountSeq comes from Accounts.
func (c *Client) Holdings(ctx context.Context, accountSeq int64) (HoldingsOverview, error) {
	seq := strconv.FormatInt(accountSeq, 10)
	return fetch[HoldingsOverview](ctx, c.api, "/api/v1/holdings", toss.WithAccount(seq))
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
		return zero, fmt.Errorf("account: decode %s: %w", path, err)
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
		return fmt.Errorf("account: request failed: status %d: %s: %s", resp.StatusCode, er.Error.Code, er.Error.Message)
	}
	return fmt.Errorf("account: request failed: status %d", resp.StatusCode)
}

// drainClose drains and closes a response body so the connection can be reused.
func drainClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
