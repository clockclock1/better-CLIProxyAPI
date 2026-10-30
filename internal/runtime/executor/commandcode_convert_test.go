package executor

import (
	"encoding/json"
	"testing"
)

func TestBuildCommandCodeGenerateBodyNeutralizesWorkspace(t *testing.T) {
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
		t.Fatalf("workingDir = %q, want /", body.Config.WorkingDir)
	}
	if body.Config.Environment != "proxy" {
		t.Fatalf("environment = %q, want proxy", body.Config.Environment)
	}
	if body.Config.IsGitRepo || body.Config.GitStatus != "" || len(body.Config.RecentCommits) != 0 {
		t.Fatalf("unexpected git context: %+v", body.Config)
	}
	if body.Params.System != commandCodeNeutralSystem {
		t.Fatalf("system = %q, want neutral suppressor", body.Params.System)
	}
	if len(body.Params.Tools) != 1 {
		t.Fatalf("tools = %#v, want only client tool", body.Params.Tools)
	}
	tool, _ := body.Params.Tools[0].(map[string]any)
	if tool["name"] != "WebSearch" {
		t.Fatalf("tool name = %#v, want WebSearch", tool["name"])
	}
}

func TestBuildCommandCodeGenerateBodyKeepsClientSystemAndNoBuiltinTools(t *testing.T) {
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
	if body.Params.System != "Cursor agent system prompt" {
		t.Fatalf("system = %q", body.Params.System)
	}
	if body.Params.Tools == nil || len(body.Params.Tools) != 0 {
		t.Fatalf("tools = %#v, want empty client tool list", body.Params.Tools)
	}
	if body.Config.WorkingDir != "/" {
		t.Fatalf("workingDir = %q", body.Config.WorkingDir)
	}
}

func TestCommandCodeWorkingDirFromMetadata(t *testing.T) {
	payload := []byte(`{"metadata":{"working_dir":"/tmp/project"},"messages":[{"role":"user","content":"x"}]}`)
	if got := commandCodeWorkingDir(payload); got != "/tmp/project" {
		t.Fatalf("got %q", got)
	}
}
