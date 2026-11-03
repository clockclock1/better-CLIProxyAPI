package helps

import (
	"testing"
	"time"
)

func TestCommandCodeQuotaExhausted(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		message string
		want    bool
	}{
		{name: "402 billing", status: 402, message: "", want: true},
		{name: "out of credits", status: 403, message: `{"error":{"message":"Out of credits."}}`, want: true},
		{name: "insufficient_quota 429", status: 429, message: "HTTP 429: insufficient_quota", want: true},
		{name: "chinese quota", status: 200, message: "当前请求的额度已用尽", want: true},
		{name: "five hour window", status: 429, message: "five_hour window limit exceeded", want: true},
		{name: "upgrade_required stays transport", status: 403, message: `{"error":{"code":"upgrade_required"}}`, want: false},
		{name: "generic 500", status: 500, message: "internal server error", want: false},
		{name: "empty 429 without quota words", status: 429, message: "", want: false},
		{name: "model busy", status: 503, message: "The model is busy.", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CommandCodeQuotaExhausted(tc.status, tc.message); got != tc.want {
				t.Fatalf("CommandCodeQuotaExhausted(%d, %q) = %v, want %v", tc.status, tc.message, got, tc.want)
			}
		})
	}
}

func TestCommandCodeQuotaRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	five := now.Add(2 * time.Hour).Format(time.RFC3339)
	weekly := now.Add(48 * time.Hour).Format(time.RFC3339)

	got := CommandCodeQuotaRetryAfter(map[string]string{
		CommandCodeQuotaSignalFiveHourReset: five,
		CommandCodeQuotaSignalWeeklyReset:   weekly,
	}, now)
	if got == nil || *got != 2*time.Hour {
		t.Fatalf("expected soonest five-hour delay, got %v", got)
	}

	if CommandCodeQuotaRetryAfter(map[string]string{
		CommandCodeQuotaSignalMonthlyReset: now.Add(-time.Minute).Format(time.RFC3339),
	}, now) != nil {
		t.Fatal("past reset must be ignored")
	}
}
