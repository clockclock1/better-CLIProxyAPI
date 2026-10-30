package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

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
)

var (
	_ cliproxyauth.ProviderExecutor = (*CommandCodeExecutor)(nil)
)

// CommandCodeExecutor routes OpenAI-compatible chat requests through Command Code's
// POST /alpha/generate NDJSON API (Go-plan free models and paid models).
type CommandCodeExecutor struct {
	cfg *config.Config
}

// NewCommandCodeExecutor constructs a Command Code executor.
func NewCommandCodeExecutor(cfg *config.Config) *CommandCodeExecutor {
	commandcodeauth.StartCLIVersionUpdater(context.Background())
	return &CommandCodeExecutor{cfg: cfg}
}

// Identifier returns the provider key.
func (e *CommandCodeExecutor) Identifier() string { return "commandcode" }

// RequestToFormat reports OpenAI chat as the upstream wire format after translation.
func (e *CommandCodeExecutor) RequestToFormat(_ cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	return sdktranslator.FormatOpenAI
}

// PrepareRequest injects Command Code credentials and CLI headers.
func (e *CommandCodeExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	_, apiKey := commandCodeCreds(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	applyCommandCodeHeaders(req, auth, "")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects credentials and executes the HTTP request.
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

// Refresh is a no-op; Command Code API keys do not refresh via OAuth.
func (e *CommandCodeExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	return auth, nil
}

// CountTokens is not supported by /alpha/generate.
func (e *CommandCodeExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, statusErr{code: http.StatusNotImplemented, msg: "commandcode: count tokens not supported"}
}

// Execute aggregates /alpha/generate NDJSON into an OpenAI chat.completion response.
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

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)
	if len(translated) == 0 {
		translated = req.Payload
	}
	translated = helps.SetStringIfDifferent(translated, "model", baseModel)
	if isCommandCodeEffort(parsedThinking.RawSuffix) && strings.TrimSpace(gjson.GetBytes(translated, "reasoning_effort").String()) == "" {
		translated = helps.SetStringIfDifferent(translated, "reasoning_effort", strings.ToLower(strings.TrimSpace(parsedThinking.RawSuffix)))
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayloadSource, false)
	if len(originalTranslated) == 0 {
		originalTranslated = originalPayloadSource
	}

	sessionID := commandCodeSessionID(ctx, opts, translated)
	generateBody, errBody := buildCommandCodeGenerateBody(baseModel, sessionID, translated)
	if errBody != nil {
		err = statusErr{code: http.StatusBadRequest, msg: errBody.Error()}
		return resp, err
	}
	url := strings.TrimSuffix(baseURL, "/") + "/alpha/generate"
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(generateBody))
	if errReq != nil {
		return resp, errReq
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	applyCommandCodeHeaders(httpReq, auth, sessionID)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)

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
		Body:      generateBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, errDo := doCommandCodeGenerateWithVersionRetry(ctx, httpClient, httpReq, auth, sessionID, generateBody)
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
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}

	acc := newCommandCodeOpenAIAccumulator(baseModel)
	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(nil, 52_428_800)
	for scanner.Scan() {
		event, ok := parseCommandCodeNDJSONLine(scanner.Bytes())
		if !ok {
			continue
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, event)
		if errIngest := acc.ingest(event); errIngest != nil {
			err = statusErr{code: http.StatusBadGateway, msg: errIngest.Error()}
			return resp, err
		}
	}
	if errScan := scanner.Err(); errScan != nil && ctx.Err() == nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errScan)
		return resp, errScan
	}

	openaiJSON, errJSON := acc.completionJSON()
	if errJSON != nil {
		err = errJSON
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

// ExecuteStream streams /alpha/generate NDJSON as OpenAI chat.completion.chunk frames.
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

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)
	if len(translated) == 0 {
		translated = req.Payload
	}
	translated = helps.SetStringIfDifferent(translated, "model", baseModel)
	if isCommandCodeEffort(parsedThinking.RawSuffix) && strings.TrimSpace(gjson.GetBytes(translated, "reasoning_effort").String()) == "" {
		translated = helps.SetStringIfDifferent(translated, "reasoning_effort", strings.ToLower(strings.TrimSpace(parsedThinking.RawSuffix)))
	}
	_ = originalPayloadSource

	sessionID := commandCodeSessionID(ctx, opts, translated)
	generateBody, errBody := buildCommandCodeGenerateBody(baseModel, sessionID, translated)
	if errBody != nil {
		err = statusErr{code: http.StatusBadRequest, msg: errBody.Error()}
		return nil, err
	}
	url := strings.TrimSuffix(baseURL, "/") + "/alpha/generate"
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(generateBody))
	if errReq != nil {
		return nil, errReq
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Accept", "application/x-ndjson, text/event-stream, application/json")
	applyCommandCodeHeaders(httpReq, auth, sessionID)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)

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
		Body:      generateBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClientRoundTripOnly(httpClient)
	httpResp, errDo := doCommandCodeGenerateWithVersionRetry(ctx, httpClient, httpReq, auth, sessionID, generateBody)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return nil, errDo
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		_ = httpResp.Body.Close()
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

		acc := newCommandCodeOpenAIAccumulator(baseModel)
		var param any
		emit := func(openaiChunk []byte) bool {
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

		if !emit(acc.streamChunk(map[string]any{"role": "assistant", "content": ""}, nil)) {
			return
		}

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		toolIndex := 0
		for scanner.Scan() {
			event, ok := parseCommandCodeNDJSONLine(scanner.Bytes())
			if !ok {
				continue
			}
			helps.AppendAPIResponseChunk(ctx, e.cfg, event)
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
			case "reasoning-delta":
				text := gjson.GetBytes(event, "text").String()
				_ = acc.ingest(event)
				if text == "" {
					continue
				}
				if !emit(acc.streamChunk(map[string]any{"reasoning_content": text}, nil)) {
					return
				}
			case "tool-call":
				before := len(acc.ToolCalls)
				_ = acc.ingest(event)
				if len(acc.ToolCalls) <= before {
					continue
				}
				call := acc.ToolCalls[len(acc.ToolCalls)-1]
				fn, _ := call["function"].(map[string]any)
				delta := map[string]any{
					"tool_calls": []any{
						map[string]any{
							"index": toolIndex,
							"id":    call["id"],
							"type":  "function",
							"function": map[string]any{
								"name":      fn["name"],
								"arguments": fn["arguments"],
							},
						},
					},
				}
				toolIndex++
				if !emit(acc.streamChunk(delta, nil)) {
					return
				}
			case "finish", "finish-step":
				_ = acc.ingest(event)
			default:
				_ = acc.ingest(event)
			}
		}
		if errScan := scanner.Err(); errScan != nil && ctx.Err() == nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
			return
		}

		final := acc.streamChunkWithUsage(map[string]any{}, acc.FinishReason, acc.Usage)
		reporter.Publish(ctx, helps.ParseOpenAIUsage(final))
		if !emit(final) {
			return
		}
		doneLine := []byte("data: [DONE]")
		chunks := sdktranslator.TranslateStream(ctx, to, responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), translated, doneLine, &param)
		for i := range chunks {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
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

// doCommandCodeGenerateWithVersionRetry sends /alpha/generate and, on
// upgrade_required, bumps the cached CLI version then retries once.
func doCommandCodeGenerateWithVersionRetry(ctx context.Context, client *http.Client, req *http.Request, auth *cliproxyauth.Auth, sessionID string, body []byte) (*http.Response, error) {
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
	applyCommandCodeHeaders(retryReq, auth, sessionID)
	retryResp, errRetry := client.Do(retryReq)
	if errRetry != nil {
		httpResp.Body = io.NopCloser(bytes.NewReader(errBody))
		return httpResp, nil
	}
	return retryResp, nil
}

func applyCommandCodeHeaders(req *http.Request, auth *cliproxyauth.Auth, sessionID string) {
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
	// Neutral slug — avoid pulling proxy-repo-specific server prompt/taste context.
	req.Header.Set("x-project-slug", "proxy")
	req.Header.Set("x-taste-learning", "false")
	if sessionID != "" {
		req.Header.Set("x-session-id", sessionID)
	}
}

// commandCodeSessionID is the stable prompt-cache scope. Codex pins this with
// prompt_cache_key; Command Code pins it with threadId / x-session-id. A new
// UUID per request makes the server miss the cache every turn.
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
