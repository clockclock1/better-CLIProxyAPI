package executor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
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

// TestCursorToolContinuationHintAfterResults keeps tool schemas before the
// transcript and ends with a plain-text continuation cue so Free models stop
// looping the same tool_calls JSON after results arrive.
func TestCursorToolContinuationHintAfterResults(t *testing.T) {
	payload := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "list . then DONE"},
			map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"id": "call_1", "type": "function",
					"function": map[string]any{"name": "list_dir", "arguments": `{"path":"."}`},
				}},
			},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "name": "list_dir", "content": "a.txt"},
		},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{
			"name": "list_dir", "description": "List files",
			"parameters": map[string]any{"type": "object"},
		}}},
	}
	raw, _ := json.Marshal(payload)
	prompt := cursorPromptFromPayload(raw)
	toolsAt := strings.Index(prompt, "AVAILABLE TOOLS")
	userAt := strings.Index(prompt, "User: list . then DONE")
	hintAt := strings.Index(prompt, "CONTINUE:")
	if toolsAt < 0 || userAt < 0 || hintAt < 0 {
		t.Fatalf("missing sections:\n%s", prompt)
	}
	if !(toolsAt < userAt && userAt < hintAt) {
		t.Fatalf("order want tools < transcript < CONTINUE, got tools=%d user=%d hint=%d\n%s", toolsAt, userAt, hintAt, prompt)
	}
	if !contains(prompt, `"name":"list_dir"`) {
		t.Fatalf("assistant tool envelope missing:\n%s", prompt)
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

func TestCursorRequestDeclaresTools(t *testing.T) {
	withTools, _ := json.Marshal(map[string]any{
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "read_file"}}},
	})
	withoutTools, _ := json.Marshal(map[string]any{"messages": []any{}})
	emptyTools, _ := json.Marshal(map[string]any{"tools": []any{}})
	if !cursorRequestDeclaresTools(withTools) {
		t.Fatal("expected tools to be detected")
	}
	if cursorRequestDeclaresTools(withoutTools) || cursorRequestDeclaresTools(emptyTools) || cursorRequestDeclaresTools(nil) {
		t.Fatal("empty/missing tools should not count as declared")
	}
}

// TestBuildOpenAIToolCallStreamChunks locks the streaming shape that keeps
// clients from dropping a tool turn (finish_reason=tool_calls, not stop).
func TestBuildOpenAIToolCallStreamChunks(t *testing.T) {
	calls := []cursorToolCall{
		{Name: "read_file", Arguments: json.RawMessage(`{"path":"a.txt"}`)},
		{Name: "list_dir", Arguments: json.RawMessage(`{}`)},
	}
	chunks := buildOpenAIToolCallStreamChunks("cursor/default", calls)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	open := strings.TrimPrefix(strings.TrimSpace(string(chunks[0])), "data: ")
	done := strings.TrimPrefix(strings.TrimSpace(string(chunks[1])), "data: ")
	if gjson.Get(open, "choices.0.delta.tool_calls.#").Int() != 2 {
		t.Fatalf("open tool_calls = %s", open)
	}
	if gjson.Get(open, "choices.0.delta.tool_calls.0.function.name").String() != "read_file" {
		t.Fatalf("first tool name missing: %s", open)
	}
	if gjson.Get(open, "choices.0.finish_reason").Type != gjson.Null {
		t.Fatalf("open finish_reason should be null: %s", open)
	}
	if gjson.Get(done, "choices.0.finish_reason").String() != "tool_calls" {
		t.Fatalf("done finish_reason = %s", done)
	}
}
