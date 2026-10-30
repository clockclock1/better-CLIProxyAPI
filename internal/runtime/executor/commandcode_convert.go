package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// commandCodeGenerateBody is the POST /alpha/generate request body.
type commandCodeGenerateBody struct {
	Config         commandCodeConfig `json:"config"`
	Memory         any               `json:"memory"`
	Taste          any               `json:"taste"`
	Skills         any               `json:"skills"`
	PermissionMode string            `json:"permissionMode"`
	Mode           string            `json:"mode"`
	Params         commandCodeParams `json:"params"`
}

type commandCodeConfig struct {
	WorkingDir    string   `json:"workingDir"`
	Date          string   `json:"date"`
	Environment   string   `json:"environment"`
	Structure     []any    `json:"structure"`
	IsGitRepo     bool     `json:"isGitRepo"`
	CurrentBranch string   `json:"currentBranch"`
	MainBranch    string   `json:"mainBranch"`
	GitStatus     string   `json:"gitStatus"`
	RecentCommits []string `json:"recentCommits"`
}

type commandCodeParams struct {
	Model       string   `json:"model"`
	Messages    []any    `json:"messages"`
	Tools       []any    `json:"tools"`
	System      any      `json:"system"`
	MaxTokens   int      `json:"max_tokens"`
	Stream      bool     `json:"stream"`
	Temperature *float64 `json:"temperature,omitempty"`
}

// commandCodeNeutralSystem suppresses Command Code's built-in agent prompt when the
// client did not supply its own system message. An empty system string makes
// /alpha/generate expand a multi-thousand-token workspace/agent prompt (cwd, tools,
// taste, identity). Keep this short and free of filesystem/repo framing.
const commandCodeNeutralSystem = "You are a helpful assistant. Use only the tools provided in this request when tools are needed. Do not invent a working directory, repository, or local filesystem context."

func buildCommandCodeGenerateBody(model string, openaiPayload []byte) ([]byte, error) {
	messages, system := openaiMessagesToCommandCode(openaiPayload)
	tools := openaiToolsToCommandCode(openaiPayload)
	if strings.TrimSpace(system) == "" {
		system = commandCodeNeutralSystem
	}

	maxTokens := 8192
	if v := gjson.GetBytes(openaiPayload, "max_tokens"); v.Exists() && v.Int() > 0 {
		maxTokens = int(v.Int())
	} else if v := gjson.GetBytes(openaiPayload, "max_completion_tokens"); v.Exists() && v.Int() > 0 {
		maxTokens = int(v.Int())
	}

	var temperature *float64
	if v := gjson.GetBytes(openaiPayload, "temperature"); v.Exists() && v.Type == gjson.Number {
		t := v.Float()
		temperature = &t
	}

	body := commandCodeGenerateBody{
		Config: commandCodeConfig{
			// Tools run on the client (e.g. Cursor), not on this proxy. A real host
			// cwd here only feeds Command Code's server-side workspace prompt and
			// stacks another agent layer on top of the client's own system — which
			// makes the model over-iterate. Prefer request/env override, else "/".
			WorkingDir:    commandCodeWorkingDir(openaiPayload),
			Date:          time.Now().UTC().Format("2006-01-02"),
			Environment:   commandCodeEnvironment(),
			Structure:     []any{},
			IsGitRepo:     false,
			CurrentBranch: "",
			MainBranch:    "",
			GitStatus:     "",
			RecentCommits: []string{},
		},
		Memory:         nil,
		Taste:          nil,
		Skills:         nil,
		PermissionMode: "standard",
		Mode:           "agent",
		Params: commandCodeParams{
			Model:    model,
			Messages: messages,
			// Only client-provided tools (empty slice disables upstream builtins).
			Tools: tools,
			// Wire system as a cacheable text section so repeated prefixes can hit
			// Command Code / provider prompt cache instead of billing full input each turn.
			System:      commandCodeWireSystem(system),
			MaxTokens:   maxTokens,
			Stream:      true,
			Temperature: temperature,
		},
	}
	return json.Marshal(body)
}

// commandCodeWireSystem builds the /alpha/generate system payload with an
// ephemeral cache breakpoint (same shape as the official CLI's toWireSystem).
func commandCodeWireSystem(system string) any {
	system = strings.TrimSpace(system)
	if system == "" {
		return ""
	}
	return []any{
		map[string]any{
			"type": "text",
			"text": system,
			"cache_control": map[string]any{
				"type": "ephemeral",
			},
		},
	}
}

func commandCodeWorkingDir(openaiPayload []byte) string {
	if v := strings.TrimSpace(os.Getenv("COMMANDCODE_WORKING_DIR")); v != "" {
		return v
	}
	for _, path := range []string{
		"metadata.working_dir",
		"metadata.workingDir",
		"metadata.cwd",
		"working_dir",
		"workingDir",
	} {
		if v := strings.TrimSpace(gjson.GetBytes(openaiPayload, path).String()); v != "" {
			return v
		}
	}
	// Neutral placeholder — do not leak process cwd into upstream prompts.
	return "/"
}

