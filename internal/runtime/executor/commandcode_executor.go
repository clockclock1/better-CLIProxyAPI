package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	commandcodeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/commandcode"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	_ cliproxyauth.ProviderExecutor = (*CommandCodeExecutor)(nil)
)

const commandCodeStreamKeepAlive = 15 * time.Second

// CommandCodeExecutor routes OpenAI-compatible chat requests through Command Code
// (/alpha/generate, /provider/v1/chat/completions, or /provider/v1/messages).
type CommandCodeExecutor struct {
	cfg *config.Config
}

// NewCommandCodeExecutor constructs a Command Code executor.
func NewCommandCodeExecutor(cfg *config.Config) *CommandCodeExecutor {
	commandcodeauth.StartCLIVersionUpdater(context.Background())
	return &CommandCodeExecutor{cfg: cfg}
}

func (e *CommandCodeExecutor) Identifier() string { return "commandcode" }

func (e *CommandCodeExecutor) RequestToFormat(_ cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	return sdktranslator.FormatOpenAI
}

func (e *CommandCodeExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	_, apiKey := commandCodeCreds(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	applyCommandCodeHeaders(req, auth, "", "")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

func (e *CommandCodeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("commandcode executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

func (e *CommandCodeExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	return auth, nil
}

func (e *CommandCodeExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, statusErr{code: http.StatusNotImplemented, msg: "commandcode: count tokens not supported"}
}

func (e *CommandCodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	parsedThinking := thinking.ParseSuffix(req.Model)
	baseModel := parsedThinking.ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := commandCodeCreds(auth)
	if apiKey == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "commandcode executor: missing API key"}
		return resp, err
	}

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FormatOpenAI

	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)
	if len(translated) == 0 {
		translated = req.Payload
	}
	translated = helps.SetStringIfDifferent(translated, "model", baseModel)
	if isCommandCodeEffort(parsedThinking.RawSuffix) && strings.TrimSpace(gjson.GetBytes(translated, "reasoning_effort").String()) == "" {
		translated = helps.SetStringIfDifferent(translated, "reasoning_effort", strings.ToLower(strings.TrimSpace(parsedThinking.RawSuffix)))
	}

	sessionID := commandCodeSessionID(ctx, opts, translated)
	httpResp, protocol, errDo := e.doCommandCodeUpstream(ctx, auth, baseURL, apiKey, baseModel, sessionID, translated, false, reporter, false)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return resp, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("commandcode executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		maybeLearnCommandCodeMaxTokens(baseModel, b)
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}

	openaiJSON, errAgg := aggregateCommandCodeResponse(protocol, baseModel, httpResp.Body)
	if errAgg != nil {
		err = errAgg
		return resp, err
	}
	reporter.ObserveResponseModel(openaiJSON)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(openaiJSON))

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), translated, openaiJSON, &param)
	if len(out) == 0 {
		out = openaiJSON
	}
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *CommandCodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	parsedThinking := thinking.ParseSuffix(req.Model)
	baseModel := parsedThinking.ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := commandCodeCreds(auth)
	if apiKey == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "commandcode executor: missing API key"}
		return nil, err
	}

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FormatOpenAI

	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)
	if len(translated) == 0 {
		translated = req.Payload
	}
	translated = helps.SetStringIfDifferent(translated, "model", baseModel)
	if isCommandCodeEffort(parsedThinking.RawSuffix) && strings.TrimSpace(gjson.GetBytes(translated, "reasoning_effort").String()) == "" {
		translated = helps.SetStringIfDifferent(translated, "reasoning_effort", strings.ToLower(strings.TrimSpace(parsedThinking.RawSuffix)))
	}

	sessionID := commandCodeSessionID(ctx, opts, translated)
	httpResp, protocol, errDo := e.doCommandCodeUpstream(ctx, auth, baseURL, apiKey, baseModel, sessionID, translated, true, reporter, true)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return nil, errDo
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		_ = httpResp.Body.Close()
		maybeLearnCommandCodeMaxTokens(baseModel, b)
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("commandcode executor: close response body error: %v", errClose)
			}
		}()
		var param any
		emitOpenAI := func(openaiChunk []byte) bool {
			streamLine := append([]byte("data: "), openaiChunk...)
			chunks := sdktranslator.TranslateStream(ctx, to, responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), translated, streamLine, &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}
		emitDone := func() {
			doneLine := []byte("data: [DONE]")
			chunks := sdktranslator.TranslateStream(ctx, to, responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), translated, doneLine, &param)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}

		switch protocol {
		case commandCodeProtocolOpenAI:
			streamCommandCodeOpenAI(ctx, httpResp.Body, emitOpenAI, emitDone, reporter, out)
		case commandCodeProtocolMessages:
			streamCommandCodeMessages(ctx, baseModel, httpResp.Body, emitOpenAI, emitDone, reporter, out)
		default:
			streamCommandCodeCLI(ctx, baseModel, httpResp.Body, emitOpenAI, emitDone, reporter, out)
		}
	}()

	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *CommandCodeExecutor) doCommandCodeUpstream(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	baseURL, apiKey, model, sessionID string,
	openaiPayload []byte,
	stream bool,
	reporter *helps.UsageReporter,
	roundTripOnly bool,
) (*http.Response, string, error) {
	protocol := resolveCommandCodeProtocol(model, baseURL, apiKey)
	for attempt := 0; attempt < 2; attempt++ {
		body, errBody := buildCommandCodeBody(protocol, model, sessionID, openaiPayload, stream)
		if errBody != nil {
			return nil, protocol, statusErr{code: http.StatusBadRequest, msg: errBody.Error()}
		}
		url := commandCodeProtocolEndpoint(baseURL, protocol)
		httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if errReq != nil {
			return nil, protocol, errReq
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		if stream {
			httpReq.Header.Set("Accept", "text/event-stream, application/x-ndjson, application/json")
		}
		applyCommandCodeHeaders(httpReq, auth, sessionID, protocol)
		var attrs map[string]string
		if auth != nil {
			attrs = auth.Attributes
		}
		util.ApplyCustomHeadersFromAttrs(httpReq, attrs)

		var authID, authLabel, authType, authValue string
		if auth != nil {
			authID = auth.ID
			authLabel = auth.Label
			authType, authValue = auth.AccountInfo()
		}
		helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
			URL:       url,
			Method:    http.MethodPost,
			Headers:   httpReq.Header.Clone(),
			Body:      body,
			Provider:  e.Identifier(),
			AuthID:    authID,
			AuthLabel: authLabel,
			AuthType:  authType,
			AuthValue: authValue,
		})

		httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
		if roundTripOnly {
			httpClient = reporter.TrackHTTPClientRoundTripOnly(httpClient)
		} else {
			httpClient = reporter.TrackHTTPClient(httpClient)
		}
		httpResp, errDo := doCommandCodeWithVersionRetry(ctx, httpClient, httpReq, auth, sessionID, protocol, body)
		if errDo != nil {
			return nil, protocol, errDo
		}
		if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
			if protocol == commandCodeProtocolOpenAI || protocol == commandCodeProtocolCLI {
				rememberCommandCodeProtocol(baseURL, apiKey, protocol)
			}
			return httpResp, protocol, nil
		}
		errBodyBytes, _ := io.ReadAll(httpResp.Body)
		_ = httpResp.Body.Close()
		helps.AppendAPIResponseChunk(ctx, e.cfg, errBodyBytes)
		maybeLearnCommandCodeMaxTokens(model, errBodyBytes)

		fallback := commandCodeProtocolFallback(protocol)
		if attempt == 0 && fallback != "" && isCommandCodeUpgradeRequired(httpResp.StatusCode, errBodyBytes) {
			helps.LogWithRequestID(ctx).Infof("commandcode: %s rejected (%d); falling back to %s", protocol, httpResp.StatusCode, fallback)
			rememberCommandCodeProtocol(baseURL, apiKey, fallback)
			protocol = fallback
			continue
		}
		httpResp.Body = io.NopCloser(bytes.NewReader(errBodyBytes))
		return httpResp, protocol, nil
	}
	return nil, protocol, statusErr{code: http.StatusBadGateway, msg: "commandcode: exhausted protocol attempts"}
}

