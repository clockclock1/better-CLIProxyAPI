package executor

import (
	"os"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// Command Code wire transports (Mars-Sea + official CLI).
const (
	commandCodeProtocolCLI      = "cli"      // POST /alpha/generate
	commandCodeProtocolOpenAI   = "openai"   // POST /provider/v1/chat/completions
	commandCodeProtocolMessages = "messages" // POST /provider/v1/messages (Claude)
)

const commandCodeProtocolCacheTTL = 30 * time.Minute

type commandCodeProtocolCacheEntry struct {
	protocol string
	at       time.Time
}

var commandCodeProtocolCache sync.Map // accountKey → commandCodeProtocolCacheEntry

func commandCodeAccountCacheKey(baseURL, apiKey string) string {
	return strings.TrimRight(baseURL, "/") + "\x00" + apiKey
}

func rememberCommandCodeProtocol(baseURL, apiKey, protocol string) {
	if protocol != commandCodeProtocolCLI && protocol != commandCodeProtocolOpenAI {
		return
	}
	commandCodeProtocolCache.Store(commandCodeAccountCacheKey(baseURL, apiKey), commandCodeProtocolCacheEntry{
		protocol: protocol,
		at:       time.Now(),
	})
}

func cachedCommandCodeProtocol(baseURL, apiKey string) string {
	v, ok := commandCodeProtocolCache.Load(commandCodeAccountCacheKey(baseURL, apiKey))
	if !ok {
		return ""
	}
	entry, ok := v.(commandCodeProtocolCacheEntry)
	if !ok || time.Since(entry.at) > commandCodeProtocolCacheTTL {
		return ""
	}
	return entry.protocol
}

// resolveCommandCodeProtocol picks the initial upstream transport.
// Messages-only models always use /messages. Otherwise prefer Provider
// chat/completions unless the account is remembered as Go-plan CLI-only, or
// COMMANDCODE_PROTOCOL forces a route.
func resolveCommandCodeProtocol(model, baseURL, apiKey string) string {
	forced := strings.ToLower(strings.TrimSpace(os.Getenv("COMMANDCODE_PROTOCOL")))
	if forced == commandCodeProtocolCLI {
		return commandCodeProtocolCLI
	}
	if registry.CommandCodeRequiresMessages(model) {
		return commandCodeProtocolMessages
	}
	if forced == commandCodeProtocolOpenAI || forced == commandCodeProtocolMessages {
		if forced == commandCodeProtocolMessages && !registry.CommandCodeRequiresMessages(model) {
			// Forced messages on a non-Claude model still falls through to openai;
			// only Claude answers /messages.
			return commandCodeProtocolOpenAI
		}
		if forced == commandCodeProtocolOpenAI {
			return commandCodeProtocolOpenAI
		}
	}
	if cached := cachedCommandCodeProtocol(baseURL, apiKey); cached != "" {
		return cached
	}
	return commandCodeProtocolOpenAI
}

func commandCodeProtocolEndpoint(baseURL, protocol string) string {
	base := strings.TrimRight(baseURL, "/")
	switch protocol {
	case commandCodeProtocolMessages:
		return base + "/provider/v1/messages"
	case commandCodeProtocolOpenAI:
		return base + "/provider/v1/chat/completions"
	default:
		return base + "/alpha/generate"
	}
}

func commandCodeProtocolFallback(protocol string) string {
	switch protocol {
	case commandCodeProtocolOpenAI, commandCodeProtocolMessages:
		return commandCodeProtocolCLI
	default:
		return ""
	}
}

func isCommandCodeUpgradeRequired(status int, body []byte) bool {
	if status != 403 && status != 401 {
		// upgrade_required also appears as 403 with that code; accept body match broadly.
		lower := strings.ToLower(string(body))
		return strings.Contains(lower, "upgrade_required")
	}
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "upgrade_required") ||
		strings.Contains(lower, "provider api") ||
		strings.Contains(lower, "permission_error")
}
