package executor

import (
	"encoding/json"
	"testing"
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
	raw, err := buildCommandCodeGenerateBody("deepseek/deepseek-v4.1-flash", payload)
	if err != nil {
		t.Fatal(err)
	}
	var body commandCodeGenerateBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.Config.WorkingDir != "/" {
		t.Fatalf("workingDir = %q, want neutral /", body.Config.WorkingDir)
	}
	assertCachedSystem(t, body.Params.System, commandCodeNeutralSystem)
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
}

func TestBuildCommandCodeGenerateBodyKeepsClientSystem(t *testing.T) {
	payload := []byte(`{
		"model":"deepseek/deepseek-v4.1-flash",
		"messages":[
			{"role":"system","content":"Cursor agent system prompt"},
			{"role":"user","content":"hi"}
		]
	}`)
	raw, err := buildCommandCodeGenerateBody("deepseek/deepseek-v4.1-flash", payload)
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

func TestCommandCodeWorkingDirFromMetadata(t *testing.T) {
	payload := []byte(`{"metadata":{"working_dir":"/tmp/project"},"messages":[{"role":"user","content":"x"}]}`)
	if got := commandCodeWorkingDir(payload); got != "/tmp/project" {
		t.Fatalf("got %q", got)
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