func buildCommandCodeBody(protocol, model, sessionID string, openaiPayload []byte, stream bool) ([]byte, error) {
	switch protocol {
	case commandCodeProtocolOpenAI:
		return buildCommandCodeOpenAIBody(model, openaiPayload, stream)
	case commandCodeProtocolMessages:
		return buildCommandCodeMessagesBody(model, openaiPayload, stream)
	default:
		return buildCommandCodeGenerateBody(model, sessionID, openaiPayload)
	}
}

func aggregateCommandCodeResponse(protocol, model string, body io.Reader) ([]byte, error) {
	switch protocol {
	case commandCodeProtocolOpenAI:
		b, err := io.ReadAll(body)
		if err != nil {
			return nil, err
		}
		return b, nil
	case commandCodeProtocolMessages:
		b, err := io.ReadAll(body)
		if err != nil {
			return nil, err
		}
		return messagesResponseToOpenAI(model, b)
	default:
		acc := newCommandCodeOpenAIAccumulator(model)
		scanner := bufio.NewScanner(body)
		scanner.Buffer(nil, 52_428_800)
		for scanner.Scan() {
			event, ok := parseCommandCodeNDJSONLine(scanner.Bytes())
			if !ok {
				continue
			}
			if errIngest := acc.ingest(event); errIngest != nil {
				return nil, statusErr{code: http.StatusBadGateway, msg: errIngest.Error()}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			return nil, errScan
		}
		return acc.completionJSON()
	}
}

func streamCommandCodeCLI(
	ctx context.Context,
	model string,
	body io.Reader,
	emit func([]byte) bool,
	emitDone func(),
	reporter *helps.UsageReporter,
	out chan<- cliproxyexecutor.StreamChunk,
) {
	acc := newCommandCodeOpenAIAccumulator(model)
	if !emit(acc.streamChunk(map[string]any{"role": "assistant", "content": ""}, nil)) {
		return
	}

	type lineResult struct {
		line []byte
		err  error
	}
	lines := make(chan lineResult, 8)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(body)
		scanner.Buffer(nil, 52_428_800)
		for scanner.Scan() {
			dup := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- lineResult{line: dup}:
			case <-ctx.Done():
				return
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			select {
			case lines <- lineResult{err: errScan}:
			case <-ctx.Done():
			}
		}
	}()

	ticker := time.NewTicker(commandCodeStreamKeepAlive)
	defer ticker.Stop()
	type toolStreamState struct {
		index     int
		id        string
		arguments strings.Builder
		announced bool
		named     bool
		completed bool
	}
	tools := map[string]*toolStreamState{}
	toolOrder := []*toolStreamState{}
	toolFor := func(event gjson.Result) (*toolStreamState, error) {
		id := event.Get("toolCallId").String()
		if id == "" {
			id = event.Get("id").String()
		}
		if id == "" {
			// Older gateways omit IDs on input deltas. Only infer an ID when
			// exactly one unfinished call can own the event.
			var pending *toolStreamState
			for _, tool := range toolOrder {
				if tool.completed {
					continue
				}
				if pending != nil {
					return nil, fmt.Errorf("commandcode: ambiguous tool input without a call ID")
				}
				pending = tool
			}
			if pending != nil {
				return pending, nil
			}
			id = "call_" + uuid.NewString()[:12]
		}
		if tool := tools[id]; tool != nil {
			return tool, nil
		}
		tool := &toolStreamState{index: len(toolOrder), id: id}
		tools[id] = tool
		toolOrder = append(toolOrder, tool)
		return tool, nil
	}
	emitTool := func(tool *toolStreamState, name, arguments string) bool {
		function := map[string]any{"arguments": arguments}
		call := map[string]any{"index": tool.index, "function": function}
		if !tool.announced {
			call["id"] = tool.id
			call["type"] = "function"
			tool.announced = true
		}
		if name != "" && !tool.named {
			function["name"] = name
			tool.named = true
		}
		tool.arguments.WriteString(arguments)
		return emit(acc.streamChunk(map[string]any{"tool_calls": []any{call}}, nil))
	}
	failTool := func(err error) {
		select {
		case out <- cliproxyexecutor.StreamChunk{Err: statusErr{code: http.StatusBadGateway, msg: err.Error()}}:
		case <-ctx.Done():
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Keep client-side stream-idle timers alive during long tool-arg generation.
			if !emit(acc.streamChunk(map[string]any{"content": ""}, nil)) {
				return
			}
		case lr, ok := <-lines:
			if !ok {
				for _, tool := range toolOrder {
					if !tool.completed {
						failTool(fmt.Errorf("commandcode: incomplete tool call %q", tool.id))
						return
					}
				}
				final := acc.streamChunkWithUsage(map[string]any{}, acc.FinishReason, acc.Usage)
				reporter.Publish(ctx, helps.ParseOpenAIUsage(final))
				if !emit(final) {
					return
				}
				emitDone()
				return
			}
			if lr.err != nil {
				helps.RecordAPIResponseError(ctx, nil, lr.err)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: lr.err}:
				case <-ctx.Done():
				}
				return
			}
			ticker.Reset(commandCodeStreamKeepAlive)
			event, okEvent := parseCommandCodeNDJSONLine(lr.line)
			if !okEvent {
				continue
			}
			helps.AppendAPIResponseChunk(ctx, nil, event)
			typ := gjson.GetBytes(event, "type").String()
			switch typ {
			case "error":
				if errIngest := acc.ingest(event); errIngest != nil {
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: statusErr{code: http.StatusBadGateway, msg: errIngest.Error()}}:
					case <-ctx.Done():
					}
					return
				}
			case "text-delta":
				text := gjson.GetBytes(event, "text").String()
				_ = acc.ingest(event)
				if text == "" {
					continue
				}
				if !emit(acc.streamChunk(map[string]any{"content": text}, nil)) {
					return
				}
			case "reasoning-delta", "reasoning-start":
				text := gjson.GetBytes(event, "text").String()
				_ = acc.ingest(event)
				if text == "" {
					continue
				}
				if !emit(acc.streamChunk(map[string]any{"reasoning_content": text}, nil)) {
					return
				}
			case "tool-input-start":
				tool, errTool := toolFor(gjson.ParseBytes(event))
				if errTool != nil {
					failTool(errTool)
					return
				}
				if tool.completed || tool.announced {
					continue
				}
				if !emitTool(tool, gjson.GetBytes(event, "toolName").String(), "") {
					return
				}
			case "tool-input-delta":
				deltaText := gjson.GetBytes(event, "delta").String()
				if deltaText == "" {
					deltaText = gjson.GetBytes(event, "inputTextDelta").String()
				}
				if deltaText == "" {
					continue
				}
				tool, errTool := toolFor(gjson.ParseBytes(event))
				if errTool != nil {
					failTool(errTool)
					return
				}
				if tool.completed {
					continue
				}
				if !emitTool(tool, gjson.GetBytes(event, "toolName").String(), deltaText) {
					return
				}
			case "tool-call":
				tool, errTool := toolFor(gjson.ParseBytes(event))
				if errTool != nil {
					failTool(errTool)
					return
				}
				if tool.completed {
					continue
				}
				before := len(acc.ToolCalls)
				_ = acc.ingest(event)
				if len(acc.ToolCalls) <= before {
					continue
				}
				call := acc.ToolCalls[len(acc.ToolCalls)-1]
				fn, _ := call["function"].(map[string]any)
				args, _ := fn["arguments"].(string)
				sent := tool.arguments.String()
				if sent != "" {
					if json.Valid([]byte(sent)) {
						// Completed snapshots may serialize keys/whitespace differently.
						args = ""
					} else if strings.HasPrefix(args, sent) {
						args = args[len(sent):]
					} else {
						failTool(fmt.Errorf("commandcode: inconsistent tool arguments for %q", tool.id))
						return
					}
				}
				tool.completed = true
				name, _ := fn["name"].(string)
				if !emitTool(tool, name, args) {
					return
				}
			case "finish", "finish-step":
				_ = acc.ingest(event)
			default:
				_ = acc.ingest(event)
			}
		}
	}
}

