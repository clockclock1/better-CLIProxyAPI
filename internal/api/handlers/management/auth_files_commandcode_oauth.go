package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	commandcodeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/commandcode"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	log "github.com/sirupsen/logrus"
)

// CommandCodeCallbackPayload is the studio/auth/cli redirect or POST body.
type CommandCodeCallbackPayload struct {
	APIKey    string `json:"apiKey"`
	State     string `json:"state"`
	UserID    string `json:"userId"`
	UserName  string `json:"userName"`
	KeyName   string `json:"keyName"`
	Error     string `json:"error"`
	ErrorDesc string `json:"error_description,omitempty"`
}

// commandCodeCallbackURL builds the default loopback callback used by studio/auth/cli.
// Command Code studio only accepts http://127.0.0.1:<port>/callback (or localhost).
func (h *Handler) commandCodeCallbackURL() (string, error) {
	cfg := h.configSnapshot()
	if h == nil || cfg == nil || cfg.Port <= 0 {
		return "", fmt.Errorf("server port is not configured")
	}
	return fmt.Sprintf("http://127.0.0.1:%d/callback", cfg.Port), nil
}

// validateCommandCodeCallbackURL ensures the callback is a Command Code–accepted loopback URL.
func validateCommandCodeCallbackURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("callback is required")
	}
	u, errParse := url.Parse(raw)
	if errParse != nil {
		return "", fmt.Errorf("invalid callback URL")
	}
	if !strings.EqualFold(u.Scheme, "http") {
		return "", fmt.Errorf("callback must use http://localhost or http://127.0.0.1")
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host != "127.0.0.1" && host != "localhost" {
		return "", fmt.Errorf("callback host must be localhost or 127.0.0.1")
	}
	port := u.Port()
	if port != "" {
		if _, errPort := net.LookupPort("tcp", port); errPort != nil {
			return "", fmt.Errorf("invalid callback port")
		}
	}
	path := u.EscapedPath()
	if path == "" || path == "/" {
		path = "/callback"
	}
	if path != "/callback" {
		return "", fmt.Errorf("callback path must be /callback")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("callback must not include query or fragment")
	}
	if port != "" {
		return fmt.Sprintf("http://%s:%s/callback", host, port), nil
	}
	return fmt.Sprintf("http://%s/callback", host), nil
}

func commandCodeCallbackOverride(c *gin.Context) string {
	if c == nil {
		return ""
	}
	for _, key := range []string{"callback", "redirect_uri", "redirect_url"} {
		if v := strings.TrimSpace(c.Query(key)); v != "" {
			return v
		}
	}
	return strings.TrimSpace(os.Getenv("COMMANDCODE_CALLBACK_URL"))
}

// ImportCommandCodeAPIKey saves a manually provided Command Code API key.
// Remote deployments should prefer this: studio posts credentials to localhost,
// so a browser on another machine cannot complete the loopback callback.
func (h *Handler) ImportCommandCodeAPIKey(c *gin.Context) {
	var body struct {
		APIKey   string `json:"api_key"`
		APIKeyAlt string `json:"apiKey"`
		UserID   string `json:"user_id"`
		UserName string `json:"user_name"`
		KeyName  string `json:"key_name"`
	}
	if c.Request != nil && c.Request.Body != nil {
		_ = c.ShouldBindJSON(&body)
	}
	apiKey := strings.TrimSpace(firstNonEmpty(body.APIKey, body.APIKeyAlt, c.Query("api_key"), c.Query("apiKey")))
	if apiKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "api_key is required",
			"hint":  "Paste the apiKey from https://commandcode.ai or from ~/.commandcode/auth.json on a machine that already ran cmdc login.",
		})
		return
	}
	apiBase := strings.TrimSpace(os.Getenv("COMMANDCODE_API_URL"))
	if apiBase == "" {
		apiBase = commandcodeauth.DefaultAPIBaseURL
	}
	keyName := strings.TrimSpace(firstNonEmpty(body.KeyName, c.Query("key_name"), "manual"))
	record, errRecord := commandcodeauth.CreateAuthRecord(&commandcodeauth.CallbackPayload{
		APIKey:   apiKey,
		UserID:   strings.TrimSpace(firstNonEmpty(body.UserID, c.Query("user_id"))),
		UserName: strings.TrimSpace(firstNonEmpty(body.UserName, c.Query("user_name"))),
		KeyName:  keyName,
	}, apiBase)
	if errRecord != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errRecord.Error()})
		return
	}
	ctx := PopulateAuthContext(context.Background(), c)
	savedPath, errSave := h.saveTokenRecord(ctx, record)
	if errSave != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save authentication tokens"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":   "ok",
		"path":     savedPath,
		"filename": record.FileName,
		"label":    record.Label,
	})
}

// RequestCommandCodeToken starts Command Code browser OAuth for Management / TUI.
// Optional query: callback / redirect_uri / redirect_url (must be http://127.0.0.1|localhost[:port]/callback).
func (h *Handler) RequestCommandCodeToken(c *gin.Context) {
	defaultCallback, errCallback := h.commandCodeCallbackURL()
	if errCallback != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
		return
	}
	rawCallback := commandCodeCallbackOverride(c)
	if rawCallback == "" {
		rawCallback = defaultCallback
	}
	callbackURL, errValidate := validateCommandCodeCallbackURL(rawCallback)
	if errValidate != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":            errValidate.Error(),
			"default_callback": defaultCallback,
			"hint":             "Use a localhost loopback URL, e.g. http://127.0.0.1:8317/callback (SSH tunnel: use the local forwarded port).",
		})
		return
	}

	state, errState := misc.GenerateRandomState()
	if errState != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	sessionProxyURL := oauthSessionProxyURLFromContext(c)
	cfg := h.oauthConfigWithProxy(sessionProxyURL)
	studioBase := strings.TrimSpace(os.Getenv("COMMANDCODE_STUDIO_URL"))
	if studioBase == "" {
		studioBase = commandcodeauth.DefaultStudioBaseURL
	}
	authURL := commandcodeauth.BuildAuthURL(studioBase, callbackURL, state, "cli")
	RegisterOAuthSessionWithProxy(state, "commandcode", sessionProxyURL)

	ctx := PopulateAuthContext(context.Background(), c)
	authDir := ""
	if cfg != nil {
		authDir = cfg.AuthDir
	}
	go h.completeCommandCodeOAuth(ctx, authDir, state)
	c.JSON(http.StatusOK, gin.H{
		"status":   "ok",
		"url":      authURL,
		"state":    state,
		"callback": callbackURL,
	})
}

