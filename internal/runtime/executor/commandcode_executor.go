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

	commandcodeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/commandcode"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/google/uuid"
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
	baseModel := thinking.ParseSuffix(req.Model).ModelName
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
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayloadSource, false)
	if len(originalTranslated) == 0 {
		originalTranslated = originalPayloadSource
	}

	generateBody, errBody := buildCommandCodeGenerateBody(baseModel, translated)
	if errBody != nil {
		err = statusErr{code: http.StatusBadRequest, msg: errBody.Error()}
		return resp, err
	}

	sessionID := commandCodeSessionID(opts)
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
	httpResp, errDo := httpClient.Do(httpReq)
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
	baseModel := thinking.ParseSuffix(req.Model).ModelName
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
	_ = originalPayloadSource

	generateBody, errBody := buildCommandCodeGenerateBody(baseModel, translated)
	if errBody != nil {
		err = statusErr{code: http.StatusBadRequest, msg: errBody.Error()}
		return nil, err
	}

	sessionID := commandCodeSessionID(opts)
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
	httpResp, errDo := httpClient.Do(httpReq)
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

func applyCommandCodeHeaders(req *http.Request, auth *cliproxyauth.Auth, sessionID string) {
	if req == nil {
		return
	}
	version := commandcodeauth.DefaultCLIVersion
	if auth != nil && auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["cli_version"]); v != "" {
			version = v
		}
	}
	if v := strings.TrimSpace(os.Getenv("COMMANDCODE_CLI_VERSION")); v != "" {
		version = v
	}
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	req.Header.Set("User-Agent", "cli")
	req.Header.Set("x-command-code-version", version)
	req.Header.Set("x-cli-environment", "production")
	req.Header.Set("x-project-slug", "cli-proxy-api")
	req.Header.Set("x-taste-learning", "false")
	req.Header.Set("x-session-id", sessionID)
}

func commandCodeSessionID(opts cliproxyexecutor.Options) string {
	if opts.Metadata != nil {
		if v, ok := opts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		if v, ok := opts.Metadata[cliproxyexecutor.ExecutionSessionMetadataKey].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return uuid.NewString()
}