func streamCommandCodeOpenAI(
	ctx context.Context,
	body io.Reader,
	emit func([]byte) bool,
	emitDone func(),
	reporter *helps.UsageReporter,
	out chan<- cliproxyexecutor.StreamChunk,
) {
	type lineResult struct {
		line []byte
		err  error
	}
	lines := make(chan lineResult, 8)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(body)
		scanner.Buffer(nil, 52_428_800)
		for scanner.Scan() {
			dup := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- lineResult{line: dup}:
			case <-ctx.Done():
				return
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			select {
			case lines <- lineResult{err: errScan}:
			case <-ctx.Done():
			}
		}
	}()

	ticker := time.NewTicker(commandCodeStreamKeepAlive)
	defer ticker.Stop()
	sawDone := false
	var lastUsage []byte

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			keepalive := []byte(`{"id":"chatcmpl-keepalive","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":""},"finish_reason":null}]}`)
			if !emit(keepalive) {
				return
			}
		case lr, ok := <-lines:
			if !ok {
				if lastUsage != nil {
					reporter.Publish(ctx, helps.ParseOpenAIUsage(lastUsage))
					_ = emit(lastUsage)
				}
				if !sawDone {
					emitDone()
				}
				return
			}
			if lr.err != nil {
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: lr.err}:
				case <-ctx.Done():
				}
				return
			}
			ticker.Reset(commandCodeStreamKeepAlive)
			trimmed := bytes.TrimSpace(lr.line)
			if len(trimmed) == 0 || bytes.HasPrefix(trimmed, []byte(":")) {
				continue
			}
			payload := trimmed
			if bytes.HasPrefix(payload, []byte("data:")) {
				payload = bytes.TrimSpace(bytes.TrimPrefix(payload, []byte("data:")))
			}
			if bytes.Equal(payload, []byte("[DONE]")) {
				sawDone = true
				emitDone()
				return
			}
			if !json.Valid(payload) {
				continue
			}
			normalized := normalizeOpenAIReasoningDelta(payload)
			if gjson.GetBytes(normalized, "usage").Exists() {
				lastUsage = normalized
				reporter.Publish(ctx, helps.ParseOpenAIUsage(normalized))
			}
			if !emit(normalized) {
				return
			}
		}
	}
}

