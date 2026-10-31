package executor

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

const commandCodeSchemaNormalizeMaxDepth = 8

// commandCodeToolParametersSchema normalizes a tool JSON Schema root to
// type:"object" so Command Code does not reject MCP/plugin schemas
// (Mars-Sea issue #35 / official CLI validator).
func commandCodeToolParametersSchema(raw json.RawMessage) map[string]any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": true}
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": true}
	}
	return normalizeCommandCodeToolParameters(parsed, 0)
}

func normalizeCommandCodeToolParameters(parameters any, depth int) map[string]any {
	obj, ok := parameters.(map[string]any)
	if !ok {
		return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": true}
	}
	if typ, ok := obj["type"].(string); ok && typ == "object" {
		return obj
	}
	if types, ok := obj["type"].([]any); ok {
		for _, t := range types {
			if s, ok := t.(string); ok && s == "object" {
				out := cloneAnyMap(obj)
				out["type"] = "object"
				return out
			}
		}
	}
	if obj["type"] == nil {
		if ref, ok := obj["$ref"].(string); ok && depth < commandCodeSchemaNormalizeMaxDepth {
			if target := resolveCommandCodeLocalRef(obj, ref); target != nil {
				merged := cloneAnyMap(target)
				for k, v := range obj {
					if k == "$ref" {
						continue
					}
					merged[k] = v
				}
				return normalizeCommandCodeToolParameters(merged, depth+1)
			}
		}
		if isCommandCodeObjectShaped(obj) {
			out := cloneAnyMap(obj)
			out["type"] = "object"
			return out
		}
		if merged := mergeCommandCodeCombinator(obj, depth); merged != nil {
			return merged
		}
		if _, ok := obj["$ref"].(string); ok {
			out := cloneAnyMap(obj)
			out["type"] = "object"
			return out
		}
	}
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": true}
}

func isCommandCodeObjectShaped(obj map[string]any) bool {
	if _, ok := obj["properties"]; ok {
		return true
	}
	if _, ok := obj["required"]; ok {
		return true
	}
	if _, ok := obj["additionalProperties"]; ok {
		return true
	}
	return false
}

func mergeCommandCodeCombinator(obj map[string]any, depth int) map[string]any {
	if depth >= commandCodeSchemaNormalizeMaxDepth {
		return nil
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		raw, ok := obj[key].([]any)
		if !ok || len(raw) == 0 {
			continue
		}
		props := map[string]any{}
		var required []string
		requiredCounts := map[string]int{}
		branches := 0
		for _, branch := range raw {
			norm := normalizeCommandCodeToolParameters(branch, depth+1)
			branches++
			if p, ok := norm["properties"].(map[string]any); ok {
				for k, v := range p {
					props[k] = v
				}
			}
			if req, ok := norm["required"].([]any); ok {
				for _, r := range req {
					if s, ok := r.(string); ok {
						requiredCounts[s]++
					}
				}
			} else if req, ok := norm["required"].([]string); ok {
				for _, s := range req {
					requiredCounts[s]++
				}
			}
		}
		if branches == 0 {
			continue
		}
		for k, n := range requiredCounts {
			if n == branches {
				required = append(required, k)
			}
		}
		out := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			out["required"] = required
		}
		return out
	}
	return nil
}

func resolveCommandCodeLocalRef(root map[string]any, ref string) map[string]any {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
	var cur any = root
	// Local $defs live beside the schema; walk from a synthetic root that also
	// looks at sibling defs on the same object.
	for _, part := range parts {
		part = strings.ReplaceAll(part, "~1", "/")
		part = strings.ReplaceAll(part, "~0", "~")
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[part]
			if !ok {
				return nil
			}
			cur = next
		default:
			return nil
		}
	}
	out, _ := cur.(map[string]any)
	return out
}

func cloneAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// commandCodeWireToolCallID clamps ids to the CLI 64-char limit (issue #23).
func commandCodeWireToolCallID(id string, seq *int) string {
	id = strings.TrimSpace(id)
	if len(id) <= 64 {
		return id
	}
	*seq++
	return "cc-" + strconv.Itoa(*seq)
}
