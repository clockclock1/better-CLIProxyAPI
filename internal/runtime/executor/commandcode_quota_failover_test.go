package executor

import (
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCommandCodeStatusErrorScopesQuota(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Quota: cliproxyauth.QuotaState{
			Signals: map[string]string{
				helps.CommandCodeQuotaSignalFiveHourReset: time.Now().Add(90 * time.Minute).UTC().Format(time.RFC3339),
			},
		},
	}
	err := commandCodeStatusError(http.StatusPaymentRequired, []byte(`{"error":{"message":"Out of credits."}}`), nil, auth)
	if !err.IsCredentialScoped() {
		t.Fatal("quota exhaustion must be credential-scoped for sibling failover")
	}
	if err.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 so MarkResult wraps credential_quota", err.StatusCode())
	}
	if err.RetryAfter() == nil || *err.RetryAfter() <= 0 {
		t.Fatalf("expected retry-after from five-hour signal, got %v", err.RetryAfter())
	}
}

func TestCommandCodeStatusErrorKeepsUpgradeRequiredUnscoped(t *testing.T) {
	err := commandCodeStatusError(http.StatusForbidden, []byte(`{"error":{"code":"upgrade_required","message":"out of date"}}`), nil, nil)
	if err.IsCredentialScoped() {
		t.Fatal("upgrade_required must stay on protocol fallback, not credential cooldown")
	}
	if err.StatusCode() != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", err.StatusCode())
	}
}

func TestCommandCodeStreamErrorScopesChineseQuota(t *testing.T) {
	err := commandCodeStreamError("当前请求的额度已用尽", nil)
	if !err.IsCredentialScoped() {
		t.Fatal("stream quota message must be credential-scoped")
	}
	if err.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", err.StatusCode())
	}
}
