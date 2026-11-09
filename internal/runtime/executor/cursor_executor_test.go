package executor

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cursor"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// TestCursorPromptFlattening covers the transcript Cursor receives: system
// prompts merge, tool results are labelled, and tool calls stay legible.
func TestCursorPromptFlattening(t *testing.T) {
	payload := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "You are a coding agent."},
			map[string]any{"role": "developer", "content": "Be concise."},
			map[string]any{"role": "user", "content": "read notes.txt"},
			map[string]any{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []any{
					map[string]any{
						"id":       "call_1",
						"type":     "function",
						"function": map[string]any{"name": "read_file", "arguments": `{"file_path":"notes.txt"}`},
					},
				},
			},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "name": "read_file", "content": "hello world"},
		},
	}
	encoded, _ := json.Marshal(payload)

	prompt := cursorPromptFromPayload(encoded)
	for _, want := range []string{
		"You are a coding agent.",
		"Be concise.",
		"User: read notes.txt",
		`"name":"read_file"`,
		`"file_path":"notes.txt"`,
		"Tool result (tool_call_id=call_1 name=read_file):",
		"hello world",
	} {
		if !contains(prompt, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, prompt)
		}
	}
}

func TestCursorPromptFlatteningMultipartContent(t *testing.T) {
	payload := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "first part"},
					map[string]any{"type": "text", "text": "second part"},
				},
			},
		},
	}
	encoded, _ := json.Marshal(payload)
	prompt := cursorPromptFromPayload(encoded)
	if !contains(prompt, "first part") || !contains(prompt, "second part") {
		t.Errorf("multipart content lost: %q", prompt)
	}
}

func TestCursorPromptEmpty(t *testing.T) {
	if got := cursorPromptFromPayload([]byte(`{}`)); got != "" {
		t.Errorf("empty payload should give empty prompt, got %q", got)
	}
}

