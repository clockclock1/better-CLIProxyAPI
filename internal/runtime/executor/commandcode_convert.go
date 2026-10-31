package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
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
	// ThreadID is the stable conversation UUID (official CLI threadId). The
	// server uses it as the prompt-cache scope the way Codex uses prompt_cache_key.
	// A fresh id on every request never reads cache back. Omit promptCache so the
	// server does not treat the turn as promptCache:"off".
	ThreadID string            `json:"threadId,omitempty"`
	Params   commandCodeParams `json:"params"`
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
	Model           string   `json:"model"`
	Messages        []any    `json:"messages"`
	Tools           []any    `json:"tools"`
	System          any      `json:"system"`
	MaxTokens       *int     `json:"max_tokens,omitempty"`
	Stream          bool     `json:"stream"`
	Temperature     *float64 `json:"temperature,omitempty"`
	ReasoningEffort string   `json:"reasoning_effort,omitempty"`
}

func buildCommandCodeGenerateBody(model, threadID string, openaiPayload []byte) ([]byte, error) {
	messages, system := openaiMessagesToCommandCode(openaiPayload)
	commandCodeOffloadImagesInPlace(messages)
	tools := openaiToolsToCommandCode(openaiPayload)
	// Do not inject a proxy-owned system prompt. Only forward the client's
	// system (if any); empty stays empty via commandCodeWireSystem.
	// Claude-style rolling breakpoint: cache the prefix through the latest
	// message, not only the system block. A system-only breakpoint leaves the
	// growing transcript uncached.
	markCommandCodeMessageCache(messages)
	markCommandCodeToolCache(tools)
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		threadID = commandCodeConversationUUID(openaiPayload)
	}

	maxTokens := resolveCommandCodeMaxTokens(model, openaiPayload, false /* messagesRequires */)
	effort := commandCodeReasoningEffort(openaiPayload)

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
		ThreadID:       threadID,
		Params: commandCodeParams{
			Model:    model,
			Messages: messages,
			// Only client-provided tools (empty slice disables upstream builtins).
			Tools: tools,
			// Wire system as a cacheable text section so repeated prefixes can hit
			// Command Code / provider prompt cache instead of billing full input each turn.
			System:          commandCodeWireSystem(system),
			MaxTokens:       maxTokens,
			Stream:          true,
			Temperature:     temperature,
			ReasoningEffort: effort,
		},
	}
	return json.Marshal(body)
}

