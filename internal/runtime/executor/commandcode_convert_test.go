package executor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestBuildCommandCodeGenerateBodyNeutralWorkspace(t *testing.T) {
	payload := []byte(`{
		"model":"deepseek/deepseek-v4.1-flash",
		"messages":[{"role":"user","content":"你好"}],
		"tools":[{
			"type":"function",
			"function":{
				"name":"WebSearch",
				"description":"search the web",
				"parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}
			}
		}]
	}`)
	raw, err := buildCommandCodeGenerateBody("deepseek/deepseek-v4.1-flash", "", payload)
	if err != nil {
		t.Fatal(err)
	}
	var body commandCodeGenerateBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.ThreadID != commandCodeConversationUUID(payload) {
		t.Fatalf("threadId = %q, want conversation uuid", body.ThreadID)
	}
	if strings.Contains(string(raw), `"promptCache"`) {
		t.Fatalf("promptCache must stay omitted, got %s", raw)
	}
	if body.Params.MaxTokens == nil || *body.Params.MaxTokens != 64000 {
		t.Fatalf("max_tokens = %#v, want catalog default 64000", body.Params.MaxTokens)
	}
	if body.Config.WorkingDir != "/" {
		t.Fatalf("workingDir = %q, want neutral /", body.Config.WorkingDir)
	}
	sections, ok := body.Params.System.([]any)
	if !ok || len(sections) != 1 {
		t.Fatalf("system = %#v, want single-space placeholder wire block", body.Params.System)
	}
	section, _ := sections[0].(map[string]any)
	if section["type"] != "text" || section["text"] != " " {
		t.Fatalf("system section = %#v, want text space placeholder", section)
	}
	if _, hasCache := section["cache_control"]; hasCache {
		t.Fatalf("space placeholder must not set cache_control, got %#v", section)
	}
	if strings.Contains(string(raw), "Do not invent a working directory") ||
		strings.Contains(string(raw), "You are a helpful assistant") ||
		strings.Contains(string(raw), "confident, efficient software engineer") {
		t.Fatalf("proxy must not inject persona/system prose, body=%s", raw)
	}
	if body.Config.IsGitRepo || len(body.Config.Structure) != 0 || body.Config.GitStatus != "" {
		t.Fatalf("workspace prompt fields must stay empty: %+v", body.Config)
	}
	if body.Config.Environment != "proxy" {
		t.Fatalf("environment = %q, want proxy", body.Config.Environment)
	}
	if body.Memory != nil || body.Taste != nil || body.Skills != nil {
		t.Fatalf("memory/taste/skills must be nil, got memory=%v taste=%v skills=%v", body.Memory, body.Taste, body.Skills)
	}
	if len(body.Params.Tools) != 1 {
		t.Fatalf("tools = %#v, want only client tool", body.Params.Tools)
	}
	tool, _ := body.Params.Tools[0].(map[string]any)
	if tool["name"] != "WebSearch" {
		t.Fatalf("tool name = %#v, want WebSearch", tool["name"])
	}
	cache, _ := tool["cache_control"].(map[string]any)
	if cache["type"] != "ephemeral" {
		t.Fatalf("last tool cache_control = %#v", tool["cache_control"])
	}
	msg, _ := body.Params.Messages[0].(map[string]any)
	content, _ := msg["content"].([]any)
	block, _ := content[0].(map[string]any)
	msgCache, _ := block["cache_control"].(map[string]any)
	if msgCache["type"] != "ephemeral" {
		t.Fatalf("latest message cache_control = %#v", block["cache_control"])
	}
}

func TestBuildCommandCodeGenerateBodyKeepsClientSystem(t *testing.T) {
	payload := []byte(`{
		"model":"deepseek/deepseek-v4.1-flash",
		"messages":[
			{"role":"system","content":"Cursor agent system prompt"},
			{"role":"user","content":"hi"}
		]
	}`)
	raw, err := buildCommandCodeGenerateBody("deepseek/deepseek-v4.1-flash", "", payload)
	if err != nil {
		t.Fatal(err)
	}
	var body commandCodeGenerateBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	assertCachedSystem(t, body.Params.System, "Cursor agent system prompt")
	if body.Params.Tools == nil || len(body.Params.Tools) != 0 {
		t.Fatalf("tools = %#v, want empty client tool list", body.Params.Tools)
	}
}

func TestBuildCommandCodeGenerateBodyEmptySystemPlaceholderOff(t *testing.T) {
	t.Setenv("COMMANDCODE_EMPTY_SYSTEM_PLACEHOLDER", "false")
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	raw, err := buildCommandCodeGenerateBody("deepseek/deepseek-v4.1-flash", "", payload)
	if err != nil {
		t.Fatal(err)
	}
	var body commandCodeGenerateBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if sys, ok := body.Params.System.(string); !ok || sys != "" {
		t.Fatalf("system = %#v, want empty string when placeholder disabled", body.Params.System)
	}
}

