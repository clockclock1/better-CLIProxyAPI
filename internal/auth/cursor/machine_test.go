package cursor

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestResolveMachineIDHonoursLiteralPin covers the operator escape hatch: a
// pinned 64-hex machine id is reused verbatim, any other pin is a seed.
func TestResolveMachineIDHonoursLiteralPin(t *testing.T) {
	pinned := strings.Repeat("ab", 32)
	if got := ResolveMachineID(map[string]any{"machine_id": pinned}, nil, ""); got != pinned {
		t.Errorf("ResolveMachineID = %q, want the literal pin", got)
	}
	upper := strings.ToUpper(pinned)
	if got := ResolveMachineID(map[string]any{"machine_id": upper}, nil, ""); got != pinned {
		t.Errorf("ResolveMachineID = %q, want the lowercased literal pin", got)
	}
	if got := ResolveMachineID(map[string]any{"machine_id": "my-seed"}, nil, ""); got != MachineID("my-seed") {
		t.Errorf("ResolveMachineID = %q, want the derived seed id", got)
	}
	if got := ResolveMachineID(map[string]any{"email": "alpha@example.com"}, nil, "tok"); got != MachineID("alpha@example.com") {
		t.Errorf("ResolveMachineID = %q, want the email-derived id", got)
	}
	if got := ResolveMachineID(nil, nil, ""); got != "" {
		t.Errorf("ResolveMachineID = %q for an empty account, want empty", got)
	}
	if got := ResolveMachineID(map[string]any{"machine_id": strings.Repeat("a", 63)}, nil, ""); got != MachineID(strings.Repeat("a", 63)) {
		t.Errorf("ResolveMachineID = %q, want a derived id for a short pin", got)
	}
}

// TestMachineIDStableAndAccountUnique locks the derivation: one account always
// maps to the same 64-hex id, two accounts never share one.
func TestMachineIDStableAndAccountUnique(t *testing.T) {
	alpha := MachineID("alpha@example.com")
	beta := MachineID("beta@example.com")

	if want := "ee852e9da714c477dfe172696f5ba6a3b75a46553251cfde6ecba42b7215c792"; alpha != want {
		t.Errorf("MachineID(alpha) = %q, want %q", alpha, want)
	}
	if want := "ff12fd68483db66b0757e77e74e2309080f7e7c88960a56b775a337b2aba7c92"; beta != want {
		t.Errorf("MachineID(beta) = %q, want %q", beta, want)
	}
	if alpha == beta {
		t.Errorf("two accounts share a machine id: %q", alpha)
	}
	if len(alpha) != 64 {
		t.Errorf("machine id is %d chars, want 64", len(alpha))
	}
	if got := MachineID("  ALPHA@Example.com "); got != alpha {
		t.Errorf("machine id is not case/space stable: %q vs %q", got, alpha)
	}
	if got := MachineID(""); got != "" {
		t.Errorf("empty seed should give an empty id, got %q", got)
	}
}

// TestChecksumGoldenVectors pins the checksum cipher: the obfuscated timestamp
// is followed by the machine id in clear text.
func TestChecksumGoldenVectors(t *testing.T) {
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 11, 5, 5, 46, 0, 0, time.UTC), "paaotu2x"},
		{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "paaotUaE"},
	}
	for _, tc := range cases {
		got := Checksum("alpha", tc.at)
		if want := tc.want + "alpha"; got != want {
			t.Errorf("Checksum(alpha, %s) = %q, want %q", tc.at.Format(time.RFC3339), got, want)
		}
	}
	if got := Checksum("", time.Now()); got != "" {
		t.Errorf("checksum without a machine id should be empty, got %q", got)
	}
}

// TestChecksumTracksTime ensures the timestamp part actually changes over time.
func TestChecksumTracksTime(t *testing.T) {
	first := Checksum("alpha", time.Date(2026, 11, 5, 5, 46, 0, 0, time.UTC))
	second := Checksum("alpha", time.Date(2026, 11, 5, 6, 0, 0, 0, time.UTC))
	if first == second {
		t.Errorf("checksum did not change with time: %q", first)
	}
	if len(first) != len("paaotu2x")+len("alpha") {
		t.Errorf("unexpected checksum length: %q", first)
	}
}