func normalizeOpenAIReasoningDelta(chunk []byte) []byte {
	// Gateway families spell thinking as reasoning / reasoning_content / reasoning_details.
	delta := gjson.GetBytes(chunk, "choices.0.delta")
	if !delta.Exists() {
		return chunk
	}
	if delta.Get("reasoning_content").Exists() {
		return chunk
	}
	text := delta.Get("reasoning").String()
	if text == "" {
		details := delta.Get("reasoning_details")
		if details.IsArray() {
			var parts []string
			details.ForEach(func(_, d gjson.Result) bool {
				t := d.Get("text").String()
				if t == "" {
					t = d.Get("summary").String()
				}
				if t != "" {
					parts = append(parts, t)
				}
				return true
			})
			text = strings.Join(parts, "")
		}
	}
	if text == "" {
		return chunk
	}
	out, err := sjson.SetBytes(chunk, "choices.0.delta.reasoning_content", text)
	if err != nil {
		return chunk
	}
	return out
}

func streamCommandCodeMessages(
	ctx context.Context,
	model string,
	body io.Reader,
	emit func([]byte) bool,
	emitDone func(),
	reporter *helps.UsageReporter,
	out chan<- cliproxyexecutor.StreamChunk,
) {
	acc := newCommandCodeOpenAIAccumulator(model)
	if !emit(acc.streamChunk(map[string]any{"role": "assistant", "content": ""}, nil)) {
		return
	}

	type lineResult struct {
		line []byte
		err  error
	}
	lines := make(chan lineResult, 8)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(body)
		scanner.Buffer(nil, 52_428_800)
		for scanner.Scan() {
			dup := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- lineResult{line: dup}:
			case <-ctx.Done():
				return
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			select {
			case lines <- lineResult{err: errScan}:
			case <-ctx.Done():
			}
		}
	}()

	ticker := time.NewTicker(commandCodeStreamKeepAlive)
	defer ticker.Stop()
	toolIndex := -1
	blockType := ""

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !emit(acc.streamChunk(map[string]any{"content": ""}, nil)) {
				return
			}
		case lr, ok := <-lines:
			if !ok {
				final := acc.streamChunkWithUsage(map[string]any{}, acc.FinishReason, acc.Usage)
				reporter.Publish(ctx, helps.ParseOpenAIUsage(final))
				_ = emit(final)
				emitDone()
				return
			}
			if lr.err != nil {
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: lr.err}:
				case <-ctx.Done():
				}
				return
			}
			ticker.Reset(commandCodeStreamKeepAlive)
			trimmed := bytes.TrimSpace(lr.line)
			if len(trimmed) == 0 || bytes.HasPrefix(trimmed, []byte(":")) || bytes.HasPrefix(trimmed, []byte("event:")) {
				continue
			}
			payload := trimmed
			if bytes.HasPrefix(payload, []byte("data:")) {
				payload = bytes.TrimSpace(bytes.TrimPrefix(payload, []byte("data:")))
			}
			if !json.Valid(payload) {
				continue
			}
			evType := gjson.GetBytes(payload, "type").String()
			switch evType {
			case "ping":
				if !emit(acc.streamChunk(map[string]any{"content": ""}, nil)) {
					return
				}
			case "content_block_start":
				blockType = gjson.GetBytes(payload, "content_block.type").String()
				if blockType == "tool_use" {
					toolIndex++
					delta := map[string]any{
						"tool_calls": []any{
							map[string]any{
								"index": toolIndex,
								"id":    gjson.GetBytes(payload, "content_block.id").String(),
								"type":  "function",
								"function": map[string]any{
									"name":      gjson.GetBytes(payload, "content_block.name").String(),
									"arguments": "",
								},
							},
						},
					}
					acc.FinishReason = "tool_calls"
					if !emit(acc.streamChunk(delta, nil)) {
						return
					}
				}
			case "content_block_delta":
				dType := gjson.GetBytes(payload, "delta.type").String()
				switch dType {
				case "text_delta":
					text := gjson.GetBytes(payload, "delta.text").String()
					acc.Content.WriteString(text)
					if text != "" && !emit(acc.streamChunk(map[string]any{"content": text}, nil)) {
						return
					}
				case "thinking_delta":
					text := gjson.GetBytes(payload, "delta.thinking").String()
					acc.Reasoning.WriteString(text)
					if text != "" && !emit(acc.streamChunk(map[string]any{"reasoning_content": text}, nil)) {
						return
					}
				case "input_json_delta":
					partial := gjson.GetBytes(payload, "delta.partial_json").String()
					if partial == "" || toolIndex < 0 {
						continue
					}
					delta := map[string]any{
						"tool_calls": []any{
							map[string]any{
								"index": toolIndex,
								"function": map[string]any{
									"arguments": partial,
								},
							},
						},
					}
					if !emit(acc.streamChunk(delta, nil)) {
						return
					}
				}
			case "message_delta":
				if stop := gjson.GetBytes(payload, "delta.stop_reason").String(); stop != "" {
					acc.FinishReason = mapMessagesStopReason(stop)
				}
				if u := usageFromMessagesEvent(gjson.ParseBytes(payload)); u != nil {
					acc.Usage = u
				}
			case "message_stop":
				final := acc.streamChunkWithUsage(map[string]any{}, acc.FinishReason, acc.Usage)
				reporter.Publish(ctx, helps.ParseOpenAIUsage(final))
				_ = emit(final)
				emitDone()
				return
			}
		}
	}
}

