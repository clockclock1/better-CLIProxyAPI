package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCommandCodeFinalPayloadRules(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("fallback=%t/stream=%t", fallback, stream), func(t *testing.T) {
				t.Setenv("COMMANDCODE_PROTOCOL", "")
				attempts := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					attempts++
					body, _ := io.ReadAll(r.Body)
					root := ""
					if r.URL.Path == "/alpha/generate" {
						root = "params."
					}
					for path, want := range map[string]string{"reasoning_effort": "low", "max_tokens": "1234"} {
						if got := gjson.GetBytes(body, root+path).String(); got != want {
							t.Errorf("%s %s = %q, want %q", r.URL.Path, path, got, want)
						}
					}
					if gjson.GetBytes(body, root+"temperature").Exists() {
						t.Error("filtered temperature restored")
					}
					if fallback && r.URL.Path != "/alpha/generate" {
						w.WriteHeader(http.StatusForbidden)
						fmt.Fprint(w, `{"error":"provider api unavailable"}`)
						return
					}
					if root != "" {
						fmt.Fprintln(w, `{"type":"text-delta","text":"ok"}`)
						fmt.Fprintln(w, `{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`)
					} else if stream {
						fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					} else {
						fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
					}
				}))
				defer server.Close()
				models := []config.PayloadModelRule{{Name: "deepseek/*", Protocol: "openai", Headers: map[string]string{"X-Audit": "yes"}}}
				cfg := &config.Config{Payload: config.PayloadConfig{
					Override: []config.PayloadRule{{Models: models, Params: map[string]any{"reasoning_effort": "low", "max_tokens": 1234}}},
					Filter:   []config.PayloadFilterRule{{Models: models, Params: []string{"temperature"}}},
				}}
				executor := &CommandCodeExecutor{cfg: cfg}
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "audit-test"}}
				req := cliproxyexecutor.Request{Model: "deepseek/deepseek-v4.1-flash", Payload: []byte(`{"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"},"max_tokens":256,"temperature":0.2}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Headers: http.Header{"X-Audit": []string{"yes"}}}
				if stream {
					resp, err := executor.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range resp.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, err := executor.Execute(context.Background(), auth, req, opts); err != nil {
					t.Fatal(err)
				}
				want := 1
				if fallback {
					want = 2
				}
				if attempts != want {
					t.Fatalf("attempts = %d, want %d", attempts, want)
				}
			})
		}
	}
}
