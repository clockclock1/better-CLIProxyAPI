# Command Code streaming tool calls

The `/alpha/generate` adapter assigns a stable OpenAI tool-call index to each upstream call ID. Both AI SDK `id` and legacy `toolCallId` fields are accepted. Input deltas remain associated with that ID when calls overlap or complete out of order.

The completed `tool-call` event supplies arguments when input deltas were absent, or the remaining suffix when a partial prefix was streamed. Complete arguments are not emitted twice, and duplicate completion events do not create additional calls. A disconnected stream with unfinished calls, ambiguous input without an ID, or an inconsistent partial argument snapshot returns a stream error instead of a successful tool-call finish.

This prevents unrelated JSON argument objects from being concatenated into one call and subsequently parsed as empty arguments by clients. It can reduce retries caused by invalid tool input; it does not impose an agent step limit or change model prompts, reasoning settings, or token accounting.

Regression checks:

```sh
go test ./internal/runtime/executor -run CommandCode -count=1
go build -o test-output ./cmd/server
```

## DeepSeek thinking controls

The adapter accepts DeepSeek's `thinking.type` switch and maps `disabled` and
`reasoning_effort: none` to Command Code's `off`. An explicit `thinking.type`
takes precedence over effort. Compatibility levels follow DeepSeek's documented
mapping: `minimal` to `low`, `medium`/`xhigh` to `high`, and `ultra` to `max`.
An omitted setting remains omitted; the upstream default is not silently lowered.

Payload rules now apply to the final Command Code request, including streaming
and a Provider-to-CLI fallback. Rule paths are relative to the business payload
(`params` on CLI), so a default for ordinary tool work can be configured as:

```yaml
payload:
  default:
    - models:
        - name: deepseek/deepseek-v4.1-flash
          protocol: openai
      params:
        reasoning_effort: low
```

Explicit request controls take priority over defaults; override rules still win.
When lowering effort is intended for only one client or conversation, select
`low` there instead of changing the service-wide default.

On a replay of the ROG session's initial empty-workspace checkpoint, the official
API's default mode also produced a long planning response (7,574 output tokens).
Its low mode used 2,697; the gateway low samples ranged from 948 to the 8,192-token
test limit. These are individual stochastic samples, not a guaranteed saving.
The confirmed transport defects were ignored disable controls and missing payload
rule application. Neither fix imposes a stopping rule on the agent's UI checks.