func mapMessagesStopReason(stop string) string {
	switch stop {
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	default:
		return "stop"
	}
}

func usageFromMessagesEvent(event gjson.Result) map[string]any {
	src := event.Get("usage")
	if !src.Exists() {
		return nil
	}
	prompt := src.Get("input_tokens").Int()
	completion := src.Get("output_tokens").Int()
	cacheRead := src.Get("cache_read_input_tokens").Int()
	cacheWrite := src.Get("cache_creation_input_tokens").Int()
	usage := map[string]any{
		"prompt_tokens":     prompt + cacheRead + cacheWrite,
		"completion_tokens": completion,
		"total_tokens":      prompt + cacheRead + cacheWrite + completion,
	}
	if cacheRead > 0 || cacheWrite > 0 {
		details := map[string]any{}
		if cacheRead > 0 {
			details["cached_tokens"] = cacheRead
		}
		if cacheWrite > 0 {
			details["cache_write_tokens"] = cacheWrite
		}
		usage["prompt_tokens_details"] = details
	}
	return usage
}

func messagesResponseToOpenAI(model string, raw []byte) ([]byte, error) {
	acc := newCommandCodeOpenAIAccumulator(model)
	content := gjson.GetBytes(raw, "content")
	if content.IsArray() {
		content.ForEach(func(_, block gjson.Result) bool {
			switch block.Get("type").String() {
			case "text":
				acc.Content.WriteString(block.Get("text").String())
			case "thinking":
				acc.Reasoning.WriteString(block.Get("thinking").String())
			case "tool_use":
				args := block.Get("input").Raw
				if args == "" {
					args = "{}"
				}
				acc.ToolCalls = append(acc.ToolCalls, map[string]any{
					"id":   block.Get("id").String(),
					"type": "function",
					"function": map[string]any{
						"name":      block.Get("name").String(),
						"arguments": args,
					},
				})
				acc.FinishReason = "tool_calls"
			}
			return true
		})
	}
	if stop := gjson.GetBytes(raw, "stop_reason").String(); stop != "" && acc.FinishReason == "stop" {
		acc.FinishReason = mapMessagesStopReason(stop)
	}
	if u := usageFromMessagesEvent(gjson.ParseBytes(raw)); u != nil {
		acc.Usage = u
	}
	return acc.completionJSON()
}

