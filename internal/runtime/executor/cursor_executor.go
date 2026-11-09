package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cursor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// CursorExecutor drives Cursor's agent backend.
//
// Cursor has no OpenAI-compatible surface: chat runs over Connect streaming with
// protobuf payloads, and only an IDE OAuth session token is accepted. Because
// the wire carries a single prompt, OpenAI messages are flattened into one
// transcript before encoding; tool calls are negotiated in-band via a JSON
// protocol in the prompt rather than native upstream tool schemas.
type CursorExecutor struct {
	cfg *config.Config
}

// NewCursorExecutor creates a Cursor executor.
func NewCursorExecutor(cfg *config.Config) *CursorExecutor {
	return &CursorExecutor{cfg: cfg}
}

// Identifier returns the provider key.
func (e *CursorExecutor) Identifier() string { return cursor.Provider }

// RequestToFormat declares that this executor consumes OpenAI chat payloads.
func (e *CursorExecutor) RequestToFormat(_ cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	return sdktranslator.FormatOpenAI
}

// PrepareRequest injects Cursor credentials into an outgoing HTTP request.
func (e *CursorExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	token, _, _ := cursorCredentials(auth)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/connect+proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Connect-Accept-Encoding", "gzip")
	req.Header.Set("User-Agent", "connect-es/1.6.1")
	req.Header.Set("X-Cursor-Client-Type", "cli")
	req.Header.Set("X-Cursor-Client-Version", cursor.DefaultClientVersion)
	req.Header.Set("X-Ghost-Mode", "true")
	cursorIdentityHeaders(req.Header, auth, token)
	return nil
}

// HttpRequest executes an arbitrary request with Cursor credentials attached.
func (e *CursorExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("cursor executor: request is nil")
	}
	httpReq := req.WithContext(ctx)
	if errPrepare := e.PrepareRequest(httpReq, auth); errPrepare != nil {
		return nil, errPrepare
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
}

// Refresh validates the stored session token. Cursor issues no refresh grant for
// CLI sessions, so a revoked token can only be reported, not renewed here.
func (e *CursorExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("cursor executor: auth is nil")
	}
	token, _, _ := cursorCredentials(auth)
	if token == "" {
		return auth, fmt.Errorf("cursor: access token missing")
	}
	auth.LastRefreshedAt = time.Now().UTC()
	return auth, nil
}

// CountTokens reports an estimate; Cursor exposes no token counting endpoint.
func (e *CursorExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	prompt := cursorPromptFromPayload(req.Payload)
	estimate := len(prompt)/4 + 1
	payload := []byte(fmt.Sprintf(`{"input_tokens":%d}`, estimate))
	return cliproxyexecutor.Response{Payload: payload}, nil
}