// buildCommandCodeOpenAIBody builds POST /provider/v1/chat/completions.
func buildCommandCodeOpenAIBody(model string, openaiPayload []byte, stream bool) ([]byte, error) {
	messages := openaiMessagesToProviderOpenAI(openaiPayload)
	commandCodeOffloadImagesInPlace(messages)
	tools := openaiToolsToProviderOpenAI(openaiPayload)
	maxTokens := resolveCommandCodeMaxTokens(model, openaiPayload, false)
	effort := commandCodeReasoningEffort(openaiPayload)

	body := map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   stream,
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if maxTokens != nil {
		body["max_tokens"] = *maxTokens
	}
	if effort != "" {
		body["reasoning_effort"] = effort
	}
	if v := gjson.GetBytes(openaiPayload, "temperature"); v.Exists() && v.Type == gjson.Number {
		body["temperature"] = v.Float()
	}
	if stream {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	return json.Marshal(body)
}

// buildCommandCodeMessagesBody builds POST /provider/v1/messages (Claude).
func buildCommandCodeMessagesBody(model string, openaiPayload []byte, stream bool) ([]byte, error) {
	system, messages := openaiMessagesToProviderMessages(openaiPayload)
	commandCodeOffloadImagesInPlace(messages)
	tools := openaiToolsToProviderMessages(openaiPayload)
	// Messages endpoint requires max_tokens.
	maxTokens := resolveCommandCodeMaxTokens(model, openaiPayload, true)
	if maxTokens == nil {
		cap := registry.CommandCodeMaxOutputTokens(model)
		maxTokens = &cap
	}
	markCommandCodeMessageCache(messages)
	markCommandCodeToolCache(tools)

	body := map[string]any{
		"model":      model,
		"messages":   messages,
		"max_tokens": *maxTokens,
		"stream":     stream,
	}
	if system != nil {
		body["system"] = system
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	effort := commandCodeReasoningEffort(openaiPayload)
	if effort != "" && effort != "off" {
		body["thinking"] = map[string]any{"type": "adaptive"}
		body["output_config"] = map[string]any{"effort": effort}
	}
	return json.Marshal(body)
}

// resolveCommandCodeMaxTokens applies catalog caps and optional omit.
// Client max_tokens/max_completion_tokens of 0 omits the field on CLI/OpenAI
// (Mars-Sea #74). Messages always sends a positive value.
func resolveCommandCodeMaxTokens(model string, openaiPayload []byte, messagesRequires bool) *int {
	capTokens := registry.CommandCodeMaxOutputTokens(model)
	if learned := learnedCommandCodeMaxTokens(model); learned > 0 && (capTokens <= 0 || learned < capTokens) {
		capTokens = learned
	}
	explicitZero := false
	value := capTokens
	if v := gjson.GetBytes(openaiPayload, "max_tokens"); v.Exists() {
		if v.Int() == 0 {
			explicitZero = true
		} else if v.Int() > 0 {
			value = int(v.Int())
		}
	} else if v := gjson.GetBytes(openaiPayload, "max_completion_tokens"); v.Exists() {
		if v.Int() == 0 {
			explicitZero = true
		} else if v.Int() > 0 {
			value = int(v.Int())
		}
	}
	if explicitZero && !messagesRequires {
		return nil
	}
	if capTokens > 0 && value > capTokens {
		value = capTokens
	}
	if value <= 0 {
		value = registry.CommandCodeMaxOutputTokens(model)
	}
	if value <= 0 {
		value = 64000
	}
	return &value
}

var commandCodeLearnedMaxTokens sync.Map // model → int

func learnedCommandCodeMaxTokens(model string) int {
	if v, ok := commandCodeLearnedMaxTokens.Load(strings.TrimSpace(model)); ok {
		if n, ok := v.(int); ok {
			return n
		}
	}
	return 0
}

func noteCommandCodeLearnedMaxTokens(model string, ceiling int) {
	model = strings.TrimSpace(model)
	if model == "" || ceiling <= 0 {
		return
	}
	if prev := learnedCommandCodeMaxTokens(model); prev > 0 && prev <= ceiling {
		return
	}
	commandCodeLearnedMaxTokens.Store(model, ceiling)
}

// commandCodeWireSystem builds the /alpha/generate system payload with an
// ephemeral cache breakpoint (same shape as the official CLI's toWireSystem).
func markCommandCodeMessageCache(messages []any) {
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for j := len(content) - 1; j >= 0; j-- {
			block, ok := content[j].(map[string]any)
			if !ok {
				continue
			}
			switch block["type"] {
			case "text", "tool-result":
				block["cache_control"] = map[string]any{"type": "ephemeral"}
				return
			}
		}
	}
}

func markCommandCodeToolCache(tools []any) {
	if len(tools) == 0 {
		return
	}
	tool, ok := tools[len(tools)-1].(map[string]any)
	if !ok {
		return
	}
	tool["cache_control"] = map[string]any{"type": "ephemeral"}
}

// commandCodeConversationUUID is a stable thread id for clients that do not send
// a session header. It is derived from the system prompt plus the first user
// turn, so later turns of the same chat keep the same cache scope.
func commandCodeConversationUUID(openaiPayload []byte) string {
	_, system := openaiMessagesToCommandCode(openaiPayload)
	system = strings.TrimSpace(system)
	firstUser := ""
	gjson.GetBytes(openaiPayload, "messages").ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "user" {
			return true
		}
		firstUser = strings.TrimSpace(contentToPlainText(msg.Get("content")))
		return false
	})
	if system == "" && firstUser == "" {
		return ""
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("commandcode-conversation\x00"+system+"\x00"+firstUser)).String()
}

func commandCodeReasoningEffort(openaiPayload []byte) string {
	if v := strings.TrimSpace(gjson.GetBytes(openaiPayload, "reasoning_effort").String()); isCommandCodeEffort(v) {
		return strings.ToLower(v)
	}
	return ""
}

