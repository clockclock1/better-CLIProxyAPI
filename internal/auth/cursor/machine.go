package cursor

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Cursor rate limits and abuse-scores by machine, not only by account: the IDE
// stamps every request with a machine id inside the x-cursor-checksum header.
// A proxy that reuses one machine id for every account makes all of them look
// like a single machine, so each account gets its own derived fingerprint here.
//
// The derivation is deterministic (a keyed digest of the account identity), so
// an account keeps the same machine id across restarts while two accounts never
// share one.
const (
	// machineIDDomain namespaces the derivation so ids cannot collide with other
	// tooling that hashes the same account identity.
	machineIDDomain = "cli-proxy-api.cursor.machine.v1"

	// checksumKeySeed is the initial rolling key of Cursor's checksum cipher.
	checksumKeySeed = 165

	// checksumTimeUnit is the coarse time step (about 1000 seconds) the cipher
	// encodes, matching the IDE's own checksum routine.
	checksumTimeUnit = 1_000_000
)

// Header names Cursor reads for client identity.
const (
	HeaderChecksum  = "X-Cursor-Checksum"
	HeaderClientKey = "X-Client-Key"
	HeaderSessionID = "X-Session-Id"
)

// MachineID derives the stable 64-hex machine id of one account.
//
// An empty seed yields an empty id, which callers treat as "identity unknown"
// and therefore send no machine headers at all.
func MachineID(seed string) string {
	normalized := strings.ToLower(strings.TrimSpace(seed))
	if normalized == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(machineIDDomain + "|" + normalized))
	return hex.EncodeToString(sum[:])
}

// ResolveMachineID returns the machine id reported for one account.
//
// An override that is already a 64-character hex value is honoured verbatim, so
// an operator can reproduce a known machine; any other override is treated as a
// seed and derived, which keeps every reported id uniform in shape.
func ResolveMachineID(metadata map[string]any, attributes map[string]string, accessToken string) string {
	if override := machineIDOverride(metadata, attributes); isMachineIDLiteral(override) {
		return strings.ToLower(override)
	}
	return MachineID(AccountSeed(metadata, attributes, accessToken))
}

// isMachineIDLiteral reports whether a value is already a machine id rather than
// a seed, i.e. exactly 64 hexadecimal characters.
func isMachineIDLiteral(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		switch c := value[i]; {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// AccountSeed picks the most stable identity available for an account.
//
// An explicit override wins, so an operator can pin the machine id reported for
// one account. Otherwise the email is preferred, then the auth id, and finally
// a digest of the access token for account files that carry neither.
func AccountSeed(metadata map[string]any, attributes map[string]string, accessToken string) string {
	if override := machineIDOverride(metadata, attributes); override != "" {
		return override
	}
	for _, candidate := range []string{
		attributeValue(attributes, "email"),
		metadataValue(metadata, "email"),
		attributeValue(attributes, "auth_id"),
		metadataValue(metadata, "auth_id"),
	} {
		if candidate != "" {
			return candidate
		}
	}
	token := NormalizeAccessToken(accessToken)
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return "token:" + hex.EncodeToString(sum[:])
}

// machineIDOverride reads a pinned machine id from the account record. Both the
// flat key and the machine_ids block written by account tooling are honoured.
func machineIDOverride(metadata map[string]any, attributes map[string]string) string {
	for _, key := range []string{"machine_id", "machineId"} {
		if value := attributeValue(attributes, key); value != "" {
			return value
		}
		if value := metadataValue(metadata, key); value != "" {
			return value
		}
	}
	nested, ok := metadata["machine_ids"].(map[string]any)
	if !ok {
		return ""
	}
	return metadataValue(nested, "machineId")
}

func attributeValue(attributes map[string]string, key string) string {
	if attributes == nil {
		return ""
	}
	return strings.TrimSpace(attributes[key])
}

func metadataValue(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// NormalizeAccessToken drops the "<label>::" prefix account tooling prepends.
func NormalizeAccessToken(accessToken string) string {
	token := strings.TrimSpace(accessToken)
	if idx := strings.Index(token, "::"); idx >= 0 {
		return strings.TrimSpace(token[idx+2:])
	}
	return token
}

// ClientKey mirrors the IDE's x-client-key header: a digest of the raw token.
func ClientKey(accessToken string) string {
	token := NormalizeAccessToken(accessToken)
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// SessionID mirrors the IDE's persistent x-session-id: a name-based UUID
// derived from the token, so one account keeps one session identity.
func SessionID(accessToken string) string {
	token := NormalizeAccessToken(accessToken)
	if token == "" {
		return ""
	}
	return uuid.NewSHA1(uuid.NameSpaceDNS, []byte(token)).String()
}

// Checksum renders Cursor's checksum for a machine id.
//
// The coarse timestamp is obfuscated with a rolling-key XOR and encoded as
// URL-safe base64 without padding; the machine id follows in clear text, which
// is why the value is both a freshness token and the machine identity.
func Checksum(machineID string, at time.Time) string {
	if strings.TrimSpace(machineID) == "" {
		return ""
	}
	stamp := uint64(at.UnixMilli() / checksumTimeUnit)
	raw := []byte{
		byte(stamp >> 40),
		byte(stamp >> 32),
		byte(stamp >> 24),
		byte(stamp >> 16),
		byte(stamp >> 8),
		byte(stamp),
	}
	key := byte(checksumKeySeed)
	for i := range raw {
		raw[i] = (raw[i] ^ key) + byte(i%256)
		key = raw[i]
	}
	return base64.RawURLEncoding.EncodeToString(raw) + machineID
}

// Identity is the per-account client identity reported to Cursor.
type Identity struct {
	MachineID string
	ClientKey string
	SessionID string
	Checksum  string
}

// IdentityFor derives the complete identity of one account at a point in time.
func IdentityFor(metadata map[string]any, attributes map[string]string, accessToken string, at time.Time) Identity {
	machineID := ResolveMachineID(metadata, attributes, accessToken)
	if machineID == "" {
		return Identity{}
	}
	return Identity{
		MachineID: machineID,
		ClientKey: ClientKey(accessToken),
		SessionID: SessionID(accessToken),
		Checksum:  Checksum(machineID, at),
	}
}

// SetIdentityHeaders stamps the account identity onto an outgoing request.
//
// Only derivable values are sent, and a header a caller already set is left
// untouched so a custom client can override any part of the identity.
func SetIdentityHeaders(header http.Header, identity Identity) {
	if header == nil {
		return
	}
	setHeaderIfAbsent(header, HeaderChecksum, identity.Checksum)
	setHeaderIfAbsent(header, HeaderClientKey, identity.ClientKey)
	setHeaderIfAbsent(header, HeaderSessionID, identity.SessionID)
}

func setHeaderIfAbsent(header http.Header, key, value string) {
	if strings.TrimSpace(value) == "" || header.Get(key) != "" {
		return
	}
	header.Set(key, value)
}
