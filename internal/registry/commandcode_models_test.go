package registry

import "testing"

func TestGetCommandCodeModelsCatalog(t *testing.T) {
	models := GetCommandCodeModels()
	if len(models) < 90 {
		t.Fatalf("expected full Command Code catalog, got %d models", len(models))
	}
	want := map[string]bool{
		"poolside/laguna-s-2.1-free": false,
		"claude-sonnet-5-5":         false,
		"gpt-5.4":                   false,
		"deepseek/deepseek-v4-pro":  false,
		"google/gemini-3.5-flash":   false,
		"xai/grok-4.7":              false,
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
}