func TestCommandCodeWorkingDirFromMetadata(t *testing.T) {
	payload := []byte(`{"metadata":{"working_dir":"/tmp/project"},"messages":[{"role":"user","content":"x"}]}`)
	if got := commandCodeWorkingDir(payload); got != "/tmp/project" {
		t.Fatalf("got %q", got)
	}
}

func TestCommandCodeConversationUUIDStableAcrossTurns(t *testing.T) {
	first := []byte(`{"messages":[{"role":"system","content":"stable system"},{"role":"user","content":"hello"}]}`)
	second := []byte(`{"messages":[{"role":"system","content":"stable system"},{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"again"}]}`)
	other := []byte(`{"messages":[{"role":"system","content":"stable system"},{"role":"user","content":"different"}]}`)
	a := commandCodeConversationUUID(first)
	b := commandCodeConversationUUID(second)
	c := commandCodeConversationUUID(other)
	if a == "" || a != b {
		t.Fatalf("conversation uuid not stable: %q vs %q", a, b)
	}
	if a == c {
		t.Fatalf("different first turn reused uuid %q", a)
	}
}

func TestUsageFromCommandCodeTotalIncludesCache(t *testing.T) {
	// AI SDK inputTokens.total already includes cache read and write.
	// Cache share is cacheRead / inputTokens = 100/130.
	event := []byte(`{"type":"finish","totalUsage":{"inputTokens":130,"outputTokens":4,"totalTokens":134,"inputTokenDetails":{"cacheReadTokens":100,"cacheWriteTokens":20}}}`)
	usage := usageFromCommandCodeTotal(gjson.ParseBytes(event))
	if usage["prompt_tokens"] != int64(130) {
		t.Fatalf("prompt_tokens = %#v, want 130", usage["prompt_tokens"])
	}
	if usage["total_tokens"] != int64(134) {
		t.Fatalf("total_tokens = %#v, want 134", usage["total_tokens"])
	}
	details, _ := usage["prompt_tokens_details"].(map[string]any)
	if details["cached_tokens"] != int64(100) {
		t.Fatalf("cached_tokens = %#v", details["cached_tokens"])
	}
	if details["cache_write_tokens"] != int64(20) {
		t.Fatalf("cache_write_tokens = %#v", details["cache_write_tokens"])
	}
}

func TestUsageFromCommandCodeTotalFoldsExcludedCache(t *testing.T) {
	event := []byte(`{"type":"finish","totalUsage":{"inputTokens":10,"outputTokens":4,"inputTokenDetails":{"cacheReadTokens":100,"cacheWriteTokens":20}}}`)
	usage := usageFromCommandCodeTotal(gjson.ParseBytes(event))
	if usage["prompt_tokens"] != int64(130) {
		t.Fatalf("prompt_tokens = %#v, want 130", usage["prompt_tokens"])
	}
	if usage["total_tokens"] != int64(134) {
		t.Fatalf("total_tokens = %#v, want 134", usage["total_tokens"])
	}
}

func assertCachedSystem(t *testing.T, system any, wantText string) {
	t.Helper()
	sections, ok := system.([]any)
	if !ok || len(sections) != 1 {
		t.Fatalf("system = %#v, want one cached text section", system)
	}
	section, _ := sections[0].(map[string]any)
	if section["type"] != "text" || section["text"] != wantText {
		t.Fatalf("system section = %#v, want text %q", section, wantText)
	}
	cache, _ := section["cache_control"].(map[string]any)
	if cache["type"] != "ephemeral" {
		t.Fatalf("cache_control = %#v, want ephemeral", section["cache_control"])
	}
}

func TestOpenAIMessagesToCommandCodeReplaysReasoning(t *testing.T) {
	payload := []byte(`{
		"messages":[{
			"role":"assistant",
			"content":"ok",
			"reasoning_content":"think first",
			"tool_calls":[{"id":"call_1","type":"function","function":{"name":"Read","arguments":"{\"path\":\"a\"}"}}]
		}]
	}`)
	messages, _ := openaiMessagesToCommandCode(payload)
	if len(messages) != 1 {
		t.Fatalf("messages = %#v", messages)
	}
	msg := messages[0].(map[string]any)
	content := msg["content"].([]any)
	if len(content) < 3 {
		t.Fatalf("content = %#v, want reasoning+text+tool-call", content)
	}
	first := content[0].(map[string]any)
	if first["type"] != "reasoning" || first["text"] != "think first" {
		t.Fatalf("first block = %#v, want reasoning", first)
	}
}