func maybeLearnCommandCodeMaxTokens(model string, errBody []byte) {
	// e.g. max_tokens: 200000 > 128000
	msg := string(errBody)
	idx := strings.Index(strings.ToLower(msg), "max_tokens:")
	if idx < 0 {
		return
	}
	fragment := msg[idx:]
	gt := strings.Index(fragment, ">")
	if gt < 0 {
		return
	}
	rest := strings.TrimSpace(fragment[gt+1:])
	var ceiling int
	for i, r := range rest {
		if r < '0' || r > '9' {
			rest = rest[:i]
			break
		}
	}
	if _, err := fmt.Sscanf(rest, "%d", &ceiling); err == nil && ceiling > 0 {
		noteCommandCodeLearnedMaxTokens(model, ceiling)
	}
}

func commandCodeCreds(a *cliproxyauth.Auth) (baseURL, apiKey string) {
	baseURL = commandcodeauth.DefaultAPIBaseURL
	if a == nil {
		return baseURL, ""
	}
	if a.Attributes != nil {
		if b := strings.TrimSpace(a.Attributes["base_url"]); b != "" {
			baseURL = b
		}
		if k := strings.TrimSpace(a.Attributes["api_key"]); k != "" {
			apiKey = k
		}
	}
	if a.Metadata != nil {
		if apiKey == "" {
			if k, ok := a.Metadata["api_key"].(string); ok {
				apiKey = strings.TrimSpace(k)
			}
		}
		if baseURL == commandcodeauth.DefaultAPIBaseURL {
			if b, ok := a.Metadata["base_url"].(string); ok && strings.TrimSpace(b) != "" {
				baseURL = strings.TrimSpace(b)
			}
		}
	}
	if v := strings.TrimSpace(os.Getenv("COMMANDCODE_API_URL")); v != "" && baseURL == commandcodeauth.DefaultAPIBaseURL {
		baseURL = v
	}
	return strings.TrimRight(baseURL, "/"), apiKey
}

