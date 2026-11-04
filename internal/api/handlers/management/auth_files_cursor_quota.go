package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	cursorauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cursor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

const cursorCurrentPeriodUsagePath = "/aiserver.v1.DashboardService/GetCurrentPeriodUsage"

type cursorPlanUsage struct {
	TotalSpend       float64 `json:"totalSpend"`
	BonusSpend       float64 `json:"bonusSpend"`
	RemainingBonus   bool    `json:"remainingBonus"`
	BonusTooltip     string  `json:"bonusTooltip"`
	AutoPercentUsed  float64 `json:"autoPercentUsed"`
	APIPercentUsed   float64 `json:"apiPercentUsed"`
	TotalPercentUsed float64 `json:"totalPercentUsed"`
}

type cursorSpendLimitUsage struct {
	PooledLimit       json.RawMessage `json:"pooledLimit"`
	PooledRemaining   json.RawMessage `json:"pooledRemaining"`
	IndividualLimit   float64         `json:"individualLimit"`
	LimitType         string          `json:"limitType"`
	OverallLimit      float64         `json:"overallLimit"`
	OverallRemaining  float64         `json:"overallRemaining"`
}

type cursorCurrentPeriodUsage struct {
	BillingCycleStart                  string                 `json:"billingCycleStart"`
	BillingCycleEnd                    string                 `json:"billingCycleEnd"`
	PlanUsage                          *cursorPlanUsage       `json:"planUsage"`
	SpendLimitUsage                    *cursorSpendLimitUsage `json:"spendLimitUsage"`
	DisplayThreshold                   float64                `json:"displayThreshold"`
	DisplayMessage                     string                 `json:"displayMessage"`
	AutoModelSelectedDisplayMessage    string                 `json:"autoModelSelectedDisplayMessage"`
	NamedModelSelectedDisplayMessage   string                 `json:"namedModelSelectedDisplayMessage"`
}

func cursorAPIBase(auth *coreauth.Auth) string {
	base := cursorauth.DefaultAPIBaseURL
	if auth != nil {
		if auth.Attributes != nil {
			if v := strings.TrimSpace(auth.Attributes["api_base_url"]); v != "" {
				base = v
			}
		}
		if auth.Metadata != nil {
			if v, ok := auth.Metadata["api_base_url"].(string); ok && strings.TrimSpace(v) != "" {
				base = strings.TrimSpace(v)
			}
		}
	}
	if v := strings.TrimSpace(os.Getenv("CURSOR_API_URL")); v != "" {
		base = v
	}
	return strings.TrimRight(base, "/")
}

func cursorAccessToken(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["access_token"]); v != "" {
			return v
		}
		if v := strings.TrimSpace(auth.Attributes["api_key"]); v != "" {
			return v
		}
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["access_token"].(string); ok {
			if t := strings.TrimSpace(v); t != "" {
				return t
			}
		}
		if v, ok := auth.Metadata["api_key"].(string); ok {
			if t := strings.TrimSpace(v); t != "" {
				return t
			}
		}
	}
	return ""
}

func cursorMembershipType(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["membership_type"]); v != "" {
			return v
		}
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["membership_type"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (h *Handler) cursorHTTPClient(c *gin.Context, auth *coreauth.Auth) *http.Client {
	proxyURL := ""
	if c != nil {
		proxyURL = oauthSessionProxyURLFromContext(c)
	}
	if proxyURL == "" && auth != nil && strings.TrimSpace(auth.ProxyURL) != "" {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	cfg := h.oauthConfigWithProxy(proxyURL)
	if cfg == nil {
		cfg = &config.Config{}
	}
	return util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 20 * time.Second})
}

