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

type commandCodeModelsStore struct {
	mu       sync.RWMutex
	models   []*ModelInfo
	rawJSON  []byte
	revision uint64
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
	clonedData := append([]byte(nil), data...)
	commandCodeCatalogStore.mu.Lock()
	if bytes.Equal(commandCodeCatalogStore.rawJSON, clonedData) {
		commandCodeCatalogStore.mu.Unlock()
		return false, nil
	}
	commandCodeCatalogStore.models = models
	commandCodeCatalogStore.rawJSON = clonedData
	commandCodeCatalogStore.revision++
	commandCodeCatalogStore.mu.Unlock()
	return true, nil
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
			cloned.OutputTokenLimit = 8192
		}
		if cloned.MaxCompletionTokens <= 0 {
			cloned.MaxCompletionTokens = cloned.OutputTokenLimit
		}
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
	if ctxLen <= 0 {
		ctxLen = 200000
	}
	outTokens := raw.MaxCompletionTokens
	if outTokens <= 0 {
		outTokens = 8192
	}
	return &ModelInfo{
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
		SupportedInputModalities:   commandCodeInputModalities(id),
		SupportedOutputModalities:  []string{"text"},
		SupportedGenerationMethods: []string{"generateContent"},
	}
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
		ContextLength:              200000,
		MaxCompletionTokens:        8192,
		InputTokenLimit:            200000,
		OutputTokenLimit:           8192,
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
		ContextLength:              200000,
		MaxCompletionTokens:        8192,
		InputTokenLimit:            200000,
		OutputTokenLimit:           8192,
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
		ContextLength:              200000,
		MaxCompletionTokens:        8192,
		InputTokenLimit:            200000,
		OutputTokenLimit:           8192,
		SupportedInputModalities:   []string{"text", "image"},
		SupportedOutputModalities:  []string{"text"},
		SupportedGenerationMethods: []string{"generateContent"},
	},
}