// Execute runs one non-streaming turn.
func (e *CursorExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	targetModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, targetModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	httpReq, errPrep := e.buildRunRequest(ctx, auth, req, opts)
	if errPrep != nil {
		return resp, errPrep
	}

	httpResp, errDo := e.doStream(ctx, auth, httpReq)
	if errDo != nil {
		return resp, errDo
	}
	defer func() { _ = httpResp.Body.Close() }()

	text, runErr, errConsume := consumeCursorRun(httpResp.Body)
	if errConsume != nil {
		return resp, errConsume
	}
	// Cursor closes a completed run with a "canceled / KV stream closed" trailer.
	// Treating that as a failure would discard an answer the model already
	// produced, so a trailer only wins when nothing was generated.
	if runErr != nil && strings.TrimSpace(text) == "" {
		return resp, cursorStatusErrorForRun(runErr, httpResp.StatusCode, httpResp.Header)
	}

	if strings.TrimSpace(text) == "" {
		return resp, &cursorRunError{code: "empty_response", message: "cursor returned no text"}
	}

	// Emit an OpenAI-shaped body, then let the shared translator restyle it for
	// the client's requested format (Claude messages, Responses, ...).
	openAIResponse := buildOpenAIChatResponse(req.Model, text)
	if calls, ok := parseCursorToolCalls(text); ok {
		openAIResponse = buildOpenAIToolCallResponse(req.Model, calls)
	}
	targetFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	out := openAIResponse
	if targetFormat != sdktranslator.FormatOpenAI {
		originalRequest := opts.OriginalRequest
		if len(originalRequest) == 0 {
			originalRequest = req.Payload
		}
		var translateParam any
		translated := sdktranslator.TranslateNonStream(
			ctx,
			sdktranslator.FormatOpenAI,
			targetFormat,
			req.Model,
			originalRequest,
			req.Payload,
			openAIResponse,
			&translateParam,
		)
		if len(translated) > 0 {
			out = translated
		}
	}
	reporter.Publish(ctx, helps.ParseInteractionsUsage(out))
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

// ExecuteStream runs one streaming turn and emits OpenAI SSE chunks.
func (e *CursorExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	targetModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, targetModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	httpReq, errPrep := e.buildRunRequest(ctx, auth, req, opts)
	if errPrep != nil {
		return nil, errPrep
	}

	httpResp, errDo := e.doStream(ctx, auth, httpReq)
	if errDo != nil {
		return nil, errDo
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	streamCtx, cancelStream := context.WithCancel(ctx)

	go func() {
		<-streamCtx.Done()
		_ = httpResp.Body.Close()
	}()

	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	originalRequest := opts.OriginalRequest
	if len(originalRequest) == 0 {
		originalRequest = req.Payload
	}

	go func() {
		defer close(out)
		defer cancelStream()
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				logCloseError(errClose)
			}
		}()

		reader := cursor.NewConnectFrameReader()
		buffer := make([]byte, 32*1024)
		var text strings.Builder
		var terminalErr *cursor.RunError
		var translateParam any
		// Tool replies are a single JSON object in the final text. Streaming that
		// object as content and then finishing with "stop" makes clients drop the
		// turn, so buffer until the run ends whenever tools were declared.
		bufferForTools := cursorRequestDeclaresTools(req.Payload)

		emitSSE := func(chunk []byte) bool {
			lines := sdktranslator.TranslateStream(
				streamCtx,
				sdktranslator.FormatOpenAI,
				responseFormat,
				req.Model,
				originalRequest,
				req.Payload,
				chunk,
				&translateParam,
			)
			for _, line := range lines {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: line}:
				case <-streamCtx.Done():
					return false
				}
			}
			return true
		}

		emitText := func(delta string) bool {
			return emitSSE(buildOpenAIStreamChunk(req.Model, delta, false))
		}

		for {
			if streamCtx.Err() != nil {
				return
			}
			n, errRead := httpResp.Body.Read(buffer)
			if n > 0 {
				frames, errFrames := reader.Push(buffer[:n])
				if errFrames != nil {
					out <- cliproxyexecutor.StreamChunk{Err: errFrames}
					return
				}
				for _, frame := range frames {
					if frame.EndStream {
						terminalErr = cursor.DecodeEndStream(frame.Payload)
						continue
					}
					event := cursor.DecodeServerEvent(frame.Payload)
					if event.TextDelta == "" {
						continue
					}
					text.WriteString(event.TextDelta)
					if bufferForTools {
						continue
					}
					if !emitText(event.TextDelta) {
						return
					}
				}
			}
			if errRead != nil {
				if errRead != io.EOF {
					out <- cliproxyexecutor.StreamChunk{Err: errRead}
					return
				}
				break
			}
		}

		// A completed run ends with a "canceled / KV stream closed" trailer. Only
		// surface it when the model produced nothing, otherwise finish cleanly.
		if terminalErr != nil && text.Len() == 0 {
			out <- cliproxyexecutor.StreamChunk{Err: cursorStatusErrorForRun(terminalErr, httpResp.StatusCode, httpResp.Header)}
			return
		}

		finalText := text.String()
		if bufferForTools {
			if calls, ok := parseCursorToolCalls(finalText); ok {
				for _, chunk := range buildOpenAIToolCallStreamChunks(req.Model, calls) {
					if !emitSSE(chunk) {
						return
					}
				}
				reporter.Publish(streamCtx, helps.ParseInteractionsUsage([]byte(finalText)))
				return
			}
			// Tools were declared but the model answered in prose: flush the
			// buffered answer now so the client still sees the text.
			if finalText != "" {
				if !emitText(finalText) {
					return
				}
			}
		}
		for _, line := range sdktranslator.TranslateStream(
			streamCtx,
			sdktranslator.FormatOpenAI,
			responseFormat,
			req.Model,
			originalRequest,
			req.Payload,
			buildOpenAIStreamChunk(req.Model, "", true),
			&translateParam,
		) {
			out <- cliproxyexecutor.StreamChunk{Payload: line}
		}

		reporter.Publish(streamCtx, helps.ParseInteractionsUsage([]byte(finalText)))
	}()

	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

