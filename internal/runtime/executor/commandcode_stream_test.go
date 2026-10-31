package executor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestCommandCodeCLIStreamToolArguments(t *testing.T) {
	start := func(id, name string) map[string]any {
		return map[string]any{"type": "tool-input-start", "id": id, "toolName": name}
	}
	delta := func(id, text string) map[string]any {
		return map[string]any{"type": "tool-input-delta", "id": id, "delta": text}
	}
	complete := func(id, name string, input map[string]any) map[string]any {
		return map[string]any{"type": "tool-call", "toolCallId": id, "toolName": name, "input": input}
	}
	editInput := map[string]any{"file_path": "email.js", "old_string": "before", "new_string": "after"}
	readInput := map[string]any{"file_path": "ui.js"}
	editJSON, _ := json.Marshal(editInput)
	readJSON, _ := json.Marshal(readInput)
	tests := []struct {
		name   string
		events []map[string]any
	}{
		{"complete_events_only", []map[string]any{
			complete("edit-1", "edit", editInput), complete("read-2", "read", readInput),
		}},
		{"sequential", []map[string]any{
			start("edit-1", "edit"), delta("edit-1", string(editJSON)), complete("edit-1", "edit", editInput),
			start("read-2", "read"), delta("read-2", string(readJSON)), complete("read-2", "read", readInput),
		}},
		// AI SDK can emit both inputs before either completed tool-call event.
		// The recorded DSH failure merged these two JSON objects into one call.
		{"inputs_before_completions", []map[string]any{
			start("edit-1", "edit"), delta("edit-1", string(editJSON)),
			start("read-2", "read"), delta("read-2", string(readJSON)),
			complete("edit-1", "edit", editInput), complete("read-2", "read", readInput),
		}},
		{"interleaved_reversed_completions", []map[string]any{
			start("edit-1", "edit"), start("read-2", "read"),
			delta("edit-1", string(editJSON[:12])), delta("read-2", string(readJSON[:8])),
			delta("edit-1", string(editJSON[12:])), delta("read-2", string(readJSON[8:])),
			complete("read-2", "read", readInput), complete("edit-1", "edit", editInput),
		}},
		{"start_without_deltas", []map[string]any{
			start("edit-1", "edit"), start("read-2", "read"),
			complete("edit-1", "edit", editInput), complete("read-2", "read", readInput),
		}},
		{"partial_delta_completed_by_snapshot", []map[string]any{
			start("edit-1", "edit"), delta("edit-1", string(editJSON[:12])), complete("edit-1", "edit", editInput),
			complete("read-2", "read", readInput),
		}},
		{"legacy_tool_call_id", []map[string]any{
			{"type": "tool-input-start", "toolCallId": "edit-1", "toolName": "edit"},
			{"type": "tool-input-delta", "toolCallId": "edit-1", "inputTextDelta": string(editJSON)},
			complete("edit-1", "edit", editInput), complete("read-2", "read", readInput),
		}},
		{"duplicate_completion", []map[string]any{
			complete("edit-1", "edit", editInput), complete("edit-1", "edit", editInput),
			complete("read-2", "read", readInput),
		}},
		{"delta_before_start", []map[string]any{
			delta("edit-1", string(editJSON)), start("edit-1", "edit"), complete("edit-1", "edit", editInput),
			complete("read-2", "read", readInput),
		}},
		{"anonymous_delta_with_one_pending_call", []map[string]any{
			start("edit-1", "edit"), {"type": "tool-input-delta", "delta": string(editJSON)},
			complete("edit-1", "edit", editInput), complete("read-2", "read", readInput),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			type call struct{ id, name, args string }
			calls := map[int]*call{}
			var wire strings.Builder
			for _, event := range tt.events {
				data, err := json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
				wire.Write(data)
				wire.WriteByte('\n')
			}
			wire.WriteString(`{"type":"finish","finishReason":"tool-calls","totalUsage":{"inputTokens":10,"outputTokens":20,"totalTokens":30}}` + "\n")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := false
			errors := make(chan cliproxyexecutor.StreamChunk, 1)
			streamCommandCodeCLI(ctx, "deepseek/deepseek-v4.1-flash", strings.NewReader(wire.String()), func(data []byte) bool {
				gjson.GetBytes(data, "choices.0.delta.tool_calls").ForEach(func(_, item gjson.Result) bool {
					index := int(item.Get("index").Int())
					if calls[index] == nil {
						calls[index] = &call{}
					}
					c := calls[index]
					c.id += item.Get("id").String()
					c.name += item.Get("function.name").String()
					c.args += item.Get("function.arguments").String()
					return true
				})
				return true
			}, func() { done = true }, helps.NewUsageReporter(ctx, "commandcode", "deepseek/deepseek-v4.1-flash", nil), errors)
			select {
			case chunk := <-errors:
				t.Fatalf("stream error: %v", chunk.Err)
			default:
			}
			if !done {
				t.Fatal("stream did not finish")
			}
			if len(calls) != 2 {
				t.Fatalf("got %d tool calls, want exactly two", len(calls))
			}
			for i, want := range []call{{"edit-1", "edit", string(editJSON)}, {"read-2", "read", string(readJSON)}} {
				got := calls[i]
				if got == nil || *got != want {
					t.Errorf("tool index %d = %+v, want %+v", i, got, want)
				}
				if got != nil && !json.Valid([]byte(got.args)) {
					t.Errorf("tool index %d arguments are invalid JSON: %s", i, got.args)
				}
			}
		})
	}
}

func TestCommandCodeCLIStreamRejectsIncompleteToolInput(t *testing.T) {
	for _, tt := range []struct {
		name, events, wantError string
	}{
		{
			name: "truncated_stream",
			events: `{"type":"tool-input-start","id":"edit-1","toolName":"edit"}
{"type":"tool-input-delta","id":"edit-1","delta":"{\"file_path\":"}
`,
			wantError: "incomplete tool call",
		},
		{
			name: "ambiguous_anonymous_delta",
			events: `{"type":"tool-input-start","id":"edit-1","toolName":"edit"}
{"type":"tool-input-start","id":"read-2","toolName":"read"}
{"type":"tool-input-delta","delta":"{}"}
`,
			wantError: "ambiguous tool input",
		},
		{
			name: "inconsistent_snapshot",
			events: `{"type":"tool-input-start","id":"edit-1","toolName":"edit"}
{"type":"tool-input-delta","id":"edit-1","delta":"{\"other\":"}
{"type":"tool-call","toolCallId":"edit-1","toolName":"edit","input":{"file_path":"email.js"}}
`,
			wantError: "inconsistent tool arguments",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			errors := make(chan cliproxyexecutor.StreamChunk, 1)
			done, finished := false, false
			streamCommandCodeCLI(ctx, "test", strings.NewReader(tt.events), func(data []byte) bool {
				if gjson.GetBytes(data, "choices.0.finish_reason").Type == gjson.String {
					finished = true
				}
				return true
			}, func() { done = true }, helps.NewUsageReporter(ctx, "commandcode", "test", nil), errors)
			if done || finished {
				t.Fatal("incomplete tool input must not finish as a successful tool call")
			}
			select {
			case chunk := <-errors:
				if chunk.Err == nil || !strings.Contains(chunk.Err.Error(), tt.wantError) {
					t.Fatalf("stream error = %v, want %q", chunk.Err, tt.wantError)
				}
			default:
				t.Fatal("missing stream error")
			}
		})
	}
}
