# Command Code streaming tool calls

The `/alpha/generate` adapter assigns a stable OpenAI tool-call index to each upstream call ID. Both AI SDK `id` and legacy `toolCallId` fields are accepted. Input deltas remain associated with that ID when calls overlap or complete out of order.

The completed `tool-call` event supplies arguments when input deltas were absent, or the remaining suffix when a partial prefix was streamed. Complete arguments are not emitted twice, and duplicate completion events do not create additional calls. A disconnected stream with unfinished calls, ambiguous input without an ID, or an inconsistent partial argument snapshot returns a stream error instead of a successful tool-call finish.

This prevents unrelated JSON argument objects from being concatenated into one call and subsequently parsed as empty arguments by clients. It can reduce retries caused by invalid tool input; it does not impose an agent step limit or change model prompts, reasoning settings, or token accounting.

Regression checks:

```sh
go test ./internal/runtime/executor -run CommandCode -count=1
go build -o test-output ./cmd/server
```