// TestAccountSeedPrefersStableIdentity covers the seed priority and the pinned
// override an operator can put in an account file.
func TestAccountSeedPrefersStableIdentity(t *testing.T) {
	meta := map[string]any{"email": "alpha@example.com", "auth_id": "auth0|42"}
	if got := AccountSeed(meta, nil, "tok"); got != "alpha@example.com" {
		t.Errorf("seed = %q, want the email", got)
	}
	if got := AccountSeed(map[string]any{"auth_id": "auth0|42"}, nil, "tok"); got != "auth0|42" {
		t.Errorf("seed = %q, want the auth id", got)
	}
	tokenSeed := AccountSeed(nil, nil, "tok")
	if len(tokenSeed) != len("token:")+64 {
		t.Errorf("token seed = %q, want a token digest", tokenSeed)
	}
	if got := AccountSeed(meta, map[string]string{"machine_id": " pinned "}, "tok"); got != "pinned" {
		t.Errorf("seed = %q, want the pinned override", got)
	}
	if got := AccountSeed(meta, nil, ""); got != "alpha@example.com" {
		t.Errorf("seed = %q without a token", got)
	}
	if got := AccountSeed(map[string]any{"machine_ids": map[string]any{"machineId": "nested-id"}}, nil, ""); got != "nested-id" {
		t.Errorf("seed = %q, want the nested machine id", got)
	}
	if got := AccountSeed(nil, nil, ""); got != "" {
		t.Errorf("seed = %q for an empty account, want empty", got)
	}
}

// TestClientKeyAndSessionIDNormalizeToken covers the "label::token" shape some
// account tooling writes.
func TestClientKeyAndSessionIDNormalizeToken(t *testing.T) {
	plain := "token-value"
	prefixed := "cursor::" + plain
	if ClientKey(prefixed) != ClientKey(plain) {
		t.Errorf("client key ignores the token prefix")
	}
	if SessionID(prefixed) != SessionID(plain) {
		t.Errorf("session id ignores the token prefix")
	}
	if len(ClientKey(plain)) != 64 {
		t.Errorf("client key is not a 64-char digest: %q", ClientKey(plain))
	}
	if _, errParse := time.Parse(time.RFC3339, time.Now().Format(time.RFC3339)); errParse != nil {
		t.Fatalf("sanity: %v", errParse)
	}
	if SessionID("") != "" || ClientKey("") != "" {
		t.Errorf("empty token should give empty values")
	}
}

// TestIdentityHeadersArePerAccount is the behavioural guarantee: two accounts
// proxied from one host present two different machines.
func TestIdentityHeadersArePerAccount(t *testing.T) {
	at := time.Date(2026, 11, 5, 5, 46, 0, 0, time.UTC)

	first := http.Header{}
	SetIdentityHeaders(first, IdentityFor(map[string]any{"email": "alpha@example.com"}, nil, "token-a", at))
	second := http.Header{}
	SetIdentityHeaders(second, IdentityFor(map[string]any{"email": "beta@example.com"}, nil, "token-b", at))

	for _, header := range []http.Header{first, second} {
		for _, key := range []string{HeaderChecksum, HeaderClientKey, HeaderSessionID} {
			if header.Get(key) == "" {
				t.Errorf("header %s is missing", key)
			}
		}
	}
	if first.Get(HeaderChecksum) == second.Get(HeaderChecksum) {
		t.Errorf("both accounts sent the same checksum: %q", first.Get(HeaderChecksum))
	}
	if first.Get(HeaderClientKey) == second.Get(HeaderClientKey) {
		t.Errorf("both accounts sent the same client key")
	}
	if first.Get(HeaderSessionID) == second.Get(HeaderSessionID) {
		t.Errorf("both accounts sent the same session id")
	}
}

// TestSetIdentityHeadersKeepsCallerValues ensures an explicit header survives,
// so a client that sets its own identity is not overwritten.
func TestSetIdentityHeadersKeepsCallerValues(t *testing.T) {
	header := http.Header{}
	header.Set(HeaderChecksum, "custom")
	SetIdentityHeaders(header, IdentityFor(map[string]any{"email": "alpha@example.com"}, nil, "token-a", time.Now()))
	if got := header.Get(HeaderChecksum); got != "custom" {
		t.Errorf("checksum = %q, want the caller value", got)
	}
	if header.Get(HeaderClientKey) == "" {
		t.Errorf("remaining identity headers should still be filled")
	}
	empty := http.Header{}
	SetIdentityHeaders(empty, Identity{})
	if len(empty) != 0 {
		t.Errorf("empty identity should set no headers, got %v", empty)
	}
	SetIdentityHeaders(nil, IdentityFor(map[string]any{"email": "alpha@example.com"}, nil, "tok", time.Now()))
}