func doCommandCodeWithVersionRetry(ctx context.Context, client *http.Client, req *http.Request, auth *cliproxyauth.Auth, sessionID, protocol string, body []byte) (*http.Response, error) {
	httpResp, errDo := client.Do(req)
	if errDo != nil {
		return nil, errDo
	}
	if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
		return httpResp, nil
	}
	errBody, _ := io.ReadAll(httpResp.Body)
	_ = httpResp.Body.Close()
	changed, minVersion := commandcodeauth.NoteUpgradeRequired(errBody)
	if minVersion == "" && !strings.Contains(strings.ToLower(string(errBody)), "upgrade_required") {
		httpResp.Body = io.NopCloser(bytes.NewReader(errBody))
		return httpResp, nil
	}
	helps.LogWithRequestID(ctx).Infof("commandcode: upgrade_required (min=%s changed=%v); retrying with cli version %s", minVersion, changed, commandcodeauth.CurrentCLIVersion())
	retryReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, req.URL.String(), bytes.NewReader(body))
	if errReq != nil {
		httpResp.Body = io.NopCloser(bytes.NewReader(errBody))
		return httpResp, nil
	}
	retryReq.Header = req.Header.Clone()
	applyCommandCodeHeaders(retryReq, auth, sessionID, protocol)
	retryResp, errRetry := client.Do(retryReq)
	if errRetry != nil {
		httpResp.Body = io.NopCloser(bytes.NewReader(errBody))
		return httpResp, nil
	}
	return retryResp, nil
}

