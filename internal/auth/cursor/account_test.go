package cursor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestParseAccountFileFlattened covers the account-tooling shape, which is the
// file users drop into the auth directory.
func TestParseAccountFileFlattened(t *testing.T) {
	raw := []byte(`{
	  "email": "user@example.com",
	  "access_token": "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhdXRoMHx1c2VyXzEiLCJleHAiOjE3OTg3ODg3NzB9.sig",
	  "refresh_token": "refresh-1",
	  "auth_id": "auth0|user_1",
	  "membership_type": "free"
	}`)

	parsed, errParse := ParseAccountFile(raw)
	if errParse != nil {
		t.Fatalf("ParseAccountFile() error = %v", errParse)
	}
	if parsed.Email != "user@example.com" {
		t.Errorf("Email = %q", parsed.Email)
	}
	if parsed.RefreshToken != "refresh-1" {
		t.Errorf("RefreshToken = %q", parsed.RefreshToken)
	}
	if parsed.AuthID != "auth0|user_1" {
		t.Errorf("AuthID = %q", parsed.AuthID)
	}
	if parsed.MembershipType != "free" {
		t.Errorf("MembershipType = %q", parsed.MembershipType)
	}
}

// TestParseAccountFileIDERaw ensures the IDE credential blob alone is enough.
func TestParseAccountFileIDERaw(t *testing.T) {
	raw := []byte(`{
	  "cursor_auth_raw": {
	    "accessToken": "eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjE3OTg3ODg3NzB9.sig",
	    "refreshToken": "refresh-2",
	    "authId": "auth0|user_2",
	    "cachedEmail": "ide@example.com",
	    "stripeMembershipType": "pro"
	  }
	}`)

	parsed, errParse := ParseAccountFile(raw)
	if errParse != nil {
		t.Fatalf("ParseAccountFile() error = %v", errParse)
	}
	if parsed.AccessToken == "" {
		t.Error("AccessToken should be extracted from cursor_auth_raw")
	}
	if parsed.Email != "ide@example.com" {
		t.Errorf("Email = %q", parsed.Email)
	}
	if parsed.MembershipType != "pro" {
		t.Errorf("MembershipType = %q", parsed.MembershipType)
	}
}

func TestParseAccountFileMissingToken(t *testing.T) {
	if _, errParse := ParseAccountFile([]byte(`{"email":"x@example.com"}`)); errParse == nil {
		t.Fatal("expected an error when no access token is present")
	}
}

func TestParseAccountFileEmpty(t *testing.T) {
	if _, errParse := ParseAccountFile(nil); errParse == nil {
		t.Fatal("expected an error for empty input")
	}
}

func TestTokenExpiry(t *testing.T) {
	// {"exp":1798788770} base64url encoded payload.
	token := "header.eyJleHAiOjE3OTg3ODg3NzB9.sig"
	expiry, ok := TokenExpiry(token)
	if !ok {
		t.Fatal("TokenExpiry() should decode the exp claim")
	}
	if expiry.Year() != 2027 {
		t.Errorf("expiry year = %d, want 2027", expiry.Year())
	}
	if _, okBad := TokenExpiry("not-a-jwt"); okBad {
		t.Error("TokenExpiry() should fail for a non-JWT")
	}
}

func TestTokenSubject(t *testing.T) {
	token := "header.eyJzdWIiOiJhdXRoMHx1c2VyXzEifQ.sig"
	if got := TokenSubject(token); got != "auth0|user_1" {
		t.Errorf("TokenSubject() = %q", got)
	}
}

func TestAccountFileNameIsSafe(t *testing.T) {
	cases := map[string]string{
		"user@example.com":  "cursor-user@example.com.json",
		"../../etc/passwd":  "cursor-.._.._etc_passwd.json",
		"":                  "cursor-account.json",
		"normal.name+tag@x": "cursor-normal.name_tag@x.json",
	}
	for input, want := range cases {
		if got := AccountFileName(input); got != want {
			t.Errorf("AccountFileName(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestSampleAccountFileParses guards the shape users actually have on disk.
func TestSampleAccountFileParses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.json")
	payload := map[string]any{
		"id":                     "cursor_1",
		"email":                  "sample@outlook.com",
		"access_token":           "eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjE3OTg3ODg3NzB9.sig",
		"refresh_token":          "same-token",
		"auth_id":                "auth0|user_1",
		"membership_type":        "free",
		"subscription_status":    "active",
		"usage_updated_at":       1793606431,
		"tags":                   []any{},
		"created_at":             1793604891,
		"last_used":              1793606431,
		"quota_query_last_error": nil,
		"cursor_auth_raw": map[string]any{
			"accessToken":          "eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjE3OTg3ODg3NzB9.sig",
			"cachedEmail":          "Sample@outlook.com",
			"stripeMembershipType": "pro",
		},
		"cursor_usage_raw": map[string]any{"membershipType": "free"},
	}
	encoded, _ := json.Marshal(payload)
	if errWrite := os.WriteFile(path, encoded, 0o600); errWrite != nil {
		t.Fatalf("write sample: %v", errWrite)
	}
	read, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read sample: %v", errRead)
	}
	parsed, errParse := ParseAccountFile(read)
	if errParse != nil {
		t.Fatalf("ParseAccountFile() error = %v", errParse)
	}
	if parsed.Email != "sample@outlook.com" {
		t.Errorf("Email = %q", parsed.Email)
	}
}
