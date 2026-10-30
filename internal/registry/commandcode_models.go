package registry

// GetCommandCodeModels returns the static Command Code Go-plan free models plus
// common aliases used by the desktop CLI. These map to POST /alpha/generate.
func GetCommandCodeModels() []*ModelInfo {
	return cloneModelInfos(staticCommandCodeModels)
}

var staticCommandCodeModels = []*ModelInfo{
	{
		ID:                         "poolside/laguna-s-2.1-free",
		Object:                     "model",
		Created:                    0,
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
		Created:                    0,
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
		Created:                    0,
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