func commandCodeEnvironment() string {
	if v := strings.TrimSpace(os.Getenv("COMMANDCODE_ENVIRONMENT")); v != "" {
		return v
	}
	// Keep host OS out of upstream env-context injection unless explicitly set.
	return "proxy"
}

func openaiMessagesToCommandCode(payload []byte) (messages []any, system string) {
	var systemParts []string
	rawMessages := gjson.GetBytes(payload, "messages")
	if !rawMessages.IsArray() {
		return []any{}, ""
	}
	rawMessages.ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		switch role {
		case "system":
			if t := contentToPlainText(msg.Get("content")); t != "" {
				systemParts = append(systemParts, t)
			}
		case "assistant":
			content := make([]any, 0)
			if msg.Get("content").Type == gjson.String {
				if t := msg.Get("content").String(); t != "" {
					content = append(content, map[string]any{"type": "text", "text": t})
				}
			} else if msg.Get("content").IsArray() {
				msg.Get("content").ForEach(func(_, part gjson.Result) bool {
					if part.Get("type").String() == "text" && part.Get("text").String() != "" {
						content = append(content, map[string]any{"type": "text", "text": part.Get("text").String()})
					}
					return true
				})
			}
			if msg.Get("tool_calls").IsArray() {
				msg.Get("tool_calls").ForEach(func(_, call gjson.Result) bool {
					input := map[string]any{}
					args := call.Get("function.arguments").String()
					if args != "" {
						if err := json.Unmarshal([]byte(args), &input); err != nil {
							input = map[string]any{"raw": args}
						}
					}
					content = append(content, map[string]any{
						"type":       "tool-call",
						"toolCallId": call.Get("id").String(),
						"toolName":   call.Get("function.name").String(),
						"input":      input,
					})
					return true
				})
			}
			if len(content) > 0 {
				messages = append(messages, map[string]any{"role": "assistant", "content": content})
			}
		case "tool":
			value := contentToPlainText(msg.Get("content"))
			messages = append(messages, map[string]any{
				"role": "tool",
				"content": []any{
					map[string]any{
						"type":       "tool-result",
						"toolCallId": msg.Get("tool_call_id").String(),
						"toolName":   msg.Get("name").String(),
						"output": map[string]any{
							"type":  "text",
							"value": value,
						},
					},
				},
			})
		default: // user
			parts := make([]any, 0)
			if msg.Get("content").Type == gjson.String {
				if t := msg.Get("content").String(); t != "" {
					parts = append(parts, map[string]any{"type": "text", "text": t})
				}
			} else if msg.Get("content").IsArray() {
				msg.Get("content").ForEach(func(_, part gjson.Result) bool {
					switch part.Get("type").String() {
					case "text":
						if t := part.Get("text").String(); t != "" {
							parts = append(parts, map[string]any{"type": "text", "text": t})
						}
					case "image_url":
						url := part.Get("image_url.url").String()
						if url == "" {
							url = part.Get("image_url").String()
						}
						if strings.HasPrefix(url, "data:") {
							mime := "image/png"
							if i := strings.Index(url, ";"); i > 5 {
								mime = url[5:i]
							}
							parts = append(parts, map[string]any{
								"type":     "image",
								"image":    url,
								"mimeType": mime,
							})
						}
					}
					return true
				})
			}
			if len(parts) > 0 {
				messages = append(messages, map[string]any{"role": "user", "content": parts})
			}
		}
		return true
	})
	return messages, strings.Join(systemParts, "\n\n")
}

func openaiToolsToCommandCode(payload []byte) []any {
	tools := make([]any, 0)
	raw := gjson.GetBytes(payload, "tools")
	if !raw.IsArray() {
		return tools
	}
	raw.ForEach(func(_, tool gjson.Result) bool {
		if tool.Get("type").String() != "function" {
			return true
		}
		fn := tool.Get("function")
		schema := map[string]any{"type": "object", "properties": map[string]any{}}
		if fn.Get("parameters").Exists() {
			_ = json.Unmarshal([]byte(fn.Get("parameters").Raw), &schema)
		}
		tools = append(tools, map[string]any{
			"name":         fn.Get("name").String(),
			"description":  fn.Get("description").String(),
			"input_schema": schema,
		})
		return true
	})
	return tools
}

func contentToPlainText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	var parts []string
	content.ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() == "text" && part.Get("text").String() != "" {
			parts = append(parts, part.Get("text").String())
		}
		return true
	})
	return strings.Join(parts, "\n")
}

func mapCommandCodeFinishReason(raw string) string {
	r := strings.ToLower(strings.TrimSpace(raw))
	switch r {
	case "tool_use", "tool-calls", "tool_calls":
		return "tool_calls"
	case "length", "max_tokens":
		return "length"
	case "refusal", "content_filter", "content-filter":
		return "content_filter"
	default:
		if r == "" {
			return "stop"
		}
		return "stop"
	}
}