func (h *Handler) completeCommandCodeOAuth(ctx context.Context, authDir, state string) {
	sessionProxyURL := GetOAuthSessionProxyURL(state)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	go watchOAuthSessionCancel(ctx, cancel, state, "commandcode")

	waitFile := filepath.Join(authDir, fmt.Sprintf(".oauth-commandcode-%s.oauth", state))
	defer func() {
		if errRemove := os.Remove(waitFile); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			log.Warn("failed to remove Command Code OAuth callback file")
		}
	}()

	result, errWait := waitCommandCodeOAuthCallback(ctx, waitFile, state)
	if errWait != nil {
		if IsOAuthSessionPending(state, "commandcode") {
			SetOAuthSessionError(state, errWait.Error())
		}
		return
	}
	if !IsOAuthSessionPending(state, "commandcode") {
		return
	}
	if result.State != state {
		SetOAuthSessionError(state, "State code error")
		return
	}
	if result.Error != "" {
		msg := result.Error
		if result.ErrorDesc != "" {
			msg = result.ErrorDesc
		}
		SetOAuthSessionError(state, msg)
		return
	}
	if strings.TrimSpace(result.APIKey) == "" {
		SetOAuthSessionError(state, "Missing API key from Command Code callback")
		return
	}

	apiBase := strings.TrimSpace(os.Getenv("COMMANDCODE_API_URL"))
	if apiBase == "" {
		apiBase = commandcodeauth.DefaultAPIBaseURL
	}
	record, errRecord := commandcodeauth.CreateAuthRecord(&commandcodeauth.CallbackPayload{
		APIKey:   result.APIKey,
		State:    result.State,
		UserID:   result.UserID,
		UserName: result.UserName,
		KeyName:  result.KeyName,
	}, apiBase)
	if errRecord != nil {
		if IsOAuthSessionPending(state, "commandcode") {
			SetOAuthSessionError(state, "Failed to create Command Code authentication record")
		}
		return
	}
	applySessionProxyToAuthRecord(record, sessionProxyURL)
	if errGuard := claimOAuthSessionCommit(state, "commandcode"); errGuard != nil {
		return
	}
	if _, errSave := h.saveTokenRecord(ctx, record); errSave != nil {
		FailOAuthSessionCommit(state, "Failed to save authentication tokens")
		return
	}
	CompleteOAuthSession(state)
	log.Info("Command Code authentication successful")
}

func waitCommandCodeOAuthCallback(ctx context.Context, path, state string) (*CommandCodeCallbackPayload, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !IsOAuthSessionPending(state, "commandcode") {
			return nil, errOAuthSessionNotPending
		}
		if ctx.Err() != nil {
			return nil, errors.New("Timeout waiting for OAuth callback")
		}
		data, errRead := os.ReadFile(path)
		if errRead == nil {
			var result CommandCodeCallbackPayload
			if errDecode := json.Unmarshal(data, &result); errDecode != nil {
				return nil, errors.New("Invalid OAuth callback")
			}
			return &result, nil
		}
		if !errors.Is(errRead, os.ErrNotExist) {
			return nil, errors.New("Failed to read OAuth callback")
		}
		select {
		case <-ctx.Done():
			return nil, errors.New("Timeout waiting for OAuth callback")
		case <-ticker.C:
		}
	}
}

// WriteCommandCodeOAuthCallbackFile persists a studio/auth/cli callback for a pending session.
func WriteCommandCodeOAuthCallbackFile(authDir, state string, payload *CommandCodeCallbackPayload) (string, error) {
	if payload == nil {
		return "", fmt.Errorf("callback payload is nil")
	}
	if err := ValidateOAuthState(state); err != nil {
		return "", err
	}
	if !IsOAuthSessionPending(state, "commandcode") {
		return "", errOAuthSessionNotPending
	}
	if strings.TrimSpace(authDir) == "" {
		return "", fmt.Errorf("auth dir is empty")
	}
	payload.State = strings.TrimSpace(state)
	fileName := fmt.Sprintf(".oauth-commandcode-%s.oauth", state)
	filePath := filepath.Join(authDir, fileName)
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		return "", fmt.Errorf("create oauth callback dir: %w", err)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal oauth callback payload: %w", err)
	}
	tmp, errCreate := os.CreateTemp(authDir, ".oauth-callback-*")
	if errCreate != nil {
		return "", fmt.Errorf("create oauth callback file: %w", errCreate)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if _, errWrite := tmp.Write(data); errWrite != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write oauth callback file: %w", errWrite)
	}
	if errClose := tmp.Close(); errClose != nil {
		return "", fmt.Errorf("close oauth callback file: %w", errClose)
	}
	if errRename := os.Rename(tmpPath, filePath); errRename != nil {
		return "", fmt.Errorf("publish oauth callback file: %w", errRename)
	}
	return filePath, nil
}
