package registry

import (
	"encoding/json"
	"testing"
)

func TestGetCommandCodeModelsCatalog(t *testing.T) {
	models := GetCommandCodeModels()
	if len(models) < 90 {
		t.Fatalf("expected full Command Code catalog, got %d models", len(models))
	}
	want := map[string]bool{
		"poolside/laguna-s-2.1-free": false,
		"claude-sonnet-5-5":          false,
		"gpt-5.4":                    false,
		"deepseek/deepseek-v4-pro":   false,
		"google/gemini-3.5-flash":    false,
		"xai/grok-4.7":               false,
	}
	for _, m := range models {
		if m == nil || m.ID == "" {
			t.Fatal("nil/empty model entry")
		}
		if m.Type != "commandcode" {
			t.Fatalf("model %s type = %q, want commandcode", m.ID, m.Type)
		}
		if _, ok := want[m.ID]; ok {
			want[m.ID] = true
		}
	}
	for id, found := range want {
		if !found {
			t.Fatalf("missing model %s", id)
		}
	}
	var sonnet, laguna, flash *ModelInfo
	for _, m := range models {
		switch m.ID {
		case "claude-sonnet-5-5":
			sonnet = m
		case "poolside/laguna-s-2.1-free":
			laguna = m
		case "deepseek/deepseek-v4.1-flash":
			flash = m
		}
	}
	if sonnet == nil || sonnet.ContextLength != 1000000 || sonnet.MaxCompletionTokens != 64000 || sonnet.Thinking == nil {
		t.Fatalf("sonnet limits = ctx %d out %d thinking %#v", sonnet.ContextLength, sonnet.MaxCompletionTokens, sonnet.Thinking)
	}
	if laguna == nil || laguna.ContextLength != 256000 || laguna.MaxCompletionTokens != 32768 {
		t.Fatalf("laguna limits = ctx %d out %d", laguna.ContextLength, laguna.MaxCompletionTokens)
	}
	if flash == nil || flash.Thinking == nil || len(flash.Thinking.Levels) != 4 || flash.Thinking.Levels[0] != "off" {
		t.Fatalf("flash efforts = %#v", flash.Thinking)
	}
}

func TestValidateCommandCodeModelsJSONProviderPayload(t *testing.T) {
	payload := map[string]any{
		"object": "list",
		"data": []map[string]any{
			{"id": "claude-sonnet-5-5", "object": "model", "owned_by": "command-code", "name": "Claude Sonnet 5.5", "context_length": 1000000},
			{"id": "poolside/laguna-s-2.1-free", "object": "model", "owned_by": "command-code", "name": "Laguna"},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	models, errValidate := ValidateCommandCodeModelsJSON(raw)
	if errValidate != nil {
		t.Fatalf("ValidateCommandCodeModelsJSON: %v", errValidate)
	}
	if len(models) != 2 {
		t.Fatalf("len=%d", len(models))
	}
	if models[0].DisplayName != "Claude Sonnet 5.5" || models[0].ContextLength != 1000000 {
		t.Fatalf("unexpected first model: %+v", models[0])
	}
	changed, errLoad := loadCommandCodeModelsFromBytes(raw, "test")
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if !changed {
		t.Fatal("expected catalog change on first load")
	}
	changedAgain, errLoadAgain := loadCommandCodeModelsFromBytes(raw, "test")
	if errLoadAgain != nil {
		t.Fatal(errLoadAgain)
	}
	if changedAgain {
		t.Fatal("identical payload should not bump revision")
	}
}
