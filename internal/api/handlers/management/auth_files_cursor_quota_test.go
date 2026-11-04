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

func TestBuildAuthFileEntry_CursorSupportsQuota(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	authRec := &coreauth.Auth{
		ID:       "cursor-user.json",
		FileName: "cursor-user.json",
		Provider: "cursor",
		Label:    "Cursor (user)",
		Attributes: map[string]string{
			"path": "/tmp/cursor-user.json",
		},
	}
	entry := h.buildAuthFileEntry(authRec, authFileQuotaContext{})
	if entry == nil {
		t.Fatal("expected auth file entry")
	}
	if entry["supports_quota"] != true {
		t.Fatalf("supports_quota = %#v, want true", entry["supports_quota"])
	}
	if entry["quota_provider"] != "cursor" {
		t.Fatalf("quota_provider = %#v", entry["quota_provider"])
	}
	if _, ok := entry["quota_plugin_id"]; ok {
		t.Fatal("v0 auth files should not expose quota_plugin_id for builtin cursor")
	}

	v8Entry := h.buildAuthFileEntry(authRec, authFileQuotaContext{v8: true})
	if v8Entry["quota_plugin_id"] != builtinCursorQuotaPluginID {
		t.Fatalf("v8 quota_plugin_id = %#v, want %s", v8Entry["quota_plugin_id"], builtinCursorQuotaPluginID)
	}
}

func TestFetchPluginQuota_CursorBuiltin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer cursor-test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !strings.HasSuffix(r.URL.Path, cursorCurrentPeriodUsagePath) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"billingCycleStart":"1793446533182",
			"billingCycleEnd":"1796038533182",
			"planUsage":{"totalSpend":106,"bonusSpend":106,"autoPercentUsed":100,"apiPercentUsed":0,"totalPercentUsed":53},
			"displayThreshold":200,
			"displayMessage":"You've used 0% of your included usage",
			"autoModelSelectedDisplayMessage":"You've used 53% of your included total usage",
			"namedModelSelectedDisplayMessage":"You've used 0% of your included API usage"
		}`))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	authRec := &coreauth.Auth{
		ID:       "cursor-plugin-route.json",
		FileName: "cursor-plugin-route.json",
		Provider: "cursor",
		Metadata: map[string]any{
			"access_token":  "cursor-test-token",
			"api_base_url":  upstream.URL,
			"membership_type": "free",
		},
		Attributes: map[string]string{
			"access_token":  "cursor-test-token",
			"api_base_url":  upstream.URL,
			"membership_type": "free",
		},
	}
	authIndex := authRec.EnsureIndex()
	_, _ = manager.Register(context.Background(), authRec)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.SetPluginHost(pluginhost.New())

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{{Key: "id", Value: builtinCursorQuotaPluginID}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/plugins/"+builtinCursorQuotaPluginID+"/quota?auth_index="+authIndex, nil)
	h.GetPluginQuota(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var quotaResp pluginapi.QuotaFetchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &quotaResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if quotaResp.Subscription == nil || quotaResp.Subscription.Plan != "free" {
		t.Fatalf("subscription = %#v", quotaResp.Subscription)
	}
	if len(quotaResp.Groups) != 1 || len(quotaResp.Groups[0].Buckets) != 3 {
		t.Fatalf("groups = %#v", quotaResp.Groups)
	}
	total := quotaResp.Groups[0].Buckets[0]
	if total.Window != "total" || total.RemainingFraction != 0.47 {
		t.Fatalf("total bucket = %#v", total)
	}
}

func TestBuildCursorQuotaResponse_Fractions(t *testing.T) {
	authRec := &coreauth.Auth{
		Metadata: map[string]any{"membership_type": "free"},
	}
	usage := &cursorCurrentPeriodUsage{
		BillingCycleEnd: "1796038533182",
		PlanUsage: &cursorPlanUsage{
			TotalPercentUsed: 53,
			AutoPercentUsed:  100,
			APIPercentUsed:   0,
			TotalSpend:       106,
		},
		DisplayMessage: "ok",
	}
	resp := buildCursorQuotaResponse(authRec, usage)
	if len(resp.Groups) != 1 || len(resp.Groups[0].Buckets) != 3 {
		t.Fatalf("unexpected groups: %#v", resp.Groups)
	}
	if got := resp.Groups[0].Buckets[0].RemainingFraction; got != 0.47 {
		t.Fatalf("total remaining = %v", got)
	}
	if got := resp.Groups[0].Buckets[1].RemainingFraction; got != 0 {
		t.Fatalf("auto remaining = %v", got)
	}
	if got := resp.Groups[0].Buckets[2].RemainingFraction; got != 1 {
		t.Fatalf("api remaining = %v", got)
	}
}