/* ---------- request building ---------- */

func (e *CursorExecutor) buildRunRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*http.Request, error) {
	token, agentBase, baseErr := cursorCredentials(auth)
	if baseErr != nil {
		return nil, baseErr
	}
	prompt := cursorPromptFromPayload(req.Payload)
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("cursor: prompt is empty")
	}
	workspace := cursorWorkspace(auth)
	model := thinking.ParseSuffix(req.Model).ModelName

	frames := cursor.BuildRunFrames(cursor.RunRequest{
		Prompt:        prompt,
		Model:         cursorWireModel(model),
		WorkspacePath: workspace,
		ClientVersion: cursor.DefaultClientVersion,
	})
	body := make([]byte, 0, 4096)
	for _, frame := range frames {
		body = append(body, frame...)
	}
	for i := 0; i < 3; i++ {
		body = append(body, cursor.HeartbeatFrame()...)
	}

	url := strings.TrimRight(agentBase, "/") + cursor.AgentPath
	httpReq, errNew := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if errNew != nil {
		return nil, errNew
	}
	if errPrepare := e.PrepareRequest(httpReq, auth); errPrepare != nil {
		return nil, errPrepare
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	return httpReq, nil
}

func (e *CursorExecutor) doStream(ctx context.Context, auth *cliproxyauth.Auth, httpReq *http.Request) (*http.Response, error) {
	httpResp, errDo := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
	if errDo != nil {
		return nil, errDo
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		errData, _ := io.ReadAll(io.LimitReader(httpResp.Body, 1<<20))
		_ = httpResp.Body.Close()
		return nil, &cursorRunError{
			code:    fmt.Sprintf("http_%d", httpResp.StatusCode),
			message: strings.TrimSpace(string(errData)),
			status:  httpResp.StatusCode,
			headers: httpResp.Header.Clone(),
		}
	}
	_ = ctx
	return httpResp, nil
}

// consumeCursorRun drains a run stream and returns the concatenated text.
func consumeCursorRun(body io.Reader) (string, *cursor.RunError, error) {
	reader := cursor.NewConnectFrameReader()
	buffer := make([]byte, 32*1024)
	var text strings.Builder
	var terminalErr *cursor.RunError

	for {
		n, errRead := body.Read(buffer)
		if n > 0 {
			frames, errFrames := reader.Push(buffer[:n])
			if errFrames != nil {
				return text.String(), nil, errFrames
			}
			for _, frame := range frames {
				if frame.EndStream {
					terminalErr = cursor.DecodeEndStream(frame.Payload)
					continue
				}
				event := cursor.DecodeServerEvent(frame.Payload)
				if event.TextDelta != "" {
					text.WriteString(event.TextDelta)
				}
			}
		}
		if errRead != nil {
			if errRead == io.EOF {
				break
			}
			return text.String(), terminalErr, errRead
		}
	}
	return text.String(), terminalErr, nil
}

/* ---------- payload helpers ---------- */

// cursorToolProtocol is appended to the prompt when the client declares tools.
//
// Cursor has no native tool-calling schema on this endpoint: the model can only
// answer with text. Clients still expect OpenAI tool_calls, so the prompt
// requests a single JSON object and the response is parsed back into tool calls.
const cursorToolProtocol = `TOOL PROTOCOL (mandatory):
You may call the tools listed above. To call a tool, reply with ONE JSON object
and nothing else:
{"tool_calls":[{"name":"<tool name>","arguments":{<json arguments>}}]}
Emit no prose before or after that object, no markdown fences, and exactly one
entry per tool you need. If no tool is required, answer the user in plain text
(never an empty tool_calls object). Do not repeat a tool call that already has
a Tool result in the transcript unless you need different arguments.`