// TestCursorWireModelPinning locks the Free-tier mapping discovered by probing:
// the bare grok-4.7 id is rejected upstream, only the -medium variant runs.
func TestCursorWireModelPinning(t *testing.T) {
	cases := map[string]string{
		"":                 "default",
		"auto":             "default",
		"default":          "default",
		"grok-4.7":         "grok-4.7-medium",
		"grok-4-7":         "grok-4.7-medium",
		"cursor-grok-4.7":  "grok-4.7-medium",
		"grok-4.6":         "grok-4.6",
		"grok-4-6":         "grok-4.6",
		"cursor/grok-4.7":  "grok-4.7-medium",
		"cursor/default":   "default",
		"some-other-model": "some-other-model",
	}
	for input, want := range cases {
		if got := cursorWireModel(input); got != want {
			t.Errorf("cursorWireModel(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCursorStatusFromCode(t *testing.T) {
	cases := map[string]int{
		"resource_exhausted": 429,
		"unauthenticated":    401,
		"permission_denied":  403,
		"invalid_argument":   400,
		"not_found":          404,
		"unavailable":        502,
		"internal":           502,
	}
	for code, want := range cases {
		if got := cursorStatusFromCode(code, ""); got != want {
			t.Errorf("cursorStatusFromCode(%q) = %d, want %d", code, got, want)
		}
	}
}

// TestCursorStatusFromDetail covers the generic "Error" envelopes Cursor sends,
// where the status is only recoverable from the human-readable detail.
func TestCursorStatusFromDetail(t *testing.T) {
	cases := map[string]int{
		"You've hit your usage limit":   429,
		"Authentication error":          401,
		"Model unavailable on Free":     403,
		"ERROR_RATE_LIMITED_CHANGEABLE": 429,
		"something unrecognised":        502,
	}
	for detail, want := range cases {
		if got := cursorStatusFromCode("", detail); got != want {
			t.Errorf("cursorStatusFromCode(\"\", %q) = %d, want %d", detail, got, want)
		}
	}
}

// TestCursorStatusErrorKeepsHTTPStatus ensures a real HTTP failure is not
// rewritten by the end-stream classifier.
func TestCursorStatusErrorKeepsHTTPStatus(t *testing.T) {
	runErr := &cursor.RunError{Code: "internal", Message: "boom", Detail: "x"}
	errStatus := cursorStatusErrorForRun(runErr, 500, nil)
	if errStatus.StatusCode() != 500 {
		t.Errorf("StatusCode() = %d, want 500", errStatus.StatusCode())
	}
}

func TestCursorStatusErrorNilRunError(t *testing.T) {
	errStatus := cursorStatusErrorForRun(nil, 200, nil)
	if errStatus.StatusCode() != 502 {
		t.Errorf("StatusCode() = %d, want fallback 502", errStatus.StatusCode())
	}
}

func TestCursorStreamChunkShape(t *testing.T) {
	raw := string(buildOpenAIStreamChunk("cursor/default", "hello", false))
	if !contains(raw, `"content":"hello"`) {
		t.Errorf("chunk missing content: %q", raw)
	}
	if !contains(raw, `"object":"chat.completion.chunk"`) {
		t.Errorf("chunk object wrong: %q", raw)
	}
}

func TestCursorStreamChunkFinish(t *testing.T) {
	raw := string(buildOpenAIStreamChunk("m", "", true))
	if !contains(raw, `"finish_reason":"stop"`) {
		t.Errorf("chunk missing finish_reason: %q", raw)
	}
}

func TestBuildOpenAIChatResponse(t *testing.T) {
	raw := buildOpenAIChatResponse("cursor/default", "hi there")
	var decoded map[string]any
	if errJSON := json.Unmarshal(raw, &decoded); errJSON != nil {
		t.Fatalf("response is not JSON: %v", errJSON)
	}
	choices, _ := decoded["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices = %v", decoded["choices"])
	}
	message, _ := choices[0].(map[string]any)["message"].(map[string]any)
	if message["content"] != "hi there" {
		t.Errorf("content = %v", message["content"])
	}
	if message["role"] != "assistant" {
		t.Errorf("role = %v", message["role"])
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func startsWith(value, prefix string) bool {
	return len(value) >= len(prefix) && value[:len(prefix)] == prefix
}

// TestCursorPrepareRequestPerAccountMachine covers the proxy-wide guarantee that
// every account reaches Cursor as its own machine: the checksum must carry that
// account's machine id, and two accounts must never share one.
func TestCursorPrepareRequestPerAccountMachine(t *testing.T) {
	exec := NewCursorExecutor(nil)

	machineOf := func(email, token string) string {
		auth := &cliproxyauth.Auth{
			Provider:   cursor.Provider,
			Attributes: map[string]string{"access_token": token, "email": email},
			Metadata:   map[string]any{"email": email, "access_token": token},
		}
		req, errReq := http.NewRequest(http.MethodPost, "https://agentn.global.api5.cursor.sh"+cursor.AgentPath, nil)
		if errReq != nil {
			t.Fatalf("build request: %v", errReq)
		}
		if errPrep := exec.PrepareRequest(req, auth); errPrep != nil {
			t.Fatalf("prepare request: %v", errPrep)
		}
		checksum := req.Header.Get(cursor.HeaderChecksum)
		if len(checksum) <= 64 {
			t.Fatalf("checksum %q does not carry a machine id", checksum)
		}
		machine := checksum[len(checksum)-64:]
		if want := cursor.MachineID(email); machine != want {
			t.Errorf("checksum machine id = %q, want %q", machine, want)
		}
		if req.Header.Get(cursor.HeaderClientKey) == "" || req.Header.Get(cursor.HeaderSessionID) == "" {
			t.Errorf("identity headers incomplete: %v", req.Header)
		}
		if got := req.Header.Get("X-Cursor-Client-Type"); got != "cli" {
			t.Errorf("client type = %q, want cli", got)
		}
		if got := req.Header.Get("X-Ghost-Mode"); got != "true" {
			t.Errorf("ghost mode = %q, want true", got)
		}
		return machine
	}

	first := machineOf("alpha@example.com", "token-a")
	second := machineOf("beta@example.com", "token-b")
	if first == second {
		t.Fatalf("two accounts share a machine id: %q", first)
	}
	if again := machineOf("alpha@example.com", "token-a"); again != first {
		t.Fatalf("machine id is not stable for one account: %q vs %q", again, first)
	}
}

// TestCursorPrepareRequestHonoursPinnedMachineID covers the per-account override
// an operator can put in an auth file.
func TestCursorPrepareRequestHonoursPinnedMachineID(t *testing.T) {
	exec := NewCursorExecutor(nil)
	pinned := strings.Repeat("ab", 32)
	auth := &cliproxyauth.Auth{
		Provider:   cursor.Provider,
		Attributes: map[string]string{"access_token": "token-a"},
		Metadata:   map[string]any{"email": "alpha@example.com", "machine_id": pinned},
	}
	req, errReq := http.NewRequest(http.MethodPost, "https://example.invalid", nil)
	if errReq != nil {
		t.Fatalf("build request: %v", errReq)
	}
	if errPrep := exec.PrepareRequest(req, auth); errPrep != nil {
		t.Fatalf("prepare request: %v", errPrep)
	}
	if got := req.Header.Get(cursor.HeaderChecksum); !strings.HasSuffix(got, pinned) {
		t.Errorf("checksum = %q, want the pinned machine id suffix", got)
	}
}

// TestCursorPrepareRequestWithoutIdentity keeps a token-less auth harmless.
func TestCursorPrepareRequestWithoutIdentity(t *testing.T) {
	exec := NewCursorExecutor(nil)
	req, errReq := http.NewRequest(http.MethodPost, "https://example.invalid", nil)
	if errReq != nil {
		t.Fatalf("build request: %v", errReq)
	}
	if errPrep := exec.PrepareRequest(req, nil); errPrep != nil {
		t.Fatalf("prepare request: %v", errPrep)
	}
	if got := req.Header.Get(cursor.HeaderChecksum); got != "" {
		t.Errorf("checksum = %q, want none without an auth", got)
	}
	if got := req.Header.Get("X-Cursor-Client-Type"); got != "cli" {
		t.Errorf("client type = %q, want cli", got)
	}
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
