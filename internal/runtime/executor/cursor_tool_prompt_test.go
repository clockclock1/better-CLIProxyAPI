package executor

import (
	"encoding/json"
	"testing"
)

// TestCursorToolDeclarationInPrompt locks the in-band tool protocol: Cursor has
// no native tool schema, so the schemas and the answer format live in the prompt.
func TestCursorToolDeclarationInPrompt(t *testing.T) {
	payload := map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "read notes.txt"}},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{
			"name":        "read_file",
			"description": "Read a file",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
				"required":   []any{"path"},
			},
		}}},
	}
	raw, _ := json.Marshal(payload)
	prompt := cursorPromptFromPayload(raw)

	for _, want := range []string{"AVAILABLE TOOLS", "- read_file: Read a file", "TOOL PROTOCOL", `"tool_calls"`} {
		if !contains(prompt, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, prompt)
		}
	}
}

// TestCursorNoToolsMeansNoProtocol keeps the prompt clean for plain chat.
func TestCursorNoToolsMeansNoProtocol(t *testing.T) {
	payload := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	raw, _ := json.Marshal(payload)
	prompt := cursorPromptFromPayload(raw)
	if contains(prompt, "TOOL PROTOCOL") || contains(prompt, "AVAILABLE TOOLS") {
		t.Errorf("tool protocol should be absent without tools:\n%s", prompt)
	}
}

// TestBuildOpenAIToolCallResponse asserts the emitted shape clients expect.
func TestBuildOpenAIToolCallResponse(t *testing.T) {
	calls := []cursorToolCall{{Name: "read_file", Arguments: json.RawMessage(`{"path":"a.txt"}`)}}
	raw := buildOpenAIToolCallResponse("cursor/default", calls)

	var decoded map[string]any
	if errJSON := json.Unmarshal(raw, &decoded); errJSON != nil {
		t.Fatalf("response is not JSON: %v", errJSON)
	}
	choices, _ := decoded["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices = %v", decoded["choices"])
	}
	choice := choices[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Errorf("finish_reason = %v", choice["finish_reason"])
	}
	message := choice["message"].(map[string]any)
	if message["content"] != nil {
		t.Errorf("content = %v, want null for a tool call turn", message["content"])
	}
	toolCalls, _ := message["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("tool_calls = %v", message["tool_calls"])
	}
	call := toolCalls[0].(map[string]any)
	if call["type"] != "function" {
		t.Errorf("tool call type = %v", call["type"])
	}
	fn := call["function"].(map[string]any)
	if fn["name"] != "read_file" {
		t.Errorf("name = %v", fn["name"])
	}
	if fn["arguments"] != `{"path":"a.txt"}` {
		t.Errorf("arguments = %v", fn["arguments"])
	}
}
