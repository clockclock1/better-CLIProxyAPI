// Package cursor implements Cursor account support for CLIProxyAPI.
//
// Cursor exposes no OpenAI-compatible endpoint. Its agent backend speaks
// Connect over HTTP/2 with protobuf payloads, and only an IDE OAuth session
// token is accepted; the user API keys minted by DashboardService are rejected
// on every other route, so auth records carry the session token itself.
package cursor

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Provider is the auth/executor provider key.
const Provider = "cursor"

// DefaultAgentBaseURL is the Connect endpoint that serves agent runs.
const DefaultAgentBaseURL = "https://agentn.global.api5.cursor.sh"

// DefaultAPIBaseURL serves the remaining unary services (models, auth, usage).
const DefaultAPIBaseURL = "https://api2.cursor.sh"

// DefaultClientVersion identifies the CLI client on the wire. Cursor rejects
// unknown or stale client versions, so this is pinned rather than derived.
const DefaultClientVersion = "cli-2026.07.08-0c04a8a"

// AgentPath is the streaming run method.
const AgentPath = "/agent.v1.AgentService/Run"

// accountFile is the on-disk shape produced by Cursor account tooling.
// Only the OAuth material is required; everything else is descriptive.
type accountFile struct {
	Type                  string          `json:"type"`
	ID                    string          `json:"id"`
	Email                 string          `json:"email"`
	AccessToken           string          `json:"access_token"`
	RefreshToken          string          `json:"refresh_token"`
	AuthID                string          `json:"auth_id"`
	MembershipType        string          `json:"membership_type"`
	SubscriptionStatus    string          `json:"subscription_status"`
	SignUpType            string          `json:"sign_up_type"`
	CursorAuthRaw         json.RawMessage `json:"cursor_auth_raw"`
	CursorUsageRaw        json.RawMessage `json:"cursor_usage_raw"`
	QuotaQueryLastError   any             `json:"quota_query_last_error"`
	QuotaQueryLastErrorAt any             `json:"quota_query_last_error_at"`
	UsageUpdatedAt        int64           `json:"usage_updated_at"`
	Tags                  []string        `json:"tags"`
	CreatedAt             int64           `json:"created_at"`
	LastUsed              int64           `json:"last_used"`
}

// cursorAuthRaw mirrors the IDE's own credential blob, used as a fallback when
// the flattened fields are absent.
type cursorAuthRaw struct {
	AccessToken             string `json:"accessToken"`
	RefreshToken            string `json:"refreshToken"`
	AuthID                  string `json:"authId"`
	CachedEmail             string `json:"cachedEmail"`
	CachedSignUpType        string `json:"cachedSignUpType"`
	StripeMembershipType    string `json:"stripeMembershipType"`
	StripeSubscriptionState string `json:"stripeSubscriptionStatus"`
}

// ParsedAccount is the normalized credential extracted from an account file.
type ParsedAccount struct {
	Email          string
	AccessToken    string
	RefreshToken   string
	AuthID         string
	MembershipType string
}

// ParseAccountFile accepts both the flattened account JSON and an IDE
// credentials blob, so a user can drop either shape into the auth directory.
func ParseAccountFile(raw []byte) (*ParsedAccount, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, fmt.Errorf("cursor: empty account file")
	}
	var acc accountFile
	if errJSON := json.Unmarshal(raw, &acc); errJSON != nil {
		return nil, fmt.Errorf("cursor: parse account file: %w", errJSON)
	}

	parsed := &ParsedAccount{
		Email:          strings.TrimSpace(acc.Email),
		AccessToken:    strings.TrimSpace(acc.AccessToken),
		RefreshToken:   strings.TrimSpace(acc.RefreshToken),
		AuthID:         strings.TrimSpace(acc.AuthID),
		MembershipType: strings.TrimSpace(acc.MembershipType),
	}

	if len(acc.CursorAuthRaw) > 0 {
		var inner cursorAuthRaw
		if errInner := json.Unmarshal(acc.CursorAuthRaw, &inner); errInner == nil {
			if parsed.AccessToken == "" {
				parsed.AccessToken = strings.TrimSpace(inner.AccessToken)
			}
			if parsed.RefreshToken == "" {
				parsed.RefreshToken = strings.TrimSpace(inner.RefreshToken)
			}
			if parsed.Email == "" {
				parsed.Email = strings.TrimSpace(inner.CachedEmail)
			}
			if parsed.AuthID == "" {
				parsed.AuthID = strings.TrimSpace(inner.AuthID)
			}
			if parsed.MembershipType == "" {
				parsed.MembershipType = strings.TrimSpace(inner.StripeMembershipType)
			}
		}
	}

	if parsed.AccessToken == "" {
		return nil, fmt.Errorf("cursor: access token missing (expected access_token or cursor_auth_raw.accessToken)")
	}
	return parsed, nil
}

// AccountFileName derives a stable, filesystem-safe file name for an account.
// Upstream-controlled values must never introduce path components.
func AccountFileName(email string) string {
	identifier := sanitizeFileComponent(email)
	if identifier == "" {
		return "cursor-account.json"
	}
	return "cursor-" + identifier + ".json"
}

func sanitizeFileComponent(value string) string {
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.', r == '@':
			return r
		default:
			return '_'
		}
	}, value)
	if len(mapped) > 160 {
		digest := sha256.Sum256([]byte(value))
		return fmt.Sprintf("account-%x", digest[:8])
	}
	return mapped
}

// TokenExpiry reports the JWT exp claim in UTC. Cursor session tokens are JWTs,
// but the claim is advisory: the backend still rejects revoked sessions.
func TokenExpiry(token string) (time.Time, bool) {
	claims, ok := decodeJWTClaims(token)
	if !ok {
		return time.Time{}, false
	}
	exp, ok := claims["exp"].(float64)
	if !ok || exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(exp), 0).UTC(), true
}

// TokenSubject reports the auth0 subject carried by the session token.
func TokenSubject(token string) string {
	claims, ok := decodeJWTClaims(token)
	if !ok {
		return ""
	}
	subject, _ := claims["sub"].(string)
	return subject
}

func decodeJWTClaims(token string) (map[string]any, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, false
	}
	payload, errDecode := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if errDecode != nil {
		if payload, errDecode = base64.URLEncoding.DecodeString(parts[1]); errDecode != nil {
			return nil, false
		}
	}
	var claims map[string]any
	if errJSON := json.Unmarshal(payload, &claims); errJSON != nil {
		return nil, false
	}
	return claims, true
}

// QuotaSignals summarizes usage for scheduler display.
func QuotaSignals(membershipType string, expiry time.Time) map[string]string {
	signals := make(map[string]string, 3)
	if membershipType != "" {
		signals["plan"] = membershipType
	}
	if !expiry.IsZero() {
		signals["token_expires_at"] = expiry.Format(time.RFC3339)
	}
	return signals
}
