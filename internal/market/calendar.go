package market

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

// --- KR (KRX+NXT integrated) calendar ---

// KrSession is a KR trading session. The single-price-auction field is nil when
// that sub-window is absent; the open/close helpers use only Start/EndTime.
type KrSession struct {
	StartTime                   string  `json:"startTime"`
	SinglePriceAuctionStartTime *string `json:"singlePriceAuctionStartTime,omitempty"`
	SinglePriceAuctionEndTime   *string `json:"singlePriceAuctionEndTime,omitempty"`
	EndTime                     string  `json:"endTime"`
}

// IntegratedHour holds the three KR sessions. Each is nil when that session is
// closed; when all three are closed the API sends integrated: null.
type IntegratedHour struct {
	PreMarket     *KrSession `json:"preMarket"`
	RegularMarket *KrSession `json:"regularMarket"`
	AfterMarket   *KrSession `json:"afterMarket"`
}

// KrMarketDay is one KR business day. Integrated is nil on a full holiday.
type KrMarketDay struct {
	Date       string          `json:"date"`
	Integrated *IntegratedHour `json:"integrated"`
}

// KrMarketCalendar returns the previous/current/next KR business days.
type KrMarketCalendar struct {
	Today               KrMarketDay `json:"today"`
	PreviousBusinessDay KrMarketDay `json:"previousBusinessDay"`
	NextBusinessDay     KrMarketDay `json:"nextBusinessDay"`
}

// IsTradingDay reports whether the KR market trades at all this day. A full
// holiday sends integrated: null.
func (d KrMarketDay) IsTradingDay() bool {
	return d.Integrated != nil
}

// IsRegularOpen reports whether at falls inside the KR regular session
// [start, end). A holiday or a missing regular session is always closed.
func (d KrMarketDay) IsRegularOpen(at time.Time) (bool, error) {
	if d.Integrated == nil || d.Integrated.RegularMarket == nil {
		return false, nil
	}
	return withinSession(d.Integrated.RegularMarket.StartTime, d.Integrated.RegularMarket.EndTime, at)
}

// --- US calendar ---

// UsSession is a US trading session (day/pre/regular/after all share this
// shape).
type UsSession struct {
	StartTime string `json:"startTime"`
	EndTime   string `json:"endTime"`
}

// UsMarketDay is one US business day. Every session is nil on a holiday.
type UsMarketDay struct {
	Date          string     `json:"date"`
	DayMarket     *UsSession `json:"dayMarket"`
	PreMarket     *UsSession `json:"preMarket"`
	RegularMarket *UsSession `json:"regularMarket"`
	AfterMarket   *UsSession `json:"afterMarket"`
}

// UsMarketCalendar returns the previous/current/next US business days.
type UsMarketCalendar struct {
	Today               UsMarketDay `json:"today"`
	PreviousBusinessDay UsMarketDay `json:"previousBusinessDay"`
	NextBusinessDay     UsMarketDay `json:"nextBusinessDay"`
}

// IsTradingDay reports whether the US market has a regular session this day. A
// holiday sends all four sessions null.
func (d UsMarketDay) IsTradingDay() bool {
	return d.RegularMarket != nil
}

// IsRegularOpen reports whether at falls inside the US regular session
// [start, end). A holiday or a missing regular session is always closed.
func (d UsMarketDay) IsRegularOpen(at time.Time) (bool, error) {
	if d.RegularMarket == nil {
		return false, nil
	}
	return withinSession(d.RegularMarket.StartTime, d.RegularMarket.EndTime, at)
}

// KrCalendar fetches the KR trading calendar (GET /api/v1/market-calendar/KR).
// date (YYYY-MM-DD) is optional; pass "" for the current day.
func (c *Client) KrCalendar(ctx context.Context, date string) (KrMarketCalendar, error) {
	return fetch[KrMarketCalendar](ctx, c.api, calendarPath("KR", date))
}

// UsCalendar fetches the US trading calendar (GET /api/v1/market-calendar/US).
// date (YYYY-MM-DD, US local) is optional; pass "" for the current day.
func (c *Client) UsCalendar(ctx context.Context, date string) (UsMarketCalendar, error) {
	return fetch[UsMarketCalendar](ctx, c.api, calendarPath("US", date))
}

func calendarPath(country, date string) string {
	path := "/api/v1/market-calendar/" + country
	if date != "" {
		path += "?" + url.Values{"date": {date}}.Encode()
	}
	return path
}

// withinSession parses the RFC3339 bounds and reports whether at is in
// [start, end). Comparison is on absolute instants, so differing zone offsets
// (e.g. +09:00 vs Z) are handled correctly.
func withinSession(start, end string, at time.Time) (bool, error) {
	s, err := time.Parse(time.RFC3339, start)
	if err != nil {
		return false, fmt.Errorf("market: parse session start %q: %w", start, err)
	}
	e, err := time.Parse(time.RFC3339, end)
	if err != nil {
		return false, fmt.Errorf("market: parse session end %q: %w", end, err)
	}
	return !at.Before(s) && at.Before(e), nil
}