func (h *Handler) fetchCursorQuota(c *gin.Context, auth *coreauth.Auth) (pluginapi.QuotaFetchResponse, error) {
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	token := cursorAccessToken(auth)
	if token == "" {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("missing Cursor access token")
	}

	url := cursorAPIBase(auth) + cursorCurrentPeriodUsagePath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("{}"))
	if err != nil {
		return pluginapi.QuotaFetchResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("User-Agent", "connect-es/1.6.1")
	req.Header.Set("X-Cursor-Client-Type", "cli")
	req.Header.Set("X-Cursor-Client-Version", cursorauth.DefaultClientVersion)
	req.Header.Set("X-Ghost-Mode", "true")

	client := h.cursorHTTPClient(c, auth)
	resp, errDo := client.Do(req)
	if errDo != nil {
		return pluginapi.QuotaFetchResponse{}, errDo
	}
	defer resp.Body.Close()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return pluginapi.QuotaFetchResponse{}, errRead
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 240 {
			msg = msg[:240]
		}
		if msg == "" {
			msg = resp.Status
		}
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("cursor GetCurrentPeriodUsage: %s", msg)
	}

	var usage cursorCurrentPeriodUsage
	if errJSON := json.Unmarshal(body, &usage); errJSON != nil {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("decode GetCurrentPeriodUsage: %w", errJSON)
	}

	out := buildCursorQuotaResponse(auth, &usage)
	h.applyCursorQuotaRouting(ctx, auth, out.Groups)
	return out, nil
}

func buildCursorQuotaResponse(auth *coreauth.Auth, usage *cursorCurrentPeriodUsage) pluginapi.QuotaFetchResponse {
	resp := pluginapi.QuotaFetchResponse{}
	if usage == nil {
		return resp
	}

	plan := cursorMembershipType(auth)
	if plan != "" {
		resp.Subscription = &pluginapi.QuotaSubscription{
			Plan:     plan,
			TierName: plan,
			TierID:   plan,
		}
	}

	resetTime := cursorMillisToRFC3339(usage.BillingCycleEnd)
	buckets := make([]pluginapi.QuotaBucket, 0, 3)
	if usage.PlanUsage != nil {
		pu := usage.PlanUsage
		totalDesc := strings.TrimSpace(usage.AutoModelSelectedDisplayMessage)
		if totalDesc == "" {
			totalDesc = fmt.Sprintf("周期总用量：已用 %.0f%%", pu.TotalPercentUsed)
		}
		buckets = append(buckets, pluginapi.QuotaBucket{
			Window:            "total",
			DisplayName:       "周期总用量",
			RemainingFraction: cursorRemainingFraction(pu.TotalPercentUsed),
			ResetTime:         resetTime,
			Description:       totalDesc,
		})

		autoDesc := fmt.Sprintf("Auto 用量：已用 %.0f%%", pu.AutoPercentUsed)
		buckets = append(buckets, pluginapi.QuotaBucket{
			Window:            "auto",
			DisplayName:       "Auto 用量",
			RemainingFraction: cursorRemainingFraction(pu.AutoPercentUsed),
			ResetTime:         resetTime,
			Description:       autoDesc,
		})

		apiDesc := strings.TrimSpace(usage.NamedModelSelectedDisplayMessage)
		if apiDesc == "" {
			apiDesc = strings.TrimSpace(usage.DisplayMessage)
		}
		if apiDesc == "" {
			apiDesc = fmt.Sprintf("API 用量：已用 %.0f%%", pu.APIPercentUsed)
		}
		buckets = append(buckets, pluginapi.QuotaBucket{
			Window:            "api",
			DisplayName:       "API 用量",
			RemainingFraction: cursorRemainingFraction(pu.APIPercentUsed),
			ResetTime:         resetTime,
			Description:       apiDesc,
		})
	}
	if len(buckets) > 0 {
		resp.Groups = []pluginapi.QuotaGroup{{
			DisplayName: "额度限额",
			Buckets:     buckets,
		}}
	}

	summary := make([]pluginapi.QuotaMetric, 0, 6)
	if usage.PlanUsage != nil {
		summary = append(summary,
			pluginapi.QuotaMetric{Key: "total_percent_used", Label: "Total used", Value: usage.PlanUsage.TotalPercentUsed, Unit: "%", Format: "number"},
			pluginapi.QuotaMetric{Key: "auto_percent_used", Label: "Auto used", Value: usage.PlanUsage.AutoPercentUsed, Unit: "%", Format: "number"},
			pluginapi.QuotaMetric{Key: "api_percent_used", Label: "API used", Value: usage.PlanUsage.APIPercentUsed, Unit: "%", Format: "number"},
			pluginapi.QuotaMetric{Key: "total_spend", Label: "Total spend", Value: usage.PlanUsage.TotalSpend, Unit: "units", Format: "number"},
		)
		if usage.PlanUsage.BonusSpend > 0 {
			summary = append(summary, pluginapi.QuotaMetric{
				Key: "bonus_spend", Label: "Bonus spend", Value: usage.PlanUsage.BonusSpend, Unit: "units", Format: "number",
			})
		}
	}
	if usage.DisplayThreshold > 0 {
		summary = append(summary, pluginapi.QuotaMetric{
			Key: "display_threshold", Label: "Display threshold", Value: usage.DisplayThreshold, Unit: "units", Format: "number",
		})
	}
	if usage.SpendLimitUsage != nil && usage.SpendLimitUsage.OverallLimit > 0 {
		summary = append(summary,
			pluginapi.QuotaMetric{Key: "overall_limit", Label: "Spend limit", Value: usage.SpendLimitUsage.OverallLimit, Unit: "units", Format: "number"},
			pluginapi.QuotaMetric{Key: "overall_remaining", Label: "Spend remaining", Value: usage.SpendLimitUsage.OverallRemaining, Unit: "units", Format: "number"},
		)
	}
	if len(summary) > 0 {
		resp.Summary = summary
	}
	return resp
}

