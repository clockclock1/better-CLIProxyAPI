package commandcode

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// CreateAuthRecord builds a Command Code OAuth credential record from a callback payload.
func CreateAuthRecord(payload *CallbackPayload, apiBase string) (*coreauth.Auth, error) {
	if payload == nil || strings.TrimSpace(payload.APIKey) == "" {
		return nil, fmt.Errorf("commandcode: api key is required")
	}
	apiKey := strings.TrimSpace(payload.APIKey)
	userName := strings.TrimSpace(payload.UserName)
	userID := strings.TrimSpace(payload.UserID)
	keyName := strings.TrimSpace(payload.KeyName)
	if apiBase == "" {
		apiBase = DefaultAPIBaseURL
	}
	apiBase = strings.TrimRight(apiBase, "/")

	identifier := userName
	if identifier == "" {
		identifier = userID
	}
	if identifier == "" {
		digest := sha256.Sum256([]byte(apiKey))
		identifier = fmt.Sprintf("user-%x", digest[:8])
	}
	fileIdentifier := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == '@' {
			return r
		}
		return '_'
	}, identifier)
	if fileIdentifier != identifier || len(fileIdentifier) > 160 {
		digest := sha256.Sum256([]byte(identifier))
		fileIdentifier = fmt.Sprintf("user-%x", digest[:8])
	}
	fileName := fmt.Sprintf("commandcode-%s.json", fileIdentifier)
	label := fmt.Sprintf("Command Code (%s)", identifier)
	if keyName != "" {
		label = fmt.Sprintf("Command Code (%s / %s)", identifier, keyName)
	}

	attributes := map[string]string{
		"api_key":   apiKey,
		"base_url":  apiBase,
		"auth_kind": "oauth",
		"user_name": userName,
		"user_id":   userID,
		"key_name":  keyName,
	}
	metadata := map[string]any{
		"type":      "commandcode",
		"api_key":   apiKey,
		"base_url":  apiBase,
		"auth_kind": "oauth",
		"user_name": userName,
		"user_id":   userID,
		"key_name":  keyName,
		"authenticated_at": time.Now().UTC().Format(time.RFC3339),
	}

	return &coreauth.Auth{
		ID:         fileName,
		Provider:   "commandcode",
		FileName:   fileName,
		Label:      label,
		Status:     coreauth.StatusActive,
		Attributes: attributes,
		Metadata:   metadata,
	}, nil
}

// CredentialFileName returns a stable auth filename for a user identifier.
func CredentialFileName(userName, userID string) string {
	identifier := strings.TrimSpace(userName)
	if identifier == "" {
		identifier = strings.TrimSpace(userID)
	}
	if identifier == "" {
		return "commandcode-oauth.json"
	}
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == '@' {
			return r
		}
		return '_'
	}, identifier)
	if safe == "" || len(safe) > 160 {
		digest := sha256.Sum256([]byte(identifier))
		safe = fmt.Sprintf("user-%x", digest[:8])
	}
	return fmt.Sprintf("commandcode-%s.json", safe)
}