// cursorToolContinuationHint is appended when the latest turn is a tool result
// so the model is not biased toward emitting another tool JSON object.
const cursorToolContinuationHint = `CONTINUE: Tool results above are already available.
Write the next assistant reply now. Prefer plain text. Emit a tool_calls JSON
object only when a new, distinct tool call is still required.`

// cursorToolCall is one parsed tool invocation.
type cursorToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type cursorToolEnvelope struct {
	ToolCalls []cursorToolCall `json:"tool_calls"`
}

// parseCursorToolCalls extracts a tool-call envelope from model output.
//
// The model may wrap the object in prose or fences despite instructions, so the
// outermost JSON object is located rather than assuming a bare payload.
func parseCursorToolCalls(text string) ([]cursorToolCall, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, false
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start < 0 || end <= start {
		return nil, false
	}
	var envelope cursorToolEnvelope
	if errJSON := json.Unmarshal([]byte(trimmed[start:end+1]), &envelope); errJSON != nil {
		return nil, false
	}
	calls := make([]cursorToolCall, 0, len(envelope.ToolCalls))
	for _, call := range envelope.ToolCalls {
		if strings.TrimSpace(call.Name) == "" {
			continue
		}
		arguments := call.Arguments
		if len(arguments) == 0 {
			arguments = json.RawMessage("{}")
		}
		calls = append(calls, cursorToolCall{Name: strings.TrimSpace(call.Name), Arguments: arguments})
	}
	if len(calls) == 0 {
		return nil, false
	}
	return calls, true
}

// cursorPromptFromPayload flattens OpenAI chat messages into one transcript.
// Cursor accepts a single prompt, so roles are replayed as labelled blocks.
func cursorPromptFromPayload(payload []byte) string {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return strings.TrimSpace(gjson.GetBytes(payload, "prompt").String())
	}
	var systemParts []string
	var turns []string
	lastRole := ""

	messages.ForEach(func(_, message gjson.Result) bool {
		role := strings.TrimSpace(message.Get("role").String())
		content := cursorFlattenContent(message.Get("content"))
		switch role {
		case "system", "developer":
			if content != "" {
				systemParts = append(systemParts, content)
			}
			lastRole = role
		case "assistant":
			chunks := make([]string, 0, 3)
			if content != "" {
				chunks = append(chunks, content)
			}
			if envelope := cursorAssistantToolEnvelope(message); envelope != "" {
				// Replay prior tool calls in the same JSON shape the protocol
				// asks the model to emit, so multiturn history stays consistent.
				chunks = append(chunks, envelope)
			}
			if len(chunks) > 0 {
				turns = append(turns, "Assistant: "+strings.Join(chunks, "\n"))
			}
			lastRole = role
		case "tool", "function":
			name := message.Get("name").String()
			callID := message.Get("tool_call_id").String()
			turns = append(turns, fmt.Sprintf("Tool result (tool_call_id=%s name=%s):\n%s", callID, name, content))
			lastRole = "tool"
		default:
			if content != "" {
				turns = append(turns, "User: "+content)
			}
			lastRole = "user"
		}
		return true
	})

	blocks := make([]string, 0, 4)
	if len(systemParts) > 0 {
		blocks = append(blocks, strings.Join(systemParts, "\n\n"))
	}
	// Keep tool schemas near the system prompt, not after the latest tool
	// result — trailing "call a tool" text biases Free models into loops.
	if declaration := cursorToolDeclaration(payload); declaration != "" {
		blocks = append(blocks, declaration)
	}
	blocks = append(blocks, turns...)
	if lastRole == "tool" && cursorRequestDeclaresTools(payload) {
		blocks = append(blocks, cursorToolContinuationHint)
	}
	return strings.TrimSpace(strings.Join(blocks, "\n\n"))
}

