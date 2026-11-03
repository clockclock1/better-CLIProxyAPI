package registry

// Cursor exposes a small, account-tier dependent model set. They are defined
// statically rather than fetched: the catalog endpoint requires an IDE session
// token, and a missing catalog must not hide the models from clients.
//
// Free-tier gating is per model: `grok-4.7` is only served under its registry
// variant name, which the executor maps at request time.
const (
	cursorModelDefault = "cursor/default"
	cursorModelGrok47  = "cursor/grok-4.7"
	cursorModelGrok46  = "cursor/grok-4.6"
)

// staticCursorModels returns the built-in Cursor model definitions.
func staticCursorModels() []*ModelInfo {
	return []*ModelInfo{
		{
			ID:                         cursorModelDefault,
			Object:                     "model",
			Type:                       "cursor",
			OwnedBy:                    "cursor",
			DisplayName:                "Auto (Cursor)",
			Description:                "Cursor Auto, the default balanced model.",
			ContextLength:              200000,
			MaxCompletionTokens:        32000,
			InputTokenLimit:            200000,
			OutputTokenLimit:           32000,
			SupportedInputModalities:   []string{"text", "image"},
			SupportedOutputModalities:  []string{"text"},
			SupportedGenerationMethods: []string{"generateContent", "countTokens"},
		},
		{
			ID:                         cursorModelGrok47,
			Object:                     "model",
			Type:                       "cursor",
			OwnedBy:                    "cursor",
			DisplayName:                "Grok 4.7 (Cursor)",
			Description:                "Cursor-hosted Grok 4.7.",
			ContextLength:              256000,
			MaxCompletionTokens:        32000,
			InputTokenLimit:            256000,
			OutputTokenLimit:           32000,
			SupportedInputModalities:   []string{"text"},
			SupportedOutputModalities:  []string{"text"},
			SupportedGenerationMethods: []string{"generateContent", "countTokens"},
		},
		{
			ID:                         cursorModelGrok46,
			Object:                     "model",
			Type:                       "cursor",
			OwnedBy:                    "cursor",
			DisplayName:                "Grok 4.6 (Cursor)",
			Description:                "Cursor-hosted Grok 4.6.",
			ContextLength:              256000,
			MaxCompletionTokens:        32000,
			InputTokenLimit:            256000,
			OutputTokenLimit:           32000,
			SupportedInputModalities:   []string{"text"},
			SupportedOutputModalities:  []string{"text"},
			SupportedGenerationMethods: []string{"generateContent", "countTokens"},
		},
	}
}

// GetCursorModels returns the Cursor model catalog.
func GetCursorModels() []*ModelInfo {
	return staticCursorModels()
}
