package executor

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeCommandCodeToolParametersAllOf(t *testing.T) {
	raw := json.RawMessage(`{
		"allOf":[
			{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},
			{"type":"object","properties":{"b":{"type":"number"}},"required":["b"]}
		]
	}`)
	schema := commandCodeToolParametersSchema(raw)
	if schema["type"] != "object" {
		t.Fatalf("type=%#v", schema["type"])
	}
	props, _ := schema["properties"].(map[string]any)
	if props["a"] == nil || props["b"] == nil {
		t.Fatalf("properties=%#v", props)
	}
}

func TestCommandCodeWireToolCallIDClamp(t *testing.T) {
	seq := 0
	short := commandCodeWireToolCallID("call_abc", &seq)
	if short != "call_abc" {
		t.Fatalf("short id mutated: %q", short)
	}
	long := strings.Repeat("x", 80)
	alias := commandCodeWireToolCallID(long, &seq)
	if len(alias) > 64 || !strings.HasPrefix(alias, "cc-") {
		t.Fatalf("alias=%q", alias)
	}
}
