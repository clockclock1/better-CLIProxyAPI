package synthesizer

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cursor"
)

// cursorAccountLooksLikeAuthFile reports whether an untyped auth file carries
// Cursor OAuth material.
//
// Account tooling writes flattened fields (access_token/refresh_token/email)
// while the IDE writes a cursor_auth_raw blob. Either shape is accepted. The
// issuer string inside the JWT is a strong signal but is not decoded here: the
// curated companion fields identify the file just as reliably and keep this
// check cheap for every file the watcher scans.
func cursorAccountLooksLikeAuthFile(metadata map[string]any) bool {
	if metadata == nil {
		return false
	}
	if _, ok := metadata["cursor_auth_raw"]; ok {
		return true
	}
	if _, ok := metadata["cursor_usage_raw"]; ok {
		return true
	}
	accessToken, _ := metadata["access_token"].(string)
	if strings.TrimSpace(accessToken) == "" {
		return false
	}
	// Cursor account files always carry the account identity alongside the
	// token. Requiring one of those companions keeps unrelated providers'
	// tokens from being captured by this heuristic.
	for _, key := range []string{"auth_id", "refresh_token", "membership_type", "subscription_status", "sign_up_type"} {
		if value, isStr := metadata[key].(string); isStr && strings.TrimSpace(value) != "" {
			return true
		}
	}
	// A JWT minted by Cursor's own auth host is unambiguous.
	if strings.Contains(accessToken, "authentication.cursor.sh") {
		return true
	}
	return false
}

// normalizeCursorAuthMetadata flattens the IDE credential blob into the keys the
// executor reads, so both file shapes behave identically downstream.
//
// It also strips fields the shared Auth struct would misread. Account tooling
// writes `created_at`/`last_used` as Unix seconds, but Auth.CreatedAt is a
// time.Time and rejects a number, which fails the whole file before the
// synthesizer ever runs. Those values are descriptive only, so removing them is
// safe and the framework fills in its own timestamps.
func normalizeCursorAuthMetadata(metadata map[string]any) {
	if metadata == nil {
		return
	}
	for _, key := range []string{"created_at", "last_used", "usage_updated_at"} {
		if value, isNum := metadata[key].(float64); isNum && value > 1_000_000_000 {
			delete(metadata, key)
		}
	}
	accessToken, _ := metadata["access_token"].(string)
	refreshToken, _ := metadata["refresh_token"].(string)
	authID, _ := metadata["auth_id"].(string)
	email, _ := metadata["email"].(string)

	raw, ok := metadata["cursor_auth_raw"].(map[string]any)
	if !ok {
		return
	}
	if val, isStr := raw["accessToken"].(string); isStr {
		if strings.TrimSpace(accessToken) == "" {
			metadata["access_token"] = strings.TrimSpace(val)
		}
	}
	if val, isStr := raw["refreshToken"].(string); isStr {
		if strings.TrimSpace(refreshToken) == "" {
			metadata["refresh_token"] = strings.TrimSpace(val)
		}
	}
	if val, isStr := raw["authId"].(string); isStr {
		if strings.TrimSpace(authID) == "" {
			metadata["auth_id"] = strings.TrimSpace(val)
		}
	}
	if val, isStr := raw["cachedEmail"].(string); isStr {
		if strings.TrimSpace(email) == "" {
			metadata["email"] = strings.TrimSpace(val)
		}
	}
	if _, hasAgent := metadata["base_url"]; !hasAgent {
		metadata["base_url"] = cursor.DefaultAgentBaseURL
	}
}
