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
			_, _ = w.Write([]byte(`{"credits":{"planId":"individual-go","monthlyCredits":7.5,"purchasedCredits":2,"freeCredits":0.5}}`))
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
	if len(quotaResp.Summary) < 4 {
		t.Fatalf("expected credit summary metrics, got %+v", quotaResp.Summary)
	}
	if len(quotaResp.Groups) != 1 || len(quotaResp.Groups[0].Buckets) != 1 {
		t.Fatalf("unexpected groups: %+v", quotaResp.Groups)
	}
	if got := quotaResp.Groups[0].Buckets[0].RemainingFraction; got != 0.75 {
		t.Fatalf("remaining fraction = %v, want 0.75", got)
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
}
