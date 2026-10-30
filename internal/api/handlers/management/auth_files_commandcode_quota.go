package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	commandcodeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/commandcode"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// planMonthlyCredits mirrors Command Code harness PLAN_TOTAL_CREDITS.
var commandCodePlanMonthlyCredits = map[string]float64{
	"individual-go":       10,
	"individual-go-v1":    10,
	"individual-goat":     70,
	"individual-pro":      30,
	"individual-pro-v1":   80,
	"individual-provider": 15,
	"individual-max":      150,
	"individual-ultra":    300,
	"teams-pro":           40,
}

var commandCodePlanNames = map[string]string{
	"individual-go":       "Go",
	"individual-go-v1":    "Go",
	"individual-goat":     "GOAT",
	"individual-pro":      "Pro",
	"individual-pro-v1":   "Pro",
	"individual-provider": "Provider",
	"individual-max":      "Max",
	"individual-ultra":    "Ultra",
	"teams-pro":           "Teams Pro",
}

type commandCodeCreditsResponse struct {
	Credits *struct {
		PlanID            string   `json:"planId"`
		MonthlyCredits    float64  `json:"monthlyCredits"`
		PurchasedCredits  float64  `json:"purchasedCredits"`
		FreeCredits       float64  `json:"freeCredits"`
		SandboxMinutes    *float64 `json:"sandboxMinutes"`
		SandboxAccess     bool     `json:"sandboxAccess"`
		WindowLimits      any      `json:"windowLimits"`
	} `json:"credits"`
	PlanID string `json:"planId"`
}

type commandCodeSubscriptionResponse struct {
	Data *struct {
		PlanID            string `json:"planId"`
		Status            string `json:"status"`
		CurrentPeriodEnd  string `json:"currentPeriodEnd"`
	} `json:"data"`
	PlanID string `json:"planId"`
	Status string `json:"status"`
}

type commandCodeUsageSummaryResponse struct {
	TotalCost float64 `json:"totalCost"`
}

type commandCodeWhoamiResponse struct {
	User *struct {
		ID       string `json:"id"`
		UserName string `json:"userName"`
	} `json:"user"`
	Org *struct {
		ID    string `json:"id"`
		Login string `json:"login"`
	} `json:"org"`
}

func commandCodeAPIBase(auth *coreauth.Auth) string {
	base := commandcodeauth.DefaultAPIBaseURL
	if auth != nil {
		if auth.Attributes != nil {
			if v := strings.TrimSpace(auth.Attributes["base_url"]); v != "" {
				base = v
			}
		}
		if auth.Metadata != nil {
			if v, ok := auth.Metadata["base_url"].(string); ok && strings.TrimSpace(v) != "" {
				base = strings.TrimSpace(v)
			}
		}
	}
	if v := strings.TrimSpace(os.Getenv("COMMANDCODE_API_URL")); v != "" {
		base = v
	}
	return strings.TrimRight(base, "/")
}

func commandCodeAPIKey(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["api_key"]); v != "" {
			return v
		}
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["api_key"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func commandCodeCLIVersion(auth *coreauth.Auth) string {
	version := commandcodeauth.DefaultCLIVersion
	if auth != nil && auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["cli_version"]); v != "" {
			version = v
		}
	}
	if v := strings.TrimSpace(os.Getenv("COMMANDCODE_CLI_VERSION")); v != "" {
		version = v
	}
	return version
}

func (h *Handler) commandCodeHTTPClient(c *gin.Context, auth *coreauth.Auth) *http.Client {
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

func (h *Handler) commandCodeGET(ctx context.Context, client *http.Client, auth *coreauth.Auth, path string, out any) error {
	apiKey := commandCodeAPIKey(auth)
	if apiKey == "" {
		return fmt.Errorf("missing Command Code API key")
	}
	url := commandCodeAPIBase(auth) + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cli")
	req.Header.Set("x-command-code-version", commandCodeCLIVersion(auth))
	req.Header.Set("x-cli-environment", "production")

	resp, errDo := client.Do(req)
	if errDo != nil {
		return errDo
	}
	defer resp.Body.Close()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return errRead
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 240 {
			msg = msg[:240]
		}
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("commandcode %s: %s", path, msg)
	}
	if out == nil {
		return nil
	}
	if errJSON := json.Unmarshal(body, out); errJSON != nil {
		return fmt.Errorf("decode %s: %w", path, errJSON)
	}
	return nil
}

