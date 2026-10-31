package registry

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

//go:embed models/commandcode_models.json
var embeddedCommandCodeModelsJSON []byte

//go:embed models/commandcode_capabilities.json
var embeddedCommandCodeCapabilitiesJSON []byte

type commandCodeModelsStore struct {
	mu        sync.RWMutex
	models    []*ModelInfo
	rawJSON   []byte
	revision  uint64
	endpoints map[string][]string // model id → supported_endpoints from /provider/v1/models
}

var commandCodeCatalogStore = &commandCodeModelsStore{}

func init() {
	if _, err := loadCommandCodeModelsFromBytes(embeddedCommandCodeModelsJSON, "embed"); err != nil {
		log.Warnf("registry: failed to parse embedded commandcode_models.json (will rely on static fallback and remote refresh): %v", err)
	}
}

// GetCommandCodeModels returns the active Command Code model catalog.
// Prefers the dynamically refreshed / embedded catalog, then the static fallback.
func GetCommandCodeModels() []*ModelInfo {
	commandCodeCatalogStore.mu.RLock()
	models := commandCodeCatalogStore.models
	commandCodeCatalogStore.mu.RUnlock()
	if len(models) > 0 {
		return cloneModelInfos(models)
	}
	return cloneModelInfos(staticCommandCodeModels)
}

// GetCommandCodeModelsRevision returns the revision counter of the Command Code catalog.
func GetCommandCodeModelsRevision() uint64 {
	commandCodeCatalogStore.mu.RLock()
	defer commandCodeCatalogStore.mu.RUnlock()
	return commandCodeCatalogStore.revision
}

// GetCommandCodeModelsSnapshot returns a copy of raw JSON and current catalog revision.
func GetCommandCodeModelsSnapshot() ([]byte, uint64) {
	commandCodeCatalogStore.mu.RLock()
	defer commandCodeCatalogStore.mu.RUnlock()
	return append([]byte(nil), commandCodeCatalogStore.rawJSON...), commandCodeCatalogStore.revision
}

func loadCommandCodeModelsFromBytes(data []byte, source string) (bool, error) {
	models, err := ValidateCommandCodeModelsJSON(data)
	if err != nil {
		return false, fmt.Errorf("%s: %w", source, err)
	}
	endpoints := extractCommandCodeEndpoints(data, models)
	clonedData := append([]byte(nil), data...)
	commandCodeCatalogStore.mu.Lock()
	if bytes.Equal(commandCodeCatalogStore.rawJSON, clonedData) {
		commandCodeCatalogStore.mu.Unlock()
		return false, nil
	}
	commandCodeCatalogStore.models = models
	commandCodeCatalogStore.rawJSON = clonedData
	commandCodeCatalogStore.endpoints = endpoints
	commandCodeCatalogStore.revision++
	commandCodeCatalogStore.mu.Unlock()
	return true, nil
}

func extractCommandCodeEndpoints(data []byte, models []*ModelInfo) map[string][]string {
	out := make(map[string][]string)
	var providerPayload commandCodeProviderModelsPayload
	if err := json.Unmarshal(bytes.TrimSpace(data), &providerPayload); err == nil && len(providerPayload.Data) > 0 {
		for _, raw := range providerPayload.Data {
			id := strings.TrimSpace(raw.ID)
			if id == "" || len(raw.SupportedEndpoints) == 0 {
				continue
			}
			out[id] = append([]string(nil), raw.SupportedEndpoints...)
		}
	}
	for _, m := range models {
		if m == nil {
			continue
		}
		id := strings.TrimSpace(m.ID)
		if id == "" || len(m.SupportedEndpoints) == 0 {
			continue
		}
		if _, ok := out[id]; !ok {
			out[id] = append([]string(nil), m.SupportedEndpoints...)
		}
	}
	return out
}

// CommandCodeSupportedEndpoints returns catalog-published routes for a model
// (e.g. /messages, /chat/completions). Empty when unknown.
func CommandCodeSupportedEndpoints(modelID string) []string {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil
	}
	commandCodeCatalogStore.mu.RLock()
	defer commandCodeCatalogStore.mu.RUnlock()
	eps := commandCodeCatalogStore.endpoints[modelID]
	if len(eps) == 0 {
		if idx := strings.LastIndex(modelID, "/"); idx >= 0 {
			eps = commandCodeCatalogStore.endpoints[modelID[idx+1:]]
		}
	}
	if len(eps) == 0 {
		return nil
	}
	return append([]string(nil), eps...)
}

