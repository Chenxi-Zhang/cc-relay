# Learnings - OpenAI Protocol Implementation

## 2025-05-01: OpenAI Types & SSE Utilities

### Key Findings
- OpenAI SSE format is simpler than Anthropic: just `data: {json}\n\n`, no `event:` prefix
- OpenAI terminates streams with `data: [DONE]\n\n` (Anthropic uses `event: message_stop`)
- OpenAI response content is a string in `choices[].message.content` (Anthropic uses array of content blocks)
- `FinishReason` must be `*string` (pointer) to properly handle null during streaming chunks
- Existing `sse.go` already has `IsStreamingRequest` — OpenAI version named `OpenAIGetIsStreaming` to avoid collision
- Existing `SetSSEHeaders` takes `http.Header`; OpenAI version `SetOpenAISSEHeaders` takes `http.ResponseWriter` for convenience
- Project requires Go 1.25.0 which is not yet publicly available — verified tests in isolated module

### File Structure
- `openai_types.go`: All OpenAI API types (request, response, chunk, models, error)
- `openai_sse.go`: SSE utilities specific to OpenAI format (headers, chunk writing, done signal, stream detection)
- `openai_types_test.go`: TDD tests covering JSON roundtrips and SSE output format

### Naming Convention
- OpenAI-specific functions prefixed with `OpenAI` or use `OpenAISSE` to distinguish from Anthropic SSE functions
