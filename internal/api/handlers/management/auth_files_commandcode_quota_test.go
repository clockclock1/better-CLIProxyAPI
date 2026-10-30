package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestFetchCredentialQuota_CommandCodeBuiltin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer cc-test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("x-command-code-version"); got == "" {
			http.Error(w, "missing version", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/alpha/billing/credits"):
			_, _ = w.Write([]byte(`{"credits":{"planId":"individual-go","monthlyCredits":7.5,"purchasedCredits":0,"freeCredits":0},"windowLimits":{"limited":true,"fiveHour":{"used":0.3,"cap":3,"exceeded":false,"resetAt":1761800000000},"weekly":{"used":1.5,"cap":6,"exceeded":false,"resetAt":1762000000000}}}`))
		case strings.HasSuffix(r.URL.Path, "/alpha/billing/subscriptions"):
			_, _ = w.Write([]byte(`{"data":{"planId":"individual-go","status":"active","currentPeriodEnd":"2026-11-01T00:00:00Z"}}`))
		case strings.HasSuffix(r.URL.Path, "/alpha/usage/summary"):
			_, _ = w.Write([]byte(`{"totalCost":1.25}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "commandcode-test.json",
		FileName: "commandcode-test.json",
		Provider: "commandcode",
		Metadata: map[string]any{
			"api_key":  "cc-test-key",
			"base_url": upstream.URL,
		},
		Attributes: map[string]string{
			"api_key":  "cc-test-key",
			"base_url": upstream.URL,
		},
	}
	authIndex := auth.EnsureIndex()
	_, _ = manager.Register(context.Background(), auth)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.SetPluginHost(pluginhost.New())

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/quota/fetch", strings.NewReader(`{"auth_index":"`+authIndex+`"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.FetchCredentialQuota(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var quotaResp pluginapi.QuotaFetchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &quotaResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if quotaResp.Subscription == nil || quotaResp.Subscription.Plan != "Go" {
		t.Fatalf("unexpected subscription: %+v", quotaResp.Subscription)
	}
	if len(quotaResp.Summary) != 0 {
		t.Fatalf("window-limit meters should not emit credit pills, got %+v", quotaResp.Summary)
	}
	if len(quotaResp.Groups) != 1 || len(quotaResp.Groups[0].Buckets) != 3 {
		t.Fatalf("expected five_hour+weekly+monthly buckets, got %+v", quotaResp.Groups)
	}
	gotWindows := []string{
		quotaResp.Groups[0].Buckets[0].Window,
		quotaResp.Groups[0].Buckets[1].Window,
		quotaResp.Groups[0].Buckets[2].Window,
	}
	if gotWindows[0] != "five_hour" || gotWindows[1] != "weekly" || gotWindows[2] != "monthly" {
		t.Fatalf("windows = %#v", gotWindows)
	}
	if got := quotaResp.Groups[0].Buckets[0].RemainingFraction; got < 0.89 || got > 0.91 {
		t.Fatalf("five_hour remaining = %v, want ~0.9", got)
	}
	if got := quotaResp.Groups[0].Buckets[1].RemainingFraction; got < 0.74 || got > 0.76 {
		t.Fatalf("weekly remaining = %v, want ~0.75", got)
	}
	if got := quotaResp.Groups[0].Buckets[2].RemainingFraction; got != 0.75 {
		t.Fatalf("monthly remaining = %v, want 0.75", got)
	}
}

func TestBuildAuthFileEntry_CommandCodeSupportsQuota(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	auth := &coreauth.Auth{
		ID:       "commandcode-user.json",
		FileName: "commandcode-user.json",
		Provider: "commandcode",
		Label:    "Command Code (user)",
		Attributes: map[string]string{
			"path": "/tmp/commandcode-user.json",
		},
	}
	entry := h.buildAuthFileEntry(auth, authFileQuotaContext{})
	if entry == nil {
		t.Fatal("expected auth file entry")
	}
	if entry["supports_quota"] != true {
		t.Fatalf("supports_quota = %#v, want true", entry["supports_quota"])
	}
	if entry["quota_provider"] != "commandcode" {
		t.Fatalf("quota_provider = %#v", entry["quota_provider"])
	}
	if _, ok := entry["quota_plugin_id"]; ok {
		t.Fatal("v0 auth files should not expose quota_plugin_id for builtin commandcode")
	}

	v8Entry := h.buildAuthFileEntry(auth, authFileQuotaContext{v8: true})
	if v8Entry["quota_plugin_id"] != builtinCommandCodeQuotaPluginID {
		t.Fatalf("v8 quota_plugin_id = %#v, want %s", v8Entry["quota_plugin_id"], builtinCommandCodeQuotaPluginID)
	}
}

func TestFetchPluginQuota_CommandCodeBuiltin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer cc-test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/alpha/billing/credits"):
			_, _ = w.Write([]byte(`{"credits":{"planId":"individual-pro","monthlyCredits":24,"purchasedCredits":0,"freeCredits":0}}`))
		case strings.HasSuffix(r.URL.Path, "/alpha/billing/subscriptions"):
			_, _ = w.Write([]byte(`{"data":{"planId":"individual-pro","status":"active","currentPeriodEnd":"2026-11-01T00:00:00Z"}}`))
		case strings.HasSuffix(r.URL.Path, "/alpha/usage/summary"):
			_, _ = w.Write([]byte(`{"totalCost":6}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "commandcode-plugin-route.json",
		FileName: "commandcode-plugin-route.json",
		Provider: "commandcode",
		Metadata: map[string]any{
			"api_key":  "cc-test-key",
			"base_url": upstream.URL,
		},
		Attributes: map[string]string{
			"api_key":  "cc-test-key",
			"base_url": upstream.URL,
		},
	}
	authIndex := auth.EnsureIndex()
	_, _ = manager.Register(context.Background(), auth)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	// No quota plugin registered — builtin path must work without plugin host providers.
	h.SetPluginHost(pluginhost.New())

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{{Key: "id", Value: builtinCommandCodeQuotaPluginID}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/plugins/"+builtinCommandCodeQuotaPluginID+"/quota?auth_index="+authIndex, nil)
	h.GetPluginQuota(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var quotaResp pluginapi.QuotaFetchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &quotaResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if quotaResp.Subscription == nil || quotaResp.Subscription.Plan != "Pro" {
		t.Fatalf("unexpected subscription: %+v", quotaResp.Subscription)
	}
}
