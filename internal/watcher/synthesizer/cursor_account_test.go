package synthesizer

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// TestCursorAccountDetectedFromFlattenedFile covers the exact file a user drops
// into the auth directory: no `type` field, but Cursor OAuth material present.
func TestCursorAccountDetectedFromFlattenedFile(t *testing.T) {
	raw := cursorFixture(t, map[string]any{
		"email":           "user@outlook.com",
		"access_token":    cursorJWT(),
		"refresh_token":   "refresh",
		"auth_id":         "auth0|user_1",
		"membership_type": "free",
	})

	ctx := &SynthesisContext{AuthDir: t.TempDir(), Now: time.Now()}
	auths, errSynthesize := SynthesizeAuthFile(ctx, filepath.Join(ctx.AuthDir, "user@outlook.com.json"), raw)
	if errSynthesize != nil {
		t.Fatalf("SynthesizeAuthFile() error = %v", errSynthesize)
	}
	if len(auths) != 1 {
		t.Fatalf("got %d auths, want 1", len(auths))
	}
	if auths[0].Provider != "cursor" {
		t.Errorf("Provider = %q, want cursor", auths[0].Provider)
	}
	if got := auths[0].Metadata["type"]; got != "cursor" {
		t.Errorf("metadata type = %v, want cursor", got)
	}
	if auths[0].Metadata["access_token"] != cursorJWT() {
		t.Error("access_token should survive into metadata")
	}
	if auths[0].Label != "user@outlook.com" {
		t.Errorf("Label = %q", auths[0].Label)
	}
}

// TestCursorAccountFromIDERawBlob ensures the IDE credential blob is flattened.
func TestCursorAccountFromIDERawBlob(t *testing.T) {
	raw := cursorFixture(t, map[string]any{
		"cursor_auth_raw": map[string]any{
			"accessToken":          cursorJWT(),
			"refreshToken":         "refresh-2",
			"cachedEmail":          "ide@outlook.com",
			"stripeMembershipType": "pro",
		},
	})

	ctx := &SynthesisContext{AuthDir: t.TempDir(), Now: time.Now()}
	auths, errSynthesize := SynthesizeAuthFile(ctx, filepath.Join(ctx.AuthDir, "ide.json"), raw)
	if errSynthesize != nil {
		t.Fatalf("SynthesizeAuthFile() error = %v", errSynthesize)
	}
	if len(auths) != 1 {
		t.Fatalf("got %d auths, want 1", len(auths))
	}
	if auths[0].Provider != "cursor" {
		t.Errorf("Provider = %q", auths[0].Provider)
	}
	if auths[0].Metadata["access_token"] != cursorJWT() {
		t.Errorf("access_token not flattened: %v", auths[0].Metadata["access_token"])
	}
	if auths[0].Metadata["email"] != "ide@outlook.com" {
		t.Errorf("email not flattened: %v", auths[0].Metadata["email"])
	}
}

// TestUntypedFileWithForeignTokenIsIgnored guards against claiming auth files
// that belong to another provider.
func TestUntypedFileWithForeignTokenIsIgnored(t *testing.T) {
	raw := cursorFixture(t, map[string]any{
		"email":        "someone@example.com",
		"access_token": "ya29.a0AfH6SMB_fake_google_token",
	})

	ctx := &SynthesisContext{AuthDir: t.TempDir(), Now: time.Now()}
	auths, errSynthesize := SynthesizeAuthFile(ctx, filepath.Join(ctx.AuthDir, "other.json"), raw)
	if errSynthesize != nil {
		t.Fatalf("SynthesizeAuthFile() error = %v", errSynthesize)
	}
	if len(auths) != 0 {
		t.Fatalf("got %d auths, want 0 (file must not be claimed)", len(auths))
	}
}

// TestTypedCursorFileStillWorks verifies an explicit type field is honored.
func TestTypedCursorFileStillWorks(t *testing.T) {
	raw := cursorFixture(t, map[string]any{
		"type":         "cursor",
		"email":        "typed@outlook.com",
		"access_token": cursorJWT(),
	})

	ctx := &SynthesisContext{AuthDir: t.TempDir(), Now: time.Now()}
	auths, errSynthesize := SynthesizeAuthFile(ctx, filepath.Join(ctx.AuthDir, "typed.json"), raw)
	if errSynthesize != nil {
		t.Fatalf("SynthesizeAuthFile() error = %v", errSynthesize)
	}
	if len(auths) != 1 || auths[0].Provider != "cursor" {
		t.Fatalf("auths = %+v", auths)
	}
}

func cursorFixture(t *testing.T, payload map[string]any) []byte {
	t.Helper()
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		t.Fatalf("marshal fixture: %v", errMarshal)
	}
	return raw
}

// cursorJWT is a syntactically valid session token issued by Cursor's auth host,
// which is what the detection heuristic keys on.
func cursorJWT() string {
	return "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
		"eyJzdWIiOiJhdXRoMHx1c2VyXzEiLCJpc3MiOiJodHRwczovL2F1dGhlbnRpY2F0aW9uLmN1cnNvci5zaCIsImV4cCI6MTc5ODc4ODc3MH0." +
		"signature"
}

// TestCursorAccountWithUnixTimestamps loads: account tooling writes created_at
// and last_used as Unix seconds, which the shared Auth struct rejects as a
// created_at time.Time. That used to fail the whole file.
func TestCursorAccountWithUnixTimestamps(t *testing.T) {
	raw := cursorFixture(t, map[string]any{
		"email":                  "ts@outlook.com",
		"access_token":           cursorJWT(),
		"refresh_token":          "refresh",
		"created_at":             1793691861,
		"last_used":              1793692165,
		"usage_updated_at":       1793692165,
		"tags":                   []any{},
		"quota_query_last_error": nil,
	})

	ctx := &SynthesisContext{AuthDir: t.TempDir(), Now: time.Now()}
	auths, errSynthesize := SynthesizeAuthFile(ctx, filepath.Join(ctx.AuthDir, "ts.json"), raw)
	if errSynthesize != nil {
		t.Fatalf("SynthesizeAuthFile() error = %v", errSynthesize)
	}
	if len(auths) != 1 {
		t.Fatalf("got %d auths, want 1", len(auths))
	}
	if auths[0].Provider != "cursor" {
		t.Errorf("Provider = %q", auths[0].Provider)
	}
	if _, stillThere := auths[0].Metadata["created_at"]; stillThere {
		t.Error("created_at should be stripped so Auth.CreatedAt parses")
	}
}
