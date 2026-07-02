package market

import (
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

func TestKrMarketDay_IsRegularOpen(t *testing.T) {
	day := KrMarketDay{
		Date: "2026-03-25",
		Integrated: &IntegratedHour{
			RegularMarket: &KrSession{
				StartTime: "2026-03-25T09:00:00+09:00",
				EndTime:   "2026-03-25T15:30:00+09:00",
			},
		},
	}

	cases := []struct {
		name string
		at   string
		want bool
	}{
		{"before open", "2026-03-25T08:59:00+09:00", false},
		{"at open (inclusive)", "2026-03-25T09:00:00+09:00", true},
		{"midday", "2026-03-25T12:00:00+09:00", true},
		{"at close (exclusive)", "2026-03-25T15:30:00+09:00", false},
		{"after close", "2026-03-25T16:00:00+09:00", false},
		// Same instant expressed in another offset must compare correctly.
		{"midday in UTC", "2026-03-25T03:00:00Z", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			open, err := day.IsRegularOpen(mustTime(t, tc.at))
			if err != nil {
				t.Fatalf("IsRegularOpen: %v", err)
			}
			if open != tc.want {
				t.Fatalf("IsRegularOpen(%s) = %v, want %v", tc.at, open, tc.want)
			}
		})
	}
}

func TestKrMarketDay_IsRegularOpenHolidayIsClosed(t *testing.T) {
	day := KrMarketDay{Date: "2026-05-05", Integrated: nil}
	open, err := day.IsRegularOpen(mustTime(t, "2026-05-05T12:00:00+09:00"))
	if err != nil {
		t.Fatalf("IsRegularOpen: %v", err)
	}
	if open {
		t.Fatal("holiday must never be open")
	}
}

func TestUsMarketDay_IsRegularOpen(t *testing.T) {
	day := UsMarketDay{
		Date: "2026-03-25",
		RegularMarket: &UsSession{
			StartTime: "2026-03-25T22:30:00+09:00",
			EndTime:   "2026-03-26T05:00:00+09:00",
		},
	}
	open, err := day.IsRegularOpen(mustTime(t, "2026-03-26T00:00:00+09:00"))
	if err != nil {
		t.Fatalf("IsRegularOpen: %v", err)
	}
	if !open {
		t.Fatal("00:00 KST should be within the US regular session")
	}

	closed, err := day.IsRegularOpen(mustTime(t, "2026-03-26T06:00:00+09:00"))
	if err != nil {
		t.Fatalf("IsRegularOpen: %v", err)
	}
	if closed {
		t.Fatal("06:00 KST is after the US regular session")
	}
}

func TestUsMarketDay_IsRegularOpenHolidayIsClosed(t *testing.T) {
	day := UsMarketDay{Date: "2026-07-03"}
	open, err := day.IsRegularOpen(mustTime(t, "2026-07-04T00:00:00+09:00"))
	if err != nil {
		t.Fatalf("IsRegularOpen: %v", err)
	}
	if open {
		t.Fatal("US holiday must never be open")
	}
}
