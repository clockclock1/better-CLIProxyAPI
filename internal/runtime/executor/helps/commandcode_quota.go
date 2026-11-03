package helps

import (
	"strings"
	"time"
)

// Command Code rolling windows can pin a credential for up to a month; keep
// the stored reset horizon bounded so a bad signal cannot block rotation forever.
const commandCodeQuotaResetMaxDelay = 40 * 24 * time.Hour

// CommandCodeQuotaSignal keys store window reset timestamps on Auth.Quota.Signals
// so a credential-scoped 429 can cool until the exhausted window recovers.
const (
	CommandCodeQuotaSignalFiveHourReset = "five_hour_quota_reset_at"
	CommandCodeQuotaSignalWeeklyReset   = "weekly_quota_reset_at"
	CommandCodeQuotaSignalMonthlyReset  = "monthly_quota_reset_at"
)

// CommandCodeQuotaExhausted reports whether an upstream status/body indicates
// account-wide Command Code quota or billing refusal. Matching follows the
// desktop PROVIDER_BILLING_REFUSED classifier plus window-limit phrasing so a
// single exhausted Go account cools and rotation can move to siblings.
func CommandCodeQuotaExhausted(status int, message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if status == 402 {
		return true
	}
	if lower == "" {
		return false
	}
	if strings.Contains(lower, "upgrade_required") {
		// Protocol/version rejection must stay on the transport fallback path,
		// not credential cooldown.
		return false
	}
	patterns := [...]string{
		"insufficient credits",
		"insufficient_credits",
		"insufficient balance",
		"insufficient funds",
		"insufficient quota",
		"insufficient_quota",
		"payment required",
		"payment_required",
		"exceeded your current quota",
		"out of credits",
		"credit balance is too low",
		"quota exhausted",
		"quota_exceeded",
		"usage limit",
		"usage_limit",
		"rate limit reached",
		"window limit",
		"windowlimits",
		"five hour",
		"five_hour",
		"额度已用尽",
		"额度已用完",
		"余额不足",
		"当前请求的额度已用尽",
	}
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	if status == 429 {
		if strings.Contains(lower, "quota") || strings.Contains(lower, "credit") || strings.Contains(lower, "limit") {
			return true
		}
	}
	return false
}

// CommandCodeQuotaRetryAfter picks the soonest plausible reset among stored
// window signals. Returns nil when no usable future reset is present.
func CommandCodeQuotaRetryAfter(signals map[string]string, now time.Time) *time.Duration {
	if len(signals) == 0 {
		return nil
	}
	keys := [...]string{
		CommandCodeQuotaSignalFiveHourReset,
		CommandCodeQuotaSignalWeeklyReset,
		CommandCodeQuotaSignalMonthlyReset,
	}
	var best *time.Duration
	for _, key := range keys {
		raw := strings.TrimSpace(signals[key])
		if raw == "" {
			continue
		}
		resetAt, errParse := time.Parse(time.RFC3339, raw)
		if errParse != nil {
			continue
		}
		delay := resetAt.Sub(now)
		if delay <= 0 || delay > commandCodeQuotaResetMaxDelay {
			continue
		}
		if best == nil || delay < *best {
			d := delay
			best = &d
		}
	}
	return best
}