// CommandCodeRequiresMessages reports whether the model must use Anthropic
// Messages (/provider/v1/messages). Catalog supported_endpoints wins; otherwise
// the claude-* prefix rule matches the official CLI / Mars-Sea adapter.
func CommandCodeRequiresMessages(modelID string) bool {
	eps := CommandCodeSupportedEndpoints(modelID)
	if len(eps) > 0 {
		for _, e := range eps {
			if e == "/messages" {
				return true
			}
		}
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(modelID))
	if idx := strings.LastIndex(lower, "/"); idx >= 0 {
		lower = lower[idx+1:]
	}
	return strings.HasPrefix(lower, "claude-") || strings.HasPrefix(lower, "claude_")
}

type commandCodeProviderModelsPayload struct {
	Object string                        `json:"object"`
	Data   []commandCodeProviderModelRaw `json:"data"`
}

type commandCodeProviderModelRaw struct {
	ID                  string   `json:"id"`
	Object              string   `json:"object"`
	Created             int64    `json:"created"`
	OwnedBy             string   `json:"owned_by"`
	Name                string   `json:"name"`
	DisplayName         string   `json:"display_name"`
	ContextLength       int      `json:"context_length"`
	MaxCompletionTokens int      `json:"max_completion_tokens"`
	SupportedEndpoints  []string `json:"supported_endpoints"`
}

type commandCodeModelsFilePayload struct {
	CommandCode []*ModelInfo `json:"commandcode,omitempty"`
	Models      []*ModelInfo `json:"models,omitempty"`
}

// ValidateCommandCodeModelsJSON parses provider /v1/models, {commandcode:[...]}, or a ModelInfo array.
func ValidateCommandCodeModelsJSON(data []byte) ([]*ModelInfo, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty Command Code models payload")
	}

	var providerPayload commandCodeProviderModelsPayload
	if err := json.Unmarshal(trimmed, &providerPayload); err == nil && len(providerPayload.Data) > 0 {
		out := make([]*ModelInfo, 0, len(providerPayload.Data))
		seen := make(map[string]struct{}, len(providerPayload.Data))
		for _, raw := range providerPayload.Data {
			info := commandCodeModelInfoFromProvider(raw)
			if info == nil {
				continue
			}
			id := strings.TrimSpace(info.ID)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, info)
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("Command Code provider models payload contained no usable models")
		}
		return out, nil
	}

	var envelope commandCodeModelsFilePayload
	if err := json.Unmarshal(trimmed, &envelope); err == nil {
		candidates := envelope.CommandCode
		if len(candidates) == 0 {
			candidates = envelope.Models
		}
		if len(candidates) > 0 {
			return normalizeCommandCodeModelInfos(candidates)
		}
	}

	var direct []*ModelInfo
	if err := json.Unmarshal(trimmed, &direct); err == nil && len(direct) > 0 {
		return normalizeCommandCodeModelInfos(direct)
	}
	return nil, fmt.Errorf("unrecognized Command Code models payload")
}