func TestOpenAIMessagesToProviderOpenAIReplaysReasoningContent(t *testing.T) {
	payload := []byte(`{
		"messages":[{
			"role":"assistant",
			"content":null,
			"reasoning_content":"beam thinks",
			"tool_calls":[{"id":"call_1","type":"function","function":{"name":"X","arguments":"{}"}}]
		}]
	}`)
	messages := openaiMessagesToProviderOpenAI(payload)
	if len(messages) != 1 {
		t.Fatalf("messages = %#v", messages)
	}
	msg := messages[0].(map[string]any)
	if msg["reasoning_content"] != "beam thinks" {
		t.Fatalf("reasoning_content = %#v", msg["reasoning_content"])
	}
	if msg["tool_calls"] == nil {
		t.Fatal("expected tool_calls")
	}
}

func TestCommandCodeToolSchemaNormalizeRoot(t *testing.T) {
	schema := commandCodeToolParametersSchema(json.RawMessage(`{"properties":{"q":{"type":"string"}},"required":["q"]}`))
	if schema["type"] != "object" {
		t.Fatalf("type = %#v, want object", schema["type"])
	}
	refSchema := commandCodeToolParametersSchema(json.RawMessage(`{
		"$defs":{"P":{"type":"object","properties":{"a":{"type":"string"}}}},
		"$ref":"#/$defs/P"
	}`))
	if refSchema["type"] != "object" {
		t.Fatalf("ref schema type = %#v", refSchema["type"])
	}
}

func TestResolveCommandCodeMaxTokensOmit(t *testing.T) {
	payload := []byte(`{"max_tokens":0}`)
	if got := resolveCommandCodeMaxTokens("deepseek/deepseek-v4.1-flash", payload, false); got != nil {
		t.Fatalf("omit path got %#v, want nil", got)
	}
	if got := resolveCommandCodeMaxTokens("claude-sonnet-5-5", payload, true); got == nil || *got <= 0 {
		t.Fatalf("messages requires positive max_tokens, got %#v", got)
	}
}

func TestResolveCommandCodeProtocolMessagesForClaude(t *testing.T) {
	if got := resolveCommandCodeProtocol("claude-sonnet-5-5", "https://api.commandcode.ai", "key"); got != commandCodeProtocolMessages {
		t.Fatalf("got %q, want messages", got)
	}
	t.Setenv("COMMANDCODE_PROTOCOL", "cli")
	if got := resolveCommandCodeProtocol("deepseek/deepseek-v4.1-flash", "https://api.commandcode.ai", "key"); got != commandCodeProtocolCLI {
		t.Fatalf("forced cli got %q", got)
	}
}

func TestCommandCodeOffloadImagesBudget(t *testing.T) {
	big := "data:image/png;base64," + strings.Repeat("A", commandCodeImageMaxBytes/2)
	messages := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				commandCodeCLIImageShape(big),
				commandCodeCLIImageShape(big),
				commandCodeCLIImageShape(big),
			},
		},
	}
	commandCodeOffloadImagesInPlace(messages)
	content := messages[0].(map[string]any)["content"].([]any)
	omitted := 0
	kept := 0
	for _, part := range content {
		block := part.(map[string]any)
		if block["type"] == "text" && block["text"] == commandCodeImageOmitted {
			omitted++
		}
		if block["type"] == "image" {
			kept++
		}
	}
	if omitted == 0 {
		t.Fatalf("expected some images offloaded, content=%#v", content)
	}
	if kept == 0 {
		t.Fatalf("expected at least one image kept under budget")
	}
}

func TestBuildCommandCodeOpenAIBody(t *testing.T) {
	payload := []byte(`{
		"messages":[
			{"role":"system","content":"sys"},
			{"role":"assistant","content":"","reasoning_content":"r","tool_calls":[{"id":"c1","type":"function","function":{"name":"T","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"c1","content":"done"},
			{"role":"user","content":"hi"}
		],
		"tools":[{"type":"function","function":{"name":"T","parameters":{"properties":{}}}}]
	}`)
	raw, err := buildCommandCodeOpenAIBody("deepseek/deepseek-v4.1-flash", payload, true)
	if err != nil {
		t.Fatal(err)
	}
	if !gjson.GetBytes(raw, "stream").Bool() {
		t.Fatal("stream want true")
	}
	foundReasoning := false
	gjson.GetBytes(raw, "messages").ForEach(func(_, m gjson.Result) bool {
		if m.Get("reasoning_content").String() == "r" {
			foundReasoning = true
		}
		return true
	})
	if !foundReasoning {
		t.Fatalf("missing reasoning_content in body: %s", raw)
	}
	if gjson.GetBytes(raw, "tools.0.function.parameters.type").String() != "object" {
		t.Fatalf("tool schema not normalized: %s", raw)
	}
}

func TestIsCommandCodeUpgradeRequired(t *testing.T) {
	if !isCommandCodeUpgradeRequired(403, []byte(`{"error":{"code":"upgrade_required"}}`)) {
		t.Fatal("expected upgrade_required")
	}
	if isCommandCodeUpgradeRequired(500, []byte(`internal`)) {
		t.Fatal("500 without upgrade_required should be false")
	}
}