func cursorRemainingFraction(percentUsed float64) float64 {
	remaining := 1 - percentUsed/100
	if remaining < 0 {
		return 0
	}
	if remaining > 1 {
		return 1
	}
	// Avoid ugly float noise near 0/1.
	return math.Round(remaining*10000) / 10000
}

func cursorMillisToRFC3339(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// applyCursorQuotaRouting cools credentials when the period-total meter is empty.
// Auto/API buckets alone being 100% used is common on Free and must not disable the auth.
func (h *Handler) applyCursorQuotaRouting(ctx context.Context, auth *coreauth.Auth, groups []pluginapi.QuotaGroup) {
	if h == nil || h.authManager == nil || auth == nil || len(groups) == 0 {
		return
	}
	now := time.Now()
	var soonestReset time.Time
	exhausted := false
	signals := map[string]string{}

	for _, group := range groups {
		for _, bucket := range group.Buckets {
			resetAt := time.Time{}
			if raw := strings.TrimSpace(bucket.ResetTime); raw != "" {
				if parsed, errParse := time.Parse(time.RFC3339, raw); errParse == nil {
					resetAt = parsed
				}
			}
			if strings.EqualFold(strings.TrimSpace(bucket.Window), "total") {
				if !resetAt.IsZero() {
					signals["cursor_billing_cycle_end"] = resetAt.UTC().Format(time.RFC3339)
				}
				if bucket.RemainingFraction <= 0 {
					exhausted = true
					if !resetAt.IsZero() && resetAt.After(now) && (soonestReset.IsZero() || resetAt.Before(soonestReset)) {
						soonestReset = resetAt
					}
				}
			}
		}
	}

	delay := 30 * time.Minute
	if exhausted && !soonestReset.IsZero() {
		if d := soonestReset.Sub(now); d > 0 {
			delay = d
		}
	}
	recoverAt := now.Add(delay)

	_, _, errModify := h.authManager.Modify(ctx, auth.ID, 0, func(current *coreauth.Auth) error {
		if current.Quota.Signals == nil {
			current.Quota.Signals = map[string]string{}
		}
		for key, value := range signals {
			current.Quota.Signals[key] = value
		}
		if plan := cursorMembershipType(current); plan != "" {
			current.Quota.Signals["plan"] = plan
		}
		if exhausted {
			current.Quota.Exceeded = true
			current.Quota.Reason = "credential_quota"
			current.Quota.NextRecoverAt = recoverAt
			current.NextRetryAfter = recoverAt
			current.Unavailable = true
			current.Status = coreauth.StatusError
			current.StatusMessage = "cursor period usage exhausted"
			return nil
		}
		if current.Quota.Exceeded && current.Quota.Reason == "credential_quota" {
			current.Quota.Exceeded = false
			current.Quota.Reason = ""
			current.Quota.NextRecoverAt = time.Time{}
			current.NextRetryAfter = time.Time{}
			current.Unavailable = false
			if current.Status == coreauth.StatusError && strings.Contains(current.StatusMessage, "cursor period usage exhausted") {
				current.Status = coreauth.StatusActive
				current.StatusMessage = ""
			}
		}
		return nil
	})
	if errModify != nil {
		log.WithError(errModify).Warnf("failed to apply Cursor quota routing for %s", auth.ID)
	}
}