func (h *Handler) fetchCommandCodeQuota(c *gin.Context, auth *coreauth.Auth) (pluginapi.QuotaFetchResponse, error) {
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	client := h.commandCodeHTTPClient(c, auth)

	var credits commandCodeCreditsResponse
	if err := h.commandCodeGET(ctx, client, auth, "/alpha/billing/credits", &credits); err != nil {
		return pluginapi.QuotaFetchResponse{}, err
	}

	var subscription commandCodeSubscriptionResponse
	_ = h.commandCodeGET(ctx, client, auth, "/alpha/billing/subscriptions", &subscription)

	var usage commandCodeUsageSummaryResponse
	_ = h.commandCodeGET(ctx, client, auth, "/alpha/usage/summary", &usage)

	monthly := 0.0
	purchased := 0.0
	free := 0.0
	planID := strings.TrimSpace(credits.PlanID)
	if credits.Credits != nil {
		monthly = credits.Credits.MonthlyCredits
		purchased = credits.Credits.PurchasedCredits
		free = credits.Credits.FreeCredits
		if planID == "" {
			planID = strings.TrimSpace(credits.Credits.PlanID)
		}
	}
	subStatus := ""
	periodEnd := ""
	if subscription.Data != nil {
		if planID == "" {
			planID = strings.TrimSpace(subscription.Data.PlanID)
		}
		subStatus = strings.TrimSpace(subscription.Data.Status)
		periodEnd = strings.TrimSpace(subscription.Data.CurrentPeriodEnd)
	} else {
		if planID == "" {
			planID = strings.TrimSpace(subscription.PlanID)
		}
		subStatus = strings.TrimSpace(subscription.Status)
	}

	totalRemaining := monthly + purchased + free
	summary := []pluginapi.QuotaMetric{
		{Key: "monthly_credits", Label: "Monthly credits", Value: monthly, Unit: "credits", Format: "number"},
		{Key: "purchased_credits", Label: "Purchased credits", Value: purchased, Unit: "credits", Format: "number"},
		{Key: "free_credits", Label: "Free credits", Value: free, Unit: "credits", Format: "number"},
		{Key: "total_remaining", Label: "Total remaining", Value: totalRemaining, Unit: "credits", Format: "number"},
	}
	if usage.TotalCost > 0 {
		summary = append(summary, pluginapi.QuotaMetric{
			Key: "total_spent", Label: "Usage spend", Value: usage.TotalCost, Unit: "credits", Format: "number",
		})
	}
	if credits.Credits != nil && credits.Credits.SandboxMinutes != nil {
		summary = append(summary, pluginapi.QuotaMetric{
			Key: "sandbox_minutes", Label: "Sandbox minutes", Value: *credits.Credits.SandboxMinutes, Unit: "min", Format: "number",
		})
	}

	resp := pluginapi.QuotaFetchResponse{Summary: summary}
	planName := commandCodePlanNames[planID]
	if planName == "" && planID != "" {
		planName = planID
	}
	if planName != "" || subStatus != "" {
		resp.Subscription = &pluginapi.QuotaSubscription{
			Plan:     planName,
			TierName: planID,
			TierID:   planID,
		}
	}

	planTotal, hasPlanTotal := commandCodePlanMonthlyCredits[planID]
	if hasPlanTotal && planTotal > 0 && (subStatus == "" || strings.EqualFold(subStatus, "active")) {
		remaining := monthly
		if remaining < 0 {
			remaining = 0
		}
		if remaining > planTotal {
			remaining = planTotal
		}
		fraction := remaining / planTotal
		bucket := pluginapi.QuotaBucket{
			Window:            "monthly",
			RemainingFraction: fraction,
			ResetTime:         periodEnd,
			Description:       fmt.Sprintf("%.1f / %.0f monthly credits remaining", remaining, planTotal),
		}
		resp.Groups = []pluginapi.QuotaGroup{{
			DisplayName: "Plan credits",
			Buckets:     []pluginapi.QuotaBucket{bucket},
		}}
	} else if totalRemaining > 0 || purchased > 0 || free > 0 {
		// No known plan pool: expose remaining as a synthetic full bucket when any credits exist.
		resp.Groups = []pluginapi.QuotaGroup{{
			DisplayName: "Credits",
			Buckets: []pluginapi.QuotaBucket{{
				Window:            "balance",
				RemainingFraction: 1,
				Description:       fmt.Sprintf("%.1f credits remaining", totalRemaining),
			}},
		}}
	}

	return resp, nil
}

// lookupCommandCodeIdentity fetches /alpha/whoami to fill username when importing an API key.
func (h *Handler) lookupCommandCodeIdentity(c *gin.Context, apiKey, apiBase string) (userID, userName string) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "", ""
	}
	if apiBase == "" {
		apiBase = commandcodeauth.DefaultAPIBaseURL
	}
	auth := &coreauth.Auth{
		Attributes: map[string]string{"api_key": apiKey, "base_url": strings.TrimRight(apiBase, "/")},
		Metadata:   map[string]any{"api_key": apiKey, "base_url": strings.TrimRight(apiBase, "/")},
	}
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	client := h.commandCodeHTTPClient(c, auth)
	var whoami commandCodeWhoamiResponse
	if err := h.commandCodeGET(ctx, client, auth, "/alpha/whoami", &whoami); err != nil {
		return "", ""
	}
	if whoami.User != nil {
		userID = strings.TrimSpace(whoami.User.ID)
		userName = strings.TrimSpace(whoami.User.UserName)
	}
	if userName == "" && whoami.Org != nil {
		userName = strings.TrimSpace(whoami.Org.Login)
	}
	return userID, userName
}
