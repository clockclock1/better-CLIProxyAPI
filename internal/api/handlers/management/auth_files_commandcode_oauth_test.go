package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestValidateCommandCodeCallbackURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "http://127.0.0.1:8317/callback", want: "http://127.0.0.1:8317/callback"},
		{in: "http://localhost:18317/callback", want: "http://localhost:18317/callback"},
		{in: "http://127.0.0.1:8317/", want: "http://127.0.0.1:8317/callback"},
		{in: "http://127.0.0.1:8317", want: "http://127.0.0.1:8317/callback"},
		{in: "https://127.0.0.1:8317/callback", wantErr: true},
		{in: "http://186.241.81.108:8317/callback", wantErr: true},
		{in: "http://127.0.0.1:8317/commandcode/callback", wantErr: true},
		{in: "http://127.0.0.1:8317/callback?x=1", wantErr: true},
	}
	for _, tc := range cases {
		got, err := validateCommandCodeCallbackURL(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%q: expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("%q: got %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestRequestCommandCodeTokenHonorsCallbackOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{cfg: &config.Config{Port: 8317, AuthDir: t.TempDir()}}
	router := gin.New()
	router.GET("/commandcode-auth-url", h.RequestCommandCodeToken)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/commandcode-auth-url?callback="+url.QueryEscape("http://127.0.0.1:18317/callback"), nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["callback"] != "http://127.0.0.1:18317/callback" {
		t.Fatalf("callback = %#v", body["callback"])
	}
	authURL, _ := body["url"].(string)
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("callback"); got != "http://127.0.0.1:18317/callback" {
		t.Fatalf("auth url callback = %q", got)
	}

	wBad := httptest.NewRecorder()
	reqBad := httptest.NewRequest(http.MethodGet, "/commandcode-auth-url?callback="+url.QueryEscape("http://example.com/callback"), nil)
	router.ServeHTTP(wBad, reqBad)
	if wBad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d %s", wBad.Code, wBad.Body.String())
	}
}