func normalizeCommandCodeModelInfos(in []*ModelInfo) ([]*ModelInfo, error) {
	out := make([]*ModelInfo, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, m := range in {
		if m == nil {
			continue
		}
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		cloned := cloneModelInfo(m)
		if cloned.Object == "" {
			cloned.Object = "model"
		}
		if cloned.OwnedBy == "" {
			cloned.OwnedBy = "command-code"
		}
		cloned.Type = "commandcode"
		if cloned.DisplayName == "" {
			cloned.DisplayName = commandCodeDisplayName(id)
		}
		if cloned.ContextLength <= 0 {
			cloned.ContextLength = 200000
		}
		if cloned.InputTokenLimit <= 0 {
			cloned.InputTokenLimit = cloned.ContextLength
		}
		if cloned.OutputTokenLimit <= 0 {
			cloned.OutputTokenLimit = commandCodeDefaultMaxOutputTokens
		}
		if cloned.MaxCompletionTokens <= 0 {
			cloned.MaxCompletionTokens = cloned.OutputTokenLimit
		}
		applyCommandCodeCapability(cloned)
		if len(cloned.SupportedInputModalities) == 0 {
			cloned.SupportedInputModalities = commandCodeInputModalities(id)
		}
		if len(cloned.SupportedOutputModalities) == 0 {
			cloned.SupportedOutputModalities = []string{"text"}
		}
		if len(cloned.SupportedGenerationMethods) == 0 {
			cloned.SupportedGenerationMethods = []string{"generateContent"}
		}
		out = append(out, cloned)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("Command Code models payload contained no usable models")
	}
	return out, nil
}

func commandCodeModelInfoFromProvider(raw commandCodeProviderModelRaw) *ModelInfo {
	id := strings.TrimSpace(raw.ID)
	if id == "" {
		return nil
	}
	display := strings.TrimSpace(raw.DisplayName)
	if display == "" {
		display = strings.TrimSpace(raw.Name)
	}
	if display == "" {
		display = commandCodeDisplayName(id)
	}
	ctxLen := raw.ContextLength
	outTokens := raw.MaxCompletionTokens
	info := &ModelInfo{
		ID:                         id,
		Object:                     "model",
		Created:                    raw.Created,
		OwnedBy:                    firstNonEmptyString(strings.TrimSpace(raw.OwnedBy), "command-code"),
		Type:                       "commandcode",
		DisplayName:                display,
		ContextLength:              ctxLen,
		MaxCompletionTokens:        outTokens,
		InputTokenLimit:            ctxLen,
		OutputTokenLimit:           outTokens,
		SupportedEndpoints:         append([]string(nil), raw.SupportedEndpoints...),
		SupportedInputModalities:   commandCodeInputModalities(id),
		SupportedOutputModalities:  []string{"text"},
		SupportedGenerationMethods: []string{"generateContent"},
	}
	applyCommandCodeCapability(info)
	if info.ContextLength <= 0 {
		info.ContextLength = 200000
		info.InputTokenLimit = info.ContextLength
	}
	if info.MaxCompletionTokens <= 0 {
		info.MaxCompletionTokens = commandCodeDefaultMaxOutputTokens
		info.OutputTokenLimit = info.MaxCompletionTokens
	}
	return info
}

func commandCodeDisplayName(id string) string {
	base := id
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	base = strings.TrimSuffix(base, ":free")
	parts := strings.FieldsFunc(base, func(r rune) bool { return r == '-' || r == '_' })
	for i, p := range parts {
		if p == "" {
			continue
		}
		if strings.ToUpper(p) == p && len(p) <= 3 {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

func commandCodeInputModalities(id string) []string {
	lower := strings.ToLower(id)
	if strings.Contains(lower, "vision") ||
		strings.Contains(lower, "omni") ||
		strings.Contains(lower, "gemini") ||
		strings.HasPrefix(lower, "gpt-") ||
		strings.HasPrefix(lower, "claude-") ||
		strings.Contains(lower, "glyph") ||
		strings.Contains(lower, "beam") ||
		strings.Contains(lower, "laguna") ||
		strings.Contains(lower, "muse") ||
		strings.Contains(lower, "grok") ||
		strings.Contains(lower, "fugu") ||
		strings.Contains(lower, "nemotron") ||
		strings.Contains(lower, "hy3") ||
		strings.Contains(lower, "hy4") {
		return []string{"text", "image"}
	}
	return []string{"text"}
}

// commandCodeDefaultMaxOutputTokens matches the official CLI DEFAULT_MAX_TOKENS
// used when a catalog row leaves maxOutputTokens null.
const commandCodeDefaultMaxOutputTokens = 64000

type commandCodeCapability struct {
	ContextLength    int      `json:"context_length"`
	MaxOutputTokens  int      `json:"max_output_tokens"`
	ReasoningEfforts []string `json:"reasoning_efforts"`
}

var commandCodeCapabilities = loadCommandCodeCapabilities()

func loadCommandCodeCapabilities() map[string]commandCodeCapability {
	out := map[string]commandCodeCapability{}
	if len(bytes.TrimSpace(embeddedCommandCodeCapabilitiesJSON)) == 0 {
		return out
	}
	if err := json.Unmarshal(embeddedCommandCodeCapabilitiesJSON, &out); err != nil {
		log.Warnf("registry: commandcode capabilities: %v", err)
	}
	return out
}

func commandCodeCapabilityFor(modelID string) (commandCodeCapability, bool) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return commandCodeCapability{}, false
	}
	if cap, ok := commandCodeCapabilities[modelID]; ok {
		return cap, true
	}
	if idx := strings.LastIndex(modelID, "/"); idx >= 0 {
		if cap, ok := commandCodeCapabilities[modelID[idx+1:]]; ok {
			return cap, true
		}
	}
	return commandCodeCapability{}, false
}

// CommandCodeMaxOutputTokens is the catalog output cap for a model, or the CLI default.
func CommandCodeMaxOutputTokens(modelID string) int {
	if cap, ok := commandCodeCapabilityFor(modelID); ok && cap.MaxOutputTokens > 0 {
		return cap.MaxOutputTokens
	}
	return commandCodeDefaultMaxOutputTokens
}

func applyCommandCodeCapability(info *ModelInfo) {
	if info == nil {
		return
	}
	cap, ok := commandCodeCapabilityFor(info.ID)
	if !ok {
		return
	}
	if info.ContextLength <= 0 && cap.ContextLength > 0 {
		info.ContextLength = cap.ContextLength
	}
	if info.InputTokenLimit <= 0 {
		info.InputTokenLimit = info.ContextLength
	}
	if cap.MaxOutputTokens > 0 && (info.MaxCompletionTokens <= 0 ||
		info.MaxCompletionTokens == commandCodeDefaultMaxOutputTokens && cap.MaxOutputTokens != commandCodeDefaultMaxOutputTokens) {
		info.MaxCompletionTokens = cap.MaxOutputTokens
		info.OutputTokenLimit = cap.MaxOutputTokens
	}
	if info.OutputTokenLimit <= 0 {
		info.OutputTokenLimit = info.MaxCompletionTokens
	}
	if info.Thinking == nil && len(cap.ReasoningEfforts) > 0 {
		info.Thinking = &ThinkingSupport{Levels: append([]string(nil), cap.ReasoningEfforts...)}
	}
	if len(cap.ReasoningEfforts) > 0 && !stringSliceContains(info.SupportedParameters, "reasoning_effort") {
		info.SupportedParameters = append(info.SupportedParameters, "reasoning_effort")
	}
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// staticCommandCodeModels is the last-resort hardcoded fallback if embed/remote catalogs are unavailable.
var staticCommandCodeModels = []*ModelInfo{
	{
		ID:                         "poolside/laguna-s-2.1-free",
		Object:                     "model",
		OwnedBy:                    "command-code",
		Type:                       "commandcode",
		DisplayName:                "Laguna S 2.1 (Free)",
		ContextLength:              256000,
		MaxCompletionTokens:        32768,
		InputTokenLimit:            256000,
		OutputTokenLimit:           32768,
		SupportedInputModalities:   []string{"text", "image"},
		SupportedOutputModalities:  []string{"text"},
		SupportedGenerationMethods: []string{"generateContent"},
	},
	{
		ID:                         "reflection/beam:free",
		Object:                     "model",
		OwnedBy:                    "command-code",
		Type:                       "commandcode",
		DisplayName:                "Beam (Free)",
		ContextLength:              1048576,
		MaxCompletionTokens:        131072,
		InputTokenLimit:            1048576,
		OutputTokenLimit:           131072,
		SupportedInputModalities:   []string{"text", "image"},
		SupportedOutputModalities:  []string{"text"},
		SupportedGenerationMethods: []string{"generateContent"},
	},
	{
		ID:                         "stealth/glyph-cluster:free",
		Object:                     "model",
		OwnedBy:                    "command-code",
		Type:                       "commandcode",
		DisplayName:                "Glyph Cluster (Free)",
		ContextLength:              256000,
		MaxCompletionTokens:        256000,
		InputTokenLimit:            256000,
		OutputTokenLimit:           256000,
		SupportedInputModalities:   []string{"text", "image"},
		SupportedOutputModalities:  []string{"text"},
		SupportedGenerationMethods: []string{"generateContent"},
	},
}