// cursorAssistantToolEnvelope rebuilds prior assistant tool_calls as the
// protocol JSON object so history matches what the model is told to emit.
func cursorAssistantToolEnvelope(message gjson.Result) string {
	toolCalls := message.Get("tool_calls")
	if !toolCalls.Exists() || !toolCalls.IsArray() || len(toolCalls.Array()) == 0 {
		return ""
	}
	calls := make([]cursorToolCall, 0, len(toolCalls.Array()))
	toolCalls.ForEach(func(_, call gjson.Result) bool {
		name := strings.TrimSpace(call.Get("function.name").String())
		if name == "" {
			name = strings.TrimSpace(call.Get("name").String())
		}
		if name == "" {
			return true
		}
		args := call.Get("function.arguments").String()
		if args == "" {
			args = call.Get("arguments").Raw
		}
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		calls = append(calls, cursorToolCall{Name: name, Arguments: json.RawMessage(args)})
		return true
	})
	if len(calls) == 0 {
		return ""
	}
	raw, errJSON := json.Marshal(cursorToolEnvelope{ToolCalls: calls})
	if errJSON != nil {
		return ""
	}
	return string(raw)
}

// cursorRequestDeclaresTools reports whether the OpenAI request carries tools.
func cursorRequestDeclaresTools(payload []byte) bool {
	tools := gjson.GetBytes(payload, "tools")
	return tools.Exists() && tools.IsArray() && len(tools.Array()) > 0
}

// cursorToolDeclaration renders the client's tool schemas for the prompt.
// Returns an empty string when the request declares no tools.
func cursorToolDeclaration(payload []byte) string {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return ""
	}
	lines := make([]string, 0, 8)
	tools.ForEach(func(_, tool gjson.Result) bool {
		name := tool.Get("function.name").String()
		if name == "" {
			name = tool.Get("name").String()
		}
		if name == "" {
			return true
		}
		description := tool.Get("function.description").String()
		if description == "" {
			description = tool.Get("description").String()
		}
		parameters := tool.Get("function.parameters")
		if !parameters.Exists() {
			parameters = tool.Get("parameters")
		}
		entry := "- " + name
		if description != "" {
			entry += ": " + strings.TrimSpace(description)
		}
		if parameters.Exists() {
			entry += "\n  parameters: " + compactJSON(parameters.Raw)
		}
		lines = append(lines, entry)
		return true
	})
	if len(lines) == 0 {
		return ""
	}
	return "AVAILABLE TOOLS:\n" + strings.Join(lines, "\n") + "\n\n" + cursorToolProtocol
}

// compactJSON removes insignificant whitespace so schemas stay compact.
func compactJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "{}"
	}
	var builder strings.Builder
	inString := false
	escaped := false
	for _, r := range trimmed {
		if inString {
			builder.WriteRune(r)
			switch {
			case escaped:
				escaped = false
			case r == '\\':
				escaped = true
			case r == '"':
				inString = false
			}
			continue
		}
		switch r {
		case '"':
			inString = true
			builder.WriteRune(r)
		case ' ', '\t', '\n', '\r':
			continue
		default:
			builder.WriteRune(r)
		}
	}
	if builder.Len() == 0 {
		return "{}"
	}
	return builder.String()
}

// cursorFlattenContent collapses string or multipart content into text.
func cursorFlattenContent(content gjson.Result) string {
	if !content.Exists() {
		return ""
	}
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		parts := make([]string, 0, 4)
		content.ForEach(func(_, part gjson.Result) bool {
			if text := part.Get("text").String(); text != "" {
				parts = append(parts, text)
			}
			return true
		})
		return strings.Join(parts, "\n")
	}
	return strings.TrimSpace(content.String())
}

// cursorWireModel maps a public model name onto Cursor's wire model id.
//
// Free-tier gating only accepts the registry variant name for Grok 4.7: the
// bare id answers "Model unavailable on Free ... with fixed settings", while
// the -medium variant runs and reports itself as Grok 4.7.
func cursorWireModel(model string) string {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" || normalized == "auto" {
		return "default"
	}
	if idx := strings.LastIndex(normalized, "/"); idx >= 0 {
		normalized = normalized[idx+1:]
	}
	switch normalized {
	case "grok-4.7", "grok-4-7", "cursor-grok-4.7":
		return "grok-4.7-medium"
	case "grok-4.6", "grok-4-6":
		return "grok-4.6"
	}
	return normalized
}