func isCommandCodeEffort(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

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
	idSeq := 0
	rawMessages.ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		switch role {
		case "system":
			if t := contentToPlainText(msg.Get("content")); t != "" {
				systemParts = append(systemParts, t)
			}
		case "assistant":
			content := make([]any, 0)
			// DeepSeek thinking-mode tool loops require historical reasoning
			// replayed as {type:reasoning,text} (CLI toWireMessages / issue #34).
			if reasoning := assistantReasoningText(msg); reasoning != "" {
				content = append(content, map[string]any{"type": "reasoning", "text": reasoning})
			}
			if msg.Get("content").Type == gjson.String {
				if t := msg.Get("content").String(); t != "" {
					content = append(content, map[string]any{"type": "text", "text": t})
				}
			} else if msg.Get("content").IsArray() {
				msg.Get("content").ForEach(func(_, part gjson.Result) bool {
					typ := part.Get("type").String()
					switch typ {
					case "text":
						if t := part.Get("text").String(); t != "" {
							content = append(content, map[string]any{"type": "text", "text": t})
						}
					case "reasoning", "thinking":
						t := part.Get("text").String()
						if t == "" {
							t = part.Get("thinking").String()
						}
						if t != "" {
							content = append(content, map[string]any{"type": "reasoning", "text": t})
						}
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
					callID := commandCodeWireToolCallID(call.Get("id").String(), &idSeq)
					content = append(content, map[string]any{
						"type":       "tool-call",
						"toolCallId": callID,
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
			callID := commandCodeWireToolCallID(msg.Get("tool_call_id").String(), &idSeq)
			toolName := msg.Get("name").String()
			if toolName == "" {
				toolName = "unknown"
			}
			messages = append(messages, map[string]any{
				"role": "tool",
				"content": []any{
					map[string]any{
						"type":       "tool-result",
						"toolCallId": callID,
						"toolName":   toolName,
						"output": map[string]any{
							"type":  "text",
							"value": value,
						},
					},
				},
			})
			// CLI / OpenAI cannot carry images inside tool results — follow-up
			// user message holds any data-URL images from the tool payload.
			if media := toolResultImagePartsCLI(msg.Get("content")); len(media) > 0 {
				messages = append(messages, map[string]any{
					"role": "user",
					"content": append([]any{
						map[string]any{"type": "text", "text": "Attached image(s) from tool result:"},
					}, media...),
				})
			}
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
							parts = append(parts, commandCodeCLIImageShape(url))
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

func assistantReasoningText(msg gjson.Result) string {
	for _, path := range []string{"reasoning_content", "reasoning"} {
		if t := strings.TrimSpace(msg.Get(path).String()); t != "" {
			return t
		}
	}
	return ""
}

func toolResultImagePartsCLI(content gjson.Result) []any {
	parts := make([]any, 0)
	if !content.IsArray() {
		return parts
	}
	content.ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() != "image_url" {
			return true
		}
		url := part.Get("image_url.url").String()
		if url == "" {
			url = part.Get("image_url").String()
		}
		if strings.HasPrefix(url, "data:") {
			parts = append(parts, commandCodeCLIImageShape(url))
		}
		return true
	})
	return parts
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
		schema := commandCodeToolParametersSchema(json.RawMessage(fn.Get("parameters").Raw))
		tools = append(tools, map[string]any{
			"name":         fn.Get("name").String(),
			"description":  fn.Get("description").String(),
			"input_schema": schema,
		})
		return true
	})
	return tools
}

// openaiMessagesToProviderOpenAI keeps OpenAI chat shape and replays reasoning_content.
func openaiMessagesToProviderOpenAI(payload []byte) []any {
	out := make([]any, 0)
	rawMessages := gjson.GetBytes(payload, "messages")
	if !rawMessages.IsArray() {
		return out
	}
	idSeq := 0
	rawMessages.ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		switch role {
		case "system", "user":
			m := map[string]any{"role": role}
			if msg.Get("content").Type == gjson.String {
				m["content"] = msg.Get("content").String()
			} else if msg.Get("content").Exists() {
				var content any
				_ = json.Unmarshal([]byte(msg.Get("content").Raw), &content)
				m["content"] = content
			} else {
				m["content"] = ""
			}
			out = append(out, m)
		case "assistant":
			m := map[string]any{"role": "assistant"}
			if msg.Get("content").Type == gjson.String {
				if t := msg.Get("content").String(); t != "" {
					m["content"] = t
				} else {
					m["content"] = nil
				}
			} else if msg.Get("content").IsArray() {
				m["content"] = contentToPlainText(msg.Get("content"))
			} else {
				m["content"] = nil
			}
			if reasoning := assistantReasoningText(msg); reasoning != "" {
				m["reasoning_content"] = reasoning
			}
			if msg.Get("tool_calls").IsArray() {
				calls := make([]any, 0)
				msg.Get("tool_calls").ForEach(func(_, call gjson.Result) bool {
					calls = append(calls, map[string]any{
						"id":   commandCodeWireToolCallID(call.Get("id").String(), &idSeq),
						"type": "function",
						"function": map[string]any{
							"name":      call.Get("function.name").String(),
							"arguments": call.Get("function.arguments").String(),
						},
					})
					return true
				})
				if len(calls) > 0 {
					m["tool_calls"] = calls
				}
			}
			out = append(out, m)
		case "tool":
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": commandCodeWireToolCallID(msg.Get("tool_call_id").String(), &idSeq),
				"content":      contentToPlainText(msg.Get("content")),
			})
			if media := toolResultImagePartsOpenAI(msg.Get("content")); len(media) > 0 {
				out = append(out, map[string]any{
					"role": "user",
					"content": append([]any{
						map[string]any{"type": "text", "text": "Attached image(s) from tool result:"},
					}, media...),
				})
			}
		}
		return true
	})
	return out
}

