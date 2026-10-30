package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestImportCommandCodeAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir, Port: 8317}, nil)
	router := gin.New()
	router.POST("/commandcode-import", h.ImportCommandCodeAPIKey)
	router.POST("/oauth/import", h.ImportOAuthV8)

	body, _ := json.Marshal(map[string]string{"api_key": "cc-test-key-123456"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/commandcode-import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("import status %d body %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("resp = %#v", resp)
	}
	state, _ := resp["state"].(string)
	if state == "" {
		t.Fatalf("import must return state for Management UI polling, got %#v", resp)
	}
	_, _, _, _, completed, okSession := GetOAuthSessionDetails(state)
	if !okSession || !completed {
		t.Fatalf("expected completed oauth session, ok=%v completed=%v", okSession, completed)
	}
	router.GET("/get-auth-status", h.GetAuthStatus)
	wStatus := httptest.NewRecorder()
	reqStatus := httptest.NewRequest(http.MethodGet, "/get-auth-status?state="+state, nil)
	router.ServeHTTP(wStatus, reqStatus)
	if wStatus.Code != http.StatusOK || !strings.Contains(wStatus.Body.String(), `"status":"ok"`) {
		t.Fatalf("auth status after import: %d %s", wStatus.Code, wStatus.Body.String())
	}

	w2 := httptest.NewRecorder()
	body2, _ := json.Marshal(map[string]string{"api_key": "cc-test-key-v8"})
	req2 := httptest.NewRequest(http.MethodPost, "/oauth/import?provider=commandcode", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("v8 import status %d body %s", w2.Code, w2.Body.String())
	}
}