func applyCommandCodeHeaders(req *http.Request, auth *cliproxyauth.Auth, sessionID, protocol string) {
	if req == nil {
		return
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	version := commandcodeauth.HeaderCLIVersion(attrs)
	req.Header.Set("User-Agent", "cli")
	req.Header.Set("x-command-code-version", version)
	req.Header.Set("x-cli-environment", "production")
	req.Header.Set("x-project-slug", "proxy")
	req.Header.Set("x-taste-learning", "false")
	if sessionID != "" {
		req.Header.Set("x-session-id", sessionID)
	}
	if protocol == commandCodeProtocolMessages {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
}

func commandCodeSessionID(ctx context.Context, opts cliproxyexecutor.Options, payload []byte) string {
	if id := strings.TrimSpace(util.SessionIDFromContext(ctx)); id != "" {
		return commandCodeThreadUUID(id)
	}
	if opts.Metadata != nil {
		if v, ok := opts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string); ok && strings.TrimSpace(v) != "" {
			return commandCodeThreadUUID(v)
		}
		if v, ok := opts.Metadata[cliproxyexecutor.ExecutionSessionMetadataKey].(string); ok && strings.TrimSpace(v) != "" {
			return commandCodeThreadUUID(v)
		}
	}
	if id := helps.ProviderSessionUUID("commandcode", opts.Metadata); id != "" {
		return id
	}
	return commandCodeConversationUUID(payload)
}

func commandCodeThreadUUID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if parsed, err := uuid.Parse(id); err == nil {
		return parsed.String()
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("commandcode-thread\x00"+id)).String()
}