// cursorWorkspace reads the optional workspace path from auth metadata.
func cursorWorkspace(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if value := strings.TrimSpace(auth.Attributes["workspace_path"]); value != "" {
			return value
		}
	}
	if auth.Metadata != nil {
		if value, ok := auth.Metadata["workspace_path"].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

/* ---------- response helpers ---------- */

func buildOpenAIChatResponse(model, text string) []byte {
	response := map[string]any{
		"object": "chat.completion",
		"model":  model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"finish_reason": "stop",
				"message": map[string]any{
					"role":    "assistant",
					"content": text,
				},
			},
		},
	}
	encoded, _ := json.Marshal(response)
	return encoded
}

// buildOpenAIToolCallResponse renders a chat completion carrying tool_calls so
// clients see a normal OpenAI function-call turn.
func buildOpenAIToolCallResponse(model string, calls []cursorToolCall) []byte {
	toolCalls := make([]any, 0, len(calls))
	for i, call := range calls {
		arguments := strings.TrimSpace(string(call.Arguments))
		if arguments == "" {
			arguments = "{}"
		}
		toolCalls = append(toolCalls, map[string]any{
			"index": i,
			"id":    fmt.Sprintf("call_%d_%s", i, shortHash(call.Name+arguments)),
			"type":  "function",
			"function": map[string]any{
				"name":      call.Name,
				"arguments": arguments,
			},
		})
	}
	response := map[string]any{
		"object": "chat.completion",
		"model":  model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"finish_reason": "tool_calls",
				"message": map[string]any{
					"role":       "assistant",
					"content":    nil,
					"tool_calls": toolCalls,
				},
			},
		},
	}
	encoded, _ := json.Marshal(response)
	return encoded
}

// shortHash gives tool call ids a stable, unique suffix.
func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:4])
}

// buildOpenAIStreamChunk renders one OpenAI streaming chunk. The shared
// translator rewrites it into the client's requested format.
func buildOpenAIStreamChunk(model, content string, done bool) []byte {
	delta := map[string]any{}
	if content != "" {
		delta["content"] = content
	}
	var finish any
	if done {
		finish = "stop"
	}
	chunk := map[string]any{
		"object": "chat.completion.chunk",
		"model":  model,
		"choices": []any{
			map[string]any{"index": 0, "delta": delta, "finish_reason": finish},
		},
	}
	encoded, _ := json.Marshal(chunk)
	return []byte("data: " + string(encoded) + "\n\n")
}

// buildOpenAIToolCallStreamChunks emits OpenAI SSE frames for a tool-call turn.
//
// One chunk carries every tool call (name + full arguments), then a final chunk
// finishes with finish_reason=tool_calls. Clients that stream Cursor through this
// proxy expect that shape; finishing with "stop" after JSON content drops the turn.
func buildOpenAIToolCallStreamChunks(model string, calls []cursorToolCall) [][]byte {
	if len(calls) == 0 {
		return nil
	}
	toolCalls := make([]any, 0, len(calls))
	for i, call := range calls {
		arguments := strings.TrimSpace(string(call.Arguments))
		if arguments == "" {
			arguments = "{}"
		}
		toolCalls = append(toolCalls, map[string]any{
			"index": i,
			"id":    fmt.Sprintf("call_%d_%s", i, shortHash(call.Name+arguments)),
			"type":  "function",
			"function": map[string]any{
				"name":      call.Name,
				"arguments": arguments,
			},
		})
	}
	open := map[string]any{
		"object": "chat.completion.chunk",
		"model":  model,
		"choices": []any{
			map[string]any{
				"index": 0,
				"delta": map[string]any{
					"role":       "assistant",
					"content":    nil,
					"tool_calls": toolCalls,
				},
				"finish_reason": nil,
			},
		},
	}
	done := map[string]any{
		"object": "chat.completion.chunk",
		"model":  model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "tool_calls",
			},
		},
	}
	openRaw, _ := json.Marshal(open)
	doneRaw, _ := json.Marshal(done)
	return [][]byte{
		[]byte("data: " + string(openRaw) + "\n\n"),
		[]byte("data: " + string(doneRaw) + "\n\n"),
	}
}

func logCloseError(err error) {
	_ = err
}