func toolResultImagePartsOpenAI(content gjson.Result) []any {
	parts := make([]any, 0)
	if !content.IsArray() {
		return parts
	}
	content.ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() != "image_url" {
			return true
		}
		url := part.Get("image_url.url").String()
		if url == "" {
			url = part.Get("image_url").String()
		}
		if strings.HasPrefix(url, "data:") {
			parts = append(parts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": url},
			})
		}
		return true
	})
	return parts
}

func openaiToolsToProviderOpenAI(payload []byte) []any {
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
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        fn.Get("name").String(),
				"description": fn.Get("description").String(),
				"parameters":   commandCodeToolParametersSchema(json.RawMessage(fn.Get("parameters").Raw)),
			},
		})
		return true
	})
	return tools
}

// openaiMessagesToProviderMessages converts OpenAI chat history to Anthropic Messages.
func openaiMessagesToProviderMessages(payload []byte) (system any, messages []any) {
	var systemParts []string
	messages = make([]any, 0)
	rawMessages := gjson.GetBytes(payload, "messages")
	if !rawMessages.IsArray() {
		return nil, messages
	}
	idSeq := 0
	rawMessages.ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		switch role {
		case "system":
			if t := contentToPlainText(msg.Get("content")); t != "" {
				systemParts = append(systemParts, t)
			}
		case "assistant":
			blocks := make([]any, 0)
			// Messages thinking blocks need signatures; without one, drop reasoning
			// rather than send a half-formed thinking block (Mars-Sea adapter rule).
			if msg.Get("content").Type == gjson.String {
				if t := msg.Get("content").String(); t != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": t})
				}
			} else if msg.Get("content").IsArray() {
				msg.Get("content").ForEach(func(_, part gjson.Result) bool {
					if part.Get("type").String() == "text" && part.Get("text").String() != "" {
						blocks = append(blocks, map[string]any{"type": "text", "text": part.Get("text").String()})
					}
					return true
				})
			}
			if msg.Get("tool_calls").IsArray() {
				msg.Get("tool_calls").ForEach(func(_, call gjson.Result) bool {
					input := map[string]any{}
					args := call.Get("function.arguments").String()
					if args != "" {
						_ = json.Unmarshal([]byte(args), &input)
					}
					blocks = append(blocks, map[string]any{
						"type":  "tool_use",
						"id":    commandCodeWireToolCallID(call.Get("id").String(), &idSeq),
						"name":  call.Get("function.name").String(),
						"input": input,
					})
					return true
				})
			}
			if len(blocks) > 0 {
				messages = append(messages, map[string]any{"role": "assistant", "content": blocks})
			}
		case "tool":
			content := make([]any, 0)
			if msg.Get("content").Type == gjson.String {
				content = append(content, map[string]any{"type": "text", "text": msg.Get("content").String()})
			} else if msg.Get("content").IsArray() {
				msg.Get("content").ForEach(func(_, part gjson.Result) bool {
					switch part.Get("type").String() {
					case "text":
						content = append(content, map[string]any{"type": "text", "text": part.Get("text").String()})
					case "image_url":
						url := part.Get("image_url.url").String()
						if mediaType, data, ok := splitDataURL(url); ok {
							content = append(content, map[string]any{
								"type": "image",
								"source": map[string]any{
									"type":       "base64",
									"media_type": mediaType,
									"data":       data,
								},
							})
						}
					}
					return true
				})
			}
			if len(content) == 0 {
				content = append(content, map[string]any{"type": "text", "text": ""})
			}
			messages = append(messages, map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": commandCodeWireToolCallID(msg.Get("tool_call_id").String(), &idSeq),
						"content":     content,
					},
				},
			})
		default: // user
			parts := make([]any, 0)
			if msg.Get("content").Type == gjson.String {
				parts = append(parts, map[string]any{"type": "text", "text": msg.Get("content").String()})
			} else if msg.Get("content").IsArray() {
				msg.Get("content").ForEach(func(_, part gjson.Result) bool {
					switch part.Get("type").String() {
					case "text":
						parts = append(parts, map[string]any{"type": "text", "text": part.Get("text").String()})
					case "image_url":
						url := part.Get("image_url.url").String()
						if mediaType, data, ok := splitDataURL(url); ok {
							parts = append(parts, map[string]any{
								"type": "image",
								"source": map[string]any{
									"type":       "base64",
									"media_type": mediaType,
									"data":       data,
								},
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
	if len(systemParts) == 0 {
		return nil, messages
	}
	text := strings.Join(systemParts, "\n\n")
	system = []any{
		map[string]any{
			"type":          "text",
			"text":          text,
			"cache_control": map[string]any{"type": "ephemeral"},
		},
	}
	return system, messages
}

func splitDataURL(url string) (mediaType, data string, ok bool) {
	if !strings.HasPrefix(url, "data:") {
		return "", "", false
	}
	comma := strings.Index(url, ",")
	if comma < 0 {
		return "", "", false
	}
	meta := url[5:comma]
	data = url[comma+1:]
	mediaType = "image/png"
	if i := strings.Index(meta, ";"); i > 0 {
		mediaType = meta[:i]
	} else if meta != "" {
		mediaType = meta
	}
	return mediaType, data, true
}

func openaiToolsToProviderMessages(payload []byte) []any {
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
		tools = append(tools, map[string]any{
			"name":         fn.Get("name").String(),
			"description":  fn.Get("description").String(),
			"input_schema": commandCodeToolParametersSchema(json.RawMessage(fn.Get("parameters").Raw)),
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
	cacheRead := src.Get("inputTokenDetails.cacheReadTokens").Int()
	if cacheRead == 0 {
		cacheRead = src.Get("cacheReadTokens").Int()
	}
	cacheWrite := src.Get("inputTokenDetails.cacheWriteTokens").Int()
	if cacheWrite == 0 {
		cacheWrite = src.Get("cacheWriteTokens").Int()
	}
	// AI SDK totalUsage.inputTokens is inputTokens.total: noCache + cacheRead + cacheWrite.
	// Command Code's cache share is cacheRead / inputTokens. Adding the cache
	// buckets again inflates the denominator and cuts the reported rate.
	// Only the older shape (inputTokens is the uncached remainder, so it is
	// smaller than the cache buckets) needs those buckets folded in.
	includesCache := commandCodeInputIncludesCache(prompt, cacheRead, cacheWrite)
	if !includesCache {
		prompt += cacheRead + cacheWrite
	}
	total := src.Get("totalTokens").Int()
	if total == 0 || !includesCache {
		total = prompt + completion
	}
	usage := map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": completion,
		"total_tokens":      total,
	}
	if cacheRead > 0 || cacheWrite > 0 {
		details := map[string]any{}
		if cacheRead > 0 {
			details["cached_tokens"] = cacheRead
		}
		if cacheWrite > 0 {
			// Nested so OpenAI usage accounting treats the write as a subset of prompt_tokens.
			details["cache_write_tokens"] = cacheWrite
		}
		usage["prompt_tokens_details"] = details
		usage["cache_read_input_tokens"] = cacheRead
		usage["cache_creation_input_tokens"] = cacheWrite
	}
	return usage
}

// commandCodeInputIncludesCache reports whether inputTokens already contains
// the cache read and write buckets. The AI SDK total does; a raw uncached count does not.
func commandCodeInputIncludesCache(input, cacheRead, cacheWrite int64) bool {
	if cacheRead < 0 {
		cacheRead = 0
	}
	if cacheWrite < 0 {
		cacheWrite = 0
	}
	if cacheRead > input {
		return false
	}
	return input-cacheRead >= cacheWrite
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