func usageFromCommandCodeTotal(event gjson.Result) map[string]any {
	src := event.Get("totalUsage")
	if !src.Exists() {
		src = event.Get("usage")
	}
	if !src.Exists() {
		return nil
	}
	prompt := src.Get("inputTokens").Int()
	completion := src.Get("outputTokens").Int()
	total := src.Get("totalTokens").Int()
	if total == 0 {
		total = prompt + completion
	}
	return map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": completion,
		"total_tokens":      total,
	}
}

type commandCodeOpenAIAccumulator struct {
	ID           string
	Model        string
	Created      int64
	Content      strings.Builder
	Reasoning    strings.Builder
	FinishReason string
	Usage        map[string]any
	ToolCalls    []map[string]any
}

func newCommandCodeOpenAIAccumulator(model string) *commandCodeOpenAIAccumulator {
	return &commandCodeOpenAIAccumulator{
		ID:           "chatcmpl-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Model:        model,
		Created:      time.Now().Unix(),
		FinishReason: "stop",
		ToolCalls:    make([]map[string]any, 0),
	}
}

func (a *commandCodeOpenAIAccumulator) ingest(event []byte) error {
	typ := gjson.GetBytes(event, "type").String()
	switch typ {
	case "error":
		msg := gjson.GetBytes(event, "error").String()
		if msg == "" {
			msg = gjson.GetBytes(event, "error.message").String()
		}
		if msg == "" {
			msg = "upstream stream error"
		}
		return fmt.Errorf("%s", msg)
	case "text-delta":
		if t := gjson.GetBytes(event, "text").String(); t != "" {
			a.Content.WriteString(t)
		}
	case "reasoning-delta":
		if t := gjson.GetBytes(event, "text").String(); t != "" {
			a.Reasoning.WriteString(t)
		}
	case "tool-call":
		args := gjson.GetBytes(event, "input").Raw
		if args == "" {
			args = "{}"
		} else if gjson.GetBytes(event, "input").Type == gjson.String {
			args = gjson.GetBytes(event, "input").String()
		}
		callID := gjson.GetBytes(event, "toolCallId").String()
		if callID == "" {
			callID = "call_" + uuid.NewString()[:12]
		}
		a.ToolCalls = append(a.ToolCalls, map[string]any{
			"id":   callID,
			"type": "function",
			"function": map[string]any{
				"name":      gjson.GetBytes(event, "toolName").String(),
				"arguments": args,
			},
		})
		a.FinishReason = "tool_calls"
	case "finish", "finish-step":
		a.FinishReason = mapCommandCodeFinishReason(gjson.GetBytes(event, "finishReason").String())
		if fr := gjson.GetBytes(event, "rawFinishReason").String(); a.FinishReason == "stop" && fr != "" {
			a.FinishReason = mapCommandCodeFinishReason(fr)
		}
		if u := usageFromCommandCodeTotal(gjson.ParseBytes(event)); u != nil {
			a.Usage = u
		}
	}
	return nil
}

func (a *commandCodeOpenAIAccumulator) completionJSON() ([]byte, error) {
	msg := map[string]any{
		"role":    "assistant",
		"content": nil,
	}
	if a.Content.Len() > 0 {
		msg["content"] = a.Content.String()
	}
	if len(a.ToolCalls) > 0 {
		msg["tool_calls"] = a.ToolCalls
	}
	if a.Reasoning.Len() > 0 {
		msg["reasoning_content"] = a.Reasoning.String()
	}
	usage := a.Usage
	if usage == nil {
		usage = map[string]any{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
		}
	}
	out := map[string]any{
		"id":      a.ID,
		"object":  "chat.completion",
		"created": a.Created,
		"model":   a.Model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       msg,
				"finish_reason": a.FinishReason,
			},
		},
		"usage": usage,
	}
	return json.Marshal(out)
}

func (a *commandCodeOpenAIAccumulator) streamChunk(delta map[string]any, finish any) []byte {
	chunk := map[string]any{
		"id":      a.ID,
		"object":  "chat.completion.chunk",
		"created": a.Created,
		"model":   a.Model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"delta":         delta,
				"finish_reason": finish,
			},
		},
	}
	b, _ := json.Marshal(chunk)
	return b
}

func (a *commandCodeOpenAIAccumulator) streamChunkWithUsage(delta map[string]any, finish any, usage map[string]any) []byte {
	chunk := map[string]any{
		"id":      a.ID,
		"object":  "chat.completion.chunk",
		"created": a.Created,
		"model":   a.Model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"delta":         delta,
				"finish_reason": finish,
			},
		},
	}
	if usage != nil {
		chunk["usage"] = usage
	}
	b, _ := json.Marshal(chunk)
	return b
}

func parseCommandCodeNDJSONLine(line []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil, false
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
	}
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil, false
	}
	if !json.Valid(trimmed) {
		return nil, false
	}
	return trimmed, true
}
